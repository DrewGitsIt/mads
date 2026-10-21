package runner

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/drewkidwell/mads/providers/provider"
)

// EventSettle is the v2 tail's alone: the legacy stream never carries one,
// and the runner is unmoved by it — it neither ends nor opens a turn, and
// the turn still ends on its own EventTurnEnd.
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
	rig.sess.events <- provider.Event{Kind: provider.EventText, Key: "m1:0", Text: " there"}
	if got := streamUntil(t, stream, provider.EventText, 2); slices.Contains(got, provider.EventSettle) {
		t.Fatalf("a settle hint reached the legacy stream: %v", got)
	}
	if !rig.r.isBusy() {
		t.Fatal("a settle hint ended the turn")
	}

	rig.sess.events <- provider.Event{Kind: provider.EventTurnEnd}
	waitStream(t, stream, provider.EventTurnEnd)
	waitFor(t, "the turn to end", func() bool { return !rig.r.isBusy() })

	// A hint between turns opens nothing.
	rig.sess.events <- provider.Event{Kind: provider.EventSettle}
	rig.sess.events <- provider.Event{Kind: provider.EventError, Text: "fence"}
	if got := streamUntil(t, stream, provider.EventError, 1); slices.Contains(got, provider.EventSettle) {
		t.Fatalf("a settle hint reached the legacy stream: %v", got)
	}
	if rig.r.isBusy() {
		t.Fatal("a settle hint between turns opened one")
	}
}

// streamUntil reads the stream until the nth event of kind, returning
// every kind seen.
func streamUntil(t *testing.T, stream <-chan provider.Event, kind provider.EventKind, n int) []provider.EventKind {
	t.Helper()
	var seen []provider.EventKind
	deadline := time.After(5 * time.Second)
	for {
		select {
		case ev := <-stream:
			seen = append(seen, ev.Kind)
			if ev.Kind == kind {
				if n--; n == 0 {
					return seen
				}
			}
		case <-deadline:
			t.Fatalf("no %s on the stream (saw %v)", kind, seen)
		}
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
