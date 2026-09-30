//go:build darwin || linux

package inhibit

import (
	"log"
	"os"
	"os/exec"
	"strconv"
	"sync"
)

// The caffeinate backend is darwin's Locker (caffeinate_darwin.go wires it
// into NewLocker). It builds on linux too so its argv, Release-once
// semantics, and spawn-failure degradation are unit-tested through the
// spawn seam on the Linux CI host.

// proc is the slice of a started process caffeinateLocker needs.
type proc interface {
	Kill() error
	Wait() error
}

// spawnFunc starts name with args and returns the running process. Start
// only forks/execs, so it's bounded well under Acquire's ~2s budget.
type spawnFunc func(name string, args ...string) (proc, error)

// cmdProc adapts a started *exec.Cmd to proc.
type cmdProc struct{ cmd *exec.Cmd }

func (p cmdProc) Kill() error { return p.cmd.Process.Kill() }
func (p cmdProc) Wait() error { return p.cmd.Wait() }

// execSpawn is the real spawnFunc.
func execSpawn(name string, args ...string) (proc, error) {
	cmd := exec.Command(name, args...)
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return cmdProc{cmd}, nil
}

// caffeinateLocker holds a power assertion by running
// `caffeinate -i -s -w <our pid>`:
//
//   - -i prevents idle sleep;
//   - -s prevents system sleep (honored by macOS only on AC power);
//   - -w ties the assertion to our pid, so caffeinate exits — and the
//     assertion drops — when we die, even after a SIGKILL.
//
// caffeinate ships with macOS, so there's no cgo (release builds are
// CGO_ENABLED=0).
type caffeinateLocker struct {
	spawn spawnFunc
}

// caffeinateArgs is the argv (after the program name) for pid.
func caffeinateArgs(pid int) []string {
	return []string{"-i", "-s", "-w", strconv.Itoa(pid)}
}

func (l caffeinateLocker) Acquire(why string) Handle {
	p, err := l.spawn("caffeinate", caffeinateArgs(os.Getpid())...)
	if err != nil {
		log.Printf("inhibit: caffeinate unavailable (%s): %v", why, err)
		return HandleFunc(func() {})
	}
	var once sync.Once
	return HandleFunc(func() {
		once.Do(func() {
			_ = p.Kill()
			_ = p.Wait()
		})
	})
}
