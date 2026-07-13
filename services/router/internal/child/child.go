// Package child spawns and supervises one OS child process, discovering its
// listen address from its own "listening on <addr>" log line (the log-line
// convention all four mads services already follow via the stdlib log
// package to stderr). This is a soft contract, not a shared interface: if a
// future service changes its log wording, spawning breaks silently with a
// readyTimeout, not a compile error.
package child

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"regexp"
	"sync/atomic"
	"syscall"
	"time"
)

var listeningRe = regexp.MustCompile(`listening on (\S+)`)

// readyTimeout bounds how long Spawn waits for the child to log its
// address. Generous for "go run"'s compile step in dev; tests should spawn
// a prebuilt binary to stay fast.
const readyTimeout = 15 * time.Second

// Process is one supervised child.
type Process struct {
	ID   string // caller-assigned identity; also the lease "holder" string
	Cmd  *exec.Cmd
	Addr string // discovered from the "listening on <addr>" log line

	expected atomic.Bool
	done     chan struct{}
	exitErr  error
}

// Spawn starts argv (argv[0] is the binary/command, e.g. "go"; the rest are
// its args), always appending "-addr" "localhost:0", waits up to
// readyTimeout for the child to log its listening address, and returns once
// discovered (or errors and kills the child on timeout/early exit). onExit
// is invoked exactly once from a background goroutine after the process
// exits, with whether the exit was expected (Terminate was called) or not,
// and the exit error (nil for a clean exit).
func Spawn(ctx context.Context, id string, argv []string, onExit func(p *Process, expected bool, err error)) (*Process, error) {
	if len(argv) == 0 {
		return nil, fmt.Errorf("child: empty argv")
	}
	full := append(append([]string{}, argv...), "-addr", "localhost:0")
	cmd := exec.Command(full[0], full[1:]...) //nolint:gosec // argv is operator-configured, not user input

	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("child %s: stderr pipe: %w", id, err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("child %s: start %v: %w", id, full, err)
	}

	p := &Process{ID: id, Cmd: cmd, done: make(chan struct{})}

	readyCh := make(chan string, 1)
	var readySent atomic.Bool

	go func() {
		sc := bufio.NewScanner(stderr)
		sc.Buffer(make([]byte, 0, 4096), 1<<20)
		for sc.Scan() {
			line := sc.Text()
			log.Printf("[child %s] %s", id, line)
			if m := listeningRe.FindStringSubmatch(line); m != nil && readySent.CompareAndSwap(false, true) {
				readyCh <- m[1]
			}
		}
	}()

	// Must start unconditionally right after cmd.Start(), not after port
	// discovery: a child that fails before ever logging "listening on"
	// still needs cmd.Wait() reaped and onExit fired, otherwise Spawn's
	// ready-wait goroutine leaks forever and the process becomes a zombie.
	exitedCh := make(chan struct{})
	go func() {
		waitErr := cmd.Wait()
		p.exitErr = waitErr
		expected := p.expected.Load()
		// Run onExit (and let it finish, e.g. its lease ReleaseAll) before
		// closing done: Terminate callers — notably Bounce, which respawns
		// under the same holder ID right after Terminate returns — must not
		// observe the process as gone until its exit handling has actually
		// completed, or a late ReleaseAll could release the new child's
		// freshly claimed leases.
		if onExit != nil {
			onExit(p, expected, waitErr)
		}
		close(p.done)
		close(exitedCh)
	}()

	select {
	case addr := <-readyCh:
		p.Addr = addr
		return p, nil
	case <-exitedCh:
		return nil, fmt.Errorf("child %s: exited before ready: %w", id, p.exitErr)
	case <-time.After(readyTimeout):
		p.expected.Store(true)
		_ = cmd.Process.Kill()
		<-exitedCh
		return nil, fmt.Errorf("child %s: timed out waiting for ready", id)
	case <-ctx.Done():
		p.expected.Store(true)
		_ = cmd.Process.Kill()
		<-exitedCh
		return nil, ctx.Err()
	}
}

// Terminate sends SIGTERM, waits up to grace, then SIGKILL. Marks the exit
// "expected" before signaling, so the onExit callback fired from the
// monitor goroutine knows not to treat this as a crash. Safe to call after
// the process has already exited on its own (no-op).
func (p *Process) Terminate(ctx context.Context, grace time.Duration) error {
	p.expected.Store(true)

	select {
	case <-p.done:
		return nil
	default:
	}

	if err := p.Cmd.Process.Signal(syscall.SIGTERM); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return fmt.Errorf("child %s: signal: %w", p.ID, err)
	}

	select {
	case <-p.done:
		return nil
	case <-time.After(grace):
	case <-ctx.Done():
	}

	select {
	case <-p.done:
		return nil
	default:
	}
	_ = p.Cmd.Process.Kill()
	<-p.done
	return nil
}
