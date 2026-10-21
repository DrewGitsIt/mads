package claudecode

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/drewkidwell/mads/providers/provider"
)

// replayStream runs a captured --replay-user-messages stream through a
// streamSession the way the live transport sees it: mads's own sends (the
// replayed user lines with no origin) go through Send first, as they did when
// captured, so the turn bookkeeping is the real one.
func replayStream(t *testing.T, name string) (*streamSession, []provider.Event) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	s := &streamSession{events: make(chan provider.Event, 1024), announced: make(chan string, 1), stdin: discardWriteCloser{}}
	var evs []provider.Event
	drain := func() {
		for {
			select {
			case ev := <-s.events:
				evs = append(evs, ev)
			default:
				return
			}
		}
	}
	for _, ln := range strings.Split(string(raw), "\n") {
		if strings.TrimSpace(ln) == "" {
			continue
		}
		if l := parseWireLine(t, ln); l.Type == "user" && l.IsReplay && l.Origin == nil {
			if err := s.Send(context.Background(), "replayed"); err != nil {
				t.Fatal(err)
			}
		}
		s.handleLine([]byte(ln))
		drain()
	}
	return s, evs
}

func ofKind(evs []provider.Event, k provider.EventKind) []provider.Event {
	var out []provider.Event
	for _, ev := range evs {
		if ev.Kind == k {
			out = append(out, ev)
		}
	}
	return out
}

// testdata/stream-peer-idle.jsonl (claude-code 2.1.285, haiku, two processes
// in bypassPermissions): mads's own turn, a peer message arriving idle (the
// CLI runs a turn for it), a turn that leaves a background sleep running, the
// unprompted turn its completion triggers, and a message from a
// default-mode sender the CLI held.
func TestPeerIdleStream(t *testing.T) {
	s, evs := replayStream(t, "stream-peer-idle.jsonl")

	if got := s.PeerAddress(); got != "uds:/tmp/cc-socks/77585.sock" {
		t.Errorf("PeerAddress = %q, want uds: + the init line's messaging_socket_path", got)
	}

	in := ofKind(evs, provider.EventInbound)
	if len(in) != 2 {
		t.Fatalf("inbound events = %+v, want the delivered message and the held one (own sends echo nothing)", in)
	}
	got := in[0]
	want := provider.Inbound{Channel: "peer", From: "uds:/tmp/cc-socks/77987.sock", FromName: "a-3c"}
	if got.Inbound == nil || *got.Inbound != want || got.Text != "PROBE-IDLE hello from A, reply with just: ack" {
		t.Errorf("delivered = %+v %q, want %+v with the bare body", got.Inbound, got.Text, want)
	}
	held := in[1]
	wantHeld := provider.Inbound{Channel: "peer", From: "uds:/tmp/cc-socks/78354.sock", FromName: "a-6f", State: "held", HoldReason: "mode-mismatch"}
	if held.Inbound == nil || *held.Inbound != wantHeld || held.Text != "" {
		t.Errorf("held = %+v %q, want %+v", held.Inbound, held.Text, wantHeld)
	}

	// One end per turn the CLI ran: mads's two, the peer's, and the
	// background completion's unprompted one.
	if n := len(ofKind(evs, provider.EventTurnEnd)); n != 4 {
		t.Errorf("turn ends = %d, want 4", n)
	}
	// The peer's turn opened like a Send: the turn end after the inbound
	// event closes exactly it.
	var order []provider.EventKind
	for _, ev := range evs {
		if ev.Kind == provider.EventInbound || ev.Kind == provider.EventTurnEnd {
			order = append(order, ev.Kind)
		}
	}
	wantOrder := []provider.EventKind{provider.EventTurnEnd, provider.EventInbound, provider.EventTurnEnd, provider.EventTurnEnd, provider.EventTurnEnd, provider.EventInbound}
	if strings.Join(kinds(order), ",") != strings.Join(kinds(wantOrder), ",") {
		t.Errorf("inbound/turn-end order = %v, want %v", order, wantOrder)
	}
}

func kinds(ks []provider.EventKind) []string {
	out := make([]string, len(ks))
	for i, k := range ks {
		out[i] = string(k)
	}
	return out
}

// testdata/stream-peer-mid.jsonl: a peer message arriving while mads's turn
// runs a 30s tool call. The CLI echoes it after the tool's result, inside
// the same turn, and the turn ends once.
func TestPeerMidTurnStream(t *testing.T) {
	_, evs := replayStream(t, "stream-peer-mid.jsonl")
	in := ofKind(evs, provider.EventInbound)
	if len(in) != 1 || in[0].Inbound.FromName != "a-5f" || in[0].Text != "PROBE-MID hello mid-turn from A" {
		t.Fatalf("inbound = %+v, want the one mid-turn message", in)
	}
	if n := len(ofKind(evs, provider.EventTurnEnd)); n != 2 {
		t.Errorf("turn ends = %d, want 2 (mads's first turn, then the one the message landed in)", n)
	}
}

// A peer message that lands idle with no turn mads opened: the turn the CLI
// runs for it ends exactly once, on its origin-"peer" result line.
func TestPeerIdleArrivalEndsOnce(t *testing.T) {
	s := &streamSession{events: make(chan provider.Event, 16), announced: make(chan string, 1), stdin: discardWriteCloser{}}
	s.handleLine([]byte(`{"type":"user","isReplay":true,"message":{"role":"user","content":"Another Claude session sent a message:\n<cross-session-message from=\"uds:/tmp/cc-socks/1.sock\" from-name=\"x\">\nhi\n</cross-session-message>"},"origin":{"kind":"peer","from":"uds:/tmp/cc-socks/1.sock","name":"x","body":"hi"}}`))
	s.handleLine([]byte(`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"toolu_1","name":"Bash","input":{"command":"ls"}}]}}`))
	// A background continuation inside the peer's turn stays silent.
	s.handleLine([]byte(`{"type":"result","subtype":"success","origin":{"kind":"task-notification"}}`))
	s.handleLine([]byte(`{"type":"result","subtype":"success","origin":{"kind":"peer","from":"uds:/tmp/cc-socks/1.sock"},"usage":{"input_tokens":1}}`))
	if n := len(drainTurnEnds(s)); n != 1 {
		t.Fatalf("turn ends = %d, want 1", n)
	}
}

// mads's own send, echoed back by --replay-user-messages, says nothing new.
func TestOwnReplayIsSilent(t *testing.T) {
	l := parseWireLine(t, `{"type":"user","message":{"role":"user","content":[{"type":"text","text":"hello"}]},"session_id":"s","parent_tool_use_id":null,"isReplay":true}`)
	if evs := l.events(false, true); len(evs) != 0 {
		t.Errorf("own replay events = %+v, want none", evs)
	}
	// The hold's follow-up ("dropped") is bookkeeping too.
	l = parseWireLine(t, `{"type":"system","subtype":"peer_message_hold","state":"dropped","from":"uds:/tmp/cc-socks/2.sock","from_name":"a-6f","outcome":"discarded"}`)
	if evs := l.events(false, true); len(evs) != 0 {
		t.Errorf("dropped hold events = %+v, want none", evs)
	}
}

// Both transcript shapes of a peer message (testdata/transcript_peer.jsonl,
// built from real claude-code 2.1.285 lines with the bodies replaced): the
// idle arrival's isMeta user line and the mid-turn queued_command
// attachment, with and without an origin. A message recorded twice renders
// once; other isMeta lines stay out, and a non-peer queued command is the
// human's own mid-turn send (TestQueuedPromptTranscript).
func TestPeerTranscript(t *testing.T) {
	f, err := os.Open(filepath.Join("testdata", "transcript_peer.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	msgs, err := normalizeTranscript(f)
	if err != nil {
		t.Fatal(err)
	}
	type row struct{ role, text, from, name string }
	var got []row
	for _, m := range msgs {
		r := row{role: m.Role, text: m.Text}
		if m.Inbound != nil {
			r.from, r.name = m.Inbound.From, m.Inbound.FromName
			if m.Inbound.Channel != "peer" {
				t.Errorf("channel = %q, want peer", m.Inbound.Channel)
			}
		}
		got = append(got, r)
	}
	want := []row{
		{role: "user", text: "Please start the study."},
		{role: "agent", text: "Started."},
		{role: "inbound", text: "Supervisor here. Confirm you are the study session, then proceed.", from: "uds:/tmp/cc-socks/64861.sock", name: "repo-9c"},
		{role: "agent", text: "Confirmed."},
		{role: "user", text: "Now run the counts."},
		{role: "inbound", text: "Rulings on your questions. Build on the prior work; don't restart.", from: "uds:/tmp/cc-socks/64861.sock", name: "repo-9c"},
		{role: "inbound", text: "Second sender, tag only.", from: "uds:/tmp/cc-socks/70001.sock", name: "a-b6"},
		{role: "user", text: "also check the logs"},
		{role: "agent", text: "Counts done."},
	}
	if len(got) != len(want) {
		t.Fatalf("messages =\n%+v\nwant\n%+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("message %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	if msgs[2].Timestamp.IsZero() {
		t.Error("inbound entry lost its timestamp")
	}
}

// Two records that both carry a msg_id are one message only when the ids
// match: the same words sent twice are two messages.
func TestPeerSeenKeepsRepeatedWords(t *testing.T) {
	var p peerSeen
	in := &provider.Inbound{Channel: "peer", From: "uds:a"}
	if p.repeat(in, "ok", "m1") || p.repeat(in, "ok", "m2") {
		t.Error("two ids with the same body deduped")
	}
	if !p.repeat(in, "ok", "m1") {
		t.Error("same id twice not deduped")
	}
	if !p.repeat(in, "ok", "") {
		t.Error("an id-less record of a seen body not deduped")
	}
}
