package store

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
)

func newLeaseStore(t *testing.T) (*Leases, *Tasks, *Log) {
	t.Helper()
	l := openMigratedDB(t)
	return NewLeases(l), NewTasks(l), l
}

func TestClaimRenewRelease(t *testing.T) {
	leases, tasks, _ := newLeaseStore(t)
	ctx := context.Background()
	task := mkTask(t, tasks, contracts.Task{Title: "A"})

	first, err := leases.Claim(ctx, task.ID, "pipeline", time.Minute)
	if err != nil {
		t.Fatal(err)
	}

	// Another holder is refused while the lease is live.
	_, err = leases.Claim(ctx, task.ID, "intruder", time.Minute)
	var held *LeaseHeldError
	if !errors.As(err, &held) || held.Holder != "pipeline" {
		t.Fatalf("want LeaseHeldError(pipeline), got %v", err)
	}

	// Re-claim by the holder extends the meter.
	leases.now = func() time.Time { return first.ClaimedAt.Add(30 * time.Second) }
	renewed, err := leases.Claim(ctx, task.ID, "pipeline", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if !renewed.ExpiresAt.After(first.ExpiresAt) {
		t.Error("re-claim did not extend the lease")
	}

	if err := leases.Release(ctx, task.ID, "someone-else"); err == nil {
		t.Error("release by non-holder succeeded")
	}
	if err := leases.Release(ctx, task.ID, "pipeline"); err != nil {
		t.Fatal(err)
	}
	// Idempotent: releasing a free task is a no-op.
	if err := leases.Release(ctx, task.ID, "pipeline"); err != nil {
		t.Errorf("double release: %v", err)
	}
	if _, err := leases.Claim(ctx, task.ID, "intruder", time.Minute); err != nil {
		t.Errorf("claim after release: %v", err)
	}
}

func TestClaimOverExpiredLease(t *testing.T) {
	leases, tasks, _ := newLeaseStore(t)
	ctx := context.Background()
	task := mkTask(t, tasks, contracts.Task{Title: "A"})

	start := time.Now().UTC()
	leases.now = func() time.Time { return start }
	if _, err := leases.Claim(ctx, task.ID, "pipeline", time.Minute); err != nil {
		t.Fatal(err)
	}
	leases.now = func() time.Time { return start.Add(2 * time.Minute) }
	if _, err := leases.Claim(ctx, task.ID, "newcomer", time.Minute); err != nil {
		t.Errorf("claim over expired lease: %v", err)
	}
}

func TestOnlyHolderMayWrite(t *testing.T) {
	leases, tasks, _ := newLeaseStore(t)
	ctx := context.Background()
	task := mkTask(t, tasks, contracts.Task{Title: "A"})
	if _, err := leases.Claim(ctx, task.ID, "executor", time.Minute); err != nil {
		t.Fatal(err)
	}

	task.Title = "A edited"
	var held *LeaseHeldError
	if err := tasks.Update(ctx, &task, "user"); !errors.As(err, &held) {
		t.Fatalf("non-holder write: want LeaseHeldError, got %v", err)
	}
	if err := tasks.AddNote(ctx, task.ID, &contracts.Note{Type: contracts.NoteContext, Content: "x", Source: "user"}, "user"); !errors.As(err, &held) {
		t.Fatalf("non-holder note: want LeaseHeldError, got %v", err)
	}

	// The holder writes freely, and the write renews the meter.
	var before string
	if err := leases.log.db.QueryRow("SELECT expires_at FROM task_leases WHERE task_id = ?", task.ID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Millisecond)
	if err := tasks.Update(ctx, &task, "executor"); err != nil {
		t.Fatal(err)
	}
	var after string
	if err := leases.log.db.QueryRow("SELECT expires_at FROM task_leases WHERE task_id = ?", task.ID).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after <= before {
		t.Error("holder write did not renew the lease")
	}
}

func TestExpireStaleBlocksMidFlightTasks(t *testing.T) {
	leases, tasks, l := newLeaseStore(t)
	ctx := context.Background()
	task := mkTask(t, tasks, contracts.Task{Title: "A", Status: contracts.StatusInProgress})
	idle := mkTask(t, tasks, contracts.Task{Title: "B"}) // ready, also leased

	start := time.Now().UTC()
	leases.now = func() time.Time { return start }
	if _, err := leases.Claim(ctx, task.ID, "executor", time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, err := leases.Claim(ctx, idle.ID, "executor", time.Minute); err != nil {
		t.Fatal(err)
	}

	// Nothing expired yet: sweep is a no-op.
	if n, err := leases.ExpireStale(ctx); err != nil || n != 0 {
		t.Fatalf("premature sweep: n=%d err=%v", n, err)
	}

	// The meter runs out.
	leases.now = func() time.Time { return start.Add(2 * time.Minute) }
	n, err := leases.ExpireStale(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("sweep released %d, want 2", n)
	}

	got, err := tasks.Get(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != contracts.StatusBlocked {
		t.Errorf("mid-flight task after expiry: status %s, want blocked", got.Status)
	}
	gotIdle, err := tasks.Get(ctx, idle.ID)
	if err != nil {
		t.Fatal(err)
	}
	if gotIdle.Status != contracts.StatusReady {
		t.Errorf("idle task should keep its status, got %s", gotIdle.Status)
	}

	events, err := l.Query(ctx, Filter{Types: []string{"task.status_changed"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].TaskID != task.ID {
		t.Errorf("want one status_changed event for the blocked task, got %+v", events)
	}
}

func TestReleaseAllForCrashedHolder(t *testing.T) {
	leases, tasks, l := newLeaseStore(t)
	ctx := context.Background()
	a := mkTask(t, tasks, contracts.Task{Title: "A", Status: contracts.StatusInProgress})
	b := mkTask(t, tasks, contracts.Task{Title: "B", Status: contracts.StatusInProgress})
	other := mkTask(t, tasks, contracts.Task{Title: "C", Status: contracts.StatusInProgress})

	for _, tk := range []string{a.ID, b.ID} {
		if _, err := leases.Claim(ctx, tk, "session-ses_x", time.Hour); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := leases.Claim(ctx, other.ID, "session-ses_y", time.Hour); err != nil {
		t.Fatal(err)
	}

	n, err := leases.ReleaseAll(ctx, "session-ses_x", "session process exited unexpectedly")
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("released %d, want 2", n)
	}
	for _, tk := range []string{a.ID, b.ID} {
		got, _ := tasks.Get(ctx, tk)
		if got.Status != contracts.StatusBlocked {
			t.Errorf("task %s: status %s, want blocked", tk, got.Status)
		}
	}
	gotOther, _ := tasks.Get(ctx, other.ID)
	if gotOther.Status != contracts.StatusInProgress {
		t.Error("another holder's lease was collateral damage")
	}
	events, _ := l.Query(ctx, Filter{Types: []string{"task.status_changed"}})
	if len(events) != 2 {
		t.Errorf("want 2 blocked announcements, got %d", len(events))
	}
}

// Contention: many claimants race for one task; exactly one lease exists
// afterward and losers got LeaseHeldError, not corruption.
func TestClaimContention(t *testing.T) {
	leases, tasks, _ := newLeaseStore(t)
	ctx := context.Background()
	task := mkTask(t, tasks, contracts.Task{Title: "A"})

	const claimants = 8
	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		wins int
	)
	for i := 0; i < claimants; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := leases.Claim(ctx, task.ID, fmt.Sprintf("holder-%d", i), time.Minute)
			var held *LeaseHeldError
			switch {
			case err == nil:
				mu.Lock()
				wins++
				mu.Unlock()
			case errors.As(err, &held):
			default:
				t.Errorf("claimant %d: unexpected error %v", i, err)
			}
		}(i)
	}
	wg.Wait()
	if wins != 1 {
		t.Errorf("%d claimants won, want exactly 1", wins)
	}
}
