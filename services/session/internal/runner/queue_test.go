package runner

import (
	"context"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/providers/fake"
	"github.com/drewkidwell/mads/providers/provider"
	"github.com/drewkidwell/mads/services/data/datatest"
)

func startFakeRunner(t *testing.T) (*Runner, *dataclient.Client, string) {
	t.Helper()
	ctx := context.Background()
	base := datatest.Start(t)
	sess := newSession(t, base)
	data := dataclient.New(base, sess.ID)
	reg := provider.NewRegistry()
	if err := reg.Register(fake.New()); err != nil {
		t.Fatalf("register fake: %v", err)
	}
	r := New(sess.ID, data, reg)
	if err := r.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = r.Shutdown(ctx) })
	return r, data, sess.ID
}

// awaitTurnEnd drains ch up to the next turn_end.
func awaitTurnEnd(t *testing.T, ch <-chan provider.Event) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				t.Fatal("event channel closed before turn_end")
			}
			if ev.Kind == provider.EventTurnEnd {
				return
			}
		case <-deadline:
			t.Fatal("timed out waiting for turn_end")
		}
	}
}

// TestSendOrQueueSendsWhenIdle: with no turn in flight it is a plain Send —
// nothing queued, nothing published.
func TestSendOrQueueSendsWhenIdle(t *testing.T) {
	r, data, id := startFakeRunner(t)
	ch, unsubscribe := r.Subscribe()
	defer unsubscribe()

	q, err := r.SendOrQueue(context.Background(), "echo hi")
	if err != nil || q != nil {
		t.Fatalf("SendOrQueue idle = (%+v, %v), want sent", q, err)
	}
	if got := drainText(t, ch); got != "you said hi" {
		t.Fatalf("reply = %q", got)
	}
	if got := eventsOfType[contracts.QueueChanged](t, data, id, contracts.SessionQueueChanged); len(got) != 0 {
		t.Errorf("idle send published %d queue events", len(got))
	}
}

// TestSendOrQueueDeliversAfterTurn: a message sent mid-turn waits, is listed
// and published, then goes out as its own ordinary user turn once the
// in-flight one ends — no UI involved, which is the point: the pane that
// queued it can be long gone.
func TestSendOrQueueDeliversAfterTurn(t *testing.T) {
	ctx := context.Background()
	r, data, id := startFakeRunner(t)
	ch, unsubscribe := r.Subscribe()
	defer unsubscribe()

	if err := r.Send(ctx, "slow"); err != nil {
		t.Fatalf("send: %v", err)
	}
	q, err := r.SendOrQueue(ctx, "echo queued")
	if err != nil || q == nil {
		t.Fatalf("SendOrQueue mid-turn = (%+v, %v), want queued", q, err)
	}
	if got := r.QueuedMessages(); len(got) != 1 || got[0].ID != q.ID || got[0].Text != "echo queued" {
		t.Fatalf("QueuedMessages = %+v", got)
	}

	if err := r.Interrupt(ctx); err != nil {
		t.Fatalf("interrupt: %v", err)
	}
	awaitTurnEnd(t, ch)
	if got := drainText(t, ch); got != "you said queued" {
		t.Fatalf("queued reply = %q, want %q", got, "you said queued")
	}
	if got := r.QueuedMessages(); len(got) != 0 {
		t.Errorf("queue after delivery = %+v, want empty", got)
	}

	changes := eventsOfType[contracts.QueueChanged](t, data, id, contracts.SessionQueueChanged)
	if len(changes) != 2 {
		t.Fatalf("queue events = %+v, want queued then delivered", changes)
	}
	if len(changes[0].Queued) != 1 || changes[0].Queued[0].ID != q.ID || changes[0].Delivered != nil {
		t.Errorf("first event = %+v, want the one queued entry", changes[0])
	}
	if len(changes[1].Queued) != 0 || changes[1].Delivered == nil || changes[1].Delivered.ID != q.ID {
		t.Errorf("second event = %+v, want it delivered and the queue empty", changes[1])
	}

	// Delivered as the user's own turn: no attribution rides it into the
	// transcript, so it can't render as a linked session's injection.
	msgs := waitForTranscriptLen(t, ctx, r, 3) // slow (interrupted, no reply), queued, its reply
	for _, m := range msgs {
		if m.Role == "user" && m.Attribution != nil {
			t.Errorf("user message %q carries attribution %+v", m.Text, m.Attribution)
		}
	}
}

// TestCancelQueued: a cancelled message never goes out, and cancelling one
// that isn't queued reports false.
func TestCancelQueued(t *testing.T) {
	ctx := context.Background()
	r, data, id := startFakeRunner(t)
	ch, unsubscribe := r.Subscribe()
	defer unsubscribe()

	if err := r.Send(ctx, "slow"); err != nil {
		t.Fatalf("send: %v", err)
	}
	first, _ := r.SendOrQueue(ctx, "echo first")
	second, _ := r.SendOrQueue(ctx, "echo second")
	if !r.CancelQueued(first.ID) {
		t.Fatal("CancelQueued(first) = false")
	}
	if r.CancelQueued(first.ID) || r.CancelQueued("q_nope") {
		t.Fatal("CancelQueued of a message not queued = true")
	}
	if got := r.QueuedMessages(); len(got) != 1 || got[0].ID != second.ID {
		t.Fatalf("queue after cancel = %+v, want only second", got)
	}

	if err := r.Interrupt(ctx); err != nil {
		t.Fatalf("interrupt: %v", err)
	}
	awaitTurnEnd(t, ch)
	if got := drainText(t, ch); got != "you said second" {
		t.Fatalf("next turn = %q, want the second message (the first was cancelled)", got)
	}
	changes := eventsOfType[contracts.QueueChanged](t, data, id, contracts.SessionQueueChanged)
	if len(changes) != 4 || len(changes[2].Queued) != 1 || changes[2].Queued[0].ID != second.ID {
		t.Fatalf("queue events = %+v, want queued, queued, cancelled, delivered", changes)
	}
}

// TestQueuedUserMessageStopsDigestCoalescing: a user message queued between
// two same-link digests keeps its place — the digests don't merge across it.
func TestQueuedUserMessageStopsDigestCoalescing(t *testing.T) {
	r := &Runner{}
	digest := contracts.Attribution{LinkID: "lnk_a", DigestAttributed: true}
	r.pending = []pendingInjection{
		{text: "d1", attribution: digest},
		{text: "mine", userID: "q_1"},
		{text: "d2", attribution: digest},
	}
	if got := r.drainNextLocked(); got.text != "d1" || got.userID != "" {
		t.Fatalf("first drain = %+v, want d1 alone", got)
	}
	if got := r.drainNextLocked(); got.text != "mine" || got.userID != "q_1" {
		t.Fatalf("second drain = %+v, want the user's message", got)
	}
	if got := r.drainNextLocked(); got.text != "d2" {
		t.Fatalf("third drain = %+v, want d2", got)
	}
}
