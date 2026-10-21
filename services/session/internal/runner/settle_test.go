package runner

import (
	"context"
	"testing"
	"time"

	"github.com/drewkidwell/mads/providers/provider"
)

// EventSettle is opt-in (StartOptions.SettleHints, which the runner doesn't
// set yet), but a consumer that does see one must be unmoved by it: it
// neither ends nor opens a turn, and the turn still ends on its own
// EventTurnEnd. The kind carries no content, so nothing records it either.
func TestSettleHintIsHarmlessToTheRunner(t *testing.T) {
	ctx := context.Background()
	rig := startPeer(t)
	stream, unsub := rig.r.Subscribe()
	defer unsub()

	if err := rig.r.Send(ctx, "hello"); err != nil {
		t.Fatal(err)
	}
	rig.sess.events <- provider.Event{Kind: provider.EventText, Key: "m1:0", Text: "hi"}
	rig.sess.events <- provider.Event{Kind: provider.EventSettle, Key: "m1:0"}
	waitStream(t, stream, provider.EventSettle)
	if !rig.r.isBusy() {
		t.Fatal("a settle hint ended the turn")
	}

	rig.sess.events <- provider.Event{Kind: provider.EventTurnEnd}
	waitStream(t, stream, provider.EventTurnEnd)
	waitFor(t, "the turn to end", func() bool { return !rig.r.isBusy() })

	// A hint between turns opens nothing.
	rig.sess.events <- provider.Event{Kind: provider.EventSettle}
	waitStream(t, stream, provider.EventSettle)
	if rig.r.isBusy() {
		t.Fatal("a settle hint between turns opened one")
	}
}

func waitStream(t *testing.T, stream <-chan provider.Event, kind provider.EventKind) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case ev := <-stream:
			if ev.Kind == kind {
				return
			}
		case <-deadline:
			t.Fatalf("no %s on the stream", kind)
		}
	}
}
