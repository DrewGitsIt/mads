package runner

import (
	"testing"

	"github.com/drewkidwell/mads/providers/provider"
)

// TestHubPTYReplayCapBounded proves the ring buffer never grows past
// ptyReplayCap and, once full, keeps only the most recently written bytes —
// the "bounded server-side ring buffer... size on the order of a few MB"
// acceptance criterion.
func TestHubPTYReplayCapBounded(t *testing.T) {
	h := newHub()
	big := make([]byte, ptyReplayCap+1000)
	for i := range big {
		big[i] = byte('a' + i%26)
	}
	h.broadcast(provider.Event{Kind: provider.EventPTYOutput, Text: string(big)})

	replay, _, unsubscribe := h.subscribeWithPTYReplay()
	defer unsubscribe()
	if len(replay) != ptyReplayCap {
		t.Fatalf("replay len = %d, want cap %d", len(replay), ptyReplayCap)
	}
	if string(replay) != string(big[len(big)-ptyReplayCap:]) {
		t.Fatal("replay does not match the most recently written bytes")
	}
}

// TestHubPTYReplaySnapshotAtSubscribeTime proves subscribeWithPTYReplay's
// snapshot-then-subscribe is atomic: a subscriber sees every byte exactly
// once, split between the replay and the live channel at the moment it
// attached, never duplicated and never dropped in the gap between the two.
func TestHubPTYReplaySnapshotAtSubscribeTime(t *testing.T) {
	h := newHub()
	h.broadcast(provider.Event{Kind: provider.EventPTYOutput, Text: "hello "})

	replay, ch, unsubscribe := h.subscribeWithPTYReplay()
	defer unsubscribe()
	if string(replay) != "hello " {
		t.Fatalf("replay = %q, want %q", replay, "hello ")
	}

	h.broadcast(provider.Event{Kind: provider.EventPTYOutput, Text: "world"})
	select {
	case ev := <-ch:
		if ev.Text != "world" {
			t.Fatalf("live event text = %q, want %q", ev.Text, "world")
		}
	default:
		t.Fatal("expected the post-subscribe broadcast to land on the live channel")
	}
}

// TestHubPTYReplayEmptyForChatEvents proves the replay buffer only ever
// captures EventPTYOutput, never EventText — the mechanism that keeps
// terminal replay transparent to chat-mode consumers without a query param.
func TestHubPTYReplayEmptyForChatEvents(t *testing.T) {
	h := newHub()
	h.broadcast(provider.Event{Kind: provider.EventText, Text: "chat text"})

	replay, _, unsubscribe := h.subscribeWithPTYReplay()
	defer unsubscribe()
	if replay != nil {
		t.Fatalf("replay = %q, want nil for a chat-only session", replay)
	}
}

// TestHubChatOverflowClosesSubscriber: a slow consumer whose buffer fills
// must not silently lose chat frames (a swallowed turn_end froze the
// conversation pane until remount — observed live 2026-09-09). The hub
// closes the subscription instead, which the SSE handler surfaces as a
// stream end → client reconnect → transcript refetch.
func TestHubChatOverflowClosesSubscriber(t *testing.T) {
	h := newHub()
	ch, unsubscribe := h.subscribe()
	defer unsubscribe()
	for i := 0; i < cap(ch)+1; i++ {
		h.broadcast(provider.Event{Kind: provider.EventText, Text: "x"})
	}
	drained := 0
	for range ch { // closed by the overflowing broadcast
		drained++
	}
	if drained != cap(ch) {
		t.Errorf("drained %d frames, want the full buffer %d", drained, cap(ch))
	}
}

// TestHubPTYOverflowKeepsSubscriber: PTY floods stay silently droppable —
// the ring buffer replays them on the next attach, so overflow must NOT
// kill the subscription the way a chat frame does.
func TestHubPTYOverflowKeepsSubscriber(t *testing.T) {
	h := newHub()
	ch, unsubscribe := h.subscribe()
	defer unsubscribe()
	for i := 0; i < cap(ch)+100; i++ {
		h.broadcast(provider.Event{Kind: provider.EventPTYOutput, Text: "y"})
	}
	// The subscription survives the flood: the full buffer drains, and the
	// channel is empty-but-open afterwards. (A chat frame arriving while
	// still full would close it — that's TestHubChatOverflowClosesSubscriber.)
	for i := 0; i < cap(ch); i++ {
		if _, ok := <-ch; !ok {
			t.Fatal("subscription closed by PTY overflow")
		}
	}
	select {
	case _, ok := <-ch:
		if !ok {
			t.Fatal("subscription closed by PTY overflow")
		}
	default: // empty but open — fine
	}
}

// The background list is live-only, so a viewer connecting mid-run gets the
// latest one first, on either feed — and once the process exits, an empty
// list both reaches the viewers already connected and is what a new one
// gets. Clearing is a no-op when nothing was running.
func TestHubBackgroundPrimesNewSubscribers(t *testing.T) {
	h := newHub()
	_, early, unsubEarly := h.subscribeWithPTYReplay()
	defer unsubEarly()
	h.clearBackground()
	if len(early) != 0 {
		t.Fatalf("clearing with nothing running sent %+v", <-early)
	}

	running := []provider.BackgroundTask{{TaskID: "b1", ToolUseID: "toolu_1"}}
	h.broadcast(provider.Event{Kind: provider.EventBackground, Background: running})
	h.broadcast(provider.Event{Kind: provider.EventText, Text: "after"})
	<-early
	<-early

	chat, unsubChat := h.subscribe()
	defer unsubChat()
	_, term, unsubTerm := h.subscribeWithPTYReplay()
	defer unsubTerm()
	for name, ch := range map[string]<-chan provider.Event{"chat": chat, "terminal": term} {
		select {
		case ev := <-ch:
			if ev.Kind != provider.EventBackground || len(ev.Background) != 1 || ev.Background[0].TaskID != "b1" {
				t.Errorf("%s feed opened with %+v", name, ev)
			}
		default:
			t.Errorf("%s feed got no background list on connect", name)
		}
	}

	h.clearBackground()
	if ev := <-early; ev.Kind != provider.EventBackground || len(ev.Background) != 0 {
		t.Errorf("connected viewer got %+v, want an empty list", ev)
	}
	late, unsubLate := h.subscribe()
	defer unsubLate()
	if ev := <-late; ev.Kind != provider.EventBackground || len(ev.Background) != 0 {
		t.Errorf("late viewer opened with %+v, want an empty list", ev)
	}
}
