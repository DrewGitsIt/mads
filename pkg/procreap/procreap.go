// Package procreap kills what an agent process left behind: its process
// group, and the descendants that left that group. It is the codexcli
// reaper (providers/codexcli/reap.go on the codex branch) lifted into a
// package any adapter can use without importing another adapter; codexcli
// can switch to it unchanged.
//
// Why a group is not enough: harnesses put the commands, MCP servers and
// kernels they spawn into sessions of their own (codex for every tool;
// hermes for background processes, MCP servers and code kernels —
// tools/process_registry.py:1246, tools/mcp_tool.py:594,
// tools/code_kernel.py:642, all start_new_session=True). Killing the
// agent's group misses exactly those, and once the agent exits they
// reparent to init, so the tree that tied them to it is gone: the reaper
// has to have looked before.
//
// Never the wrong process:
//   - A snapshot only walks the tree while the root is unreaped: listings
//     run under mu, and MarkExited (called before the root is waited)
//     takes mu, so the root pid can't be reused mid-walk, and no snapshot
//     runs once it is.
//   - Each remembered process carries its start time; it is signalled only
//     if a fresh listing shows the same pid with the same pgid AND start.
//   - Every listing prunes what is gone or no longer the same process.
package procreap

import (
	"bufio"
	"bytes"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Proc is one row of the process table: enough to walk a tree and to
// recognise the same process again later. Start is ps's lstart.
type Proc struct {
	PID, PPID, PGID int
	Start           string
}

// ListProcs reads the whole process table with POSIX ps (macOS and Linux
// procps both take -A -o and lstart). LC_ALL=C pins lstart's format; it is
// only ever compared with itself.
func ListProcs() ([]Proc, error) {
	cmd := exec.Command("ps", "-A", "-o", "pid=,ppid=,pgid=,lstart=")
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	return ParseProcs(out), nil
}

// ParseProcs parses ListProcs' ps output.
func ParseProcs(out []byte) []Proc {
	var procs []Proc
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 4 {
			continue
		}
		pid, err1 := strconv.Atoi(f[0])
		ppid, err2 := strconv.Atoi(f[1])
		pgid, err3 := strconv.Atoi(f[2])
		if err1 != nil || err2 != nil || err3 != nil {
			continue
		}
		procs = append(procs, Proc{PID: pid, PPID: ppid, PGID: pgid, Start: strings.Join(f[3:], " ")})
	}
	return procs
}

// DescendantsOf walks procs from root (exclusive).
func DescendantsOf(root int, procs []Proc) []Proc {
	children := map[int][]Proc{}
	for _, p := range procs {
		children[p.PPID] = append(children[p.PPID], p)
	}
	var out []Proc
	queue := []int{root}
	seen := map[int]bool{root: true}
	for len(queue) > 0 {
		pid := queue[0]
		queue = queue[1:]
		for _, c := range children[pid] {
			if seen[c.PID] {
				continue
			}
			seen[c.PID] = true
			out = append(out, c)
			queue = append(queue, c.PID)
		}
	}
	return out
}

// Reaper remembers the processes it has seen descending from one root
// process, so they can be killed once the root is gone.
type Reaper struct {
	root int
	// List and Kill are seams for tests; New sets ListProcs/syscall.Kill.
	List func() ([]Proc, error)
	Kill func(pid int, sig syscall.Signal) error

	mu      sync.Mutex
	seen    map[int]Proc
	exited  bool // the root has been (or is being) waited: never walk its pid again
	pending bool // an async snapshot is scheduled
}

// New returns a reaper for the process root.
func New(root int) *Reaper {
	return &Reaper{root: root, List: ListProcs, Kill: syscall.Kill, seen: map[int]Proc{}}
}

// MarkExited must be called just before the root is waited (reaped by the
// OS): after it the root pid may belong to anyone.
func (r *Reaper) MarkExited() {
	r.mu.Lock()
	r.exited = true
	r.mu.Unlock()
}

// Snapshot remembers the root's current descendants and forgets what has
// since gone or changed.
func (r *Reaper) Snapshot() {
	if r.root <= 1 {
		return // never walk init's tree
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.exited {
		return
	}
	procs, err := r.List()
	if err != nil {
		return
	}
	r.pruneLocked(procs)
	for _, p := range DescendantsOf(r.root, procs) {
		r.seen[p.PID] = p
	}
}

func (r *Reaper) pruneLocked(procs []Proc) {
	alive := make(map[int]Proc, len(procs))
	for _, p := range procs {
		alive[p.PID] = p
	}
	for pid, was := range r.seen {
		if now, ok := alive[pid]; !ok || !sameProc(was, now) {
			delete(r.seen, pid)
		}
	}
}

func sameProc(a, b Proc) bool {
	return a.PID == b.PID && a.PGID == b.PGID && a.Start == b.Start && a.Start != ""
}

// SnapshotSoon schedules one snapshot after delay — long enough for a
// command the agent just announced to have been spawned. Calls while one
// is already scheduled coalesce.
func (r *Reaper) SnapshotSoon(delay time.Duration) {
	if r.root <= 1 {
		return
	}
	r.mu.Lock()
	if r.pending || r.exited {
		r.mu.Unlock()
		return
	}
	r.pending = true
	r.mu.Unlock()
	time.AfterFunc(delay, func() {
		r.mu.Lock()
		r.pending = false
		r.mu.Unlock()
		r.Snapshot()
	})
}

// Seen reports how many processes the reaper currently remembers.
func (r *Reaper) Seen() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.seen)
}

// Reap kills every remembered descendant that is still the process it was
// (sameProc against a fresh listing). A group leader is killed as a group,
// so whatever it started dies with it. pid ≤ 1, init's group and the
// caller's own group are never signalled.
func (r *Reaper) Reap() {
	if r.root <= 1 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.seen) == 0 {
		return
	}
	procs, err := r.List()
	if err != nil {
		return
	}
	r.pruneLocked(procs)
	own := syscall.Getpgrp()
	for _, p := range r.seen {
		if p.PID <= 1 {
			continue
		}
		if p.PGID == p.PID && p.PGID > 1 && p.PGID != own {
			_ = r.Kill(-p.PGID, syscall.SIGKILL)
		}
		_ = r.Kill(p.PID, syscall.SIGKILL)
	}
	r.seen = map[int]Proc{}
}

// KillGroup SIGKILLs the process group pgid — the root's own, when it was
// started with Setpgid. Call it before the root is waited: an unreaped
// root still holds its pid, so the group id can't have been reused. Never
// init's group or the caller's own.
func KillGroup(pgid int) {
	if pgid <= 1 || pgid == syscall.Getpgrp() {
		return
	}
	_ = syscall.Kill(-pgid, syscall.SIGKILL)
}
