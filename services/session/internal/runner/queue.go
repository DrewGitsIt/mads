package runner

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/providers/provider"
)

// SendOrQueue relays one user turn, or — if a turn is already in flight,
// or a Land is running — queues it behind that, FIFO with any queued
// injections, for onTurnEnd (or endLanding) to deliver. It returns the
// queued entry when it queued, nil when it sent.
//
// The queue lives here rather than in the UI so a message queued in one
// session is still delivered after the user navigates to another (or
// reloads): the conversation pane used to hold it in component state and
// dropped it on unmount, never sent.
func (r *Runner) SendOrQueue(ctx context.Context, text string) (*contracts.QueuedMessage, error) {
	// Two passes at most: the turn can go in flight between the busy check
	// here and Send's own, in which case Send refuses and the second pass
	// queues.
	for attempt := 0; ; attempt++ {
		r.mu.Lock()
		if r.sess == nil {
			r.mu.Unlock()
			return nil, ErrNotReady
		}
		if r.busy || r.landing {
			return r.queueUserLocked(text), nil
		}
		r.mu.Unlock()
		err := r.Send(ctx, text)
		if errors.Is(err, provider.ErrTurnInFlight) {
			// The harness itself refused: it has a turn running that this
			// runner never marked (every runner-started turn, compaction
			// passes included, sets busy — this is the backstop). That
			// turn's own EventTurnEnd reaches onTurnEnd, which delivers
			// the queue, so queueing is right even though busy is false.
			r.mu.Lock()
			return r.queueUserLocked(text), nil
		}
		if errors.Is(err, ErrTurnInFlight) && attempt == 0 {
			continue
		}
		return nil, err
	}
}

// queueUserLocked appends one user message to the queue and announces the
// new queue. Called with r.mu held; it unlocks r.mu.
func (r *Runner) queueUserLocked(text string) *contracts.QueuedMessage {
	q := contracts.QueuedMessage{ID: newQueueID(), Text: text, QueuedAt: time.Now().UTC()}
	r.pending = append(r.pending, pendingInjection{text: text, userID: q.ID, queuedAt: q.QueuedAt})
	snapshot := r.queuedLocked()
	r.mu.Unlock()
	r.publishQueue(contracts.QueueChanged{Queued: snapshot})
	return &q
}

// QueuedMessages returns the user's queued messages in delivery order. Never
// nil, so it encodes as [] rather than null.
func (r *Runner) QueuedMessages() []contracts.QueuedMessage {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.queuedLocked()
}

// CancelQueued removes one queued user message before it is delivered.
// Reports false if no such message is queued — it may already have gone out.
func (r *Runner) CancelQueued(id string) bool {
	r.mu.Lock()
	idx := -1
	for i, p := range r.pending {
		if p.userID != "" && p.userID == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		r.mu.Unlock()
		return false
	}
	r.pending = append(r.pending[:idx:idx], r.pending[idx+1:]...)
	snapshot := r.queuedLocked()
	r.mu.Unlock()
	r.publishQueue(contracts.QueueChanged{Queued: snapshot})
	return true
}

// queuedLocked snapshots the user entries of r.pending; linked-session
// injections share the slice for ordering but aren't the user's to see or
// cancel. Must be called with r.mu held.
func (r *Runner) queuedLocked() []contracts.QueuedMessage {
	out := []contracts.QueuedMessage{}
	for _, p := range r.pending {
		if p.userID != "" {
			out = append(out, contracts.QueuedMessage{ID: p.userID, Text: p.text, QueuedAt: p.queuedAt})
		}
	}
	return out
}

// publishQueue appends a SessionQueueChanged event. Best-effort, like the
// rest of the conversation log: the queue itself is the Runner's state, the
// event only tells an open conversation it moved.
func (r *Runner) publishQueue(change contracts.QueueChanged) {
	if change.Queued == nil {
		change.Queued = []contracts.QueuedMessage{}
	}
	r.appendConversation(contracts.SessionQueueChanged, change)
}

func newQueueID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return "q_" + hex.EncodeToString(b[:])
}
