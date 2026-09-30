//go:build darwin || linux

package inhibit

import (
	"errors"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"testing"
	"time"
)

type fakeProc struct{ kills, waits int }

func (p *fakeProc) Kill() error { p.kills++; return nil }
func (p *fakeProc) Wait() error { p.waits++; return nil }

func TestCaffeinateArgv(t *testing.T) {
	var gotName string
	var gotArgs []string
	fp := &fakeProc{}
	l := caffeinateLocker{spawn: func(name string, args ...string) (proc, error) {
		gotName, gotArgs = name, args
		return fp, nil
	}}

	h := l.Acquire("pipeline run ses_test")
	if gotName != "caffeinate" {
		t.Fatalf("spawned %q, want caffeinate", gotName)
	}
	want := []string{"-i", "-s", "-w", strconv.Itoa(os.Getpid())}
	if !slices.Equal(gotArgs, want) {
		t.Fatalf("argv = %q, want %q", gotArgs, want)
	}
	if fp.kills != 0 || fp.waits != 0 {
		t.Fatalf("process touched before Release: kills=%d waits=%d", fp.kills, fp.waits)
	}
	h.Release()
}

// Release kills and reaps caffeinate exactly once, however often it's called.
func TestCaffeinateReleaseOnce(t *testing.T) {
	fp := &fakeProc{}
	l := caffeinateLocker{spawn: func(string, ...string) (proc, error) { return fp, nil }}

	h := l.Acquire("double release")
	h.Release()
	h.Release()
	if fp.kills != 1 || fp.waits != 1 {
		t.Fatalf("after two Releases: kills=%d waits=%d, want 1 and 1", fp.kills, fp.waits)
	}
}

// A failed spawn (no caffeinate binary) degrades to a no-op Handle.
func TestCaffeinateSpawnFailureIsNoop(t *testing.T) {
	l := caffeinateLocker{spawn: func(string, ...string) (proc, error) {
		return nil, errors.New("exec: \"caffeinate\": executable file not found in $PATH")
	}}

	h := l.Acquire("spawn fails")
	if h == nil {
		t.Fatal("Acquire returned a nil Handle")
	}
	h.Release() // must not panic
	h.Release()
}

// The real exec seam starts, kills, and reaps a live process; a stand-in
// long-running binary plays caffeinate.
func TestCaffeinateExecSpawnKillsAndReaps(t *testing.T) {
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Skip("no sleep on PATH")
	}
	var p proc
	l := caffeinateLocker{spawn: func(_ string, _ ...string) (proc, error) {
		var err error
		p, err = execSpawn(sleep, "60")
		return p, err
	}}

	h := l.Acquire("real exec")
	done := make(chan struct{})
	go func() { h.Release(); h.Release(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Release did not kill and reap the process")
	}
	if cp := p.(cmdProc); cp.cmd.ProcessState == nil {
		t.Fatal("process not reaped after Release")
	}
}

// The real exec seam's start failure (missing binary) degrades to a no-op.
func TestCaffeinateExecSpawnMissingBinary(t *testing.T) {
	l := caffeinateLocker{spawn: func(_ string, args ...string) (proc, error) {
		return execSpawn("/nonexistent/mads-caffeinate", args...)
	}}
	l.Acquire("missing binary").Release()
}
