package codexcli

import (
	"bufio"
	"bytes"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// proc is one row of the process table: enough to walk a tree and to
// recognise the same process again later.
type proc struct{ pid, ppid, pgid int }

// listProcs reads the whole process table with POSIX ps (macOS and Linux
// procps both take -A -o).
func listProcs() ([]proc, error) {
	out, err := exec.Command("ps", "-A", "-o", "pid=,ppid=,pgid=").Output()
	if err != nil {
		return nil, err
	}
	return parseProcs(out), nil
}

func parseProcs(out []byte) []proc {
	var procs []proc
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) != 3 {
			continue
		}
		pid, err1 := strconv.Atoi(f[0])
		ppid, err2 := strconv.Atoi(f[1])
		pgid, err3 := strconv.Atoi(f[2])
		if err1 != nil || err2 != nil || err3 != nil {
			continue
		}
		procs = append(procs, proc{pid: pid, ppid: ppid, pgid: pgid})
	}
	return procs
}

// descendantsOf walks procs from root (exclusive).
func descendantsOf(root int, procs []proc) []proc {
	children := map[int][]proc{}
	for _, p := range procs {
		children[p.ppid] = append(children[p.ppid], p)
	}
	var out []proc
	queue := []int{root}
	seen := map[int]bool{root: true}
	for len(queue) > 0 {
		pid := queue[0]
		queue = queue[1:]
		for _, c := range children[pid] {
			if seen[c.pid] {
				continue
			}
			seen[c.pid] = true
			out = append(out, c)
			queue = append(queue, c.pid)
		}
	}
	return out
}

// reaper remembers every process it has seen descending from one
// app-server, so they can be killed once the app-server is gone.
//
// Why not just the process group: codex puts each tool command, MCP server
// and helper it spawns into a group of its own (captured 2026-10-20 — the
// `sleep 37` a turn ran had pgid == its own pid while app-server had
// another), so killpg on app-server's group misses exactly the processes
// that outlive it. And once app-server exits they reparent to init, so the
// tree that tied them to this session is gone: the reaper has to have
// looked before. It looks when a tool starts (session.go, a short delay
// after commandExecution item/started) and at shutdown, and kills what it
// saw when the app-server has exited, however it exited.
type reaper struct {
	root int
	list func() ([]proc, error) // listProcs; a test counts calls

	mu      sync.Mutex
	seen    map[int]proc
	pending bool // an async snapshot is scheduled
}

func newReaper(root int) *reaper { return &reaper{root: root, list: listProcs, seen: map[int]proc{}} }

// snapshot adds the app-server's current descendants to what the reaper
// will kill. It never forgets one: a tool that finished is simply gone (or
// its pid reused, which reap's pgid check guards against).
func (r *reaper) snapshot() {
	if r.root <= 1 {
		return // no process of ours (a test's stub conn): never walk init's tree
	}
	procs, err := r.list()
	if err != nil {
		return
	}
	desc := descendantsOf(r.root, procs)
	r.mu.Lock()
	for _, p := range desc {
		r.seen[p.pid] = p
	}
	r.mu.Unlock()
}

// snapshotSoon schedules one snapshot after delay — long enough for a
// command codex just announced to have actually been spawned. Calls while
// one is already scheduled coalesce.
func (r *reaper) snapshotSoon(delay time.Duration) {
	if r.root <= 1 {
		return
	}
	r.mu.Lock()
	if r.pending {
		r.mu.Unlock()
		return
	}
	r.pending = true
	r.mu.Unlock()
	time.AfterFunc(delay, func() {
		r.mu.Lock()
		r.pending = false
		r.mu.Unlock()
		r.snapshot()
	})
}

// reap kills every remembered descendant that is still the process it was:
// same pid AND same process group (a reused pid would almost never land in
// the same group). A group leader is killed as a group, so whatever the
// tool itself started dies with it. The caller's own group and init's are
// never signalled.
func (r *reaper) reap() {
	if r.root <= 1 {
		return
	}
	r.mu.Lock()
	seen := make([]proc, 0, len(r.seen))
	for _, p := range r.seen {
		seen = append(seen, p)
	}
	r.mu.Unlock()
	if len(seen) == 0 {
		return
	}
	procs, err := r.list()
	if err != nil {
		return
	}
	alive := map[int]proc{}
	for _, p := range procs {
		alive[p.pid] = p
	}
	own := syscall.Getpgrp()
	for _, p := range seen {
		now, ok := alive[p.pid]
		if !ok || now.pgid != p.pgid || p.pid <= 1 {
			continue
		}
		if p.pgid == p.pid && p.pgid > 1 && p.pgid != own {
			_ = syscall.Kill(-p.pgid, syscall.SIGKILL)
		}
		_ = syscall.Kill(p.pid, syscall.SIGKILL)
	}
}
