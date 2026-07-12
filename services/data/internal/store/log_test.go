package store

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"sync"
	"testing"

	"github.com/drewkidwell/mads/pkg/contracts"
)

func openMigratedDB(t *testing.T) *Log {
	t.Helper()
	db := openTestDB(t)
	if err := Migrate(db, Migrations()); err != nil {
		t.Fatal(err)
	}
	return NewLog(db)
}

func TestAppendAssignsMonotoneSeqs(t *testing.T) {
	l := openMigratedDB(t)
	ctx := context.Background()
	var last int64
	for i := 0; i < 5; i++ {
		env := contracts.Envelope{Type: contracts.TaskCreated, Actor: "user", SessionID: "ses_a"}
		if err := l.Append(ctx, &env); err != nil {
			t.Fatal(err)
		}
		if env.Seq <= last {
			t.Fatalf("seq %d not greater than previous %d", env.Seq, last)
		}
		if env.Timestamp.IsZero() {
			t.Fatal("append did not stamp timestamp")
		}
		last = env.Seq
	}
}

func TestAppendRejectsBadEvents(t *testing.T) {
	l := openMigratedDB(t)
	ctx := context.Background()
	if err := l.Append(ctx, &contracts.Envelope{Type: "task.exploded", Actor: "user"}); err == nil {
		t.Error("unregistered type accepted")
	}
	if err := l.Append(ctx, &contracts.Envelope{Type: contracts.TaskCreated}); err == nil {
		t.Error("empty actor accepted")
	}
}

// The outbox contract (spec §15): a state mutation and its event commit or
// roll back together — never one without the other.
func TestOutboxAtomicity(t *testing.T) {
	l := openMigratedDB(t)
	ctx := context.Background()

	// Success: both the state write and the event land.
	err := l.WithTx(ctx, func(tx *Tx) error {
		if _, err := tx.Exec("INSERT INTO meta (key, value) VALUES ('committed', 'yes')"); err != nil {
			return err
		}
		return tx.Append(&contracts.Envelope{Type: contracts.SessionCreated, Actor: "user", SessionID: "ses_a"})
	})
	if err != nil {
		t.Fatal(err)
	}

	// Failure after both writes: neither survives.
	err = l.WithTx(ctx, func(tx *Tx) error {
		if _, err := tx.Exec("INSERT INTO meta (key, value) VALUES ('rolled_back', 'yes')"); err != nil {
			return err
		}
		if err := tx.Append(&contracts.Envelope{Type: contracts.SessionDeleted, Actor: "user", SessionID: "ses_a"}); err != nil {
			return err
		}
		return fmt.Errorf("business rule violated")
	})
	if err == nil {
		t.Fatal("expected error to propagate")
	}

	var n int
	if err := l.db.QueryRow("SELECT COUNT(*) FROM meta WHERE key = 'rolled_back'").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Error("rolled-back transaction left state behind")
	}
	events, err := l.Query(ctx, Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Type != contracts.SessionCreated {
		t.Errorf("event log should hold exactly the committed event, got %d events", len(events))
	}
}

func TestQueryFilters(t *testing.T) {
	l := openMigratedDB(t)
	ctx := context.Background()
	seed := []contracts.Envelope{
		{Type: contracts.SessionCreated, Actor: "user", SessionID: "ses_a"},
		{Type: contracts.TaskCreated, Actor: "user", SessionID: "ses_a", TaskID: "tsk_1"},
		{Type: contracts.TaskStatusChanged, Actor: "pipeline", SessionID: "ses_a", TaskID: "tsk_1"},
		{Type: contracts.TaskCreated, Actor: "user", SessionID: "ses_b", TaskID: "tsk_2"},
		{Type: contracts.AgentCreated, Actor: "user"}, // system-level, no session
	}
	for i := range seed {
		if err := l.Append(ctx, &seed[i]); err != nil {
			t.Fatal(err)
		}
	}

	cases := []struct {
		name string
		f    Filter
		want int
	}{
		{"all", Filter{}, 5},
		{"by session", Filter{SessionID: "ses_a"}, 3},
		{"exact type", Filter{Types: []string{"task.created"}}, 2},
		{"domain wildcard", Filter{Types: []string{"task.*"}}, 3},
		{"wildcard and session", Filter{SessionID: "ses_a", Types: []string{"task.*"}}, 2},
		{"multiple patterns", Filter{Types: []string{"session.*", "agent.created"}}, 2},
		{"since cursor", Filter{Since: seed[2].Seq}, 2},
		{"limit", Filter{Limit: 2}, 2},
	}
	for _, c := range cases {
		got, err := l.Query(ctx, c.f)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if len(got) != c.want {
			t.Errorf("%s: got %d events, want %d", c.name, len(got), c.want)
		}
		for i := 1; i < len(got); i++ {
			if got[i].Seq <= got[i-1].Seq {
				t.Errorf("%s: results not in seq order", c.name)
			}
		}
	}
}

// Cursor-resume property (spec §15): concurrent writers, a reader consuming
// in randomly-sized batches with resume-by-cursor — the reader must see
// exactly every matching event, in order, no gaps, no duplicates.
func TestCursorResumeProperty(t *testing.T) {
	l := openMigratedDB(t)
	ctx := context.Background()
	const writers, perWriter = 4, 40
	total := writers * perWriter

	var wg sync.WaitGroup
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < perWriter; i++ {
				payload, _ := json.Marshal(map[string]int{"writer": w, "n": i})
				env := contracts.Envelope{
					Type: contracts.TaskUpdated, Actor: fmt.Sprintf("agent-%d", w),
					SessionID: "ses_prop", Payload: payload,
				}
				if err := l.Append(ctx, &env); err != nil {
					t.Errorf("writer %d: %v", w, err)
					return
				}
			}
		}(w)
	}
	wg.Wait()

	rng := rand.New(rand.NewSource(1))
	var (
		cursor int64
		seen   []int64
	)
	for {
		batch, err := l.Query(ctx, Filter{Since: cursor, SessionID: "ses_prop", Limit: 1 + rng.Intn(17)})
		if err != nil {
			t.Fatal(err)
		}
		if len(batch) == 0 {
			break
		}
		for _, e := range batch {
			seen = append(seen, e.Seq)
		}
		cursor = batch[len(batch)-1].Seq
	}

	if len(seen) != total {
		t.Fatalf("resumed reader saw %d events, want %d", len(seen), total)
	}
	for i := 1; i < len(seen); i++ {
		if seen[i] <= seen[i-1] {
			t.Fatalf("duplicate or out-of-order seq at position %d: %d after %d", i, seen[i], seen[i-1])
		}
	}
	// Seqs must be gapless for this single-table log: exactly first..first+total-1.
	if seen[len(seen)-1]-seen[0] != int64(total-1) {
		t.Errorf("gap detected: first %d, last %d, count %d", seen[0], seen[len(seen)-1], total)
	}
}

func TestWaitWakesOnCommit(t *testing.T) {
	l := openMigratedDB(t)
	ctx := context.Background()

	wake := l.Wait()
	done := make(chan struct{})
	go func() {
		<-wake
		close(done)
	}()
	env := contracts.Envelope{Type: contracts.NoteCreated, Actor: "synthesizer", SessionID: "ses_a"}
	if err := l.Append(ctx, &env); err != nil {
		t.Fatal(err)
	}
	<-done // hangs (test timeout) if commit does not broadcast
}
