package codexcli

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

// proc is one row of the process table: enough to walk a tree, and to
// recognise the same process again later. start is ps's lstart — the
// process's start time — which together with pid and pgid is what makes a
// later row "the same process" rather than a reused pid.
type proc struct {
	pid, ppid, pgid int
	start           string
}

// listProcs reads the whole process table with POSIX ps (macOS and Linux
// procps both take -A -o and lstart). LC_ALL=C pins lstart's format; it is
// only ever compared with itself.
func listProcs() ([]proc, error) {
	cmd := exec.Command("ps", "-A", "-o", "pid=,ppid=,pgid=,lstart=")
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	out, err := cmd.Output()
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
		if len(f) < 4 {
			continue
		}
		pid, err1 := strconv.Atoi(f[0])
		ppid, err2 := strconv.Atoi(f[1])
		pgid, err3 := strconv.Atoi(f[2])
		if err1 != nil || err2 != nil || err3 != nil {
			continue
		}
		procs = append(procs, proc{pid: pid, ppid: ppid, pgid: pgid, start: strings.Join(f[3:], " ")})
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

// reaper remembers the processes it has seen descending from one
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
//
// Never the wrong process:
//   - A snapshot only walks the tree while app-server is unreaped: the
//     listing runs under mu, and conn.wait takes mu (markExited) before
//     cmd.Wait, so the root pid cannot be reused mid-listing, and no
//     snapshot runs once it is.
//   - Each remembered process carries its start time; it is signalled only
//     if a fresh listing shows the same pid with the same pgid AND start.
//   - Every listing prunes what is gone or no longer the same process.
type reaper struct {
	root int
	list func() ([]proc, error)                  // listProcs
	kill func(pid int, sig syscall.Signal) error // syscall.Kill

	mu      sync.Mutex
	seen    map[int]proc
	exited  bool // app-server has been (or is being) waited: never walk its pid again
	pending bool // an async snapshot is scheduled
}

func newReaper(root int) *reaper {
	return &reaper{root: root, list: listProcs, kill: syscall.Kill, seen: map[int]proc{}}
}

// markExited is called just before app-server is waited (reaped by the
// OS): after it the root pid may belong to anyone.
func (r *reaper) markExited() {
	r.mu.Lock()
	r.exited = true
	r.mu.Unlock()
}

// snapshot remembers the app-server's current descendants and forgets what
// has since gone or changed.
func (r *reaper) snapshot() {
	if r.root <= 1 {
		return // no process of ours (a test's stub conn): never walk init's tree
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.exited {
		return
	}
	procs, err := r.list()
	if err != nil {
		return
	}
	r.pruneLocked(procs)
	for _, p := range descendantsOf(r.root, procs) {
		r.seen[p.pid] = p
	}
}

// pruneLocked drops every remembered process the listing no longer shows
// as the same process.
func (r *reaper) pruneLocked(procs []proc) {
	alive := make(map[int]proc, len(procs))
	for _, p := range procs {
		alive[p.pid] = p
	}
	for pid, was := range r.seen {
		if now, ok := alive[pid]; !ok || !sameProc(was, now) {
			delete(r.seen, pid)
		}
	}
}

func sameProc(a, b proc) bool {
	return a.pid == b.pid && a.pgid == b.pgid && a.start == b.start && a.start != ""
}

// snapshotSoon schedules one snapshot after delay — long enough for a
// command codex just announced to have actually been spawned. Calls while
// one is already scheduled coalesce.
func (r *reaper) snapshotSoon(delay time.Duration) {
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
		r.snapshot()
	})
}

// reap kills every remembered descendant that is still the process it was
// (sameProc against a fresh listing). A group leader is killed as a group,
// so whatever the tool itself started dies with it. pid ≤ 1, init's group
// and the caller's own group are never signalled.
func (r *reaper) reap() {
	if r.root <= 1 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.seen) == 0 {
		return
	}
	procs, err := r.list()
	if err != nil {
		return
	}
	r.pruneLocked(procs)
	own := syscall.Getpgrp()
	for _, p := range r.seen {
		if p.pid <= 1 {
			continue
		}
		if p.pgid == p.pid && p.pgid > 1 && p.pgid != own {
			_ = r.kill(-p.pgid, syscall.SIGKILL)
		}
		_ = r.kill(p.pid, syscall.SIGKILL)
	}
	r.seen = map[int]proc{}
}
