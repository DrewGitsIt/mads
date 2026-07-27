// Package capability holds capability process/health mechanics (spec §12):
// MCP server lifecycle, skill directory availability, and guided
// diagnostics. It has no dataclient dependency and never touches the
// credential store — callers (services/session/internal/runner.Runner)
// resolve credentials themselves and pass only the resulting env slice in.
package capability

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
)

// stopGrace bounds how long Stop waits after SIGTERM before SIGKILL.
const stopGrace = 5 * time.Second

// Registry coordinates system-wide MCP server processes across independent
// OS processes via a local PID-file directory (dir is typically
// ~/.mads/mcp). "System-wide" (spec §12.5) is achieved without a
// coordinating daemon: each Session Service process independently spawns a
// capability's server the first time it's declared, claiming the slot with
// an atomic O_CREATE|O_EXCL pid-file create, and every other session just
// finds the recorded PID already alive.
//
// Known race: two sessions declaring a brand-new capability in the same
// instant could both win the exclusive-create race in the narrow window
// before the first one writes its PID — harmless in practice (the loser's
// spawn attempt fails harmlessly or produces a second, quickly-orphaned
// process), and not worth a real flock unless it proves to matter.
type Registry struct {
	dir string

	stdinMu sync.Mutex
	stdinR  *os.File // shared read end handed to every spawned process as stdin
	stdinW  *os.File // held open (never written, never closed) so stdinR never sees EOF
}

// NewRegistry returns a Registry rooted at dir. The directory is created
// lazily, on first EnsureStarted call.
func NewRegistry(dir string) *Registry {
	return &Registry{dir: dir}
}

func (r *Registry) pidPath(name string) string {
	return filepath.Join(r.dir, name+".pid")
}

// Running reports whether name has a live recorded process.
func (r *Registry) Running(name string) bool {
	pid, ok := r.readPID(name)
	if !ok {
		return false
	}
	return alive(pid)
}

func (r *Registry) readPID(name string) (int, bool) {
	data, err := os.ReadFile(r.pidPath(name))
	if err != nil {
		return 0, false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0, false
	}
	return pid, true
}

// alive reports whether pid names a live process, via a signal-0 liveness
// probe (no actual signal delivered).
func alive(pid int) bool {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}

// blockingStdin returns a shared pipe's read end whose write end this
// Registry holds open for its own lifetime, so a spawned capability
// process's stdin never reaches EOF on its own — see EnsureStarted.
//
// The read end must be a real *os.File (not a custom io.Reader wrapper
// with a Read that never returns): exec.Cmd only dups an *os.File Stdin
// directly into the child. Any other io.Reader makes it create its own
// pipe and copy from Stdin to it on an internal goroutine, and that
// goroutine's Read call — blocking forever, by construction — would
// never let cmd.Wait() return, even after the child itself is killed.
func (r *Registry) blockingStdin() (*os.File, error) {
	r.stdinMu.Lock()
	defer r.stdinMu.Unlock()
	if r.stdinR != nil {
		return r.stdinR, nil
	}
	pr, pw, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("create blocking stdin pipe: %w", err)
	}
	r.stdinR, r.stdinW = pr, pw
	return r.stdinR, nil
}

// EnsureStarted starts capability cap's server if no live process is
// recorded for it. env carries exactly cap's own resolved credential env
// vars (spec §13.3) — callers resolve from credstore themselves; this
// package never touches the credential store, keeping the "who can see
// what" boundary in one place (the Runner).
func (r *Registry) EnsureStarted(ctx context.Context, cap contracts.Capability, env []string) (alreadyRunning bool, err error) {
	if r.Running(cap.Name) {
		return true, nil
	}
	if err := os.MkdirAll(r.dir, 0o755); err != nil {
		return false, fmt.Errorf("capability %s: create registry dir: %w", cap.Name, err)
	}

	pidPath := r.pidPath(cap.Name)
	f, err := os.OpenFile(pidPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		if !errors.Is(err, os.ErrExist) {
			return false, fmt.Errorf("capability %s: claim pid file: %w", cap.Name, err)
		}
		// Lost the exclusive-create race, or a stale file left by a process
		// that died without cleaning up. If it's actually running now, we're
		// done; otherwise reclaim the stale file and retry once.
		if r.Running(cap.Name) {
			return true, nil
		}
		if rmErr := os.Remove(pidPath); rmErr != nil && !os.IsNotExist(rmErr) {
			return false, fmt.Errorf("capability %s: remove stale pid file: %w", cap.Name, rmErr)
		}
		f, err = os.OpenFile(pidPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err != nil {
			return false, fmt.Errorf("capability %s: claim pid file: %w", cap.Name, err)
		}
	}
	defer func() { _ = f.Close() }()

	fields := strings.Fields(cap.Config.Command)
	if len(fields) == 0 {
		_ = os.Remove(pidPath)
		return false, fmt.Errorf("capability %s: empty command", cap.Name)
	}
	//nolint:gosec // cap.Config.Command is operator/registry-configured, not raw user input
	cmd := exec.Command(fields[0], fields[1:]...)
	cmd.Env = append(os.Environ(), env...)
	// A nil Stdin resolves to os.DevNull, an immediate EOF — fatal for a
	// long-lived capability that reads stdin in a loop (e.g. orchestrate-mcp's
	// stdio JSON-RPC server, docs/LINKED-SESSIONS-DESIGN.md decisions 1-3),
	// which would otherwise exit right after spawning. blockingStdin never
	// yields EOF, matching "stays running until the user explicitly stops
	// them" for every Command-based capability, not just stdio ones.
	stdin, err := r.blockingStdin()
	if err != nil {
		_ = os.Remove(pidPath)
		return false, fmt.Errorf("capability %s: %w", cap.Name, err)
	}
	cmd.Stdin = stdin
	// No Pdeathsig: the server must outlive this OS process (spec §12.5,
	// "stays running until the user explicitly stops them").
	if err := cmd.Start(); err != nil {
		_ = os.Remove(pidPath)
		return false, fmt.Errorf("capability %s: start: %w", cap.Name, err)
	}
	if _, err := fmt.Fprintf(f, "%d", cmd.Process.Pid); err != nil {
		_ = os.Remove(pidPath)
		return false, fmt.Errorf("capability %s: record pid: %w", cap.Name, err)
	}
	// Reap in the background so the process table doesn't accumulate
	// zombies; this does not affect the child's ability to outlive us.
	go func() { _ = cmd.Wait() }()
	return false, nil
}

// Stop terminates the recorded process for capability name (SIGTERM, grace,
// SIGKILL), if running. Explicit-only (spec §12.5) — never called from
// session shutdown.
func (r *Registry) Stop(ctx context.Context, name string) error {
	pid, ok := r.readPID(name)
	if !ok {
		return nil
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return os.Remove(r.pidPath(name))
	}
	if err := proc.Signal(syscall.SIGTERM); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return fmt.Errorf("capability %s: signal: %w", name, err)
	}

	deadline := time.Now().Add(stopGrace)
	for time.Now().Before(deadline) && alive(pid) {
		time.Sleep(50 * time.Millisecond)
	}
	if alive(pid) {
		_ = proc.Kill()
	}
	if err := os.Remove(r.pidPath(name)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("capability %s: remove pid file: %w", name, err)
	}
	return nil
}
