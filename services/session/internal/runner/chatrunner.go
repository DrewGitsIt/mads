package runner

import (
	"context"
	"errors"
	"log"
	"time"

	"github.com/drewkidwell/mads/providers/provider"
)

// The runner's side of the v2 chat stream (chatstream.go, settle.go): where
// sends, turn ends and the process's exit reach it.

// SetChatLimits overrides the v2 journal's bounds and settle pacing. Call
// before Start; zero fields keep their defaults.
func (r *Runner) SetChatLimits(l ChatLimits) { r.chat.setLimits(l) }

// SubscribeChat opens a v2 stream connection (GET /output?v=2). last is the
// client's Last-Event-ID (or ?after), "" for none.
func (r *Runner) SubscribeChat(last string) ChatSubscription {
	r.mu.Lock()
	h := chatHello{
		Provider:    r.providerName,
		TurnActive:  r.busy,
		StartedAt:   r.turnStartedAt,
		Interrupted: r.interrupted,
		Queued:      r.queuedLocked(),
	}
	r.mu.Unlock()
	h.Keyed = r.chat.isKeyed()
	return r.chat.subscribe(last, h)
}

// sendTurn hands wire to the provider as the send with message id id,
// announcing it first as user_message frames (so they precede anything the
// turn streams). A session that stores mads's id gets it (KeyedSender);
// otherwise the stored row is labelled on settle. queued marks a delivery
// from the queue.
func (r *Runner) sendTurn(ctx context.Context, sess provider.Session, wire, id string, queued bool) error {
	if id == "" {
		id = newMessageID()
	}
	keys := r.chat.userMessage(id, wire, queued, time.Now())
	r.labeler.expect("user:"+id, wire)
	var err error
	if ks, ok := sess.(provider.KeyedSender); ok {
		err = ks.SendKeyed(ctx, wire, id)
	} else {
		err = sess.Send(ctx, wire)
	}
	if err != nil {
		r.labeler.forget("user:" + id)
		r.chat.sendFailed(keys)
		return err
	}
	r.scheduleSettle()
	return nil
}

// startLabelling turns user-row labelling on for a session that can't
// store mads's id (no KeyedSender) on a provider with stable keys, reading
// the store as it stands first: those rows predate this child and keep
// their own keys.
func (r *Runner) startLabelling(ctx context.Context, p provider.Provider, sess provider.Session, opts provider.StartOptions) {
	if _, ok := sess.(provider.KeyedSender); ok || !p.Matrix()[provider.BehaviorStableKeys].Works() {
		return
	}
	var existing []provider.Message
	ref := sess.Ref()
	if ref == "" {
		ref = opts.Resume
	}
	if reader, ok := p.(provider.TranscriptReader); ok && ref != "" {
		msgs, err := reader.Transcript(ctx, ref)
		if err != nil && !errors.Is(err, provider.ErrUnsupported) {
			log.Printf("runner: read %s before labelling: %v", r.sessionID, err)
		}
		existing = msgs
	}
	r.labeler.activate(existing)
}

// chatProcessExit: the provider process ended mid-turn (a crash, or
// Shutdown). One last look at the store, then every item still unsettled
// is declared unsaved — the agent will never store what it hadn't.
func (r *Runner) chatProcessExit() {
	r.settleNow(context.Background())
	r.chat.markUnsaved(0, true)
}

// SubscribeTerminalAcked is SubscribeTerminal for a writer that calls
// written after each event it puts on the wire, so DrainStreams can wait
// for it (GET /output).
func (r *Runner) SubscribeTerminalAcked() (replay []byte, ch <-chan provider.Event, written, unsubscribe func()) {
	return r.hub.subscribeAcked(true)
}

// DrainStreams waits, up to timeout, until every connected /output writer
// (legacy and v2) has put on the wire what it was handed — the provider-
// died path's last frames (unsaved, the error) before the process exits.
// It reports whether everything was written.
func (r *Runner) DrainStreams(timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		if r.hub.undelivered() == 0 && r.chat.undelivered() == 0 {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(5 * time.Millisecond)
	}
}
