package delegates_test

import (
	"testing"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/delegates"
	"github.com/drewkidwell/mads/providers/provider"
)

// nested is a depth-2 start, tagged as the provider tags it: with the
// spawning subagent's own spawn call.
func nested(id, toolUse, parentSpawn string, bg bool) contracts.SubagentTask {
	r := started(id, toolUse, bg)
	r.SpawnDepth, r.ParentToolUseID = 2, parentSpawn
	return r
}

func completedTask(id string) contracts.SubagentTask {
	return contracts.SubagentTask{Phase: "notification", TaskID: id, Status: "completed"}
}

// byID indexes a result by harness id, failing on a duplicate.
func byID(t *testing.T, ds []contracts.Delegate) map[string]contracts.Delegate {
	t.Helper()
	out := map[string]contracts.Delegate{}
	for _, d := range ds {
		if _, dup := out[d.HarnessID]; dup {
			t.Fatalf("%s returned twice: %+v", d.HarnessID, ds)
		}
		out[d.HarnessID] = d
	}
	return out
}

func waiting(subagents, processes int) *contracts.DelegateWaiting {
	return &contracts.DelegateWaiting{Subagents: subagents, Processes: processes}
}

func wantWaiting(t *testing.T, what string, got, want *contracts.DelegateWaiting) {
	t.Helper()
	if (got == nil) != (want == nil) || (got != nil && *got != *want) {
		t.Errorf("%s: waiting_on = %+v, want %+v", what, got, want)
	}
}

// The parent tag on a nested start names the spawning delegate by its spawn
// call; the record keeps its harness id. First seen wins: a later start
// (a re-entry relayed by someone else) never moves it.
func TestNestedStartRecordsParent(t *testing.T) {
	tr := delegates.New("ses_1", 0)
	one(t, tr.Task(started("p", "toolu_p", true), at(0)))
	c := one(t, tr.Task(nested("c", "toolu_c", "toolu_p", true), at(1)))
	if c.ParentHarnessID != "p" || c.Depth != 2 {
		t.Fatalf("child = %+v", c)
	}
	// No spawn depth on the line: one below the parent.
	r := started("g", "toolu_g", true)
	r.SpawnDepth, r.ParentToolUseID = 0, "toolu_c"
	if g := one(t, tr.Task(r, at(2))); g.ParentHarnessID != "c" || g.Depth != 3 {
		t.Errorf("grandchild = %+v", g)
	}
	// An untagged start stays the main agent's.
	if d := one(t, tr.Task(started("m", "toolu_m", true), at(3))); d.ParentHarnessID != "" {
		t.Errorf("main agent's = %+v", d)
	}
	tr.Task(completedTask("c"), at(4))
	again := nested("c", "toolu_relay", "toolu_m", true)
	for _, d := range tr.Task(again, at(5)) {
		if d.HarnessID == "c" && d.ParentHarnessID != "p" {
			t.Errorf("re-entry moved the parent: %+v", d)
		}
	}
}

// The harness's store fills a parent the live stream didn't tag (a start
// mads missed), and history records it from the meta file. A live one
// already there stands.
func TestParentFromInspectAndHistory(t *testing.T) {
	tr := delegates.New("ses_1", 0)
	one(t, tr.Task(started("p", "toolu_p", true), at(0)))
	one(t, tr.Task(started("c", "toolu_c", true), at(1)))
	if d := one(t, tr.Inspect("c", delegates.Observation{ParentHarnessID: "p"})); d.ParentHarnessID != "p" {
		t.Errorf("inspected = %+v", d)
	}
	none(t, tr.Inspect("c", delegates.Observation{ParentHarnessID: "other"}))
	none(t, tr.Inspect("p", delegates.Observation{ParentHarnessID: "p"})) // never its own parent

	d := delegates.FromHistory("ses_1", provider.DelegateRecord{
		DelegateInfo: provider.DelegateInfo{Depth: 2, ParentHarnessID: "p"},
		HarnessID:    "c", Status: "completed", EndedAt: at(9), StartedAt: at(1),
	}, delegates.CapabilityDefaults{})
	if d.ParentHarnessID != "p" || d.WaitingOn != nil {
		t.Errorf("from history = %+v", d)
	}
}

// A delegate whose run completed while its child still runs is waiting on
// it — state completed, WaitingOn set — and the child ending clears it,
// returning both records. Claude Code then re-enters the parent, which
// runs again with nothing waiting.
func TestWaitingOnChild(t *testing.T) {
	tr := delegates.New("ses_1", 0)
	one(t, tr.Task(started("p", "toolu_p", true), at(0)))
	one(t, tr.Task(nested("c", "toolu_c", "toolu_p", true), at(1)))

	p := one(t, tr.Task(completedTask("p"), at(2)))
	if p.State != contracts.DelegateCompleted {
		t.Fatalf("parent = %+v", p)
	}
	wantWaiting(t, "parent, child running", p.WaitingOn, waiting(1, 0))

	got := byID(t, tr.Task(completedTask("c"), at(3)))
	if len(got) != 2 || got["c"].State != contracts.DelegateCompleted {
		t.Fatalf("child ending = %+v", got)
	}
	wantWaiting(t, "parent, child done", got["p"].WaitingOn, nil)

	// Waiting again on a second child, then the harness re-enters the
	// parent: running never waits.
	got = byID(t, tr.Task(nested("c2", "toolu_c2", "toolu_p", true), at(4)))
	wantWaiting(t, "parent, second child", got["p"].WaitingOn, waiting(1, 0))
	none(t, tr.Task(started("c2", "toolu_c2", true), at(4))) // a repeat start changes nothing
	re := one(t, tr.Task(started("p", "toolu_relay", true), at(5)))
	if re.State != contracts.DelegateRunning || re.Runs != 2 {
		t.Fatalf("re-entry = %+v", re)
	}
	wantWaiting(t, "re-entered", re.WaitingOn, nil)
}

// The child's start after its parent completed (a nested spawn whose start
// line trails the parent's ending) makes the parent waiting: the child's
// start returns the parent's record too.
func TestChildStartReturnsWaitingParent(t *testing.T) {
	tr := delegates.New("ses_1", 0)
	one(t, tr.Task(started("p", "toolu_p", true), at(0)))
	one(t, tr.Task(completedTask("p"), at(1)))
	got := byID(t, tr.Task(nested("c", "toolu_c", "toolu_p", true), at(2)))
	if len(got) != 2 {
		t.Fatalf("got %+v", got)
	}
	wantWaiting(t, "parent", got["p"].WaitingOn, waiting(1, 0))
}

// Background processes a delegate started keep it waiting after its run
// completes; the main agent's own processes are no delegate's.
func TestWaitingOnProcesses(t *testing.T) {
	tr := delegates.New("ses_1", 0)
	one(t, tr.Task(started("p", "toolu_p", true), at(0)))
	bash := []provider.BackgroundTask{
		{TaskID: "b1", ToolUseID: "toolu_b1", ParentToolUseID: "toolu_p"},
		{TaskID: "b2", ToolUseID: "toolu_b2", ParentToolUseID: "toolu_p"},
		{TaskID: "main", ToolUseID: "toolu_main"},
	}
	none(t, tr.Background(bash, at(1))) // still running: not waiting

	p := one(t, tr.Task(completedTask("p"), at(2)))
	wantWaiting(t, "after its run", p.WaitingOn, waiting(0, 2))

	p = one(t, tr.Background(bash[1:], at(3)))
	wantWaiting(t, "one left", p.WaitingOn, waiting(0, 1))
	none(t, tr.Background(bash[1:], at(4)))
	p = one(t, tr.Background(bash[2:], at(5)))
	wantWaiting(t, "only the main agent's left", p.WaitingOn, nil)
}

// A chain waits as long as its deepest live link: a grandchild's process
// keeps its completed parent waiting, which keeps the top one waiting (it
// counts the waiting child, not the grandchild). The process ending
// unwinds the whole chain at once.
func TestWaitingNestedFixpoint(t *testing.T) {
	tr := delegates.New("ses_1", 0)
	one(t, tr.Task(started("a", "toolu_a", true), at(0)))
	one(t, tr.Task(nested("b", "toolu_b", "toolu_a", true), at(1)))
	r := nested("c", "toolu_c", "toolu_b", true)
	r.SpawnDepth = 3
	one(t, tr.Task(r, at(2)))
	proc := []provider.BackgroundTask{{TaskID: "x", ToolUseID: "toolu_x", ParentToolUseID: "toolu_c"}}
	none(t, tr.Background(proc, at(3)))

	tr.Task(completedTask("a"), at(4))
	tr.Task(completedTask("b"), at(5))
	got := byID(t, tr.Task(completedTask("c"), at(6)))
	wantWaiting(t, "c", got["c"].WaitingOn, waiting(0, 1))
	if _, moved := got["a"]; moved {
		t.Errorf("a was already waiting on b; its count didn't move: %+v", got["a"])
	}

	got = byID(t, tr.Background(nil, at(7)))
	if len(got) != 3 {
		t.Fatalf("the process ending must unwind all three: %+v", got)
	}
	for _, id := range []string{"a", "b", "c"} {
		wantWaiting(t, id, got[id].WaitingOn, nil)
	}
}

// A run that ended any way but completed waits on nothing: whatever it left
// behind isn't coming back to it.
func TestOnlyCompletedWaits(t *testing.T) {
	tr := delegates.New("ses_1", 0)
	one(t, tr.Task(started("p", "toolu_p", true), at(0)))
	one(t, tr.Task(nested("c", "toolu_c", "toolu_p", true), at(1)))
	p := one(t, tr.Task(contracts.SubagentTask{Phase: "notification", TaskID: "p", Status: "failed"}, at(2)))
	wantWaiting(t, "failed", p.WaitingOn, nil)
}

// The harness process ending stops the child and takes the processes with
// it: nothing is waited on.
func TestProcessExitClearsWaiting(t *testing.T) {
	tr := delegates.New("ses_1", 0)
	one(t, tr.Task(started("p", "toolu_p", true), at(0)))
	one(t, tr.Task(started("q", "toolu_q", true), at(0)))
	one(t, tr.Task(nested("c", "toolu_c", "toolu_p", true), at(1)))
	tr.Background([]provider.BackgroundTask{{TaskID: "x", ParentToolUseID: "toolu_q"}}, at(1))
	tr.Task(completedTask("p"), at(2))
	tr.Task(completedTask("q"), at(2))

	got := byID(t, tr.ProcessExit(at(3)))
	if len(got) != 3 || got["c"].State != contracts.DelegateStopped {
		t.Fatalf("process exit = %+v", got)
	}
	wantWaiting(t, "p", got["p"].WaitingOn, nil)
	wantWaiting(t, "q", got["q"].WaitingOn, nil)
}

// A child the roster lost (concluded stopped after Grace) stops keeping
// its parent waiting, and the expiry returns both.
func TestExpireClearsParentWaiting(t *testing.T) {
	tr := delegates.New("ses_1", 0)
	one(t, tr.Task(started("p", "toolu_p", true), at(0)))
	one(t, tr.Task(nested("c", "toolu_c", "toolu_p", true), at(1)))
	tr.Task(completedTask("p"), at(2))
	tr.Roster(nil, at(3))
	got := byID(t, tr.Expire(at(3).Add(delegates.Grace)))
	if len(got) != 2 || got["c"].State != contracts.DelegateStopped {
		t.Fatalf("expire = %+v", got)
	}
	wantWaiting(t, "p", got["p"].WaitingOn, nil)
}

// A stored WaitingOn belonged to the runner life that computed it: at Seed
// its child is orphaned and the processes are unknown, so the waiting is
// cleared and the correction published.
func TestSeedClearsStoredWaiting(t *testing.T) {
	tr := delegates.New("ses_1", 0)
	end := at(1)
	stored := []contracts.Delegate{
		{SessionID: "ses_1", HarnessID: "p", ToolUseID: "toolu_p", State: contracts.DelegateCompleted, Runs: 1,
			StartedAt: at(0), EndedAt: &end, WaitingOn: waiting(0, 1)},
		{SessionID: "ses_1", HarnessID: "q", ToolUseID: "toolu_q", State: contracts.DelegateCompleted, Runs: 1,
			StartedAt: at(0), EndedAt: &end},
	}
	got := byID(t, tr.Seed(stored, at(5)))
	if len(got) != 1 {
		t.Fatalf("seed corrections = %+v", got)
	}
	wantWaiting(t, "p", got["p"].WaitingOn, nil)
}

// The main agent's turn ending cuts short a foreground delegate it waits
// on — not a foreground one a background delegate spawned, which waits in
// that delegate's turn. A foreground chain under the main agent ends whole.
func TestTurnEndSparesForegroundUnderBackground(t *testing.T) {
	tr := delegates.New("ses_1", 0)
	one(t, tr.Task(started("bg", "toolu_bg", true), at(0)))
	one(t, tr.Task(nested("fgUnderBg", "toolu_x", "toolu_bg", false), at(1)))
	one(t, tr.Task(started("fg", "toolu_fg", false), at(0)))
	one(t, tr.Task(nested("fgUnderFg", "toolu_y", "toolu_fg", false), at(1)))

	got := byID(t, tr.TurnEnd(at(2)))
	if len(got) != 2 || got["fg"].State != contracts.DelegateStopped || got["fgUnderFg"].State != contracts.DelegateStopped {
		t.Fatalf("turn end = %+v", got)
	}
	if !tr.LiveSpawn("toolu_x") {
		t.Error("a background delegate's foreground child must outlive the main turn")
	}
}
