package gitstate

import (
	"context"
	"sync"
)

// MaxPerSession caps the computes one session runs at once, so one
// session's parts can't take every slot.
const MaxPerSession = 2

// slots hands out the MaxConcurrent compute slots. Two lanes: a shown
// session's computes (read in the last ShownFor: a window has it open, or
// a trigger for such a session) go ahead of every other waiting one, and
// the others (the boot warm-up, a repo change's sweep over sessions
// nobody shows) never take the last slot, so a shown session's compute
// never waits behind them, however slow they are. Within a lane, first
// come first served, skipping a session already at MaxPerSession.
type slots struct {
	mu      sync.Mutex
	max     int
	running int
	low     int // running in the low lane
	per     map[string]int
	waiting []*waiter
}

type waiter struct {
	session string
	high    bool
	ready   chan struct{}
	granted bool
}

func newSlots(max int) *slots {
	return &slots{max: max, per: map[string]int{}}
}

// lowMax is how many slots the low lane may hold: all but one.
func (q *slots) lowMax() int { return max(q.max-1, 1) }

// acquire waits for a slot for a compute of session, in the high lane or
// not; false when ctx ended first (nothing to release).
func (q *slots) acquire(ctx context.Context, session string, high bool) bool {
	w := &waiter{session: session, high: high, ready: make(chan struct{})}
	q.mu.Lock()
	q.waiting = append(q.waiting, w)
	q.grantLocked()
	q.mu.Unlock()
	select {
	case <-w.ready:
		return true
	case <-ctx.Done():
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if w.granted {
		q.releaseLocked(w)
		return false
	}
	for i, o := range q.waiting {
		if o == w {
			q.waiting = append(q.waiting[:i], q.waiting[i+1:]...)
			break
		}
	}
	return false
}

// release frees the slot acquire handed out for w's session and lane.
func (q *slots) release(session string, high bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.releaseLocked(&waiter{session: session, high: high})
}

func (q *slots) releaseLocked(w *waiter) {
	q.running--
	if !w.high {
		q.low--
	}
	if q.per[w.session]--; q.per[w.session] <= 0 {
		delete(q.per, w.session)
	}
	q.grantLocked()
}

func (q *slots) grantLocked() {
	for q.running < q.max {
		w := q.nextLocked()
		if w == nil {
			return
		}
		w.granted = true
		q.running++
		if !w.high {
			q.low++
		}
		q.per[w.session]++
		close(w.ready)
	}
}

// nextLocked takes the first waiter that may run now: the high lane
// first, then the low one while it has room.
func (q *slots) nextLocked() *waiter {
	for _, high := range []bool{true, false} {
		if !high && q.low >= q.lowMax() {
			return nil
		}
		for i, w := range q.waiting {
			if w.high != high || q.per[w.session] >= MaxPerSession {
				continue
			}
			q.waiting = append(q.waiting[:i], q.waiting[i+1:]...)
			return w
		}
	}
	return nil
}
