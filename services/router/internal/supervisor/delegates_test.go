package supervisor_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/pkg/delegates"
	"github.com/drewkidwell/mads/services/data/datatest"
)

func putDelegate(t *testing.T, ctx context.Context, data *dataclient.Client, d contracts.Delegate) {
	t.Helper()
	if d.StartedAt.IsZero() {
		d.StartedAt = time.Now().Add(-time.Hour)
	}
	if _, err := data.PutDelegate(ctx, d); err != nil {
		t.Fatalf("PutDelegate(%s): %v", d.HarnessID, err)
	}
}

func storedDelegate(t *testing.T, ctx context.Context, data *dataclient.Client, sessionID, harnessID string) contracts.Delegate {
	t.Helper()
	ds, err := data.SessionDelegates(ctx, sessionID)
	if err != nil {
		t.Fatalf("SessionDelegates(%s): %v", sessionID, err)
	}
	for _, d := range ds {
		if d.HarnessID == harnessID {
			return d
		}
	}
	t.Fatalf("no delegate %s in %s", harnessID, sessionID)
	return contracts.Delegate{}
}

func assertOrphaned(t *testing.T, d contracts.Delegate) {
	t.Helper()
	if d.State != contracts.DelegateOrphaned || d.Reason != delegates.ReasonRunnerRestart || !d.Inferred || d.EndedAt == nil {
		t.Fatalf("delegate %s = state %q reason %q inferred %v ended %v; want orphaned, runner_restart, inferred, ended",
			d.HarnessID, d.State, d.Reason, d.Inferred, d.EndedAt)
	}
}

// A session already inactive when the Router starts never passes through
// the active-session sweep; a delegate its dead harness process left
// recorded running must still be concluded, and the waiting look on the
// delegate that spawned it cleared with it. A session with a live child is
// its runner's to record.
func TestBootSweepConcludesStrandedDelegates(t *testing.T) {
	base := datatest.Start(t)
	data := dataclient.New(base, "router")
	sup := newSupervisor(t, data, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	idle := contracts.Session{Name: "stranded delegates"}
	if err := data.CreateSession(ctx, &idle); err != nil {
		t.Fatalf("CreateSession (idle): %v", err)
	}
	idle.Activity = contracts.SessionInactive
	if err := data.UpdateSession(ctx, &idle); err != nil {
		t.Fatalf("UpdateSession (idle): %v", err)
	}
	putDelegate(t, ctx, data, contracts.Delegate{
		SessionID: idle.ID, HarnessID: "parent", State: contracts.DelegateCompleted, Backgrounded: true,
		WaitingOn: &contracts.DelegateWaiting{Subagents: 1},
	})
	putDelegate(t, ctx, data, contracts.Delegate{
		SessionID: idle.ID, HarnessID: "child", ParentHarnessID: "parent", Depth: 2,
		State: contracts.DelegateRunning, Backgrounded: true,
	})
	putDelegate(t, ctx, data, contracts.Delegate{
		SessionID: idle.ID, HarnessID: "done", State: contracts.DelegateCompleted, Reason: "completed",
	})

	live := contracts.Session{Name: "live child"}
	if err := data.CreateSession(ctx, &live); err != nil {
		t.Fatalf("CreateSession (live): %v", err)
	}
	if _, err := sup.SpawnSession(ctx, live.ID); err != nil {
		t.Fatalf("SpawnSession (live): %v", err)
	}
	putDelegate(t, ctx, data, contracts.Delegate{SessionID: live.ID, HarnessID: "live", State: contracts.DelegateRunning})

	sup.ReconcileOrphanedSessions(ctx)

	assertOrphaned(t, storedDelegate(t, ctx, data, idle.ID, "child"))
	if d := storedDelegate(t, ctx, data, idle.ID, "parent"); d.State != contracts.DelegateCompleted || d.WaitingOn != nil {
		t.Fatalf("parent = state %q waiting %+v; want completed, no longer waiting", d.State, d.WaitingOn)
	}
	if d := storedDelegate(t, ctx, data, idle.ID, "done"); d.State != contracts.DelegateCompleted || d.Reason != "completed" {
		t.Fatalf("done = state %q reason %q; want untouched", d.State, d.Reason)
	}
	if d := storedDelegate(t, ctx, data, live.ID, "live"); d.State != contracts.DelegateRunning {
		t.Fatalf("live = state %q; want running (its child is up)", d.State)
	}
}

// A child that dies without its runner recording how its delegates ended
// (here, killed outright) leaves them concluded by the Router's exit path,
// not running until the session is next resumed.
func TestSessionExitConcludesStrandedDelegates(t *testing.T) {
	base := datatest.Start(t)
	data := dataclient.New(base, "router")
	sup := newSupervisor(t, data, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	sess := contracts.Session{Name: "killed mid-run"}
	if err := data.CreateSession(ctx, &sess); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if _, err := sup.SpawnSession(ctx, sess.ID); err != nil {
		t.Fatalf("SpawnSession: %v", err)
	}
	putDelegate(t, ctx, data, contracts.Delegate{SessionID: sess.ID, HarnessID: "research", State: contracts.DelegateRunning, Backgrounded: true})

	pid, ok := sup.SessionPID(sess.ID)
	if !ok {
		t.Fatalf("SessionPID(%s): not tracked", sess.ID)
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		t.Fatalf("FindProcess(%d): %v", pid, err)
	}
	if err := proc.Kill(); err != nil {
		t.Fatalf("kill child pid %d: %v", pid, err)
	}

	eventually(t, 5*time.Second, func() bool {
		ds, err := data.SessionDelegates(ctx, sess.ID)
		return err == nil && len(ds) == 1 && ds[0].State != contracts.DelegateRunning
	})
	assertOrphaned(t, storedDelegate(t, ctx, data, sess.ID, "research"))
}
