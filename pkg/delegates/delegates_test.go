package delegates_test

import (
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/delegates"
)

var t0 = time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)

func at(sec int) time.Time { return t0.Add(time.Duration(sec) * time.Second) }

func started(id, toolUse string, bg bool) contracts.SubagentTask {
	return contracts.SubagentTask{Phase: "started", TaskID: id, ToolUseID: toolUse, Description: "Run ls",
		SubagentType: "general-purpose", IsBackgrounded: bg, SpawnDepth: 1}
}

func one(t *testing.T, got []contracts.Delegate) contracts.Delegate {
	t.Helper()
	if len(got) != 1 {
		t.Fatalf("want exactly one changed record, got %d: %+v", len(got), got)
	}
	return got[0]
}

func none(t *testing.T, got []contracts.Delegate) {
	t.Helper()
	if len(got) != 0 {
		t.Fatalf("want no change, got %+v", got)
	}
}

// The real capture's order (providers/claudecode/testdata/
// stream-task-subagent.jsonl): the roster names the task before task_started
// and drops it BEFORE task_updated/task_notification say how it ended. The
// drop must not read as an ending — no stopped→completed flicker.
func TestBackgroundLifecycleInWireOrder(t *testing.T) {
	tr := delegates.New("ses_1", 0)
	tr.Roster([]string{"a7f5"}, at(0)) // unknown yet: ignored

	d := one(t, tr.Task(started("a7f5", "toolu_spawn", true), at(0)))
	if d.State != contracts.DelegateRunning || d.Runs != 1 || d.ToolUseID != "toolu_spawn" ||
		!d.Backgrounded || d.Description != "Run ls" || !d.StartedAt.Equal(at(0)) || d.SessionID != "ses_1" {
		t.Fatalf("started = %+v", d)
	}
	none(t, tr.Task(contracts.SubagentTask{Phase: "progress", TaskID: "a7f5", Description: "Running ls", TotalTokens: 10}, at(1)))

	tr.Roster([]string{}, at(2))
	if next, ok := tr.NextExpiry(); !ok || !next.Equal(at(2).Add(delegates.Grace)) {
		t.Fatalf("NextExpiry = %v %v", next, ok)
	}
	d = one(t, tr.Task(contracts.SubagentTask{Phase: "updated", TaskID: "a7f5", Status: "completed"}, at(2)))
	if d.State != contracts.DelegateCompleted || d.Inferred || d.Reason != "completed" || d.EndedAt == nil {
		t.Fatalf("updated = %+v", d)
	}
	d = one(t, tr.Task(contracts.SubagentTask{Phase: "notification", TaskID: "a7f5", Status: "completed",
		OutputFile: "/tmp/out", TotalTokens: 99, ToolUses: 1, DurationMS: 1900}, at(2)))
	// output_file is the task's output, not its conversation: only a run-time
	// inspection of the harness's store sets the transcript.
	if d.TranscriptPath != "" || d.TotalTokens != 99 || d.Description != "Run ls" {
		t.Fatalf("notification = %+v", d)
	}
	if _, ok := tr.NextExpiry(); ok {
		t.Error("a settled delegate must not keep a roster clock")
	}
	none(t, tr.Expire(at(60)))
}

// A backgrounded delegate that drops out of the roster and is never reported
// on (a cascading kill) is concluded stopped once Grace passes — ended when it
// left — and a report that turns up after all overrides the inference.
func TestRosterAbsenceExpiresThenReportWins(t *testing.T) {
	tr := delegates.New("ses_1", 0)
	one(t, tr.Task(started("a1", "toolu_1", true), at(0)))
	tr.Roster(nil, at(10))
	none(t, tr.Expire(at(12)))

	d := one(t, tr.Expire(at(10).Add(delegates.Grace)))
	if d.State != contracts.DelegateStopped || !d.Inferred || d.Reason != delegates.ReasonRoster || !d.EndedAt.Equal(at(10)) {
		t.Fatalf("expired = %+v", d)
	}
	d = one(t, tr.Task(contracts.SubagentTask{Phase: "notification", TaskID: "a1", Status: "killed"}, at(20)))
	if d.State != contracts.DelegateStopped || d.Inferred || d.Reason != "killed" {
		t.Fatalf("a late report must replace the inference: %+v", d)
	}
}

// Coming back into the roster cancels the clock.
func TestRosterReturnCancelsExpiry(t *testing.T) {
	tr := delegates.New("ses_1", 0)
	one(t, tr.Task(started("a1", "toolu_1", true), at(0)))
	tr.Roster(nil, at(1))
	tr.Roster([]string{"a1"}, at(2))
	none(t, tr.Expire(at(60)))
}

// A Claude Code relay resumes a finished delegate under the same task_id but
// reports the SendMessage call's tool_use_id; the anchor must not move, and
// the re-entry runs backgrounded.
func TestReentryKeepsAnchorAndCountsRuns(t *testing.T) {
	tr := delegates.New("ses_1", 0)
	one(t, tr.Task(started("a1", "toolu_spawn", false), at(0)))
	one(t, tr.Task(contracts.SubagentTask{Phase: "notification", TaskID: "a1", Status: "completed"}, at(5)))

	d := one(t, tr.Task(started("a1", "toolu_sendmessage", true), at(30)))
	if d.State != contracts.DelegateRunning || d.Runs != 2 || d.ToolUseID != "toolu_spawn" ||
		!d.Backgrounded || d.EndedAt != nil || d.Reason != "" || !d.StartedAt.Equal(at(30)) {
		t.Fatalf("re-entry = %+v", d)
	}
	if !tr.LiveSpawn("toolu_spawn") || tr.LiveSpawn("toolu_sendmessage") {
		t.Error("liveness is keyed on the original spawn id the delegate's events carry")
	}
}

// The parent waits on a foreground delegate, so one still running when the
// turn ends was cut short; backgrounded delegates outlive the turn.
func TestTurnEndStopsOnlyForeground(t *testing.T) {
	tr := delegates.New("ses_1", 0)
	one(t, tr.Task(started("fg", "toolu_fg", false), at(0)))
	one(t, tr.Task(started("bg", "toolu_bg", true), at(1)))

	d := one(t, tr.TurnEnd(at(5)))
	if d.HarnessID != "fg" || d.State != contracts.DelegateStopped || !d.Inferred || d.Reason != delegates.ReasonTurnEnd {
		t.Fatalf("turn end = %+v", d)
	}
	if !tr.LiveSpawn("toolu_bg") {
		t.Error("the backgrounded delegate must still be running after its parent's turn")
	}
}

func TestProcessExitStopsEverythingRunning(t *testing.T) {
	tr := delegates.New("ses_1", 0)
	one(t, tr.Task(started("b", "toolu_b", true), at(1)))
	one(t, tr.Task(started("a", "toolu_a", true), at(0)))
	one(t, tr.Task(contracts.SubagentTask{Phase: "notification", TaskID: "b", Status: "completed"}, at(2)))

	d := one(t, tr.ProcessExit(at(9)))
	if d.HarnessID != "a" || d.State != contracts.DelegateStopped || d.Reason != delegates.ReasonProcessExit {
		t.Fatalf("process exit = %+v", d)
	}
	none(t, tr.ProcessExit(at(10)))
}

// A runner only seeds at start, so a stored running record belonged to a
// harness process that is gone. Finished records load as they are, so a later
// resume of one keeps its identity and run count.
func TestSeedOrphansRunningAndKeepsHistory(t *testing.T) {
	tr := delegates.New("ses_1", 0)
	got := tr.Seed([]contracts.Delegate{
		{SessionID: "ses_1", HarnessID: "old", ToolUseID: "toolu_old", State: contracts.DelegateCompleted, Runs: 1, StartedAt: at(0)},
		{SessionID: "ses_1", HarnessID: "lost", ToolUseID: "toolu_lost", State: contracts.DelegateRunning, Runs: 1, StartedAt: at(1)},
	}, at(100))
	d := one(t, got)
	if d.HarnessID != "lost" || d.State != contracts.DelegateOrphaned || !d.Inferred || d.Reason != delegates.ReasonRunnerRestart {
		t.Fatalf("seed = %+v", d)
	}
	d = one(t, tr.Task(started("old", "toolu_relay", true), at(200)))
	if d.Runs != 2 || d.ToolUseID != "toolu_old" {
		t.Fatalf("resume of a seeded delegate = %+v", d)
	}
}

func TestStatusWords(t *testing.T) {
	cases := map[string]contracts.DelegateState{
		"completed": contracts.DelegateCompleted,
		"":          contracts.DelegateCompleted,
		"failed":    contracts.DelegateFailed,
		"killed":    contracts.DelegateStopped,
		"stopped":   contracts.DelegateStopped,
		"exploded":  contracts.DelegateFailed, // unknown words surface, never pass as success
	}
	for word, want := range cases {
		tr := delegates.New("ses_1", 0)
		one(t, tr.Task(started("a", "toolu_a", true), at(0)))
		d := one(t, tr.Task(contracts.SubagentTask{Phase: "notification", TaskID: "a", Status: word}, at(1)))
		if d.State != want {
			t.Errorf("status %q → %s, want %s", word, d.State, want)
		}
	}
}

// An evidenced ending stands: a second, different report doesn't rewrite it.
// A status-less patch and an empty task id change nothing.
func TestEvidencedEndingStands(t *testing.T) {
	tr := delegates.New("ses_1", 0)
	one(t, tr.Task(started("a", "toolu_a", true), at(0)))
	one(t, tr.Task(contracts.SubagentTask{Phase: "updated", TaskID: "a", Status: "failed"}, at(1)))
	none(t, tr.Task(contracts.SubagentTask{Phase: "updated", TaskID: "a", Status: "completed"}, at(2)))
	none(t, tr.Task(contracts.SubagentTask{Phase: "updated", TaskID: "a"}, at(3)))
	none(t, tr.Task(contracts.SubagentTask{Phase: "started"}, at(4)))
}

// A lifecycle that starts mid-way (the started line was missed) still yields
// a running record from its first progress line.
func TestProgressWithoutStartCreates(t *testing.T) {
	tr := delegates.New("ses_1", 0)
	d := one(t, tr.Task(contracts.SubagentTask{Phase: "progress", TaskID: "a", ToolUseID: "toolu_a", Description: "Running ls"}, at(0)))
	if d.State != contracts.DelegateRunning || d.Description != "" || d.ToolUseID != "toolu_a" {
		t.Fatalf("progress-first = %+v (a step is not a name)", d)
	}
}

// Capabilities start from the harness's matrix defaults, and a transcript —
// and with it continuing as a forked session — only on evidence.
func TestCapabilitiesFromDefaultsThenEvidence(t *testing.T) {
	tr := delegates.New("ses_1", 0)
	tr.SetDefaults(contracts.DelegateCapabilities{
		Steer: contracts.DelegateSteerRelay, StopsWithParent: true,
		Transcript: true, Continuable: true, // never defaults: dropped
	}, true)

	d := one(t, tr.Task(started("a1", "toolu_a1", true), at(0)))
	want := contracts.DelegateCapabilities{Steer: contracts.DelegateSteerRelay, StopsWithParent: true}
	if d.Capabilities == nil || *d.Capabilities != want {
		t.Fatalf("defaults = %+v, want %+v", d.Capabilities, want)
	}

	d = one(t, tr.Inspect("a1", delegates.Observation{
		TranscriptPath: "/p/agent-a1.jsonl", Type: "Explore", WorktreePath: "/wt",
	}))
	if d.TranscriptPath != "/p/agent-a1.jsonl" || d.WorktreePath != "/wt" ||
		!d.Capabilities.Transcript || !d.Capabilities.Continuable || d.Capabilities.Steer != contracts.DelegateSteerRelay {
		t.Fatalf("after inspection = %+v caps %+v", d, d.Capabilities)
	}
	// The started line's type stands; the store only fills gaps.
	if d.Type != "general-purpose" {
		t.Errorf("type = %q, want the lifecycle's own", d.Type)
	}
	none(t, tr.Inspect("a1", delegates.Observation{TranscriptPath: "/p/agent-a1.jsonl"}))
	none(t, tr.Inspect("unknown", delegates.Observation{TranscriptPath: "/x"}))
}

// A harness that can't fork never offers continuing, transcript or not; one
// with no matrix answer reads as watch-only.
func TestCapabilitiesWithoutForkOrDefaults(t *testing.T) {
	tr := delegates.New("ses_1", 0)
	tr.SetDefaults(contracts.DelegateCapabilities{}, false)
	one(t, tr.Task(started("a1", "toolu_a1", true), at(0)))
	d := one(t, tr.Inspect("a1", delegates.Observation{TranscriptPath: "/p/a1.jsonl"}))
	if !d.Capabilities.Transcript || d.Capabilities.Continuable || d.Capabilities.Steer != contracts.DelegateSteerNone {
		t.Fatalf("caps = %+v", d.Capabilities)
	}
}
