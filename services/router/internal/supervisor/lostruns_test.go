package supervisor_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/services/data/datatest"
	"github.com/drewkidwell/mads/services/router/internal/supervisor"
)

// fakeProcs is a procid.Reader for a host with machine id "m1" booted as
// "boot-now". This test process is always live; other pids are live only
// when listed, and pids in statErr fail to read (an inconclusive proof).
type fakeProcs struct {
	live    map[int]int64
	statErr map[int]error
}

func (fakeProcs) MachineID() (string, error) { return "m1", nil }
func (fakeProcs) BootID() (string, error)    { return "boot-now", nil }
func (f fakeProcs) PIDStartTicks(pid int) (int64, error) {
	if pid == os.Getpid() {
		return 1, nil
	}
	if err, ok := f.statErr[pid]; ok {
		return 0, err
	}
	if t, ok := f.live[pid]; ok {
		return t, nil
	}
	return 0, fmt.Errorf("pid %d: %w", pid, os.ErrNotExist)
}

func appendEvent(t *testing.T, data *dataclient.Client, typ contracts.EventType, sessionID string, at time.Time, payload map[string]any) {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	if err := data.Append(context.Background(), &contracts.Envelope{
		Type: typ, SessionID: sessionID, Timestamp: at, Payload: raw,
	}); err != nil {
		t.Fatalf("Append %s: %v", typ, err)
	}
}

// lostEvents returns sessionID's pipeline.failed payloads with reason "lost".
func lostEvents(t *testing.T, data *dataclient.Client, sessionID string) []map[string]any {
	t.Helper()
	evs, err := data.Events(context.Background(), 0, sessionID, []string{string(contracts.PipelineFailed)})
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	var out []map[string]any
	for _, ev := range evs {
		var p map[string]any
		if err := json.Unmarshal(ev.Payload, &p); err != nil {
			t.Fatal(err)
		}
		if p["reason"] == supervisor.LostRunReason {
			out = append(out, p)
		}
	}
	return out
}

func identity(machine, boot string, pid int, ticks int64) contracts.LeaseIdentity {
	return contracts.LeaseIdentity{MachineID: machine, BootID: boot, PID: pid, PIDStartTicks: ticks}
}

func TestReconcileLostRuns(t *testing.T) {
	base := datatest.Start(t)
	data := dataclient.New(base, "test-setup")
	sup := newSupervisor(t, dataclient.New(base, "router"), nil)
	ctx := context.Background()

	routerStart := time.Now().UTC()
	before := routerStart.Add(-time.Hour)
	after := routerStart.Add(time.Hour)
	started := func(sessionID, runID string, at time.Time, id *contracts.LeaseIdentity) {
		p := map[string]any{"template": "tpl"}
		if runID != "" {
			p["run_id"] = runID
		}
		if id != nil {
			p["identity"] = *id
		}
		appendEvent(t, data, contracts.PipelineStarted, sessionID, at, p)
	}
	idPtr := func(id contracts.LeaseIdentity) *contracts.LeaseIdentity { return &id }

	// Lost: a Router-launched run from before the identity change, no child.
	started("ses_router", "run_router", before, nil)
	// Ended: each end event closes its run.
	started("ses_ended", "run_done", before, nil)
	appendEvent(t, data, contracts.PipelineCompleted, "ses_ended", before, map[string]any{"run_id": "run_done", "outcome": "completed"})
	started("ses_ended", "run_cancel", before, idPtr(identity("m1", "boot-old", 9, 9)))
	appendEvent(t, data, contracts.PipelineCancelled, "ses_ended", before, map[string]any{"run_id": "run_cancel"})
	started("ses_ended", "run_crash", before, nil)
	appendEvent(t, data, contracts.PipelineFailed, "ses_ended", before, map[string]any{"run_id": "run_crash", "reason": "exit status 1"})
	// CLI runs with identities.
	started("ses_cli_alive", "run_alive", before, idPtr(identity("m1", "boot-now", 4242, 77)))
	started("ses_cli_dead", "run_dead", before, idPtr(identity("m1", "boot-now", 4343, 5)))
	started("ses_cli_reused", "run_reused", before, idPtr(identity("m1", "boot-now", 4242, 76)))
	started("ses_rebooted", "run_rebooted", before, idPtr(identity("m1", "boot-old", 4242, 77)))
	// Inconclusive: a foreign machine, an unreadable pid.
	started("ses_foreign", "run_foreign", before, idPtr(identity("m2", "boot-old", 1, 1)))
	started("ses_staterr", "run_staterr", before, idPtr(identity("m1", "boot-now", 4444, 1)))
	// No identity: started after this Router (may be a live old CLI) — left.
	started("ses_after", "run_after", after, nil)
	// Legacy id-less runs: one superseded by the next run (history already
	// reads it as interrupted), one still open at the end.
	started("ses_legacy", "", before, nil)
	started("ses_legacy", "", before, nil)

	// No identity but a live task lease in the session: left alone.
	leased := contracts.Session{Name: "lost-run leased"}
	if err := data.CreateSession(ctx, &leased); err != nil {
		t.Fatal(err)
	}
	task := contracts.Task{SessionID: leased.ID, Title: "t", Source: "test", Status: contracts.StatusInProgress}
	if err := data.CreateTask(ctx, &task); err != nil {
		t.Fatal(err)
	}
	if err := dataclient.New(base, "external-mads-run").Claim(ctx, task.ID, "", time.Minute); err != nil {
		t.Fatal(err)
	}
	started(leased.ID, "run_leased", before, nil)

	opts := supervisor.LostRunOptions{RouterStart: routerStart, Reader: fakeProcs{
		live:    map[int]int64{4242: 77},
		statErr: map[int]error{4444: syscall.EACCES},
	}}
	sup.ReconcileLostRuns(ctx, opts)

	want := map[string]string{ // session -> lost run_id ("" = id-less)
		"ses_router":     "run_router",
		"ses_cli_dead":   "run_dead",
		"ses_cli_reused": "run_reused",
		"ses_rebooted":   "run_rebooted",
		"ses_legacy":     "",
	}
	untouched := []string{"ses_ended", "ses_cli_alive", "ses_foreign", "ses_staterr", "ses_after", leased.ID}
	check := func() {
		t.Helper()
		for sid, runID := range want {
			got := lostEvents(t, data, sid)
			if len(got) != 1 {
				t.Fatalf("%s: lost events = %v, want exactly one", sid, got)
			}
			if gotID, _ := got[0]["run_id"].(string); gotID != runID {
				t.Errorf("%s: lost run_id = %q, want %q", sid, gotID, runID)
			}
		}
		for _, sid := range untouched {
			if got := lostEvents(t, data, sid); len(got) != 0 {
				t.Errorf("%s: lost events = %v, want none", sid, got)
			}
		}
	}
	check()
	if got := lostEvents(t, data, "ses_rebooted"); got[0]["evidence"] != "boot_id changed" {
		t.Errorf("rebooted evidence = %v, want boot_id changed", got[0]["evidence"])
	}

	// A second Router start adds nothing.
	sup.ReconcileLostRuns(ctx, opts)
	check()
}

func TestReconcileLostRunsSkipsLiveTrackedChild(t *testing.T) {
	base := datatest.Start(t)
	data := dataclient.New(base, "test-setup")
	sup := newSupervisor(t, dataclient.New(base, "router"), nil)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	if _, err := sup.SpawnPipeline(ctx, "run_live", "ses_live", "tpl_test", nil); err != nil {
		t.Fatalf("SpawnPipeline: %v", err)
	}
	// Even a dead-looking identity can't override a live child.
	appendEvent(t, data, contracts.PipelineStarted, "ses_live", time.Now().Add(-time.Hour), map[string]any{
		"run_id": "run_live", "identity": identity("m1", "boot-old", 1, 1),
	})

	sup.ReconcileLostRuns(ctx, supervisor.LostRunOptions{RouterStart: time.Now(), Reader: fakeProcs{}})
	if got := lostEvents(t, data, "ses_live"); len(got) != 0 {
		t.Fatalf("lost events for a live tracked child = %v, want none", got)
	}
	if err := sup.TerminatePipeline(ctx, "run_live"); err != nil {
		t.Fatalf("TerminatePipeline: %v", err)
	}
}
