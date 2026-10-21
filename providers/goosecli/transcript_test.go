package goosecli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/drewkidwell/mads/pkg/golden"
)

// loadExportCapture parses the committed goose 1.50.1 export capture — the
// evidence base every normalization fact here is pinned against.
func loadExportCapture(t *testing.T) exportDoc {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "captures", "oneshot-export.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc exportDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

// Transcript normalization is golden-pinned against the committed real
// export (claudecode/transcript convention): the golden holds exactly what
// the platform sees. The properties the golden encodes are also asserted
// explicitly so a careless -update can't bless a regression: the injected
// userVisible:false <turn-context> entry is absent, every message carries the entry's unix `created` as a timestamp,
// and tool results ride role:"user" entries without being misread as user
// turns.
func TestTranscriptNormalizationGolden(t *testing.T) {
	doc := loadExportCapture(t)
	msgs := normalizeExport(doc.Conversation)

	counts := map[string]int{}
	for _, m := range msgs {
		counts[m.Role]++
		if strings.Contains(m.Text, "<turn-context>") {
			t.Errorf("turn-context entry leaked into %q message: %q", m.Role, m.Text)
		}
		if m.Timestamp.IsZero() {
			t.Errorf("%q message missing timestamp: %+v", m.Role, m)
		}
	}
	// The capture: 1 typed user message (its turn-context sibling skipped),
	// 3 toolRequests (todo__todo_write, shell, todo__todo_write), their 3
	// toolResponses, and 1 final assistant text.
	if counts["user"] != 1 || counts["tool"] != 3 || counts["tool_result"] != 3 || counts["agent"] != 1 {
		t.Errorf("role counts = %v, want user:1 tool:3 tool_result:3 agent:1", counts)
	}

	got, err := json.MarshalIndent(msgs, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	golden.Assert(t, "transcript.golden.json", append(got, '\n'))
}

// Thinking blocks contribute nothing. The 1.50.1 capture ran a no-think
// model, so this rides a synthetic entry in the 1.48.0 capture's exact
// shape (a thinking block sharing its assistant entry with the text).
func TestTranscriptSkipsThinkingBlocks(t *testing.T) {
	conv := `[
	 {"id":"chatcmpl-892","role":"assistant","created":1788032384,"content":[
	  {"type":"thinking","thinking":"The user wants a file.","signature":""},
	  {"type":"text","text":"Done."}
	 ],"metadata":{"userVisible":true,"agentVisible":true}}
	]`
	var entries []exportMessage
	if err := json.Unmarshal([]byte(conv), &entries); err != nil {
		t.Fatal(err)
	}
	msgs := normalizeExport(entries)
	if len(msgs) != 1 || msgs[0].Role != "agent" || msgs[0].Text != "Done." {
		t.Fatalf("got %+v, want only the agent text %q", msgs, "Done.")
	}
}

// The IsError mapping (toolResult.value.isError → Message.IsError) — the
// committed capture only has successful results, so the error side rides a
// synthetic entry in the capture's exact toolResponse shape. The correlation
// ID must survive on both.
func TestTranscriptToolResultErrorMapping(t *testing.T) {
	conv := `[
	 {"role":"user","created":1788032398,"content":[
	  {"type":"toolResponse","id":"call_bad","toolResult":{"status":"success","value":{"content":[{"type":"text","text":"cat: /nope: No such file or directory"}],"isError":true}}}
	 ],"metadata":{"userVisible":true,"agentVisible":true}},
	 {"role":"user","created":1788032399,"content":[
	  {"type":"toolResponse","id":"call_ok","toolResult":{"status":"success","value":{"content":[{"type":"text","text":"hello"}],"isError":false}}}
	 ],"metadata":{"userVisible":true,"agentVisible":true}}
	]`
	var entries []exportMessage
	if err := json.Unmarshal([]byte(conv), &entries); err != nil {
		t.Fatal(err)
	}
	msgs := normalizeExport(entries)
	if len(msgs) != 2 {
		t.Fatalf("got %d messages, want 2: %+v", len(msgs), msgs)
	}
	failed, succeeded := msgs[0], msgs[1]
	if failed.Role != "tool_result" || !failed.IsError || failed.Tool == nil || failed.Tool.ID != "call_bad" {
		t.Errorf("failed result: %+v, want tool_result IsError=true ID=call_bad", failed)
	}
	if succeeded.Role != "tool_result" || succeeded.IsError || succeeded.Tool == nil || succeeded.Tool.ID != "call_ok" {
		t.Errorf("succeeded result: %+v, want tool_result IsError=false ID=call_ok", succeeded)
	}
}

// The visibility rule is explicit-false only: an entry with absent metadata
// (or metadata without userVisible) must stay visible — future goose
// versions may drop the field, and nil must never deref or hide content.
func TestTranscriptAbsentMetadataStaysVisible(t *testing.T) {
	conv := `[
	 {"role":"user","created":1788032382,"content":[{"type":"text","text":"no metadata at all"}]},
	 {"role":"user","created":1788032383,"content":[{"type":"text","text":"metadata without userVisible"}],"metadata":{"agentVisible":true}}
	]`
	var entries []exportMessage
	if err := json.Unmarshal([]byte(conv), &entries); err != nil {
		t.Fatal(err)
	}
	msgs := normalizeExport(entries)
	if len(msgs) != 2 || msgs[0].Role != "user" || msgs[1].Role != "user" {
		t.Fatalf("got %+v, want both entries kept as user messages", msgs)
	}
}

// Transcript end-to-end through the exec seam: the fake serves the committed
// capture for its id, proving the `session export --session-id <ref>
// --format json` argv shape and the stdout→normalize plumbing.
func TestTranscriptViaExport(t *testing.T) {
	dir := t.TempDir()
	raw, err := os.ReadFile(filepath.Join("testdata", "captures", "oneshot-export.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "20261020_1.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	p := testProvider(t, "MADS_FAKE_EXPORT_DIR="+dir)

	msgs, err := p.Transcript(context.Background(), "20261020_1")
	if err != nil {
		t.Fatalf("Transcript: %v", err)
	}
	if len(msgs) != 8 || msgs[0].Role != "user" {
		t.Fatalf("got %d messages (first %+v), want the capture's 8 starting with the user turn", len(msgs), msgs[0])
	}
}

func TestTranscriptEmptyRefErrors(t *testing.T) {
	if _, err := testProvider(t).Transcript(context.Background(), ""); err == nil {
		t.Fatal("Transcript(\"\") succeeded, want error")
	}
}

func TestTranscriptUnknownRefErrors(t *testing.T) {
	p := testProvider(t, "MADS_FAKE_EXPORT_DIR="+t.TempDir())
	_, err := p.Transcript(context.Background(), "20990101_1")
	if err == nil || !strings.Contains(err.Error(), "Session not found") {
		t.Fatalf("Transcript(unknown) = %v, want goose's Session-not-found stderr surfaced", err)
	}
}
