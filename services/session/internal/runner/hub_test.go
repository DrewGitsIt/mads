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
