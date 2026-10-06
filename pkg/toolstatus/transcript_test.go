package toolstatus

import (
	"testing"

	"github.com/drewkidwell/mads/pkg/contracts"
)

// TestStampTranscriptStatuses: the refetch path resolves recorded calls from
// their recorded results with the same engine, and leaves a call with no
// result honestly open (in_progress, never guessed interrupted — a refetch
// has no turn-end signal; the event log's sweep records carry interruption).
func TestStampTranscriptStatuses(t *testing.T) {
	msgs := []contracts.TranscriptMessage{
		{Role: "user", Text: "go"},
		{Role: "tool", Tool: &contracts.ToolUse{ID: "t1", Name: "Bash", Kind: "execute"}},
		{Role: "tool_result", Tool: &contracts.ToolUse{ID: "t1"}},
		{Role: "tool", Tool: &contracts.ToolUse{ID: "t2", Name: "Write", Kind: "edit"}},
		{Role: "tool_result", Tool: &contracts.ToolUse{ID: "t2"}, IsError: true},
		{Role: "tool", Tool: &contracts.ToolUse{ID: "t3", Name: "Read", Kind: "read"}},
		{Role: "tool", Tool: &contracts.ToolUse{Name: "no_id"}},
		// A harness permission refusal reloads as blocked, not failed.
		{Role: "tool", Tool: &contracts.ToolUse{ID: "t4", Name: "Write", Kind: "edit"}},
		{Role: "tool_result", Tool: &contracts.ToolUse{ID: "t4"}, IsError: true,
			Text: "Claude requested permissions to write to /tmp/x.txt, but you haven't granted it yet."},
	}
	StampTranscriptStatuses(msgs)

	if got := msgs[1].Tool.Status; got != "completed" {
		t.Errorf("t1 status = %q, want completed", got)
	}
	if got := msgs[4].Tool.Status; got != "failed" {
		t.Errorf("t2 result status = %q, want failed", got)
	}
	if got := msgs[3].Tool.Status; got != "failed" {
		t.Errorf("t2 call status = %q, want failed", got)
	}
	if got := msgs[5].Tool.Status; got != "in_progress" {
		t.Errorf("t3 (no result) status = %q, want in_progress (honestly open)", got)
	}
	if got := msgs[6].Tool.Status; got != "" {
		t.Errorf("id-less call status = %q, want empty (unknown)", got)
	}
	if got := msgs[7].Tool.Status; got != Blocked {
		t.Errorf("t4 (refused) call status = %q, want blocked", got)
	}
}
