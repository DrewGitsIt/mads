package runner

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/linkwrap"
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
//
// attached ride with the message either way (Send): a queued message keeps
// its attached updates and delivers them with its text.
func (r *Runner) SendOrQueue(ctx context.Context, text string, attached ...contracts.InjectPart) (*contracts.QueuedMessage, error) {
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
			return r.queueUserLocked(ctx, text, attached), nil
		}
		r.mu.Unlock()
		err := r.Send(ctx, text, attached...)
		if errors.Is(err, provider.ErrTurnInFlight) {
			// The harness itself refused: it has a turn running that this
			// runner never marked (every runner-started turn, compaction
			// passes included, sets busy — this is the backstop). That
			// turn's own EventTurnEnd reaches onTurnEnd, which delivers
			// the queue, so queueing is right even though busy is false.
			r.mu.Lock()
			return r.queueUserLocked(ctx, text, attached), nil
		}
		if errors.Is(err, ErrTurnInFlight) && attempt == 0 {
			continue
		}
		return nil, err
	}
}

// queueUserLocked appends one user message (with any attached updates) to
// the queue and announces the new queue. The attached updates are audited
// now, as a queued InjectParts is: they are accepted. Called with r.mu held;
// it unlocks r.mu.
func (r *Runner) queueUserLocked(ctx context.Context, text string, attached []contracts.InjectPart) *contracts.QueuedMessage {
	q := contracts.QueuedMessage{ID: newQueueID(), Text: text, QueuedAt: time.Now().UTC()}
	r.pending = append(r.pending, pendingInjection{text: text, attached: attached, userID: q.ID, queuedAt: q.QueuedAt, msgID: newMessageID()})
	snapshot := r.queuedLocked()
	r.mu.Unlock()
	r.publishQueue(contracts.QueueChanged{Queued: snapshot})
	r.auditAttached(ctx, attached)
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
	if p := r.pending[idx]; len(p.attached) > 0 {
		// The updates it carried were drained from the mailbox for it;
		// cancelling the user's words mustn't drop them. They stay queued
		// in its place as an injection of their own (InjectParts' shape).
		r.pending[idx] = attachedAsInjection(p)
	} else {
		r.pending = append(r.pending[:idx:idx], r.pending[idx+1:]...)
	}
	snapshot := r.queuedLocked()
	r.mu.Unlock()
	r.publishQueue(contracts.QueueChanged{Queued: snapshot})
	return true
}

// attachedAsInjection is p's attached updates on their own, as a queued
// multi-part injection: each wrapped on its own (not flagged attached, since
// no human text follows), delivered as built.
func attachedAsInjection(p pendingInjection) pendingInjection {
	attribution := p.attached[0].Attribution
	texts := make([]string, len(p.attached))
	wires := make([]string, len(p.attached))
	for i, a := range p.attached {
		texts[i] = a.Text
		wires[i] = linkwrap.Wrap(a.Text, a.Attribution)
		attribution.DigestAttributed = attribution.DigestAttributed && a.Attribution.DigestAttributed
	}
	return pendingInjection{
		text: strings.Join(texts, "\n\n"), attribution: attribution, preWire: strings.Join(wires, "\n\n"),
		injectID: newQueueID(), queuedAt: p.queuedAt, msgID: newMessageID(),
	}
}

// queuedLocked snapshots r.pending in delivery order: the user's own
// messages and linked-session injections alike, the latter with their
// Attribution so the UI can label them (and offer no cancel — CancelQueued
// only matches user entries). Must be called with r.mu held.
func (r *Runner) queuedLocked() []contracts.QueuedMessage {
	out := []contracts.QueuedMessage{}
	for _, p := range r.pending {
		if q, ok := p.queued(); ok {
			out = append(out, q)
		}
	}
	return out
}

// queued is p as the queue API shows it; false for an entry with no id,
// which only a test builds.
func (p pendingInjection) queued() (contracts.QueuedMessage, bool) {
	if p.userID != "" {
		return contracts.QueuedMessage{ID: p.userID, Text: p.text, QueuedAt: p.queuedAt}, true
	}
	if p.injectID == "" {
		return contracts.QueuedMessage{}, false
	}
	a := p.attribution
	return contracts.QueuedMessage{ID: p.injectID, Text: p.text, QueuedAt: p.queuedAt, Attribution: &a}, true
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
