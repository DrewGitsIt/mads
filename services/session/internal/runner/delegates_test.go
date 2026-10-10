package runner

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/pkg/delegates"
	"github.com/drewkidwell/mads/providers/claudecode"
	"github.com/drewkidwell/mads/providers/provider"
	"github.com/drewkidwell/mads/services/data/datatest"
)

func newDelegateRunner(t *testing.T) (*Runner, *dataclient.Client, string) {
	t.Helper()
	base := datatest.Start(t)
	sess := newSession(t, base)
	data := dataclient.New(base, sess.ID)
	return New(sess.ID, data, nil), data, sess.ID
}

func storedDelegate(t *testing.T, data *dataclient.Client, sessionID, harnessID string) contracts.Delegate {
	t.Helper()
	list, err := data.SessionDelegates(context.Background(), sessionID)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range list {
		if d.HarnessID == harnessID {
			return d
		}
	}
	t.Fatalf("no stored delegate %s in %+v", harnessID, list)
	return contracts.Delegate{}
}

func taskEv(phase, id, toolUse, status string, bg bool) provider.Event {
	return provider.Event{Kind: provider.EventTask, Task: &provider.TaskStatus{
		Phase: phase, TaskID: id, ToolUseID: toolUse, Status: status, IsBackgrounded: bg,
		Description: "Audit errors", SubagentType: "general-purpose", SpawnDepth: 1,
	}}
}

func sideTool(id, parent string) provider.Event {
	return provider.Event{Kind: provider.EventToolUse, Tool: &provider.ToolUse{ID: id, Name: "Read"}, ParentToolUseID: parent}
}

// A backgrounded delegate outlives its parent's turn: the record stays
// running, its in-flight call is NOT swept to interrupted (that override
// would outrank the result for good — the stuck-⊘ bug), the late result is
// stamped completed, and the wire's own ending (roster drop, then
// task_updated/notification) settles it without a stopped flicker.
func TestDelegateOutlivesParentTurn(t *testing.T) {
	r, data, sid := newDelegateRunner(t)

	r.observeConversation(provider.Event{Kind: provider.EventTaskRoster, TaskRoster: []string{"a1"}})
	r.observeConversation(taskEv("started", "a1", "toolu_agent", "", true))
	r.observeConversation(sideTool("toolu_read", "toolu_agent"))
	r.observeConversation(provider.Event{Kind: provider.EventTurnEnd})

	if d := storedDelegate(t, data, sid, "a1"); d.State != contracts.DelegateRunning || d.ToolUseID != "toolu_agent" || !d.Backgrounded {
		t.Fatalf("after the parent's turn: %+v", d)
	}
	if got := eventsOfType[contracts.ToolStatusChange](t, data, sid, contracts.SessionToolStatusChanged); len(got) != 0 {
		t.Fatalf("a live delegate's call was swept at turn end: %+v", got)
	}
	ev := r.observeConversation(provider.Event{Kind: provider.EventToolResult, Text: "ok",
		Tool: &provider.ToolUse{ID: "toolu_read"}, ParentToolUseID: "toolu_agent"})
	if ev.Tool.Status != provider.ToolStatusCompleted {
		t.Errorf("late result stamped %q, want completed", ev.Tool.Status)
	}

	r.observeConversation(provider.Event{Kind: provider.EventTaskRoster, TaskRoster: []string{}})
	r.observeConversation(taskEv("updated", "a1", "", "completed", false))
	r.observeConversation(provider.Event{Kind: provider.EventTask, Task: &provider.TaskStatus{
		Phase: "notification", TaskID: "a1", ToolUseID: "toolu_agent", Status: "completed", OutputFile: "/tmp/a1.md",
	}})

	d := storedDelegate(t, data, sid, "a1")
	if d.State != contracts.DelegateCompleted || d.Inferred {
		t.Fatalf("settled = %+v", d)
	}
	var states []contracts.DelegateState
	for _, c := range eventsOfType[contracts.Delegate](t, data, sid, contracts.SessionDelegateChanged) {
		states = append(states, c.State)
	}
	want := []contracts.DelegateState{contracts.DelegateRunning, contracts.DelegateCompleted}
	if len(states) != len(want) || states[0] != want[0] || states[1] != want[1] {
		t.Errorf("delegate_changed states = %v, want %v (running, settled — never stopped)", states, want)
	}
}

// A foreground delegate still running when its parent's turn ends was cut
// short with it; its unfinished call is closed out exactly once.
func TestDelegateForegroundEndsWithTurn(t *testing.T) {
	r, data, sid := newDelegateRunner(t)
	r.observeConversation(taskEv("started", "fg", "toolu_fg", "", false))
	r.observeConversation(sideTool("toolu_grep", "toolu_fg"))
	r.observeConversation(provider.Event{Kind: provider.EventTurnEnd, Interrupted: true})

	d := storedDelegate(t, data, sid, "fg")
	if d.State != contracts.DelegateStopped || !d.Inferred || d.Reason != delegates.ReasonTurnEnd {
		t.Fatalf("foreground after turn end = %+v", d)
	}
	got := eventsOfType[contracts.ToolStatusChange](t, data, sid, contracts.SessionToolStatusChanged)
	if len(got) != 1 || got[0].ToolUseID != "toolu_grep" || got[0].Status != "interrupted" {
		t.Fatalf("want the foreground delegate's call interrupted once, got %+v", got)
	}
}

// A backgrounded delegate that leaves the roster with no report (a cascading
// kill) is concluded stopped after the grace window, and its in-flight call
// is closed out then.
func TestDelegateRosterAbsenceExpires(t *testing.T) {
	r, data, sid := newDelegateRunner(t)
	r.delegates = delegates.New(sid, 30*time.Millisecond)

	r.observeConversation(taskEv("started", "a1", "toolu_agent", "", true))
	r.observeConversation(sideTool("toolu_read", "toolu_agent"))
	r.observeConversation(provider.Event{Kind: provider.EventTurnEnd})
	r.observeConversation(provider.Event{Kind: provider.EventTaskRoster, TaskRoster: []string{}})

	deadline := time.Now().Add(5 * time.Second)
	for {
		d := storedDelegate(t, data, sid, "a1")
		if d.State == contracts.DelegateStopped {
			if !d.Inferred || d.Reason != delegates.ReasonRoster {
				t.Fatalf("expired = %+v", d)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("never expired: %+v", d)
		}
		time.Sleep(10 * time.Millisecond)
	}
	got := eventsOfType[contracts.ToolStatusChange](t, data, sid, contracts.SessionToolStatusChanged)
	if len(got) != 1 || got[0].ToolUseID != "toolu_read" {
		t.Fatalf("the stopped delegate's call must close out: %+v", got)
	}
}

// The harness process ending takes every running delegate with it.
func TestDelegateProcessExit(t *testing.T) {
	r, data, sid := newDelegateRunner(t)
	r.observeConversation(taskEv("started", "a1", "toolu_agent", "", true))
	r.observeConversation(provider.Event{Kind: provider.EventTurnEnd})

	r.endDelegatesForProcessExit()
	r.flushTurnState()
	if d := storedDelegate(t, data, sid, "a1"); d.State != contracts.DelegateStopped || d.Reason != delegates.ReasonProcessExit {
		t.Fatalf("after process exit = %+v", d)
	}
}

// A runner that starts finds a running record from a runner that went away
// without closing it out: orphaned. A finished record is left alone and a
// later resume of it counts a second run.
func TestDelegateSeedOrphansStaleRunning(t *testing.T) {
	r, data, sid := newDelegateRunner(t)
	ctx := context.Background()
	for _, d := range []contracts.Delegate{
		{SessionID: sid, HarnessID: "lost", ToolUseID: "toolu_lost", State: contracts.DelegateRunning, Runs: 1, StartedAt: time.Now()},
		{SessionID: sid, HarnessID: "old", ToolUseID: "toolu_old", State: contracts.DelegateCompleted, Runs: 1, StartedAt: time.Now()},
	} {
		if _, err := data.PutDelegate(ctx, d); err != nil {
			t.Fatal(err)
		}
	}

	r.seedDelegates(ctx)
	if d := storedDelegate(t, data, sid, "lost"); d.State != contracts.DelegateOrphaned || d.Reason != delegates.ReasonRunnerRestart {
		t.Fatalf("stale running record = %+v", d)
	}
	r.observeConversation(taskEv("started", "old", "toolu_sendmessage", "", true))
	if d := storedDelegate(t, data, sid, "old"); d.State != contracts.DelegateRunning || d.Runs != 2 || d.ToolUseID != "toolu_old" {
		t.Fatalf("resumed old delegate = %+v", d)
	}
}

// Between turns, a bare thinking-token tick is as likely a backgrounded
// delegate's (claude-code doesn't tag system lines) as the main agent's, so
// it must not start a "working" state nothing would ever end.
func TestThinkingTickDoesNotStartWorking(t *testing.T) {
	r, data, sid := newDelegateRunner(t)
	r.observeConversation(provider.Event{Kind: provider.EventThinking, ThinkingTokens: 128})
	if got := eventsOfType[contracts.AgentState](t, data, sid, contracts.SessionAgentState); len(got) != 0 {
		t.Fatalf("a tick between turns published agent state: %+v", got)
	}
}

// Run-time capability checks: the provider matrix seeds a delegate's
// capabilities, and the harness's own store — read through the real
// claude-code adapter, over the layout 2.1.285 writes — corrects them for
// this delegate: its transcript is found, so it is continuable, and its
// worktree is recorded.
func TestDelegateCapabilitiesFromMatrixThenInspection(t *testing.T) {
	r, data, sid := newDelegateRunner(t)
	root := t.TempDir()
	dir := filepath.Join(root, "-private-tmp-work", "ref-1", "subagents")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "agent-a1.jsonl"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "agent-a1.meta.json"),
		[]byte(`{"agentType":"Explore","spawnDepth":1,"requestShape":"background","worktreePath":"/wt/a1"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	p := claudecode.New(claudecode.Config{TranscriptDir: root})
	r.prov = p
	r.lastRef = "ref-1"
	r.delegates.SetDefaults(delegates.Defaults(p.Matrix()))

	r.observeConversation(taskEv("started", "a1", "toolu_agent", "", true))
	d := storedDelegate(t, data, sid, "a1")
	if c := d.Capabilities; c == nil || c.Transcript || c.Steer != contracts.DelegateSteerRelay || c.Stop || !c.StopsWithParent {
		t.Fatalf("matrix defaults = %+v", d.Capabilities)
	}

	r.observeConversation(provider.Event{Kind: provider.EventTask, Task: &provider.TaskStatus{
		Phase: "notification", TaskID: "a1", Status: "completed",
	}})
	deadline := time.Now().Add(5 * time.Second)
	for {
		d = storedDelegate(t, data, sid, "a1")
		if d.Capabilities != nil && d.Capabilities.Transcript {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("inspection never landed: %+v caps %+v", d, d.Capabilities)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if d.TranscriptPath != filepath.Join(dir, "agent-a1.jsonl") || d.WorktreePath != "/wt/a1" || !d.Capabilities.Continuable {
		t.Fatalf("after inspection = %+v caps %+v", d, d.Capabilities)
	}
}

// Background processes die with the harness process: when its stream ends,
// connected viewers learn none are running.
func TestBackgroundClearsWhenProcessEnds(t *testing.T) {
	r, _, _ := newDelegateRunner(t)
	sess := &gsSession{events: make(chan provider.Event, 4)}
	r.mu.Lock()
	r.sess = sess
	r.mu.Unlock()
	viewer, unsubscribe := r.Subscribe()
	defer unsubscribe()

	sess.events <- provider.Event{Kind: provider.EventBackground, Background: []provider.BackgroundTask{{TaskID: "b1"}}}
	close(sess.events)
	r.pump(sess)

	var last provider.Event
	for len(viewer) > 0 {
		last = <-viewer
	}
	if last.Kind != provider.EventBackground || len(last.Background) != 0 {
		t.Fatalf("last event after exit = %+v, want an empty background list", last)
	}
}
