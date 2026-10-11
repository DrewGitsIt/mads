package shellwatch

import (
	"context"
	"fmt"
	"log"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// SlowAfter is how long one look may take before the tracker stops looking
// after every command and looks once per turn instead. Attribution is by
// modification time, so a later look still credits each command; only the
// header count arrives later. Rechecked on every look, so a cold first look
// doesn't slow a session for good.
const SlowAfter = 1500 * time.Millisecond

// Recorder takes one look's findings: paths relative to top. It runs on the
// tracker's goroutine, never the caller's.
type Recorder func(ctx context.Context, top string, changes []Change)

// Tracker follows one session's shell calls in one working tree. Its
// methods only take a lock and note a time, so the pump can call them on
// every event; the looking happens on one goroutine of its own.
type Tracker struct {
	dir    string
	record Recorder
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu      sync.Mutex
	top     string
	open    map[string]openCall
	closed  []Window
	prev    *Snapshot
	claimed map[string]time.Time // path → the At last recorded for it
	running bool
	again   bool
	stopped bool
	slow    bool
	seq     int
}

type openCall struct {
	start    time.Time
	delegate bool
}

// New starts a tracker for the working tree containing dir, taking its
// first look in the background — the baseline deletions and moved-in files
// are found against.
func New(dir string, record Recorder) *Tracker {
	ctx, cancel := context.WithCancel(context.Background())
	t := &Tracker{dir: dir, record: record, ctx: ctx, cancel: cancel,
		open: map[string]openCall{}, claimed: map[string]time.Time{}}
	t.mu.Lock()
	t.kickLocked()
	t.mu.Unlock()
	return t
}

// Started opens a window for a shell call that may write files. delegate
// marks a subagent's call, which can outlive the turn that started it. An
// empty id (a harness with none) stays open until the turn ends.
func (t *Tracker) Started(id string, at time.Time, delegate bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.stopped {
		return
	}
	if id == "" {
		t.seq++
		id = fmt.Sprintf("\x00anon-%d", t.seq)
	}
	if _, ok := t.open[id]; !ok {
		t.open[id] = openCall{start: at, delegate: delegate}
	}
}

// Finished closes a call's window and looks, unless the repo is slow. A
// result for a call no window was opened for (a read-only command, any
// other tool) is ignored.
func (t *Tracker) Finished(id string, at time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	c, ok := t.open[id]
	if !ok {
		return
	}
	delete(t.open, id)
	t.closed = append(t.closed, Window{Start: c.start, End: at})
	if !t.slow {
		t.kickLocked()
	}
}

// TurnEnded closes the main agent's calls still open — interrupted, or run
// in the background — and looks. A subagent's call stays open: its result
// can still come.
func (t *Tracker) TurnEnded(at time.Time) {
	t.closeOpen(at, false)
}

// ProcessEnded closes every call still open, subagents' included, and looks.
func (t *Tracker) ProcessEnded(at time.Time) {
	t.closeOpen(at, true)
}

func (t *Tracker) closeOpen(at time.Time, all bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for id, c := range t.open {
		if c.delegate && !all {
			continue
		}
		delete(t.open, id)
		t.closed = append(t.closed, Window{Start: c.start, End: at})
	}
	if len(t.closed) > 0 {
		t.kickLocked()
	}
}

// Stop takes no more calls and waits for a look in flight to finish and
// record, up to ctx — then abandons it.
func (t *Tracker) Stop(ctx context.Context) {
	t.mu.Lock()
	t.stopped = true
	t.mu.Unlock()
	done := make(chan struct{})
	go func() {
		t.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
		t.cancel()
		<-done
	}
	t.cancel()
}

// kickLocked starts a look, or, with one running, asks it to go again when
// it finishes: any number of calls ending during a look cost one more.
func (t *Tracker) kickLocked() {
	if t.running {
		t.again = true
		return
	}
	if t.stopped {
		return
	}
	t.running = true
	t.wg.Add(1)
	go t.loop()
}

func (t *Tracker) loop() {
	defer t.wg.Done()
	for {
		t.look()
		t.mu.Lock()
		if !t.again || t.ctx.Err() != nil {
			t.running = false
			t.mu.Unlock()
			return
		}
		t.again = false
		t.mu.Unlock()
	}
}

// look takes one snapshot and records what the closed windows (and the
// still-open ones, so far) changed.
func (t *Tracker) look() {
	t.mu.Lock()
	closed := t.closed
	t.closed = nil
	windows := append([]Window(nil), closed...)
	for _, c := range t.open {
		windows = append(windows, Window{Start: c.start})
	}
	prev, top := t.prev, t.top
	t.mu.Unlock()

	if top == "" {
		var err error
		if top, err = toplevel(t.ctx, t.dir); err != nil {
			log.Printf("shellwatch: %s: %v", t.dir, err)
			return
		}
		t.mu.Lock()
		t.top = top
		t.mu.Unlock()
	}
	cur, err := Take(t.ctx, top)
	if err != nil {
		if t.ctx.Err() == nil {
			log.Printf("shellwatch: %v", err)
		}
		t.mu.Lock()
		t.closed = append(closed, t.closed...) // try them again next look
		t.mu.Unlock()
		return
	}

	var changes []Change
	if len(windows) > 0 {
		t.mu.Lock()
		for _, c := range Attribute(prev, cur, windows) {
			if at, ok := t.claimed[c.Path]; ok && at.Equal(c.At) {
				continue // already recorded by an earlier look
			}
			changes = append(changes, c)
		}
		t.mu.Unlock()
	}
	if len(changes) > 0 {
		t.record(t.ctx, top, changes)
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	for _, c := range changes {
		t.claimed[c.Path] = c.At
	}
	for path := range t.claimed {
		if _, ok := cur.Entries[path]; !ok {
			delete(t.claimed, path) // committed or reverted since
		}
	}
	t.prev = cur
	if slow := cur.Took > SlowAfter; slow != t.slow {
		t.slow = slow
		if slow {
			log.Printf("shellwatch: git status in %s took %s; looking once per turn instead of after every command", top, cur.Took.Round(time.Millisecond))
		} else {
			log.Printf("shellwatch: git status in %s is fast again (%s); looking after every command", top, cur.Took.Round(time.Millisecond))
		}
	}
}

func toplevel(ctx context.Context, dir string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "--show-toplevel")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("find working tree top: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}
