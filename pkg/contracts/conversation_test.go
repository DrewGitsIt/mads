package contracts_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/golden"
)

// sampleConversationPayloads exercises every payload the session runner
// persists for subagent/task/thinking history replay (T6): a subagent's
// enriched tool_use, its tool_result, a task lifecycle notification, a main
// -agent thinking block, and a synthesized status change — plus the two
// main-agent state transitions the session rail reads (session.agent_state).
func sampleConversationPayloads() map[string]any {
	return map[string]any{
		"subagent_tool_use": contracts.SubagentEvent{
			ParentToolUseID: "toolu_01agent",
			SubagentType:    "general-purpose",
			TaskDescription: "Audit error handling",
			Kind:            "tool_use",
			Tool: &contracts.ToolUse{
				ID:        "toolu_02read",
				Name:      "Read",
				Input:     json.RawMessage(`{"file_path":"/home/u/app/main.go"}`),
				Path:      "/home/u/app/main.go",
				Kind:      "read",
				Title:     "/home/u/app/main.go",
				Status:    "in_progress",
				Locations: []contracts.ToolLocation{{Path: "/home/u/app/main.go"}},
			},
		},
		"subagent_tool_result": contracts.SubagentEvent{
			ParentToolUseID: "toolu_01agent",
			SubagentType:    "general-purpose",
			TaskDescription: "Audit error handling",
			Kind:            "tool_result",
			Text:            "exit status 1",
			Tool:            &contracts.ToolUse{ID: "toolu_02read", Status: "failed"},
			IsError:         true,
		},
		"subagent_task": contracts.SubagentTask{
			Phase:        "notification",
			TaskID:       "task-1",
			ToolUseID:    "toolu_01agent",
			SubagentType: "general-purpose",
			TotalTokens:  84213,
			ToolUses:     17,
			DurationMS:   93250,
			Status:       "completed",
			Summary:      "Audited 12 files; two handlers swallow errors.",
			OutputFile:   "/tmp/agent-task-1.md",
		},
		"thinking": contracts.ThinkingBlock{
			Text: "The user wants the error paths audited first…",
		},
		"tool_status_change": contracts.ToolStatusChange{
			ToolUseID:       "toolu_02read",
			Status:          "interrupted",
			ParentToolUseID: "toolu_01agent",
		},
		"agent_state_working": contracts.AgentState{
			State: contracts.AgentWorking,
			Label: "Running bash · make check",
			Since: time.Date(2026, 9, 28, 14, 5, 0, 0, time.UTC),
		},
		"agent_state_idle": contracts.AgentState{
			State: contracts.AgentIdle,
			Since: time.Date(2026, 9, 28, 14, 6, 12, 0, time.UTC),
		},
	}
}

// Producer side of the contract: what the runner writes into an Envelope
// payload must match the pinned golden byte-for-byte.
func TestConversationPayloadProducerMatchesGolden(t *testing.T) {
	got, err := json.MarshalIndent(sampleConversationPayloads(), "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	golden.Assert(t, "conversation_payloads.json", append(got, '\n'))
}

// Consumer side of the contract: parsing the same golden must reproduce the
// full values — including every enriched tool-use field and tag.
func TestConversationPayloadConsumerParsesGolden(t *testing.T) {
	var got struct {
		SubagentToolUse    contracts.SubagentEvent    `json:"subagent_tool_use"`
		SubagentToolResult contracts.SubagentEvent    `json:"subagent_tool_result"`
		SubagentTask       contracts.SubagentTask     `json:"subagent_task"`
		Thinking           contracts.ThinkingBlock    `json:"thinking"`
		ToolStatusChange   contracts.ToolStatusChange `json:"tool_status_change"`
		AgentStateWorking  contracts.AgentState       `json:"agent_state_working"`
		AgentStateIdle     contracts.AgentState       `json:"agent_state_idle"`
	}
	if err := json.Unmarshal(golden.Read(t, "conversation_payloads.json"), &got); err != nil {
		t.Fatalf("consumer failed to parse golden: %v", err)
	}
	want := sampleConversationPayloads()
	// Compare via compacted re-marshal: RawMessage fields preserve the
	// golden's indentation on the parsed side (same normalization
	// event_test.go's compact applies).
	check := func(field string, gotV any) {
		t.Helper()
		if compact(t, mustMarshal(t, gotV)) != compact(t, mustMarshal(t, want[field])) {
			t.Errorf("%s:\n got %+v\nwant %+v", field, gotV, want[field])
		}
	}
	check("subagent_tool_use", got.SubagentToolUse)
	check("subagent_tool_result", got.SubagentToolResult)
	check("subagent_task", got.SubagentTask)
	check("thinking", got.Thinking)
	check("tool_status_change", got.ToolStatusChange)
	check("agent_state_working", got.AgentStateWorking)
	check("agent_state_idle", got.AgentStateIdle)
}

func mustMarshal(t *testing.T, v any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal %+v: %v", v, err)
	}
	return raw
}
