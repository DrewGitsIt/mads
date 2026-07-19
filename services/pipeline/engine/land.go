package engine

import "sync"

// landQueue is a FIFO mutex: acquire() blocks until every earlier caller has
// released, in the order they called acquire (spec: "Exactly one land
// attempt runs at a time... queue order is stable FIFO"). A bare sync.Mutex
// doesn't guarantee arrival order under contention, which is why this exists
// instead of one.
type landQueue struct {
	mu      sync.Mutex
	waiters []chan struct{}
}

// acquire takes a ticket at the back of the queue and blocks until it's at
// the front. The returned release func must be called exactly once to let
// the next waiter (if any) proceed.
func (q *landQueue) acquire() (release func()) {
	q.mu.Lock()
	ticket := make(chan struct{})
	front := len(q.waiters) == 0
	q.waiters = append(q.waiters, ticket)
	q.mu.Unlock()

	if !front {
		<-ticket
	}

	return func() {
		q.mu.Lock()
		q.waiters = q.waiters[1:]
		var next chan struct{}
		if len(q.waiters) > 0 {
			next = q.waiters[0]
		}
		q.mu.Unlock()
		if next != nil {
			close(next)
		}
	}
}
