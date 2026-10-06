package claudecode

import (
	"context"
	"fmt"
	"io"
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
	resume, forkID := resumeArgs(opts)
	args = append(args, resume...)
	args = append(args, trustArgs(opts.Trust)...)
	mcpArgVals, mcpConfigPath, err := mcpArgs(opts.MCPServers)
	if err != nil {
		return nil, fmt.Errorf("mcp config: %w", err)
	}
	args = append(args, mcpArgVals...)
	args = append(args, p.cfg.ExtraArgs...)

	cmd := exec.Command(p.cfg.Bin, args...)
	cmd.Dir = opts.Cwd
	cmd.Env = p.env(opts.Env)
	master, err := pty.Start(cmd)
	if err != nil {
		if mcpConfigPath != "" {
			_ = os.Remove(mcpConfigPath)
		}
		return nil, fmt.Errorf("start %s on a pty: %w", p.cfg.Bin, err)
	}
	// The ref is only known synchronously when resuming: claude does not
	// print session ids on the terminal transport, and a fresh PTY session
	// has no wire announcement at all. Discovering it is the Runner's job
	// (SessionLocator, locate.go) — this provider's job here is just to hand
	// back a session whose Ref() starts empty and never blocks Start on it.
	s := &ptySession{cmd: cmd, master: master, ref: opts.Resume, mcpConfigPath: mcpConfigPath}
	if forkID != "" {
		// A fork's Ref() is not Resume: it stays empty until the pinned
		// fork's transcript appears (forkedPTYSession), then the Runner
		// persists it via RefAnnounced.
		s.ref = ""
		return newForkedPTYSession(s, p.cfg.TranscriptDir, forkID), nil
	}
	return s, nil
}

type ptySession struct {
	cmd           *exec.Cmd
	master        *os.File
	mcpConfigPath string

	mu     sync.Mutex
	ref    string
	closed bool
}

func (s *ptySession) Ref() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ref
}

// SetRef implements provider.RefSetter: the Runner's SessionLocator backstop
// (locate.go) calls this once it discovers this session's ref, since a fresh
// PTY session has no way to learn it on its own. Guarded against overwriting
// an already-known ref (the resumed case, where ref is opts.Resume from the
// start) — the backstop only ever runs for a session whose ref started
// empty, but this keeps the method's own contract self-evidently safe.
func (s *ptySession) SetRef(ref string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ref == "" {
		s.ref = ref
	}
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
	_ = s.master.Close()
	closeWait(s.cmd, 3*time.Second)
	if s.mcpConfigPath != "" {
		_ = os.Remove(s.mcpConfigPath)
	}
	return nil
}
