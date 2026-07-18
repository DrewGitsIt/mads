package claudecode

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/creack/pty"

	"github.com/drewkidwell/mads/providers/provider"
)

// startPTY launches the terminal transport: claude's own interactive UI on a
// pseudo-terminal (the shell dock's substrate). No structured events flow in
// this mode — Events() is nil and the byte stream is the interface.
func (p *Provider) startPTY(opts provider.StartOptions) (provider.Session, error) {
	var args []string
	if opts.Model != "" {
		args = append(args, "--model", opts.Model)
	}
	if opts.SystemPrompt != "" {
		args = append(args, "--append-system-prompt", opts.SystemPrompt)
	}
	if opts.Resume != "" {
		args = append(args, "--resume", opts.Resume)
	}
	args = append(args, disallowedToolsArgs(opts.Trust)...)
	mcpArgVals, mcpConfigPath, err := mcpArgs(opts.MCPServers)
	if err != nil {
		return nil, fmt.Errorf("mcp config: %w", err)
	}
	args = append(args, mcpArgVals...)
	args = append(args, p.cfg.ExtraArgs...)

	cmd := exec.Command(p.cfg.Bin, args...)
	cmd.Dir = opts.Cwd
	cmd.Env = p.env(opts.Env)
	// Backdated by a couple seconds: a transcript write that happens only
	// microseconds after this line can still land with an mtime that
	// compares before it (clock/filesystem timestamp granularity noise
	// between this process and the child's write), which would wrongly
	// exclude the session's own transcript from discoverRef's candidates.
	// A stale file from an earlier session is realistically seconds to
	// minutes old, so this margin doesn't reopen that door.
	spawnedAt := time.Now().Add(-2 * time.Second)
	master, err := pty.Start(cmd)
	if err != nil {
		if mcpConfigPath != "" {
			_ = os.Remove(mcpConfigPath)
		}
		return nil, fmt.Errorf("start %s on a pty: %w", p.cfg.Bin, err)
	}
	// The ref is only known synchronously when resuming: claude does not
	// print session ids on the terminal transport. A fresh session's ref is
	// instead discovered from its on-disk transcript (watchRefDiscovery) —
	// the file the same TranscriptDir this provider already reads
	// (transcript.go) — best-effort, never blocking Start on it.
	s := &ptySession{
		cmd: cmd, master: master, ref: opts.Resume, mcpConfigPath: mcpConfigPath,
		cwd: opts.Cwd, spawnedAt: spawnedAt, transcriptDir: p.cfg.TranscriptDir,
		announced: make(chan string, 1),
		done:      make(chan struct{}),
	}
	if opts.Resume != "" {
		s.refFired = true
		s.announced <- opts.Resume
	} else {
		go s.watchRefDiscovery()
	}
	return s, nil
}

type ptySession struct {
	cmd           *exec.Cmd
	master        *os.File
	mcpConfigPath string
	cwd           string
	spawnedAt     time.Time
	transcriptDir string
	// announced delivers the discovered ref exactly once, or closes with
	// nothing sent if discovery never finds an unambiguous candidate before
	// Close — best-effort forever, never user-visible as an error (the
	// session simply stays resume-less, exactly as before this existed).
	announced chan string
	done      chan struct{}

	mu       sync.Mutex
	ref      string
	closed   bool
	refFired bool // guards ref/announced against a double-fire
}

func (s *ptySession) Ref() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ref
}
func (s *ptySession) Mode() provider.Mode           { return provider.ModePTY }
func (s *ptySession) Events() <-chan provider.Event { return nil }
func (s *ptySession) Terminal() io.ReadWriter       { return s.master }

func (s *ptySession) Resize(cols, rows int) error {
	return pty.Setsize(s.master, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)}) //nolint:gosec // terminal dimensions
}

// Send types a message into the terminal (carriage return submits).
func (s *ptySession) Send(_ context.Context, text string) error {
	_, err := s.master.Write([]byte(text + "\r"))
	return err
}

// Interrupt sends ESC — claude-code's stop-current-response key.
func (s *ptySession) Interrupt(context.Context) error {
	_, err := s.master.Write([]byte{0x1b})
	return err
}

func (s *ptySession) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.mu.Unlock()
	close(s.done)
	_ = s.master.Close()
	closeWait(s.cmd, 3*time.Second)
	if s.mcpConfigPath != "" {
		_ = os.Remove(s.mcpConfigPath)
	}
	return nil
}

// RefAnnounced implements provider.RefAnnouncer — see announced's doc comment.
func (s *ptySession) RefAnnounced() <-chan string { return s.announced }

// watchRefDiscovery polls (no event to subscribe to — this is on-disk
// discovery, not a wire announcement) for a transcript that unambiguously
// belongs to this session, firing the ref once discoverRef's candidate is
// seen as the same unambiguous, non-empty file across two consecutive ticks
// (proof it's a real, stable transcript, not a coincidental match caught
// mid-write or a stale ambiguous pair). A literal size-must-grow-between-
// ticks check doesn't hold up here: a fast turn can write its whole
// transcript in well under one 750ms tick, so the file may already be
// stable by the time it's first observed. Bounded by s.done: Close makes
// this return promptly instead of polling forever past the session's
// lifetime.
func (s *ptySession) watchRefDiscovery() {
	ticker := time.NewTicker(750 * time.Millisecond)
	defer ticker.Stop()
	var pending string
	for {
		select {
		case <-s.done:
			s.fireNever()
			return
		case <-ticker.C:
			path, ok := discoverRef(s.transcriptDir, s.cwd, s.spawnedAt)
			if !ok {
				pending = ""
				continue
			}
			fi, err := os.Stat(path)
			if err != nil || fi.Size() == 0 {
				continue
			}
			if path == pending {
				s.fire(refFromPath(path))
				return
			}
			pending = path
		}
	}
}

// fire records the discovered ref and delivers it on announced, unless
// Close already raced it to fireNever.
func (s *ptySession) fire(ref string) {
	s.mu.Lock()
	if s.refFired {
		s.mu.Unlock()
		return
	}
	s.refFired = true
	s.ref = ref
	s.mu.Unlock()
	s.announced <- ref
}

// fireNever closes announced with nothing sent: discovery gave up without
// ever finding an unambiguous candidate. Best-effort forever — logged, but
// never surfaced to the user as an error; the session works exactly as it
// did before this mechanism existed (resume-less until reattached with an
// explicit ref).
func (s *ptySession) fireNever() {
	s.mu.Lock()
	if s.refFired {
		s.mu.Unlock()
		return
	}
	s.refFired = true
	s.mu.Unlock()
	log.Printf("claudecode: pty session ref discovery under %s gave up (cwd %s)", s.transcriptDir, s.cwd)
	close(s.announced)
}
