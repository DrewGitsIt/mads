package claudecode

import (
	"encoding/json"
	"testing"

	"github.com/drewkidwell/mads/providers/provider"
)

func parseWireLine(t *testing.T, raw string) wireLine {
	t.Helper()
	var l wireLine
	if err := json.Unmarshal([]byte(raw), &l); err != nil {
		t.Fatalf("unmarshal %q: %v", raw, err)
	}
	return l
}

// A stream_event content_block_delta/text_delta is the token-level text the
// interactive transport now streams (--include-partial-messages).
func TestStreamEventTextDelta(t *testing.T) {
	l := parseWireLine(t, `{"type":"stream_event","session_id":"s","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hel"}}}`)
	evs := l.events(false, true)
	if len(evs) != 1 || evs[0].Kind != provider.EventText || evs[0].Text != "Hel" {
		t.Fatalf("want one EventText 'Hel', got %+v", evs)
	}
}

// Framing and non-text deltas (tool input_json_delta, start/stop) are
// bookkeeping the whole-message assistant line already carries.
func TestStreamEventNonTextIgnored(t *testing.T) {
	for _, raw := range []string{
		`{"type":"stream_event","event":{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{"}}}`,
		`{"type":"stream_event","event":{"type":"message_stop"}}`,
	} {
		l := parseWireLine(t, raw)
		if evs := l.events(false, true); len(evs) != 0 {
			t.Fatalf("expected no events for %s, got %+v", raw, evs)
		}
	}
}

// The whole-message assistant text block is a duplicate of the streamed
// deltas on the interactive transport (partial=true) — dropped there, but
// still the sole text source for the one-shot path (partial=false).
// Only the write tools mark Mutates; Read/Grep still carry a Path for
// digest/transcript rendering but must not feed §5 file tracking — a
// session that read 15 files and edited 2 changed 2 files, not 17.
func TestToolUseMutatesOnlyForWriteTools(t *testing.T) {
	l := parseWireLine(t, `{"type":"assistant","message":{"role":"assistant","content":[`+
		`{"type":"tool_use","id":"t1","name":"Read","input":{"file_path":"a.go"}},`+
		`{"type":"tool_use","id":"t2","name":"Edit","input":{"file_path":"a.go"}},`+
		`{"type":"tool_use","id":"t3","name":"Write","input":{"file_path":"b.go"}},`+
		`{"type":"tool_use","id":"t4","name":"Grep","input":{"path":"pkg"}}]}}`)

	evs := l.events(false, false)
	if len(evs) != 4 {
		t.Fatalf("want 4 tool_use events, got %+v", evs)
	}
	want := []struct {
		path    string
		mutates bool
	}{{"a.go", false}, {"a.go", true}, {"b.go", true}, {"pkg", false}}
	for i, w := range want {
		tool := evs[i].Tool
		if tool == nil || tool.Path != w.path || tool.Mutates != w.mutates {
			t.Errorf("event %d: got %+v, want path=%q mutates=%v", i, tool, w.path, w.mutates)
		}
	}
}

func TestAssistantTextSkippedWhenPartial(t *testing.T) {
	l := parseWireLine(t, `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"whole reply"},{"type":"tool_use","id":"t1","name":"Read","input":{"file_path":"a.go"}}]}}`)

	partial := l.events(false, true)
	if len(partial) != 1 || partial[0].Kind != provider.EventToolUse {
		t.Fatalf("partial: want one tool_use only, got %+v", partial)
	}

	whole := l.events(false, false)
	if len(whole) != 2 ||
		whole[0].Kind != provider.EventText || whole[0].Text != "whole reply" ||
		whole[1].Kind != provider.EventToolUse {
		t.Fatalf("whole: want text+tool_use, got %+v", whole)
	}
}

// A "system"/"status" line with status "compacting" is the start of a native
// compaction pass (claude-code 2.1.220, empirically verified — see wire.go's
// systemEvents doc comment).
func TestSystemEventsCompactionStart(t *testing.T) {
	l := parseWireLine(t, `{"type":"system","subtype":"status","status":"compacting"}`)
	evs := l.events(false, false)
	if len(evs) != 1 || evs[0].Kind != provider.EventCompact || evs[0].Compact == nil || evs[0].Compact.Phase != "start" {
		t.Fatalf("want one EventCompact phase=start, got %+v", evs)
	}
}

// The completion line carries compact_result instead of status, "success" on
// a clean pass.
func TestSystemEventsCompactionSuccess(t *testing.T) {
	l := parseWireLine(t, `{"type":"system","subtype":"status","compact_result":"success"}`)
	evs := l.events(false, false)
	if len(evs) != 1 || evs[0].Kind != provider.EventCompact {
		t.Fatalf("want one EventCompact, got %+v", evs)
	}
	c := evs[0].Compact
	if c == nil || c.Phase != "done" || !c.Success || c.Error != "" {
		t.Fatalf("want done/success with no error, got %+v", c)
	}
}

// A failed pass carries compact_result "failed" and a human-readable
// compact_error (e.g. too little to compact).
func TestSystemEventsCompactionFailed(t *testing.T) {
	l := parseWireLine(t, `{"type":"system","subtype":"status","compact_result":"failed","compact_error":"Not enough messages to compact."}`)
	evs := l.events(false, false)
	if len(evs) != 1 || evs[0].Kind != provider.EventCompact {
		t.Fatalf("want one EventCompact, got %+v", evs)
	}
	c := evs[0].Compact
	if c == nil || c.Phase != "done" || c.Success || c.Error != "Not enough messages to compact." {
		t.Fatalf("want done/failed with error, got %+v", c)
	}
}

// The "init" subtype (session start/reconnect, and the line claude-code
// re-emits right after a compaction completes) is bookkeeping — no event.
func TestSystemEventsInitIgnored(t *testing.T) {
	l := parseWireLine(t, `{"type":"system","subtype":"init","session_id":"s1","model":"claude-x"}`)
	if evs := l.events(false, false); len(evs) != 0 {
		t.Fatalf("want no events for init line, got %+v", evs)
	}
}
