package acprpc

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/drewkidwell/mads/providers/provider"
)

// keyedItem is one item as the stream showed it: the key, the kind of its
// events, and its text joined.
type keyedItem struct {
	key, kind, text string
}

// keyedItems folds a turn's item events into items, failing the test when
// an item event lacks a key or a text/thinking key comes back after another
// item started (Event.Key's contiguity invariant).
func keyedItems(t *testing.T, events []provider.Event) []keyedItem {
	t.Helper()
	var items []keyedItem
	seen := map[string]bool{}
	for _, ev := range events {
		switch ev.Kind {
		case provider.EventText, provider.EventThinking, provider.EventToolUse, provider.EventToolResult:
		default:
			continue
		}
		if ev.Key == "" {
			t.Fatalf("%s event without a key: %+v", ev.Kind, ev)
		}
		id := string(ev.Kind) + "\x00" + ev.Key
		if n := len(items); n > 0 && items[n-1].kind == string(ev.Kind) && items[n-1].key == ev.Key {
			items[n-1].text += ev.Text
			continue
		}
		if seen[id] && ev.Kind != provider.EventToolResult {
			t.Fatalf("%s key %q reappears after another item started", ev.Kind, ev.Key)
		}
		seen[id] = true
		items = append(items, keyedItem{key: ev.Key, kind: string(ev.Kind), text: ev.Text})
	}
	return items
}

func settles(events []provider.Event) []string {
	var out []string
	for _, ev := range events {
		if ev.Kind == provider.EventSettle {
			out = append(out, ev.Key)
		}
	}
	return out
}

// TestReplayGooseCaptureKeys replays the 2026-10-20 survey capture of goose
// 1.50.1 (goosecli testdata/captures/keys/run.wire.jsonl: a plain reply,
// text→tool→text, markdown, a cancelled count, a reply after the cancel)
// through the engine with goose's key strategy. The expected keys are
// written out literally here and in goosecli's reader test over the same
// session's store rows (run.store.jsonl): both sides derive them on their
// own, and they agree only because the derivation matches what goose
// stores. chatcmpl-17 is the case for the first chunk's created: its chunks
// span four seconds (…372 to …375), and the store keeps …372.
func TestReplayGooseCaptureKeys(t *testing.T) {
	e := replayEngine(t, "keys/run.wire.jsonl", gooseConfig())
	sess, err := e.Start(context.Background(), provider.StartOptions{Mode: provider.ModeACP, Trust: provider.TrustFull, Keyed: true})
	if err != nil {
		t.Fatalf("Start against the capture: %v", err)
	}
	defer func() { _ = sess.Close() }()

	type turn struct {
		items   []keyedItem
		settles []string
	}
	want := []turn{
		{items: []keyedItem{{"chatcmpl-904@1792515348:0", "text", "Hello! How can I help you today?"}}, settles: []string{""}},
		{items: []keyedItem{
			{"chatcmpl-103@1792515354:0", "text", "I'm about to list the files in the current directory."},
			{"call_6464carb", "tool_use", ""},
			{"call_6464carb", "tool_result", "alpha.txt\nbeta.txt\nsub"},
			{"chatcmpl-928@1792515355:0", "text", "The directory contains two text files (`alpha.txt`, `beta.txt`) and a subdirectory named `sub`."},
		}, settles: []string{"call_6464carb", ""}},
		{items: []keyedItem{{"chatcmpl-17@1792515372:0", "text", ""}}, settles: []string{""}},
		{items: []keyedItem{{"chatcmpl-287@1792515379:0", "text", "1"}}, settles: []string{""}},
		{items: []keyedItem{{"chatcmpl-925@1792515387:0", "text", "READY"}}, settles: []string{""}},
	}
	for i, w := range want {
		if err := sess.Send(context.Background(), fmt.Sprintf("captured turn %d", i)); err != nil {
			t.Fatal(err)
		}
		if i == 3 { // the captured session/cancel
			if err := sess.Interrupt(context.Background()); err != nil {
				t.Fatal(err)
			}
		}
		events := drainTurn(t, sess)
		got := keyedItems(t, events)
		if len(got) != len(w.items) {
			t.Fatalf("turn %d: items %+v, want %+v", i, got, w.items)
		}
		for j := range got {
			if got[j].key != w.items[j].key || got[j].kind != w.items[j].kind {
				t.Errorf("turn %d item %d = %s %q, want %s %q", i, j, got[j].kind, got[j].key, w.items[j].kind, w.items[j].key)
			}
			if w.items[j].text != "" && got[j].text != w.items[j].text {
				t.Errorf("turn %d item %d text = %q, want %q", i, j, got[j].text, w.items[j].text)
			}
		}
		if s := settles(events); strings.Join(s, ",") != strings.Join(w.settles, ",") {
			t.Errorf("turn %d settle hints %q, want %q (tool done, then the prompt response)", i, s, w.settles)
		}
	}
}

// keyLines feeds session/update lines straight into a session's read path —
// no process — for the derivation's edge cases.
func keyLines(t *testing.T, cfg Config, settle bool, updates ...map[string]any) []provider.Event {
	t.Helper()
	s := &streamSession{cfg: &cfg, events: make(chan provider.Event, 64), toolCalls: map[string]toolCallInfo{}, asks: map[string]*permAsk{}, settle: settle}
	for _, u := range updates {
		raw, _ := json.Marshal(u)
		params, _ := json.Marshal(sessionUpdateParams{SessionID: "s", Update: raw})
		line, _ := json.Marshal(rpcMessage{JSONRPC: "2.0", Method: "session/update", Params: params})
		s.handleLine(line)
	}
	close(s.events)
	var out []provider.Event
	for ev := range s.events {
		out = append(out, ev)
	}
	return out
}

func chunkU(variant, id string, created int, text string) map[string]any {
	u := map[string]any{"sessionUpdate": variant, "content": map[string]any{"type": "text", "text": text}}
	if id != "" {
		u["messageId"] = id
	}
	if created != 0 {
		u["_meta"] = map[string]any{"goose": map[string]any{"created": created, "messageId": id}}
	}
	return u
}

func eventKeys(events []provider.Event) []string {
	var out []string
	for _, ev := range events {
		out = append(out, string(ev.Kind)+"="+ev.Key)
	}
	return out
}

func TestKeyDerivation(t *testing.T) {
	goose := gooseConfig()
	hermesLike := Config{Keys: KeyStrategy{Mode: KeyModeMessageRun}, ToolIdentity: ToolIdentity{Mode: ToolIdentityKindBased}}
	tool := map[string]any{"sessionUpdate": "tool_call", "toolCallId": "call_1", "title": "shell", "kind": "execute"}
	done := map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": "call_1", "status": "completed"}
	for _, tc := range []struct {
		name    string
		cfg     Config
		settle  bool
		updates []map[string]any
		want    []string
	}{
		{
			name: "thinking then text in one message are runs 0 and 1; the first chunk's created holds",
			cfg:  goose,
			updates: []map[string]any{
				chunkU("agent_thought_chunk", "m", 100, "hmm"),
				chunkU("agent_message_chunk", "m", 101, "Hi"),
				chunkU("agent_message_chunk", "m", 102, " there"),
			},
			want: []string{"thinking=m@100:0", "text=m@100:1", "text=m@100:1"},
		},
		{
			name: "thinking apart: text runs count text only, thinking keyed on its own",
			cfg:  Config{Keys: KeyStrategy{Mode: KeyModeMessageRun, CreatedNamespace: "goose", ThinkingApart: true}},
			updates: []map[string]any{
				chunkU("agent_thought_chunk", "m", 100, "hmm"),
				chunkU("agent_message_chunk", "m", 101, "Hi"),
				chunkU("agent_thought_chunk", "m", 102, "and"),
				chunkU("agent_message_chunk", "m", 103, "more"),
				tool,
				chunkU("agent_thought_chunk", "n", 104, "so"),
				chunkU("agent_message_chunk", "n", 105, "done"),
			},
			want: []string{"thinking=m@100:thinking:0", "text=m@100:0", "thinking=m@100:thinking:1", "text=m@100:1",
				"tool_use=call_1", "thinking=n@104:thinking:0", "text=n@104:0"},
		},
		{
			name: "a colliding id after a tool is a new message, told apart by created",
			cfg:  goose,
			updates: []map[string]any{
				chunkU("agent_message_chunk", "chatcmpl-7", 100, "a"),
				tool, done,
				chunkU("agent_message_chunk", "chatcmpl-7", 105, "b"),
			},
			want: []string{"text=chatcmpl-7@100:0", "tool_use=call_1", "tool_result=call_1", "text=chatcmpl-7@105:0"},
		},
		{
			name: "no created namespace: the messageId alone",
			cfg:  hermesLike,
			updates: []map[string]any{
				chunkU("agent_message_chunk", "uuid-1", 0, "a"),
				chunkU("agent_message_chunk", "uuid-2", 0, "b"),
			},
			want: []string{"text=uuid-1:0", "text=uuid-2:0"},
		},
		{
			name: "chunks without a messageId open anonymous messages, one per boundary",
			cfg:  hermesLike,
			updates: []map[string]any{
				chunkU("agent_message_chunk", "", 0, "a"),
				chunkU("agent_message_chunk", "", 0, "b"),
				tool,
				chunkU("agent_message_chunk", "", 0, "c"),
			},
			want: []string{"text=acp-anon-1:0", "text=acp-anon-1:0", "tool_use=call_1", "text=acp-anon-2:0"},
		},
		{
			name:    "no strategy, no keys",
			cfg:     Config{},
			updates: []map[string]any{chunkU("agent_message_chunk", "m", 100, "a"), tool, done},
			want:    []string{"text=", "tool_use=", "tool_result="},
		},
		{
			name:    "settle points only when the session asked",
			cfg:     goose,
			updates: []map[string]any{tool, done},
			want:    []string{"tool_use=call_1", "tool_result=call_1"},
		},
		{
			name:    "goose settles on tool completion",
			cfg:     goose,
			settle:  true,
			updates: []map[string]any{tool, done},
			want:    []string{"tool_use=call_1", "tool_result=call_1", "settle=call_1"},
		},
		{
			name:    "a tool_call settle point",
			cfg:     Config{Keys: hermesLike.Keys, Settle: SettlePoints{ToolCall: true}},
			settle:  true,
			updates: []map[string]any{tool, done},
			want:    []string{"tool_use=call_1", "settle=call_1", "tool_result=call_1"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := eventKeys(keyLines(t, tc.cfg, tc.settle, tc.updates...))
			if strings.Join(got, " ") != strings.Join(tc.want, " ") {
				t.Fatalf("events %v, want %v", got, tc.want)
			}
		})
	}
}
