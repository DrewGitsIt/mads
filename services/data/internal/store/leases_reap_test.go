package store

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
)

func TestClaimWithIdentityRoundTrip(t *testing.T) {
	leases, tasks, l := newLeaseStore(t)
	ctx := context.Background()
	task := mkTask(t, tasks, contracts.Task{Title: "A"})

	identity := contracts.LeaseIdentity{MachineID: "m1", BootID: "b1", PID: 42, PIDStartTicks: 999}
	lease, err := leases.ClaimWith(ctx, task.ID, ClaimRequest{Actor: "pipeline", Holder: "pipeline", TTL: time.Minute, Identity: &identity})
	if err != nil {
		t.Fatal(err)
	}
	if lease.Identity == nil || *lease.Identity != identity {
		t.Fatalf("ClaimWithIdentity: lease.Identity = %+v, want %+v", lease.Identity, identity)
	}

	var machineID, bootID sql.NullString
	var pid, pidStartTicks sql.NullInt64
	if err := l.db.QueryRow(
		"SELECT machine_id, boot_id, pid, pid_start_ticks FROM task_leases WHERE task_id = ?", task.ID,
	).Scan(&machineID, &bootID, &pid, &pidStartTicks); err != nil {
		t.Fatal(err)
	}
	if !machineID.Valid || !bootID.Valid || !pid.Valid || !pidStartTicks.Valid {
		t.Fatalf("raw columns: machine_id=%v boot_id=%v pid=%v pid_start_ticks=%v, want all valid", machineID, bootID, pid, pidStartTicks)
	}

	// A bare Claim (identity-less) leaves the columns NULL.
	other := mkTask(t, tasks, contracts.Task{Title: "B"})
	bare, err := leases.Claim(ctx, other.ID, "pipeline", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if bare.Identity != nil {
		t.Errorf("bare Claim: Identity = %+v, want nil", bare.Identity)
	}
	if err := l.db.QueryRow(
		"SELECT machine_id, boot_id, pid, pid_start_ticks FROM task_leases WHERE task_id = ?", other.ID,
	).Scan(&machineID, &bootID, &pid, &pidStartTicks); err != nil {
		t.Fatal(err)
	}
	if machineID.Valid || bootID.Valid || pid.Valid || pidStartTicks.Valid {
		t.Errorf("bare Claim raw columns: machine_id=%v boot_id=%v pid=%v pid_start_ticks=%v, want all NULL", machineID, bootID, pid, pidStartTicks)
	}
}

func TestByHolderScopesToHolder(t *testing.T) {
	leases, tasks, _ := newLeaseStore(t)
	ctx := context.Background()
	mine := mkTask(t, tasks, contracts.Task{Title: "mine"})
	theirs := mkTask(t, tasks, contracts.Task{Title: "theirs"})

	identity := contracts.LeaseIdentity{MachineID: "m1", BootID: "b1", PID: 1, PIDStartTicks: 1}
	if _, err := leases.ClaimWith(ctx, mine.ID, ClaimRequest{Actor: "pipeline", Holder: "pipeline", TTL: time.Minute, Identity: &identity}); err != nil {
		t.Fatal(err)
	}
	if _, err := leases.ClaimWith(ctx, theirs.ID, ClaimRequest{Actor: "other", Holder: "other", TTL: time.Minute, Identity: &identity}); err != nil {
		t.Fatal(err)
	}

	got, err := leases.ByHolder(ctx, "pipeline")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].TaskID != mine.ID {
		t.Fatalf("ByHolder(pipeline) = %+v, want exactly [mine]", got)
	}
}

func TestReclaimMatchingIdentity(t *testing.T) {
	leases, tasks, l := newLeaseStore(t)
	ctx := context.Background()
	task := mkTask(t, tasks, contracts.Task{Title: "A", Status: contracts.StatusInProgress})

	identity := contracts.LeaseIdentity{MachineID: "m1", BootID: "b1", PID: 42, PIDStartTicks: 999}
	if _, err := leases.ClaimWith(ctx, task.ID, ClaimRequest{Actor: "pipeline", Holder: "pipeline", TTL: time.Minute, Identity: &identity}); err != nil {
		t.Fatal(err)
	}

	ok, err := leases.Reclaim(ctx, task.ID, "pipeline", identity, "lease reclaimed: holder pipeline pid 42 provably dead (pid 42 not found)")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("Reclaim: want ok=true for a matching identity")
	}

	var count int
	if err := l.db.QueryRow("SELECT COUNT(*) FROM task_leases WHERE task_id = ?", task.ID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Errorf("lease still present after Reclaim")
	}

	// Lease loss changes no status: the task stays in_progress
	// (Interrupted) for the next run to pick up where it stopped.
	got, err := tasks.Get(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != contracts.StatusInProgress {
		t.Errorf("task status after Reclaim = %s, want in_progress", got.Status)
	}
	if events := statusChangedEvents(t, l); len(events) != 0 {
		t.Errorf("Reclaim announced status changes: %+v", events)
	}
}

// Reclaim matches by holder kind: a "pipeline" engine may reclaim any
// "pipeline:<runID>" lease it proved dead, but not another kind's.
func TestReclaimMatchesHolderKind(t *testing.T) {
	leases, tasks, l := newLeaseStore(t)
	ctx := context.Background()
	identity := contracts.LeaseIdentity{MachineID: "m1", BootID: "b1", PID: 42, PIDStartTicks: 999}

	run := mkTask(t, tasks, contracts.Task{Title: "run", Status: contracts.StatusInProgress})
	if _, err := leases.ClaimWith(ctx, run.ID, ClaimRequest{Actor: "pipeline", Holder: "pipeline:run_dead", TTL: time.Minute, Identity: &identity}); err != nil {
		t.Fatal(err)
	}
	lookalike := mkTask(t, tasks, contracts.Task{Title: "lookalike", Status: contracts.StatusInProgress})
	if _, err := leases.ClaimWith(ctx, lookalike.ID, ClaimRequest{Actor: "pipelinex", Holder: "pipelinex:run_dead", TTL: time.Minute, Identity: &identity}); err != nil {
		t.Fatal(err)
	}

	if ok, err := leases.Reclaim(ctx, run.ID, "pipeline:run_dead", identity, "evidence"); err != nil || ok {
		t.Fatalf("Reclaim by the full holder rather than its kind: ok=%v err=%v, want ok=false", ok, err)
	}
	if ok, err := leases.Reclaim(ctx, lookalike.ID, "pipeline", identity, "evidence"); err != nil || ok {
		t.Fatalf("Reclaim of another kind's lease: ok=%v err=%v, want ok=false", ok, err)
	}
	ok, err := leases.Reclaim(ctx, run.ID, "pipeline", identity, "evidence")
	if err != nil || !ok {
		t.Fatalf("Reclaim by kind: ok=%v err=%v, want ok=true", ok, err)
	}
	if leaseCount(t, l, run.ID) != 0 {
		t.Error("lease present after a matching Reclaim")
	}
	if leaseCount(t, l, lookalike.ID) != 1 {
		t.Error("another kind's lease was removed")
	}
}

func TestByKindListsLiveLeasesOfTheKind(t *testing.T) {
	leases, tasks, _ := newLeaseStore(t)
	ctx := context.Background()
	start := time.Now().UTC()
	leases.now = func() time.Time { return start }

	claim := func(actor, holder string, ttl time.Duration) string {
		t.Helper()
		task := mkTask(t, tasks, contracts.Task{Title: holder})
		if _, err := leases.ClaimWith(ctx, task.ID, ClaimRequest{Actor: actor, Holder: holder, TTL: ttl}); err != nil {
			t.Fatal(err)
		}
		return task.ID
	}
	bare := claim("pipeline", "pipeline", time.Hour)
	runA := claim("pipeline", "pipeline:run_a", time.Hour)
	runB := claim("pipeline", "pipeline:run_b", time.Hour)
	claim("pipeline", "pipeline:run_old", time.Second) // expires below
	claim("pipelinex", "pipelinex:run_a", time.Hour)
	claim("synthesizer", "synthesizer", time.Hour)

	leases.now = func() time.Time { return start.Add(time.Minute) }
	got, err := leases.ByKind(ctx, "pipeline")
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	for _, l := range got {
		ids[l.TaskID] = true
	}
	if len(got) != 3 || !ids[bare] || !ids[runA] || !ids[runB] {
		t.Fatalf("ByKind(pipeline) = %+v, want exactly the live pipeline and pipeline:* leases", got)
	}
}

func TestReclaimMismatchedIdentity(t *testing.T) {
	leases, tasks, l := newLeaseStore(t)
	ctx := context.Background()
	task := mkTask(t, tasks, contracts.Task{Title: "A", Status: contracts.StatusInProgress})

	identity := contracts.LeaseIdentity{MachineID: "m1", BootID: "b1", PID: 42, PIDStartTicks: 999}
	if _, err := leases.ClaimWith(ctx, task.ID, ClaimRequest{Actor: "pipeline", Holder: "pipeline", TTL: time.Minute, Identity: &identity}); err != nil {
		t.Fatal(err)
	}

	// Simulates a race: the lease was renewed (different start ticks) since
	// the caller established proof against the stale identity.
	stale := identity
	stale.PIDStartTicks = 111
	ok, err := leases.Reclaim(ctx, task.ID, "pipeline", stale, "evidence")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("Reclaim: want ok=false for a mismatched identity")
	}

	var count int
	if err := l.db.QueryRow("SELECT COUNT(*) FROM task_leases WHERE task_id = ?", task.ID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Error("lease was removed despite a mismatched identity")
	}
	got, err := tasks.Get(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != contracts.StatusInProgress {
		t.Errorf("task status after mismatched Reclaim = %s, want in_progress (untouched)", got.Status)
	}
}

func TestReclaimForeignHolder(t *testing.T) {
	leases, tasks, l := newLeaseStore(t)
	ctx := context.Background()
	task := mkTask(t, tasks, contracts.Task{Title: "A", Status: contracts.StatusInProgress})

	identity := contracts.LeaseIdentity{MachineID: "m1", BootID: "b1", PID: 42, PIDStartTicks: 999}
	if _, err := leases.ClaimWith(ctx, task.ID, ClaimRequest{Actor: "other", Holder: "other", TTL: time.Minute, Identity: &identity}); err != nil {
		t.Fatal(err)
	}

	// Same machine, same identity, but the lease is held by a different
	// actor: the engine must never touch another actor's lease.
	ok, err := leases.Reclaim(ctx, task.ID, "pipeline", identity, "evidence")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("Reclaim: want ok=false when the lease is held by a different actor")
	}

	var count int
	if err := l.db.QueryRow("SELECT COUNT(*) FROM task_leases WHERE task_id = ?", task.ID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Error("lease was removed despite belonging to a different actor")
	}
	got, err := tasks.Get(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != contracts.StatusInProgress {
		t.Errorf("task status after foreign-holder Reclaim = %s, want in_progress (untouched)", got.Status)
	}
}

func TestReclaimAlreadyGone(t *testing.T) {
	leases, _, _ := newLeaseStore(t)
	ctx := context.Background()

	identity := contracts.LeaseIdentity{MachineID: "m1", BootID: "b1", PID: 42, PIDStartTicks: 999}
	ok, err := leases.Reclaim(ctx, "task_nonexistent", "pipeline", identity, "evidence")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("Reclaim: want ok=false for a task_id with no lease row")
	}
}
