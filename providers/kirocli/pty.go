package kirocli

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

// startPTY launches kiro-cli's own interactive terminal REPL (`kiro-cli
// chat`) on a pseudo-terminal — the shell dock's substrate. No structured
// events flow in this mode: Events() is nil and the byte stream is the
// interface.
func (p *Provider) startPTY(opts provider.StartOptions) (provider.Session, error) {
	args := []string{"chat"}
	if opts.Model != "" {
		args = append(args, "--model", opts.Model)
	}
	if opts.Resume != "" {
		args = append(args, "--resume-id", opts.Resume)
	}
	// --trust-all-tools only for full trust (and the zero value). For
	// limited/read_only, omit it and rely on kiro-cli's own interactive
	// per-tool confirmation prompt — a human at the terminal, not the
	// automated kind-based gate the ACP transport uses. See README.
	if opts.Trust == provider.TrustFull || opts.Trust == "" {
		args = append(args, "--trust-all-tools")
	}
	args = append(args, p.cfg.ExtraArgs...)

	cmd := exec.Command(p.cfg.Bin, args...)
	cmd.Dir = opts.Cwd
	cmd.Env = p.env(opts.Env)
	master, err := pty.Start(cmd)
	if err != nil {
		return nil, fmt.Errorf("start %s on a pty: %w", p.cfg.Bin, err)
	}
	// kiro-cli's chat REPL does not print its session id on exit
	// (kirodotdev/Kiro#7788) — the same limitation claude-code's PTY
	// transport has. The ref is only known when resuming.
	return &ptySession{cmd: cmd, master: master, ref: opts.Resume}, nil
}

type ptySession struct {
	cmd    *exec.Cmd
	master *os.File
	ref    string

	mu     sync.Mutex
	closed bool
}

func (s *ptySession) Ref() string                   { return s.ref }
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

// Interrupt sends Ctrl-C — kiro-cli chat's stop-current-response key
// (unlike claude-code's ESC).
func (s *ptySession) Interrupt(context.Context) error {
	_, err := s.master.Write([]byte{0x03})
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
	return nil
}
