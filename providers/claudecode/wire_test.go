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
