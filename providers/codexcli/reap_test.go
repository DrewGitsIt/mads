package codexcli

import (
	"context"
	"errors"
	"os"
	"os/exec"
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
	procs := parseProcs([]byte("    1     0     1 Tue Sep  8 10:49:28 2026\n  100     1   100 Tue Sep  8 10:50:21 2026\n  101   100   101 Tue Sep  8 10:50:22 2026\n  102   101   101 Tue Sep  8 10:50:23 2026\n  200     1   200 Tue Sep  8 10:50:24 2026\n garbage\n"))
	got := descendantsOf(100, procs)
	if len(got) != 2 || got[0].pid != 101 || got[1].pid != 102 || got[1].pgid != 101 || got[1].start != "Tue Sep 8 10:50:23 2026" {
		t.Fatalf("descendants = %+v", got)
	}
}

// fakeTable is a scripted process table and a kill recorder.
type fakeTable struct {
	mu     sync.Mutex
	procs  []proc
	lists  int
	killed []int
}

func (f *fakeTable) list() ([]proc, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lists++
	return append([]proc(nil), f.procs...), nil
}

func (f *fakeTable) kill(pid int, _ syscall.Signal) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.killed = append(f.killed, pid)
	return nil
}

func (f *fakeTable) set(procs ...proc) {
	f.mu.Lock()
	f.procs = procs
	f.mu.Unlock()
}

func scriptedReaper(f *fakeTable) *reaper {
	r := newReaper(4242)
	r.list, r.kill = f.list, f.kill
	return r
}

// A tool the reaper saw is killed — as a group when it leads one.
func TestReapKillsTheProcessItSaw(t *testing.T) {
	f := &fakeTable{}
	f.set(proc{4242, 1, 4242, "T0"}, proc{5000, 4242, 5000, "T1"})
	r := scriptedReaper(f)
	r.snapshot()
	f.set(proc{5000, 1, 5000, "T1"}) // app-server gone, tool reparented
	r.reap()
	if got := f.killed; len(got) != 2 || got[0] != -5000 || got[1] != 5000 {
		t.Fatalf("killed %v, want the tool's group and pid", got)
	}
}

// pid reuse: the remembered pid now belongs to a different process — a
// new start time, or another group. Neither is signalled.
func TestReapSkipsReusedPids(t *testing.T) {
	for name, now := range map[string]proc{
		"new start time": {5000, 1, 5000, "T9"},
		"other group":    {5000, 1, 777, "T1"},
	} {
		t.Run(name, func(t *testing.T) {
			f := &fakeTable{}
			f.set(proc{4242, 1, 4242, "T0"}, proc{5000, 4242, 5000, "T1"})
			r := scriptedReaper(f)
			r.snapshot()
			f.set(now)
			r.reap()
			if len(f.killed) != 0 {
				t.Fatalf("signalled %v: a reused pid", f.killed)
			}
		})
	}
}

// A listing forgets what has gone, so a pid that vanishes and later comes
// back as something else is not remembered at all.
func TestSnapshotPrunesTheGone(t *testing.T) {
	f := &fakeTable{}
	f.set(proc{4242, 1, 4242, "T0"}, proc{5000, 4242, 5000, "T1"})
	r := scriptedReaper(f)
	r.snapshot()
	f.set(proc{4242, 1, 4242, "T0"})
	r.snapshot()
	r.mu.Lock()
	n := len(r.seen)
	r.mu.Unlock()
	if n != 0 {
		t.Fatalf("seen still holds %d gone processes", n)
	}
}

// Once app-server is being waited, its pid may be reused: no snapshot
// walks it again.
func TestNoSnapshotOnceExited(t *testing.T) {
	f := &fakeTable{}
	f.set(proc{4242, 1, 4242, "T0"}, proc{6000, 4242, 6000, "T2"})
	r := scriptedReaper(f)
	r.markExited()
	r.snapshot()
	r.snapshotSoon(0)
	time.Sleep(50 * time.Millisecond)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.lists != 0 {
		t.Fatalf("listed the process table %d times after exit", f.lists)
	}
}

// The real table: a live, unrelated process whose remembered start time
// does not match is left alone.
func TestReapLeavesAnUnrelatedRealProcess(t *testing.T) {
	cmd := exec.Command("sleep", "30")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	pid := cmd.Process.Pid
	r := newReaper(4242) // never walked: only reap runs, against the real table
	r.seen[pid] = proc{pid: pid, ppid: 4242, pgid: pid, start: "Thu Jan  1 00:00:00 1970"}
	r.reap()
	time.Sleep(100 * time.Millisecond)
	if err := syscall.Kill(pid, 0); err != nil {
		t.Fatalf("unrelated process %d was signalled: %v", pid, err)
	}
}

func TestReaperNeverWalksInitsTree(t *testing.T) {
	f := &fakeTable{}
	r := newReaper(0)
	r.list, r.kill = f.list, f.kill
	r.seen[1] = proc{pid: 1, ppid: 0, pgid: 1, start: "x"} // even a remembered init is never signalled
	r.snapshot()
	r.snapshotSoon(0)
	r.reap()
	time.Sleep(50 * time.Millisecond)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.lists != 0 || len(f.killed) != 0 {
		t.Fatalf("a reaper with no process of its own listed %d times, killed %v", f.lists, f.killed)
	}
}
