package claudecode

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
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

// fixtureLines loads a captured stream-json session from testdata, one
// wireLine per JSONL line, in stream order.
func fixtureLines(t *testing.T, name string) []wireLine {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	var lines []wireLine
	for _, ln := range strings.Split(string(raw), "\n") {
		if strings.TrimSpace(ln) == "" {
			continue
		}
		lines = append(lines, parseWireLine(t, ln))
	}
	return lines
}

// fixtureEvents runs every fixture line through events(), concatenated in
// stream order — the same normalization session.go's handleLine applies.
func fixtureEvents(t *testing.T, name string, partial bool) []provider.Event {
	t.Helper()
	var evs []provider.Event
	for _, l := range fixtureLines(t, name) {
		evs = append(evs, l.events(false, partial)...)
	}
	return evs
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

// taskFixture is the real claude-code 2.x capture of a Task/Agent subagent
// run (backgrounded, as claude-code spawns them by default) — the ground
// truth for subagent tagging, task lifecycle, and the double init/result
// shape the notification re-invocation produces.
const taskFixture = "stream-task-subagent.jsonl"

// Fixture identity of the one subagent the capture spawns.
const (
	fixtureParentToolUse = "toolu_01XxWGSWmBexWa3B8SJpuL1b"
	fixtureSubagentType  = "general-purpose"
	fixtureTaskDesc      = "Run ls in current directory"
)

// Every event derived from a parent-tagged assistant/user line must carry
// the sidechain identity — the subagent's tool_use, its tool_result, and its
// final text — while the main agent's own events stay untagged. Before this
// tagging existed the subagent's stream interleaved into the main transcript
// as if the main agent produced it (the live bug P0 fixes).
func TestFixtureSubagentEventsTagged(t *testing.T) {
	var tagged, main []provider.Event
	usage := 0
	for _, ev := range fixtureEvents(t, taskFixture, false) {
		if ev.Kind == provider.EventSubagentUsage {
			// One per content-block line of the subagent's: its API calls.
			if ev.ParentToolUseID != fixtureParentToolUse {
				t.Errorf("usage event tagged %q", ev.ParentToolUseID)
			}
			usage++
			continue
		}
		if ev.ParentToolUseID != "" {
			tagged = append(tagged, ev)
		} else {
			main = append(main, ev)
		}
	}

	if usage != 4 {
		t.Errorf("%d subagent usage events, want 4 (one per assistant line)", usage)
	}
	wantKinds := []provider.EventKind{provider.EventToolUse, provider.EventToolResult, provider.EventText}
	if len(tagged) != len(wantKinds) {
		t.Fatalf("want %d tagged events, got %+v", len(wantKinds), tagged)
	}
	for i, ev := range tagged {
		if ev.Kind != wantKinds[i] {
			t.Errorf("tagged event %d: kind %s, want %s", i, ev.Kind, wantKinds[i])
		}
		if ev.ParentToolUseID != fixtureParentToolUse || ev.SubagentType != fixtureSubagentType || ev.TaskDescription != fixtureTaskDesc {
			t.Errorf("tagged event %d: identity %q/%q/%q", i, ev.ParentToolUseID, ev.SubagentType, ev.TaskDescription)
		}
	}
	if tagged[0].Tool == nil || tagged[0].Tool.Name != "Bash" {
		t.Errorf("tagged tool_use: want the subagent's Bash call, got %+v", tagged[0].Tool)
	}

	// The subagent's verbatim report must never appear untagged (the main
	// agent's own reply may paraphrase it — that one is legitimately main).
	report := tagged[len(tagged)-1].Text
	for _, ev := range main {
		if ev.SubagentType != "" || ev.TaskDescription != "" {
			t.Errorf("main-agent event carries subagent identity: %+v", ev)
		}
		if ev.Kind == provider.EventText && ev.Text == report {
			t.Errorf("subagent report text leaked into the main stream: %q", ev.Text)
		}
	}
}

// The four task_* system subtypes normalize into EventTask, each phase
// carrying the fields its wire shape actually has (task_updated nests its
// status in a patch object; the others report at top level).
func TestFixtureTaskLifecycle(t *testing.T) {
	var tasks []*provider.TaskStatus
	for _, ev := range fixtureEvents(t, taskFixture, false) {
		if ev.Kind == provider.EventTask {
			if ev.Task == nil {
				t.Fatalf("EventTask without Task payload")
			}
			tasks = append(tasks, ev.Task)
		}
	}
	if len(tasks) != 4 {
		t.Fatalf("want 4 EventTask (started/progress/updated/notification), got %d: %+v", len(tasks), tasks)
	}
	for i, want := range []string{"started", "progress", "updated", "notification"} {
		if tasks[i].Phase != want {
			t.Fatalf("task %d: phase %q, want %q", i, tasks[i].Phase, want)
		}
		if tasks[i].TaskID != "a7f5f61655a886928" {
			t.Errorf("task %d: task_id %q", i, tasks[i].TaskID)
		}
	}

	started := tasks[0]
	if started.ToolUseID != fixtureParentToolUse || started.Description != fixtureTaskDesc ||
		started.SubagentType != fixtureSubagentType || started.Prompt == "" ||
		!started.IsBackgrounded || started.SpawnDepth != 1 {
		t.Errorf("started: %+v", started)
	}
	progress := tasks[1]
	if progress.TotalTokens != 10946 || progress.ToolUses != 1 || progress.DurationMS != 1905 || progress.LastToolName != "Bash" {
		t.Errorf("progress: %+v", progress)
	}
	updated := tasks[2]
	if updated.Status != "completed" {
		t.Errorf("updated: status %q from patch, want completed", updated.Status)
	}
	notification := tasks[3]
	if notification.Status != "completed" || notification.Summary == "" || notification.OutputFile == "" ||
		notification.TotalTokens != 11763 || notification.ToolUseID != fixtureParentToolUse {
		t.Errorf("notification: %+v", notification)
	}
}

// A backgrounded subagent's completion re-invokes the model inside the same
// user turn, so the process emits a second system/init and a second result
// line (stamped origin "task-notification"). The invocation must still
// normalize to exactly one turn end — the origin-less result — or every
// per-turn consumer double-fires and total_cost_usd (repeated cumulatively
// on the notification result) double-counts.
func TestFixtureDoubleInitResultSingleTurnEnd(t *testing.T) {
	lines := fixtureLines(t, taskFixture)
	inits, results := 0, 0
	for _, l := range lines {
		if l.Type == "system" && l.Subtype == "init" {
			inits++
		}
		if l.Type == "result" {
			results++
			if l.Origin != nil {
				if evs := l.events(false, false); len(evs) != 0 {
					t.Errorf("notification-origin result: want no events, got %+v", evs)
				}
			}
		}
	}
	// Guard the premise: the capture really contains the double shape.
	if inits != 2 || results != 2 {
		t.Fatalf("fixture shape drifted: %d init lines, %d result lines, want 2/2", inits, results)
	}

	var ends []provider.Event
	for _, ev := range fixtureEvents(t, taskFixture, false) {
		if ev.Kind == provider.EventTurnEnd {
			ends = append(ends, ev)
		}
	}
	if len(ends) != 1 {
		t.Fatalf("want exactly one EventTurnEnd, got %d", len(ends))
	}
	if ends[0].Usage == nil || ends[0].Usage.CostUSD == 0 || ends[0].Usage.ContextTokens == 0 {
		t.Fatalf("turn end lost its usage: %+v", ends[0].Usage)
	}
}

// If the notification re-invocation itself fails, the suppressed result
// still surfaces the failure as an EventError — just never a turn end.
func TestNotificationResultErrorStillSurfaces(t *testing.T) {
	l := parseWireLine(t, `{"type":"result","subtype":"error","is_error":true,"result":"boom","origin":{"kind":"task-notification"}}`)
	evs := l.events(false, false)
	if len(evs) != 1 || evs[0].Kind != provider.EventError || evs[0].Text != "boom" {
		t.Fatalf("want one EventError 'boom', got %+v", evs)
	}
}

// Thinking blocks emit as EventThinking in both transports: streamDeltaEvents
// drops thinking deltas, so unlike text the whole-message block is never a
// duplicate. Signature-only blocks (empty thinking text — the only shape the
// real capture carries) say nothing and emit nothing.
func TestThinkingBlockEmitted(t *testing.T) {
	l := parseWireLine(t, `{"type":"assistant","message":{"role":"assistant","content":[`+
		`{"type":"thinking","thinking":"weighing options","signature":"sig"},`+
		`{"type":"text","text":"the reply"}]}}`)

	partial := l.events(false, true)
	if len(partial) != 1 || partial[0].Kind != provider.EventThinking || partial[0].Text != "weighing options" {
		t.Fatalf("partial: want one EventThinking, got %+v", partial)
	}
	whole := l.events(false, false)
	if len(whole) != 2 || whole[0].Kind != provider.EventThinking || whole[1].Kind != provider.EventText {
		t.Fatalf("whole: want thinking+text, got %+v", whole)
	}

	empty := parseWireLine(t, `{"type":"assistant","message":{"role":"assistant","content":[{"type":"thinking","thinking":"","signature":"sig"}]}}`)
	if evs := empty.events(false, false); len(evs) != 0 {
		t.Fatalf("signature-only thinking block: want no events, got %+v", evs)
	}
}

// system/thinking_tokens lines stream a running estimate while the model
// thinks — normalized to EventThinking carrying the count and no text (the
// live "Thinking…" indicator's title source).
func TestFixtureThinkingTokens(t *testing.T) {
	var ticks []provider.Event
	for _, ev := range fixtureEvents(t, taskFixture, false) {
		if ev.Kind == provider.EventThinking {
			ticks = append(ticks, ev)
		}
	}
	// All 9 thinking events in the capture are token ticks: its thinking
	// blocks are all signature-only.
	if len(ticks) != 9 {
		t.Fatalf("want 9 thinking-token ticks, got %d: %+v", len(ticks), ticks)
	}
	if ticks[0].ThinkingTokens != 50 || ticks[0].Text != "" {
		t.Fatalf("first tick: want ThinkingTokens=50 with no text, got %+v", ticks[0])
	}
	for i, ev := range ticks {
		if ev.ThinkingTokens <= 0 {
			t.Errorf("tick %d: non-positive estimate: %+v", i, ev)
		}
	}
}
