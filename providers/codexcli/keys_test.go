package codexcli

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/drewkidwell/mads/providers/provider"
)

// liveItems folds a stream into its keyed items in order — "role:key" →
// text — failing on an item event without a key or a text/thinking key
// that comes back after another item started (the contiguity invariant).
func liveItems(t *testing.T, evs []provider.Event) (order []string, text map[string]string) {
	t.Helper()
	text = map[string]string{}
	last := ""
	for _, ev := range evs {
		role := ""
		switch ev.Kind {
		case provider.EventText:
			role = "agent"
		case provider.EventThinking:
			role = "thinking"
		case provider.EventToolUse:
			role = "tool"
		case provider.EventToolResult:
			role = "tool_result"
		default:
			continue
		}
		if ev.Key == "" {
			t.Fatalf("%s event without a key: %+v", ev.Kind, ev)
		}
		id := role + ":" + ev.Key
		if _, seen := text[id]; seen && id != last && (role == "agent" || role == "thinking") {
			t.Fatalf("%s reappears after %s started", id, last)
		}
		if _, seen := text[id]; !seen {
			order = append(order, id)
		}
		text[id] += ev.Text
		last = id
	}
	return order, text
}

// storedItems is the same fold over transcript rows (user rows dropped:
// they have no live event).
func storedItems(t *testing.T, msgs []provider.Message) (order []string, text map[string]string) {
	t.Helper()
	text = map[string]string{}
	for _, m := range msgs {
		if m.Key == "" {
			t.Fatalf("stored %s row without a key: %+v", m.Role, m)
		}
		if m.Role == "user" || m.Role == "compact" {
			continue
		}
		id := m.Role + ":" + m.Key
		order = append(order, id)
		text[id] = m.Text
	}
	return order, text
}

// The real captures: every item codex streamed and stored has the same key
// and the same text on both sides, in the same order — commentary and final
// answer as two items, each exactly as stored. inflight is the one live
// item the store may lack (run1's interrupted story).
func TestCaptureKeysMatchTheStore(t *testing.T) {
	for _, tc := range []struct {
		stream, items string
		inflight      int // live text items at the end that were never stored
	}{
		{"run1-stdout.jsonl", "run1-items-list.json", 1},
		{"toolturn-stdout.jsonl", "toolturn-items-list.json", 0},
	} {
		t.Run(tc.stream, func(t *testing.T) {
			evs, _ := replay(t, tc.stream, provider.TrustFull, nil)
			liveOrder, liveText := liveItems(t, evs)
			storedOrder, storedText := storedItems(t, normalizeItems(loadItems(t, tc.items)))
			var live []string
			for _, id := range liveOrder {
				if !strings.HasPrefix(id, "thinking:") { // thinking has no transcript row
					live = append(live, id)
				}
			}
			unsaved := live[len(live)-tc.inflight:]
			live = live[:len(live)-tc.inflight]
			if strings.Join(live, "\n") != strings.Join(storedOrder, "\n") {
				t.Fatalf("live items\n%s\nstored items\n%s", strings.Join(live, "\n"), strings.Join(storedOrder, "\n"))
			}
			for _, id := range live {
				if strings.HasPrefix(id, "tool:") {
					continue
				}
				if liveText[id] != storedText[id] {
					t.Errorf("%s: live %q, stored %q", id, liveText[id], storedText[id])
				}
			}
			for _, id := range unsaved {
				if _, ok := storedText[id]; ok || !strings.HasPrefix(id, "agent:") {
					t.Errorf("in-flight item %s: stored %v", id, ok)
				}
			}
		})
	}
	// The user rows are keyed by the ids the survey sent them with.
	var users []string
	for _, m := range normalizeItems(loadItems(t, "run1-items-list.json")) {
		if m.Role == "user" {
			users = append(users, m.Key)
		}
	}
	if got := strings.Join(users, " "); got != "user:mads-a user:mads-b user:mads-c user:mads-d user:mads-i" {
		t.Errorf("user keys = %s", got)
	}
}

// A user message mads didn't send with an id keeps codex's own item id.
func TestUserKeyWithoutClientID(t *testing.T) {
	it := threadItem{Type: "userMessage", ID: "01a1-x", Content: []userInput{{Type: "text", Text: "hi"}}}
	if got := normalizeItems([]itemEntry{{Item: it}}); len(got) != 1 || got[0].Key != "user:01a1-x" {
		t.Fatalf("rows = %+v", got)
	}
}

// SendKeyed's id is the clientUserMessageId codex stores, on turn/start and
// on a mid-turn turn/steer alike, and so the stored user row's key; a plain
// Send's minted id keys its row the same way, and SentClientIDs lists them
// all in order.
func TestSendKeyedRoundTrips(t *testing.T) {
	p := fakeProvider(t)
	s := startSession(t, p, provider.StartOptions{})
	ctx := context.Background()
	if err := s.SendKeyed(ctx, "echo mk-a", "id-one"); err != nil {
		t.Fatal(err)
	}
	drainTurn(t, s)
	if err := s.SendKeyed(ctx, "slow", "id-two"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, s, provider.EventText)
	if err := s.SendKeyed(ctx, "mk-steer", "id-three"); err != nil {
		t.Fatal(err)
	}
	drainTurn(t, s)
	sendTurn(t, s, "echo mk-b")

	ids := s.SentClientIDs()
	if len(ids) != 4 || strings.Join(ids[:3], ",") != "id-one,id-two,id-three" {
		t.Fatalf("SentClientIDs = %v", ids)
	}
	msgs, err := p.Transcript(ctx, s.Ref())
	if err != nil {
		t.Fatal(err)
	}
	var users []string
	for _, m := range msgs {
		if m.Role == "user" {
			users = append(users, m.Key)
		}
	}
	want := "user:id-one user:id-two user:id-three user:" + ids[3]
	if got := strings.Join(users, " "); got != want {
		t.Fatalf("user keys = %s, want %s", got, want)
	}
}

func rowsString(msgs []provider.Message) string {
	var b strings.Builder
	for _, m := range msgs {
		fmt.Fprintf(&b, "%s|%s|%s\n", m.Role, m.Key, m.Text)
	}
	return b.String()
}

// Chained TranscriptSince reads add up to Transcript: a read before any
// turn, a read with nothing new, reads after plain, thinking and tool turns,
// and one mid-turn.
func TestTranscriptSinceChainsToTranscript(t *testing.T) {
	p := fakeProvider(t)
	s := startSession(t, p, provider.StartOptions{})
	ctx := context.Background()
	var chained []provider.Message
	var cursor provider.TranscriptCursor
	read := func() {
		t.Helper()
		rows, next, err := p.TranscriptSince(ctx, s.Ref(), cursor)
		if err != nil {
			t.Fatalf("TranscriptSince: %v", err)
		}
		chained, cursor = append(chained, rows...), next
	}
	read()
	sendTurn(t, s, "echo mk-a")
	read()
	read()
	if c, _ := cursor.(*itemsCursor); c == nil || c.anchor == "" {
		t.Fatalf("cursor %+v has no codex anchor", cursor)
	}
	sendTurn(t, s, "think")
	read()
	if err := s.Send(ctx, "slow"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, s, provider.EventText)
	read() // mid-turn: the user message is stored, the reply not yet
	if err := s.Interrupt(ctx); err != nil {
		t.Fatal(err)
	}
	drainTurn(t, s)
	sendTurn(t, s, "tool-turn")
	read()

	full, err := p.Transcript(ctx, s.Ref())
	if err != nil {
		t.Fatal(err)
	}
	if got, want := rowsString(chained), rowsString(full); got != want {
		t.Fatalf("chained\n%s\nTranscript\n%s", got, want)
	}
	if len(full) < 10 {
		t.Fatalf("transcript too short to prove anything: %d rows", len(full))
	}
}

// A cursor that no longer names what the store holds — another thread's,
// or an item the store doesn't have — is stale, never silently re-read.
func TestTranscriptSinceStaleCursor(t *testing.T) {
	p := fakeProvider(t)
	s := startSession(t, p, provider.StartOptions{})
	sendTurn(t, s, "echo mk-a")
	ctx := context.Background()
	for name, cur := range map[string]provider.TranscriptCursor{
		"other thread":       &itemsCursor{ref: "other", last: "x"},
		"unknown item":       &itemsCursor{ref: s.Ref(), last: "msg_gone"},
		"foreign type":       42,
		"anchor on new item": nil, // filled below
	} {
		if cur == nil {
			_, c, err := p.TranscriptSince(ctx, s.Ref(), nil)
			if err != nil {
				t.Fatal(err)
			}
			ic := *c.(*itemsCursor)
			ic.last = "msg_gone" // anchor and item disagree
			cur = &ic
		}
		if _, _, err := p.TranscriptSince(ctx, s.Ref(), cur); !errors.Is(err, provider.ErrCursorStale) {
			t.Errorf("%s: err = %v, want ErrCursorStale", name, err)
		}
	}
}

// With no live session for the thread there is no connection to read on:
// ErrUnsupported, and the caller uses Transcript (which spawns one).
func TestTranscriptSinceNeedsALiveSession(t *testing.T) {
	p := fakeProvider(t)
	ctx := context.Background()
	if _, _, err := p.TranscriptSince(ctx, "01a1-none", nil); !errors.Is(err, provider.ErrUnsupported) {
		t.Fatalf("no session: err = %v", err)
	}
	s := startSession(t, p, provider.StartOptions{})
	sendTurn(t, s, "echo mk-a")
	ref := s.Ref()
	_ = s.Close()
	if _, _, err := p.TranscriptSince(ctx, ref, nil); !errors.Is(err, provider.ErrUnsupported) {
		t.Fatalf("after Close: err = %v", err)
	}
	if msgs, err := p.Transcript(ctx, ref); err != nil || len(msgs) != 2 {
		t.Fatalf("Transcript after Close = %+v, %v", msgs, err)
	}
}

// anchorAt walks back past items stored after the one it anchors on.
func TestAnchorAtWalksBack(t *testing.T) {
	p := fakeProvider(t)
	s := startSession(t, p, provider.StartOptions{})
	sendTurn(t, s, "echo mk-a")
	sendTurn(t, s, "think")
	ctx := context.Background()
	entries, err := listItems(ctx, s.c, s.Ref())
	if err != nil {
		t.Fatal(err)
	}
	mid := entries[1].Item.ID // the first turn's reply, with five items after it
	anchor, err := anchorAt(ctx, s.c, s.Ref(), mid)
	if err != nil || anchor == "" {
		t.Fatalf("anchorAt = %q, %v", anchor, err)
	}
	from, err := listItemsFrom(ctx, s.c, s.Ref(), anchor)
	if err != nil {
		t.Fatal(err)
	}
	if len(from) != len(entries)-1 || from[0].Item.ID != mid {
		t.Fatalf("from anchor: %d items starting %q, want %d from %q", len(from), from[0].Item.ID, len(entries)-1, mid)
	}
}

// Settle hints are opt-in: none without StartOptions.Keyed; with it,
// one after each item/completed, keyed to that item and after its last
// live event, and one unkeyed before the turn end.
func TestSettleHintsOnlyWhenAsked(t *testing.T) {
	p := fakeProvider(t)
	plain := startSession(t, p, provider.StartOptions{})
	for _, ev := range sendTurn(t, plain, "tool-turn") {
		if ev.Kind == provider.EventSettle {
			t.Fatalf("EventSettle without Keyed: %+v", ev)
		}
	}

	s := startSession(t, p, provider.StartOptions{Keyed: true})
	if err := s.SendKeyed(context.Background(), "tool-turn", "id-tt"); err != nil {
		t.Fatal(err)
	}
	evs := drainTurn(t, s)
	var settled []string
	lastSeen := map[string]int{}
	for i, ev := range evs {
		if ev.Kind != provider.EventSettle {
			if ev.Key != "" {
				lastSeen[ev.Key] = i
			}
			continue
		}
		if ev.Key != "" {
			if at, ok := lastSeen[ev.Key]; ok && at > i {
				t.Errorf("settle for %s before its last event", ev.Key)
			}
		}
		settled = append(settled, ev.Key)
	}
	if n := len(evs); evs[n-2].Kind != provider.EventSettle || evs[n-2].Key != "" {
		t.Errorf("event before the turn end = %+v, want an unkeyed settle", evs[n-2])
	}
	// user, reasoning, commentary, command, final answer, then the turn.
	if len(settled) != 6 || settled[0] != "user:id-tt" || !strings.HasPrefix(settled[1], "rs_") ||
		!strings.HasPrefix(settled[2], "msg_") || !strings.HasPrefix(settled[3], "exec-") ||
		!strings.HasPrefix(settled[4], "msg_") || settled[5] != "" {
		t.Errorf("settles = %q", settled)
	}
	for key := range lastSeen {
		found := false
		for _, k := range settled {
			found = found || k == key
		}
		if !found {
			t.Errorf("item %s streamed but never settled", key)
		}
	}
}
