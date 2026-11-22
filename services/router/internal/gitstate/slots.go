package gitstate

import (
	"context"
	"sync"
	"time"
)

// MaxPerSession caps the computes one session runs at once, so one
// session's parts can't take every slot.
const MaxPerSession = 2

// DefaultPromoteAfter is how long a background compute waits before it
// is served like a shown session's, so a steady load of shown sessions
// can't starve it.
const DefaultPromoteAfter = 30 * time.Second

// slots hands out the MaxConcurrent compute slots. Two lanes, decided when
// a slot is granted (not when the compute queued): a shown session's
// computes (shown says: read in the last ShownFor) go ahead of every other
// waiting one, and the others (the boot warm-up, a repo change's sweep
// over sessions nobody shows) never take the last slot, so a shown
// session's compute never waits behind them, however slow they are. A
// background compute waiting longer than promoteAfter is served as a
// shown one. Within a lane, first come first served, skipping a session
// already at MaxPerSession.
type slots struct {
	mu           sync.Mutex
	max          int
	running      int
	low          int // running in the low lane
	per          map[string]int
	waiting      []*waiter
	shown        func(session string) bool
	now          func() time.Time
	promoteAfter time.Duration
}

type waiter struct {
	session string
	since   time.Time
	ready   chan struct{}
	granted bool
	high    bool // the lane it was granted in
}

func newSlots(max int, shown func(string) bool, now func() time.Time, promoteAfter time.Duration) *slots {
	return &slots{max: max, per: map[string]int{}, shown: shown, now: now, promoteAfter: promoteAfter}
}

// lowMax is how many slots the low lane may hold: all but one.
func (q *slots) lowMax() int { return max(q.max-1, 1) }

// grant is a held slot: release it when the compute ends.
type grant struct {
	session string
	high    bool
}

// acquire waits for a slot for a compute of session; false when ctx ended
// first (nothing to release).
func (q *slots) acquire(ctx context.Context, session string) (grant, bool) {
	w := &waiter{session: session, since: q.now(), ready: make(chan struct{})}
	q.mu.Lock()
	q.waiting = append(q.waiting, w)
	q.grantLocked()
	q.mu.Unlock()
	select {
	case <-w.ready:
		return grant{session: session, high: w.high}, true
	case <-ctx.Done():
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if w.granted {
		q.releaseLocked(grant{session: session, high: w.high})
		return grant{}, false
	}
	for i, o := range q.waiting {
		if o == w {
			q.waiting = append(q.waiting[:i], q.waiting[i+1:]...)
			break
		}
	}
	return grant{}, false
}

// release frees g's slot, and grants the next.
func (q *slots) release(g grant) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.releaseLocked(g)
}

// kick grants what may run now: after a read made a session shown.
func (q *slots) kick() {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.grantLocked()
}

func (q *slots) releaseLocked(g grant) {
	q.running--
	if !g.high {
		q.low--
	}
	if q.per[g.session]--; q.per[g.session] <= 0 {
		delete(q.per, g.session)
	}
	q.grantLocked()
}

func (q *slots) grantLocked() {
	for q.running < q.max {
		w, high := q.nextLocked()
		if w == nil {
			return
		}
		w.granted, w.high = true, high
		q.running++
		if !high {
			q.low++
		}
		q.per[w.session]++
		close(w.ready)
	}
}

// nextLocked takes the first waiter that may run now, and its lane: a
// shown session's (or a long-waiting one's) first, then the low lane's
// while it has room.
func (q *slots) nextLocked() (*waiter, bool) {
	now := q.now()
	isHigh := func(w *waiter) bool {
		return now.Sub(w.since) >= q.promoteAfter || q.shown != nil && q.shown(w.session)
	}
	for _, high := range []bool{true, false} {
		if !high && q.low >= q.lowMax() {
			return nil, false
		}
		for i, w := range q.waiting {
			if q.per[w.session] >= MaxPerSession || high && !isHigh(w) {
				continue
			}
			q.waiting = append(q.waiting[:i], q.waiting[i+1:]...)
			return w, high
		}
	}
	return nil, false
}
