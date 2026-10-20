package codexcli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/drewkidwell/mads/providers/provider"
)

// startSleeper runs the stand-in's "sleeper" turn: a `sleep 300` started in
// a process group of its own (what codex does with every tool command —
// testdata/captures/README.md "Process groups"), its pid written to a file.
func startSleeper(t *testing.T) (*session, int) {
	t.Helper()
	pidFile := filepath.Join(t.TempDir(), "sleeper.pid")
	s := startSession(t, fakeProvider(t, "MADS_FAKE_CODEX_PIDFILE="+pidFile), provider.StartOptions{})
	if err := s.Send(context.Background(), "sleeper"); err != nil {
		t.Fatal(err)
	}
	var pid int
	deadline := time.Now().Add(10 * time.Second)
	for pid == 0 {
		if time.Now().After(deadline) {
			t.Fatal("sleeper never started")
		}
		if b, err := os.ReadFile(pidFile); err == nil {
			pid, _ = strconv.Atoi(string(b))
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })

	// The trap this guards: the tool is NOT in app-server's group, so a
	// group kill alone would miss it.
	server := s.c.cmd.Process.Pid
	toolGroup, err := syscall.Getpgid(pid)
	if err != nil {
		t.Fatal(err)
	}
	serverGroup, _ := syscall.Getpgid(server)
	if toolGroup == serverGroup {
		t.Fatalf("sleeper shares app-server's group %d; the stand-in no longer models codex", serverGroup)
	}
	return s, pid
}

func waitGone(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("tool process %d survived", pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// Close mid-command: app-server exits on stdin EOF, and the command it
// left running in its own group is killed too.
func TestCloseReapsToolGrandchild(t *testing.T) {
	s, pid := startSleeper(t)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	waitGone(t, pid)
}

// kill -9 of app-server itself (the survey's run2-kill): the command is
// reparented to init, and only the reaper's snapshot — taken shortly after
// the command item started — still ties it to the session.
func TestCrashReapsToolGrandchild(t *testing.T) {
	s, pid := startSleeper(t)
	deadline := time.Now().Add(5 * time.Second)
	for {
		s.c.reaper.mu.Lock()
		_, seen := s.c.reaper.seen[pid]
		s.c.reaper.mu.Unlock()
		if seen {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("reaper never saw the tool process")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := s.c.cmd.Process.Signal(syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	for range s.Events() { // drains until the read loop closes it
	}
	waitGone(t, pid)
}

func TestDescendantsOf(t *testing.T) {
	procs := parseProcs([]byte("    1     0     1\n  100     1   100\n  101   100   101\n  102   101   101\n  200     1   200\n garbage\n"))
	got := descendantsOf(100, procs)
	if len(got) != 2 || got[0].pid != 101 || got[1].pid != 102 || got[1].pgid != 101 {
		t.Fatalf("descendants = %+v", got)
	}
}

func TestReaperNeverWalksInitsTree(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	r := newReaper(0)
	r.list = func() ([]proc, error) {
		mu.Lock()
		calls++
		mu.Unlock()
		return listProcs()
	}
	r.seen[1] = proc{pid: 1, ppid: 0, pgid: 1} // even a remembered init is never signalled
	r.snapshot()
	r.snapshotSoon(0)
	r.reap()
	time.Sleep(50 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if calls != 0 {
		t.Fatalf("a reaper with no process of its own listed processes %d times", calls)
	}
}
