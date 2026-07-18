package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
)

func TestClaimWithIdentityRoundTrip(t *testing.T) {
	leases, tasks, l := newLeaseStore(t)
	ctx := context.Background()
	task := mkTask(t, tasks, contracts.Task{Title: "A"})

	identity := contracts.LeaseIdentity{MachineID: "m1", BootID: "b1", PID: 42, PIDStartTicks: 999}
	lease, err := leases.ClaimWithIdentity(ctx, task.ID, "pipeline", time.Minute, &identity)
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
	if _, err := leases.ClaimWithIdentity(ctx, mine.ID, "pipeline", time.Minute, &identity); err != nil {
		t.Fatal(err)
	}
	if _, err := leases.ClaimWithIdentity(ctx, theirs.ID, "other", time.Minute, &identity); err != nil {
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
	if _, err := leases.ClaimWithIdentity(ctx, task.ID, "pipeline", time.Minute, &identity); err != nil {
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

	got, err := tasks.Get(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != contracts.StatusReady {
		t.Errorf("task status after Reclaim = %s, want ready", got.Status)
	}

	events, err := l.Query(ctx, Filter{Types: []string{"task.status_changed"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("want one task.status_changed event, got %d", len(events))
	}
	var payload struct{ From, To, Reason string }
	if err := json.Unmarshal(events[0].Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.From != "in_progress" || payload.To != "ready" {
		t.Errorf("event payload = %+v, want from=in_progress to=ready", payload)
	}
	if payload.Reason == "" {
		t.Error("event payload.reason is empty, want the evidence text")
	}
}

func TestReclaimMismatchedIdentity(t *testing.T) {
	leases, tasks, l := newLeaseStore(t)
	ctx := context.Background()
	task := mkTask(t, tasks, contracts.Task{Title: "A", Status: contracts.StatusInProgress})

	identity := contracts.LeaseIdentity{MachineID: "m1", BootID: "b1", PID: 42, PIDStartTicks: 999}
	if _, err := leases.ClaimWithIdentity(ctx, task.ID, "pipeline", time.Minute, &identity); err != nil {
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
