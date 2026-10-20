package procreap

import (
	"os/exec"
	"sync"
	"syscall"
	"testing"
	"time"
)

// The unit table is codexcli's reap_test.go (codex branch), carried over
// with the reaper itself.

func TestDescendantsOf(t *testing.T) {
	procs := ParseProcs([]byte("    1     0     1 Tue Sep  8 10:49:28 2026\n  100     1   100 Tue Sep  8 10:50:21 2026\n  101   100   101 Tue Sep  8 10:50:22 2026\n  102   101   101 Tue Sep  8 10:50:23 2026\n  200     1   200 Tue Sep  8 10:50:24 2026\n garbage\n"))
	got := DescendantsOf(100, procs)
	if len(got) != 2 || got[0].PID != 101 || got[1].PID != 102 || got[1].PGID != 101 || got[1].Start != "Tue Sep 8 10:50:23 2026" {
		t.Fatalf("descendants = %+v", got)
	}
}

type fakeTable struct {
	mu     sync.Mutex
	procs  []Proc
	lists  int
	killed []int
}

func (f *fakeTable) list() ([]Proc, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lists++
	return append([]Proc(nil), f.procs...), nil
}

func (f *fakeTable) kill(pid int, _ syscall.Signal) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.killed = append(f.killed, pid)
	return nil
}

func (f *fakeTable) set(procs ...Proc) {
	f.mu.Lock()
	f.procs = procs
	f.mu.Unlock()
}

func scripted(f *fakeTable) *Reaper {
	r := New(4242)
	r.List, r.Kill = f.list, f.kill
	return r
}

func TestReapKillsTheProcessItSaw(t *testing.T) {
	f := &fakeTable{}
	f.set(Proc{4242, 1, 4242, "T0"}, Proc{5000, 4242, 5000, "T1"})
	r := scripted(f)
	r.Snapshot()
	f.set(Proc{5000, 1, 5000, "T1"}) // root gone, child reparented
	r.Reap()
	if got := f.killed; len(got) != 2 || got[0] != -5000 || got[1] != 5000 {
		t.Fatalf("killed %v, want the child's group and pid", got)
	}
}

func TestReapSkipsReusedPids(t *testing.T) {
	for name, now := range map[string]Proc{
		"new start time": {5000, 1, 5000, "T9"},
		"other group":    {5000, 1, 777, "T1"},
	} {
		t.Run(name, func(t *testing.T) {
			f := &fakeTable{}
			f.set(Proc{4242, 1, 4242, "T0"}, Proc{5000, 4242, 5000, "T1"})
			r := scripted(f)
			r.Snapshot()
			f.set(now)
			r.Reap()
			if len(f.killed) != 0 {
				t.Fatalf("signalled %v: a reused pid", f.killed)
			}
		})
	}
}

func TestSnapshotPrunesTheGone(t *testing.T) {
	f := &fakeTable{}
	f.set(Proc{4242, 1, 4242, "T0"}, Proc{5000, 4242, 5000, "T1"})
	r := scripted(f)
	r.Snapshot()
	f.set(Proc{4242, 1, 4242, "T0"})
	r.Snapshot()
	if n := r.Seen(); n != 0 {
		t.Fatalf("seen still holds %d gone processes", n)
	}
}

func TestNoSnapshotOnceExited(t *testing.T) {
	f := &fakeTable{}
	f.set(Proc{4242, 1, 4242, "T0"}, Proc{6000, 4242, 6000, "T2"})
	r := scripted(f)
	r.MarkExited()
	r.Snapshot()
	r.SnapshotSoon(0)
	time.Sleep(50 * time.Millisecond)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.lists != 0 {
		t.Fatalf("listed the process table %d times after exit", f.lists)
	}
}

func TestReapLeavesAnUnrelatedRealProcess(t *testing.T) {
	cmd := exec.Command("sleep", "30")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	pid := cmd.Process.Pid
	r := New(4242) // never walked: only Reap runs, against the real table
	r.seen[pid] = Proc{PID: pid, PPID: 4242, PGID: pid, Start: "Thu Jan  1 00:00:00 1970"}
	r.Reap()
	time.Sleep(100 * time.Millisecond)
	if err := syscall.Kill(pid, 0); err != nil {
		t.Fatalf("unrelated process %d was signalled: %v", pid, err)
	}
}

func TestReaperNeverWalksInitsTree(t *testing.T) {
	f := &fakeTable{}
	r := New(0)
	r.List, r.Kill = f.list, f.kill
	r.seen[1] = Proc{PID: 1, PPID: 0, PGID: 1, Start: "x"}
	r.Snapshot()
	r.SnapshotSoon(0)
	r.Reap()
	time.Sleep(50 * time.Millisecond)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.lists != 0 || len(f.killed) != 0 {
		t.Fatalf("a reaper with no process of its own listed %d times, killed %v", f.lists, f.killed)
	}
}

func TestKillGroupNeverOwnOrInit(t *testing.T) {
	KillGroup(0)
	KillGroup(1)
	KillGroup(syscall.Getpgrp()) // must not kill the test binary's own group
}
