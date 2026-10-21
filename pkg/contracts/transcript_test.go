package contracts_test

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/golden"
)

// sampleToolMessages exercises the full enriched ToolUse shape: an edit-kind
// call with a location, and an MCP call carrying its server name.
func sampleToolMessages() []contracts.TranscriptMessage {
	return []contracts.TranscriptMessage{
		{
			Role: "tool",
			Tool: &contracts.ToolUse{
				ID:        "toolu_01edit",
				Name:      "Edit",
				Input:     json.RawMessage(`{"file_path":"/home/u/app/main.go","old_string":"a","new_string":"b"}`),
				Path:      "/home/u/app/main.go",
				Kind:      "edit",
				Title:     "/home/u/app/main.go",
				Status:    "completed",
				Locations: []contracts.ToolLocation{{Path: "/home/u/app/main.go", Line: 12}},
			},
			Timestamp: time.Date(2026, 9, 9, 18, 38, 15, 0, time.UTC),
			Key:       "toolu_01edit",
		},
		{
			Role: "tool",
			Tool: &contracts.ToolUse{
				ID:     "toolu_02mcp",
				Name:   "mcp__github__create_issue",
				Input:  json.RawMessage(`{"title":"flaky test"}`),
				Kind:   "mcp",
				Title:  "github:create_issue — flaky test",
				Status: "in_progress",
				Server: "github",
			},
			Timestamp: time.Date(2026, 9, 9, 18, 38, 16, 0, time.UTC),
			Key:       "toolu_02mcp",
		},
	}
}

// Producer side of the contract: what a service writes onto the wire must
// match the pinned golden byte-for-byte.
func TestTranscriptMessageProducerMatchesGolden(t *testing.T) {
	got, err := json.MarshalIndent(sampleToolMessages(), "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	golden.Assert(t, "transcript_message.json", append(got, '\n'))
}

// Consumer side of the contract: parsing the same golden must reproduce the
// full value — including every enriched tool-use field.
func TestTranscriptMessageConsumerParsesGolden(t *testing.T) {
	var got []contracts.TranscriptMessage
	if err := json.Unmarshal(golden.Read(t, "transcript_message.json"), &got); err != nil {
		t.Fatalf("consumer failed to parse golden: %v", err)
	}
	want := sampleToolMessages()
	if len(got) != len(want) {
		t.Fatalf("parsed %d messages, want %d", len(got), len(want))
	}
	for i, w := range want {
		g := got[i]
		if g.Role != w.Role || !g.Timestamp.Equal(w.Timestamp) || g.Key != w.Key {
			t.Errorf("message %d: role/timestamp/key %q/%v/%q, want %q/%v/%q", i, g.Role, g.Timestamp, g.Key, w.Role, w.Timestamp, w.Key)
		}
		gt, wt := g.Tool, w.Tool
		if gt == nil {
			t.Fatalf("message %d: tool missing", i)
		}
		if gt.ID != wt.ID || gt.Name != wt.Name || gt.Path != wt.Path ||
			gt.Kind != wt.Kind || gt.Title != wt.Title || gt.Status != wt.Status ||
			gt.Server != wt.Server || !reflect.DeepEqual(gt.Locations, wt.Locations) ||
			compact(t, gt.Input) != compact(t, wt.Input) {
			t.Errorf("message %d: parsed tool differs:\n got %+v\nwant %+v", i, gt, wt)
		}
	}
}

// The inbound-message contract the UI codes against (ui/src/router/types.ts
// InboundSource): a transcript entry from a sender that resolved to a mads
// session, one that didn't (only the harness's label), a held one, and the
// live stream's "inbound" event.
func TestInboundShapesMatchGolden(t *testing.T) {
	resolved := &contracts.InboundSource{
		Channel: "peer", Provider: "claude-code", FromName: "repo-9c",
		SessionID: "ses_leader", SessionName: "Leader", LinkID: "lnk_1",
	}
	shapes := map[string]any{
		"transcript": []contracts.TranscriptMessage{
			{Role: "inbound", Text: "Rulings on your questions.", Inbound: resolved, Timestamp: time.Date(2026, 10, 19, 5, 22, 6, 0, time.UTC)},
			{Role: "inbound", Text: "hello", Inbound: &contracts.InboundSource{Channel: "peer", Provider: "claude-code", FromName: "a-3c"}},
		},
		"stream": contracts.InboundStreamEvent{Kind: contracts.StreamInbound, Text: "Rulings on your questions.", Inbound: resolved},
		"stream_held": contracts.InboundStreamEvent{Kind: contracts.StreamInbound, Inbound: &contracts.InboundSource{
			Channel: "peer", Provider: "claude-code", FromName: "a-6f", State: "held", HoldReason: "mode-mismatch",
		}},
		"peer_address": contracts.PeerAddress{Address: "uds:/tmp/cc-socks/77585.sock"},
	}
	got, err := json.MarshalIndent(shapes, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	golden.Assert(t, "inbound.json", append(got, '\n'))

	var back struct {
		Transcript []contracts.TranscriptMessage `json:"transcript"`
		Stream     contracts.InboundStreamEvent  `json:"stream"`
	}
	if err := json.Unmarshal(got, &back); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(back.Stream.Inbound, resolved) || !reflect.DeepEqual(back.Transcript[0].Inbound, resolved) {
		t.Errorf("round trip lost fields: %+v", back)
	}
}
