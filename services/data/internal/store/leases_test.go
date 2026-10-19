package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
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

func leaseCount(t *testing.T, l *Log, taskID string) int {
	t.Helper()
	var n int
	if err := l.db.QueryRow("SELECT COUNT(*) FROM task_leases WHERE task_id = ?", taskID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func statusChangedEvents(t *testing.T, l *Log) []contracts.Envelope {
	t.Helper()
	events, err := l.Query(context.Background(), Filter{Types: []string{"task.status_changed"}})
	if err != nil {
		t.Fatal(err)
	}
	return events
}

func TestClaimReturnsTokenAndStoresOnlyItsHash(t *testing.T) {
	leases, tasks, l := newLeaseStore(t)
	ctx := context.Background()
	task := mkTask(t, tasks, contracts.Task{Title: "A"})

	lease, err := leases.Claim(ctx, task.ID, "pipeline", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if len(lease.Token) != 22 { // 16 bytes, base64url without padding
		t.Fatalf("token %q: want 22 base64url chars", lease.Token)
	}
	var stored string
	if err := l.db.QueryRow("SELECT token_hash FROM task_leases WHERE task_id = ?", task.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored == lease.Token || strings.Contains(stored, lease.Token) {
		t.Fatal("the store kept the token itself")
	}
	if stored != hashLeaseToken(lease.Token) || len(stored) != 64 {
		t.Fatalf("token_hash = %q, want the token's hex SHA-256", stored)
	}

	// Listings never carry a token.
	got, err := leases.ByHolder(ctx, "pipeline")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Token != "" {
		t.Fatalf("ByHolder = %+v, want one lease with no token", got)
	}

	// Two claims never share a token.
	other := mkTask(t, tasks, contracts.Task{Title: "B"})
	second, err := leases.Claim(ctx, other.ID, "pipeline", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if second.Token == lease.Token {
		t.Fatal("two claims returned the same token")
	}
}

func TestClaimOnLeasedTaskIsRefusedWhateverTheHolder(t *testing.T) {
	leases, tasks, _ := newLeaseStore(t)
	ctx := context.Background()
	task := mkTask(t, tasks, contracts.Task{Title: "A"})

	if _, err := leases.Claim(ctx, task.ID, "pipeline", time.Minute); err != nil {
		t.Fatal(err)
	}
	for _, holder := range []string{"intruder", "pipeline"} {
		_, err := leases.Claim(ctx, task.ID, holder, time.Minute)
		var held *LeaseHeldError
		if !errors.As(err, &held) || held.Holder != "pipeline" {
			t.Fatalf("claim by %s over a live lease: want LeaseHeldError(pipeline), got %v", holder, err)
		}
	}
	_, err := leases.ClaimWith(ctx, task.ID, ClaimRequest{Actor: "pipeline", Holder: "pipeline:run_b", TTL: time.Minute})
	var held *LeaseHeldError
	if !errors.As(err, &held) {
		t.Fatalf("claim by another run of the same kind: want LeaseHeldError, got %v", err)
	}
}

func TestClaimOverExpiredLease(t *testing.T) {
	leases, tasks, _ := newLeaseStore(t)
	ctx := context.Background()
	task := mkTask(t, tasks, contracts.Task{Title: "A"})

	start := time.Now().UTC()
	leases.now = func() time.Time { return start }
	old, err := leases.Claim(ctx, task.ID, "pipeline", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	leases.now = func() time.Time { return start.Add(2 * time.Minute) }
	fresh, err := leases.Claim(ctx, task.ID, "newcomer", time.Minute)
	if err != nil {
		t.Fatalf("claim over expired lease: %v", err)
	}
	// The old token is dead: it can neither renew nor release the new lease.
	if _, err := leases.Renew(ctx, task.ID, old.Token); !errors.Is(err, ErrLeaseLost) {
		t.Errorf("renew with the replaced lease's token: want ErrLeaseLost, got %v", err)
	}
	if _, err := leases.Renew(ctx, task.ID, fresh.Token); err != nil {
		t.Errorf("renew with the new token: %v", err)
	}
}

func TestClaimHolderPrefixRule(t *testing.T) {
	leases, tasks, _ := newLeaseStore(t)
	ctx := context.Background()

	cases := []struct {
		actor, holder, wantHolder string
		ok                        bool
	}{
		{"pipeline", "", "pipeline", true},
		{"pipeline", "pipeline", "pipeline", true},
		{"pipeline", "pipeline:run_3f9a", "pipeline:run_3f9a", true},
		{"pipeline", "pipeline:", "", false},
		{"pipeline", "pipelinex", "", false},
		{"pipeline", "pipelinex:run_1", "", false},
		{"pipeline", "synthesizer", "", false},
		{"pipeline", "synthesizer:pipeline", "", false},
		{"user", "pipeline:run_1", "", false},
	}
	for _, tc := range cases {
		task := mkTask(t, tasks, contracts.Task{Title: tc.actor + "/" + tc.holder})
		lease, err := leases.ClaimWith(ctx, task.ID, ClaimRequest{Actor: tc.actor, Holder: tc.holder, TTL: time.Minute})
		if tc.ok {
			if err != nil {
				t.Errorf("actor %q holder %q: %v", tc.actor, tc.holder, err)
				continue
			}
			if lease.Holder != tc.wantHolder {
				t.Errorf("actor %q holder %q: lease holder %q, want %q", tc.actor, tc.holder, lease.Holder, tc.wantHolder)
			}
			continue
		}
		var verr *contracts.ValidationError
		if !errors.As(err, &verr) {
			t.Errorf("actor %q holder %q: want ValidationError, got %v", tc.actor, tc.holder, err)
		}
	}
}

func TestRenew(t *testing.T) {
	leases, tasks, l := newLeaseStore(t)
	ctx := context.Background()
	task := mkTask(t, tasks, contracts.Task{Title: "A"})

	start := time.Now().UTC()
	leases.now = func() time.Time { return start }
	lease, err := leases.Claim(ctx, task.ID, "pipeline", time.Minute)
	if err != nil {
		t.Fatal(err)
	}

	// The right token extends the lease by its TTL.
	leases.now = func() time.Time { return start.Add(30 * time.Second) }
	renewed, err := leases.Renew(ctx, task.ID, lease.Token)
	if err != nil {
		t.Fatal(err)
	}
	if want := start.Add(90 * time.Second); !renewed.ExpiresAt.Equal(want) {
		t.Errorf("renewed expiry %v, want %v", renewed.ExpiresAt, want)
	}
	if renewed.Token != "" {
		t.Error("Renew echoed a token")
	}

	// A wrong or missing token is lease_lost and changes nothing.
	for _, tok := range []string{"", "not-the-token", lease.Token + "x"} {
		if _, err := leases.Renew(ctx, task.ID, tok); !errors.Is(err, ErrLeaseLost) {
			t.Errorf("renew with token %q: want ErrLeaseLost, got %v", tok, err)
		}
	}

	// An expired lease is lease_lost even with the right token.
	leases.now = func() time.Time { return start.Add(2 * time.Minute) }
	if _, err := leases.Renew(ctx, task.ID, lease.Token); !errors.Is(err, ErrLeaseLost) {
		t.Errorf("renew of an expired lease: want ErrLeaseLost, got %v", err)
	}

	// A deleted lease is lease_lost, and Renew never creates one.
	if _, err := leases.ExpireStale(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := leases.Renew(ctx, task.ID, lease.Token); !errors.Is(err, ErrLeaseLost) {
		t.Errorf("renew of a deleted lease: want ErrLeaseLost, got %v", err)
	}
	if n := leaseCount(t, l, task.ID); n != 0 {
		t.Errorf("Renew created a lease (%d rows)", n)
	}
	if _, err := leases.Renew(ctx, "tsk_missing", lease.Token); !errors.Is(err, ErrLeaseLost) {
		t.Errorf("renew on a missing task: want ErrLeaseLost, got %v", err)
	}
}

func TestReleaseRequiresToken(t *testing.T) {
	leases, tasks, l := newLeaseStore(t)
	ctx := context.Background()
	task := mkTask(t, tasks, contracts.Task{Title: "A"})

	lease, err := leases.Claim(ctx, task.ID, "pipeline", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	for _, tok := range []string{"", "wrong"} {
		var held *LeaseHeldError
		if err := leases.Release(ctx, task.ID, tok); !errors.As(err, &held) {
			t.Errorf("release with token %q: want LeaseHeldError, got %v", tok, err)
		}
	}
	if n := leaseCount(t, l, task.ID); n != 1 {
		t.Fatal("a refused release deleted the lease")
	}
	if err := leases.Release(ctx, task.ID, lease.Token); err != nil {
		t.Fatal(err)
	}
	// Idempotent: releasing a free task is a no-op, whatever the token.
	if err := leases.Release(ctx, task.ID, lease.Token); err != nil {
		t.Errorf("double release: %v", err)
	}
	if err := leases.Release(ctx, task.ID, ""); err != nil {
		t.Errorf("tokenless release of a free task: %v", err)
	}
	if _, err := leases.Claim(ctx, task.ID, "intruder", time.Minute); err != nil {
		t.Errorf("claim after release: %v", err)
	}
}

func TestWritesToLeasedTaskNeedTheToken(t *testing.T) {
	leases, tasks, l := newLeaseStore(t)
	ctx := context.Background()
	task := mkTask(t, tasks, contracts.Task{Title: "A"})
	lease, err := leases.Claim(ctx, task.ID, "executor", time.Minute)
	if err != nil {
		t.Fatal(err)
	}

	// No token, or a wrong one, is refused, even for the holder's own actor:
	// attribution is not authorisation.
	task.Title = "A edited"
	for _, c := range []context.Context{ctx, WithLeaseToken(ctx, "wrong")} {
		var held *LeaseHeldError
		if err := tasks.Update(c, &task, "executor"); !errors.As(err, &held) || held.Holder != "executor" {
			t.Fatalf("write without the token: want LeaseHeldError(executor), got %v", err)
		}
		if err := tasks.AddNote(c, task.ID, &contracts.Note{Type: contracts.NoteContext, Content: "x", Source: "user"}, "user"); !errors.As(err, &held) {
			t.Fatalf("note without the token: want LeaseHeldError, got %v", err)
		}
		if err := tasks.AddHandoff(c, task.ID, &contracts.Handoff{From: "a", Content: "x"}, "user"); !errors.As(err, &held) {
			t.Fatalf("handoff without the token: want LeaseHeldError, got %v", err)
		}
	}

	// The token passes, whatever the actor, and renews the lease.
	var before string
	if err := l.db.QueryRow("SELECT expires_at FROM task_leases WHERE task_id = ?", task.ID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Millisecond)
	withToken := WithLeaseToken(ctx, lease.Token)
	if err := tasks.Update(withToken, &task, "pipeline"); err != nil {
		t.Fatal(err)
	}
	var after string
	if err := l.db.QueryRow("SELECT expires_at FROM task_leases WHERE task_id = ?", task.ID).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after <= before {
		t.Error("a write with the token did not renew the lease")
	}
	if err := tasks.AddNote(withToken, task.ID, &contracts.Note{Type: contracts.NoteContext, Content: "x", Source: "user"}, "user"); err != nil {
		t.Errorf("note with the token: %v", err)
	}
}

func TestWritesToFreeOrExpiredTaskAreFree(t *testing.T) {
	leases, tasks, _ := newLeaseStore(t)
	ctx := context.Background()
	task := mkTask(t, tasks, contracts.Task{Title: "A"})

	task.Title = "free"
	if err := tasks.Update(ctx, &task, "user"); err != nil {
		t.Fatalf("write to a free task: %v", err)
	}
	// A stale token on a free task is harmless.
	if err := tasks.Update(WithLeaseToken(ctx, "stale"), &task, "user"); err != nil {
		t.Fatalf("write to a free task with a stale token: %v", err)
	}

	start := time.Now().UTC().Add(-2 * time.Minute)
	leases.now = func() time.Time { return start }
	if _, err := leases.Claim(ctx, task.ID, "pipeline", time.Minute); err != nil {
		t.Fatal(err)
	}
	task.Title = "after expiry"
	if err := tasks.Update(ctx, &task, "user"); err != nil {
		t.Fatalf("write over an expired lease: %v", err)
	}
}

// Lease loss never changes task status (docs/PIPELINE-LEASES-DESIGN.md A3):
// the sweep only deletes leases, and an in_progress task stays in_progress
// (Interrupted) for the next run to pick up.
func TestExpireStaleLeavesStatusAlone(t *testing.T) {
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
	for _, tc := range []struct {
		id   string
		want contracts.TaskStatus
	}{{task.ID, contracts.StatusInProgress}, {idle.ID, contracts.StatusReady}} {
		if leaseCount(t, l, tc.id) != 0 {
			t.Errorf("task %s: lease survived the sweep", tc.id)
		}
		got, err := tasks.Get(ctx, tc.id)
		if err != nil {
			t.Fatal(err)
		}
		if got.Status != tc.want {
			t.Errorf("task %s after expiry: status %s, want %s", tc.id, got.Status, tc.want)
		}
	}
	if events := statusChangedEvents(t, l); len(events) != 0 {
		t.Errorf("the sweep announced status changes: %+v", events)
	}
}

func TestReleaseAllForCrashedHolder(t *testing.T) {
	leases, tasks, l := newLeaseStore(t)
	ctx := context.Background()
	a := mkTask(t, tasks, contracts.Task{Title: "A", Status: contracts.StatusInProgress})
	b := mkTask(t, tasks, contracts.Task{Title: "B", Status: contracts.StatusInProgress})
	other := mkTask(t, tasks, contracts.Task{Title: "C", Status: contracts.StatusInProgress})
	sibling := mkTask(t, tasks, contracts.Task{Title: "D", Status: contracts.StatusInProgress})

	for _, tk := range []string{a.ID, b.ID} {
		if _, err := leases.ClaimWith(ctx, tk, ClaimRequest{Actor: "pipeline", Holder: "pipeline:run_x", TTL: time.Hour}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := leases.Claim(ctx, other.ID, "session-ses_y", time.Hour); err != nil {
		t.Fatal(err)
	}
	// Another run of the same kind is not collateral damage either.
	if _, err := leases.ClaimWith(ctx, sibling.ID, ClaimRequest{Actor: "pipeline", Holder: "pipeline:run_y", TTL: time.Hour}); err != nil {
		t.Fatal(err)
	}

	n, err := leases.ReleaseAll(ctx, "pipeline:run_x", "engine exited unexpectedly")
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("released %d, want 2", n)
	}
	for _, tk := range []string{a.ID, b.ID} {
		if leaseCount(t, l, tk) != 0 {
			t.Errorf("task %s: lease survived ReleaseAll", tk)
		}
		got, _ := tasks.Get(ctx, tk)
		if got.Status != contracts.StatusInProgress {
			t.Errorf("task %s: status %s, want in_progress (lease loss changes no status)", tk, got.Status)
		}
	}
	for _, tk := range []string{other.ID, sibling.ID} {
		if leaseCount(t, l, tk) != 1 {
			t.Errorf("task %s: another holder's lease was collateral damage", tk)
		}
	}
	if events := statusChangedEvents(t, l); len(events) != 0 {
		t.Errorf("ReleaseAll announced status changes: %+v", events)
	}
}

func TestActiveForSessionReflectsLiveLease(t *testing.T) {
	leases, tasks, _ := newLeaseStore(t)
	ctx := context.Background()
	taskX := mkTask(t, tasks, contracts.Task{Title: "A", SessionID: "ses_x"})
	mkTask(t, tasks, contracts.Task{Title: "B", SessionID: "ses_y"})

	start := time.Now().UTC()
	leases.now = func() time.Time { return start }
	if _, err := leases.Claim(ctx, taskX.ID, "pipeline", time.Minute); err != nil {
		t.Fatal(err)
	}

	active, err := leases.ActiveForSession(ctx, "ses_x")
	if err != nil {
		t.Fatal(err)
	}
	if !active {
		t.Error("ses_x has a live lease, want active=true")
	}
	active, err = leases.ActiveForSession(ctx, "ses_y")
	if err != nil {
		t.Fatal(err)
	}
	if active {
		t.Error("ses_y has no lease, want active=false")
	}

	leases.now = func() time.Time { return start.Add(2 * time.Minute) }
	active, err = leases.ActiveForSession(ctx, "ses_x")
	if err != nil {
		t.Fatal(err)
	}
	if active {
		t.Error("ses_x's lease expired, want active=false")
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

// Migration 0031 drops every pre-token lease (no legacy path, A5) and
// leaves their tasks' status alone.
func TestMigrationDropsPreTokenLeases(t *testing.T) {
	l := openMigratedDB(t)
	db := l.db
	if err := Rollback(db, Migrations(), 30); err != nil {
		t.Fatalf("rollback to pre-0031: %v", err)
	}
	tasks := NewTasks(l)
	task := mkTask(t, tasks, contracts.Task{Title: "A", Status: contracts.StatusInProgress})
	now := time.Now().UTC()
	if _, err := db.Exec(
		`INSERT INTO task_leases (task_id, holder, ttl_ms, claimed_at, expires_at) VALUES (?, 'pipeline', 60000, ?, ?)`,
		task.ID, now.Format(time.RFC3339Nano), now.Add(time.Hour).Format(time.RFC3339Nano),
	); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(db, Migrations()); err != nil {
		t.Fatalf("re-migrate: %v", err)
	}
	if leaseCount(t, l, task.ID) != 0 {
		t.Error("a pre-token lease survived migration 0031")
	}
	got, err := tasks.Get(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != contracts.StatusInProgress {
		t.Errorf("status after migration = %s, want in_progress", got.Status)
	}
}
