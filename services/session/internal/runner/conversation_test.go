package runner

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/providers/provider"
	"github.com/drewkidwell/mads/services/data/datatest"
)

// eventsOfType fetches this session's event log filtered to one type,
// decoding each payload into T. The fake provider emits post-execution
// events with no wire tool IDs and never a parent tag, so — as the shadow
// tests do — these tests direct-drive observeConversation with the exact
// events the claude-code adapter produces (providers/claudecode/wire.go).
func eventsOfType[T any](t *testing.T, data *dataclient.Client, sessionID string, typ contracts.EventType) []T {
	t.Helper()
	envs, err := data.Events(context.Background(), 0, sessionID, []string{string(typ)})
	if err != nil {
		t.Fatalf("events %s: %v", typ, err)
	}
	out := make([]T, len(envs))
	for i, env := range envs {
		if err := json.Unmarshal(env.Payload, &out[i]); err != nil {
			t.Fatalf("decode %s payload %s: %v", typ, env.Payload, err)
		}
	}
	return out
}

// TestConversationPersistence: subagent sidechain events, task lifecycle
// updates, and thinking blocks land in the event log — tagged, enriched, and
// status-stamped — while main-agent text and thinking token ticks do not
// (the transcript refetch replays the former; the latter are live-only).
func TestConversationPersistence(t *testing.T) {
	base := datatest.Start(t)
	sess := newSession(t, base)
	data := dataclient.New(base, sess.ID)
	r := New(sess.ID, data, nil)

	events := []provider.Event{
		{Kind: provider.EventText, Text: "main agent text"}, // not persisted
		{Kind: provider.EventThinking, ThinkingTokens: 512}, // token tick: not persisted
		{Kind: provider.EventThinking, Text: "planning the audit"},
		{Kind: provider.EventTask, Task: &provider.TaskStatus{
			Phase: "started", TaskID: "task-1", ToolUseID: "toolu_agent",
			Description: "Audit errors", SubagentType: "general-purpose",
			Prompt: "Audit error handling.", IsBackgrounded: true, SpawnDepth: 1,
		}},
		{
			Kind: provider.EventThinking, Text: "sidechain thinking",
			ParentToolUseID: "toolu_agent", SubagentType: "general-purpose", TaskDescription: "Audit errors",
		},
		{
			Kind: provider.EventToolUse,
			Tool: &provider.ToolUse{
				ID: "toolu_read", Name: "Read", Path: "/repo/main.go",
				Kind: provider.ToolKindRead, Title: "/repo/main.go",
				Locations: []provider.ToolLocation{{Path: "/repo/main.go", Line: 3}},
			},
			ParentToolUseID: "toolu_agent", SubagentType: "general-purpose", TaskDescription: "Audit errors",
		},
		{
			Kind: provider.EventToolResult, Text: "package main…",
			Tool:            &provider.ToolUse{ID: "toolu_read"},
			ParentToolUseID: "toolu_agent", SubagentType: "general-purpose", TaskDescription: "Audit errors",
		},
		{
			Kind: provider.EventText, Text: "found two swallowed errors",
			ParentToolUseID: "toolu_agent", SubagentType: "general-purpose", TaskDescription: "Audit errors",
		},
		{Kind: provider.EventTask, Task: &provider.TaskStatus{
			Phase: "notification", TaskID: "task-1", ToolUseID: "toolu_agent",
			TotalTokens: 9001, ToolUses: 1, DurationMS: 1234, Status: "completed",
			Summary: "done", OutputFile: "/tmp/task-1.md",
		}},
	}
	for _, ev := range events {
		r.observeConversation(ev)
	}

	sub := eventsOfType[contracts.SubagentEvent](t, data, sess.ID, contracts.SessionSubagentEvent)
	if len(sub) != 4 {
		t.Fatalf("persisted %d subagent events, want 4: %+v", len(sub), sub)
	}
	wantKinds := []string{"thinking", "tool_use", "tool_result", "text"}
	for i, se := range sub {
		if se.Kind != wantKinds[i] {
			t.Errorf("subagent event %d kind = %q, want %q", i, se.Kind, wantKinds[i])
		}
		if se.ParentToolUseID != "toolu_agent" || se.SubagentType != "general-purpose" || se.TaskDescription != "Audit errors" {
			t.Errorf("subagent event %d lost its tags: %+v", i, se)
		}
	}
	if tu := sub[1].Tool; tu == nil || tu.Kind != "read" || tu.Title != "/repo/main.go" ||
		tu.Status != "in_progress" || len(tu.Locations) != 1 || tu.Locations[0].Line != 3 {
		t.Errorf("subagent tool_use not enriched+stamped: %+v", sub[1].Tool)
	}
	if tr := sub[2].Tool; tr == nil || tr.ID != "toolu_read" || tr.Status != "completed" {
		t.Errorf("subagent tool_result not stamped completed: %+v", sub[2].Tool)
	}
	if sub[2].Text != "package main…" {
		t.Errorf("subagent tool_result text = %q", sub[2].Text)
	}

	tasks := eventsOfType[contracts.SubagentTask](t, data, sess.ID, contracts.SessionSubagentTask)
	if len(tasks) != 2 {
		t.Fatalf("persisted %d task events, want 2: %+v", len(tasks), tasks)
	}
	if tasks[0].Phase != "started" || tasks[0].TaskID != "task-1" || !tasks[0].IsBackgrounded ||
		tasks[0].SpawnDepth != 1 || tasks[0].Prompt != "Audit error handling." {
		t.Errorf("started task payload: %+v", tasks[0])
	}
	if tasks[1].Phase != "notification" || tasks[1].Status != "completed" || tasks[1].Summary != "done" ||
		tasks[1].TotalTokens != 9001 || tasks[1].OutputFile != "/tmp/task-1.md" {
		t.Errorf("notification task payload: %+v", tasks[1])
	}

	thinking := eventsOfType[contracts.ThinkingBlock](t, data, sess.ID, contracts.SessionThinking)
	if len(thinking) != 1 || thinking[0].Text != "planning the audit" {
		t.Fatalf("persisted thinking = %+v, want exactly the main-agent block (no token ticks, no sidechain)", thinking)
	}
}

// TestCompactStatusPersisted: both phases of a compaction pass land in the
// event log, failure reason included — a conversation opened after the fact
// has no other way to learn it (/output never replays chat frames).
func TestCompactStatusPersisted(t *testing.T) {
	base := datatest.Start(t)
	sess := newSession(t, base)
	data := dataclient.New(base, sess.ID)
	r := New(sess.ID, data, nil)

	r.observeConversation(provider.Event{Kind: provider.EventCompact, Compact: &provider.CompactStatus{Phase: "start"}})
	r.observeConversation(provider.Event{Kind: provider.EventCompact, Compact: &provider.CompactStatus{
		Phase: "done", Error: "Not enough messages to compact.",
	}})
	r.observeConversation(provider.Event{Kind: provider.EventCompact}) // no detail: nothing to persist

	got := eventsOfType[contracts.CompactStatus](t, data, sess.ID, contracts.SessionCompact)
	if len(got) != 2 {
		t.Fatalf("persisted %d compact events, want 2: %+v", len(got), got)
	}
	if got[0].Phase != "start" {
		t.Errorf("first compact event = %+v, want phase start", got[0])
	}
	if got[1].Phase != "done" || got[1].Success || got[1].Error != "Not enough messages to compact." {
		t.Errorf("second compact event = %+v, want a failed done with its reason", got[1])
	}
}

// TestObserveConversationStampsStatuses: the T5 engine's verdicts ride the
// returned (broadcast) events — in_progress on announcement, completed/
// failed on the result — and the turn-end sweep persists "interrupted" for
// a call whose result never came, tagged with its sidechain parent.
func TestObserveConversationStampsStatuses(t *testing.T) {
	base := datatest.Start(t)
	sess := newSession(t, base)
	data := dataclient.New(base, sess.ID)
	r := New(sess.ID, data, nil)

	ev := r.observeConversation(provider.Event{
		Kind: provider.EventToolUse,
		Tool: &provider.ToolUse{ID: "t1", Name: "Bash"},
	})
	if ev.Tool.Status != provider.ToolStatusInProgress {
		t.Errorf("tool_use status = %q, want in_progress", ev.Tool.Status)
	}
	r.observeConversation(provider.Event{
		Kind: provider.EventToolUse,
		Tool: &provider.ToolUse{ID: "t2", Name: "Read"}, ParentToolUseID: "toolu_agent",
	})
	ev = r.observeConversation(provider.Event{
		Kind: provider.EventToolResult, IsError: true,
		Tool: &provider.ToolUse{ID: "t1"},
	})
	if ev.Tool.Status != provider.ToolStatusFailed {
		t.Errorf("error tool_result status = %q, want failed", ev.Tool.Status)
	}

	r.observeConversation(provider.Event{Kind: provider.EventTurnEnd})

	changes := eventsOfType[contracts.ToolStatusChange](t, data, sess.ID, contracts.SessionToolStatusChanged)
	if len(changes) != 1 {
		t.Fatalf("persisted %d tool status changes, want 1 (only the orphan): %+v", len(changes), changes)
	}
	if changes[0].ToolUseID != "t2" || changes[0].Status != "interrupted" || changes[0].ParentToolUseID != "toolu_agent" {
		t.Errorf("orphan sweep = %+v, want t2 interrupted tagged toolu_agent", changes[0])
	}

	// Idempotent across turns: nothing left open, a second turn end adds
	// nothing.
	r.observeConversation(provider.Event{Kind: provider.EventTurnEnd})
	if got := eventsOfType[contracts.ToolStatusChange](t, data, sess.ID, contracts.SessionToolStatusChanged); len(got) != 1 {
		t.Errorf("second turn end appended %d extra changes", len(got)-1)
	}
}

// TestPermissionParkStatusFlow: a parked tool call goes "pending" in the
// event log the moment RequestPermission parks it, and an allow returns it
// to "in_progress" — the tool_use_id tie the hook wire itself never carries.
func TestPermissionParkStatusFlow(t *testing.T) {
	base := datatest.Start(t)
	sess := newSession(t, base)
	data := dataclient.New(base, sess.ID)
	r := New(sess.ID, data, nil)

	r.observeConversation(provider.Event{
		Kind: provider.EventToolUse,
		Tool: &provider.ToolUse{ID: "t1", Name: "Bash"},
	})

	done := make(chan string, 1)
	go func() { done <- r.RequestPermission("Bash", json.RawMessage(`{"command":"ls"}`)) }()

	waitFor(t, "pending status change", func() bool {
		changes := eventsOfType[contracts.ToolStatusChange](t, data, sess.ID, contracts.SessionToolStatusChanged)
		return len(changes) == 1 && changes[0].ToolUseID == "t1" && changes[0].Status == "pending"
	})

	reqs := r.PermissionRequests()
	if len(reqs) != 1 {
		t.Fatalf("pending permission requests = %d, want 1", len(reqs))
	}
	if !r.ResolvePermission(reqs[0].ID, "allow") {
		t.Fatal("ResolvePermission returned false")
	}
	select {
	case decision := <-done:
		if decision != "allow" {
			t.Fatalf("decision = %q, want allow", decision)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("RequestPermission never returned")
	}

	waitFor(t, "in_progress status change", func() bool {
		changes := eventsOfType[contracts.ToolStatusChange](t, data, sess.ID, contracts.SessionToolStatusChanged)
		return len(changes) == 2 && changes[1].ToolUseID == "t1" && changes[1].Status == "in_progress"
	})

	// The eventual result closes it out normally.
	ev := r.observeConversation(provider.Event{Kind: provider.EventToolResult, Tool: &provider.ToolUse{ID: "t1"}})
	if ev.Tool.Status != provider.ToolStatusCompleted {
		t.Errorf("post-allow result status = %q, want completed", ev.Tool.Status)
	}
}
