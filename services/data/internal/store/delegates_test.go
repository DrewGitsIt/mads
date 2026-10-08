package store

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
)

func newDelegateStore(t *testing.T) (*Delegates, *Sessions, *Log) {
	t.Helper()
	l := openMigratedDB(t)
	return NewDelegates(l), NewSessions(l), l
}

func delegateEvents(t *testing.T, l *Log, sessionID string) []contracts.Delegate {
	t.Helper()
	envs, err := l.Query(context.Background(), Filter{
		SessionID: sessionID, Types: []string{string(contracts.SessionDelegateChanged)},
	})
	if err != nil {
		t.Fatal(err)
	}
	out := make([]contracts.Delegate, 0, len(envs))
	for _, e := range envs {
		var d contracts.Delegate
		if err := json.Unmarshal(e.Payload, &d); err != nil {
			t.Fatal(err)
		}
		out = append(out, d)
	}
	return out
}

func running(sessionID, harnessID string) contracts.Delegate {
	return contracts.Delegate{
		SessionID: sessionID, HarnessID: harnessID, ToolUseID: "toolu_1",
		State: contracts.DelegateRunning, Runs: 1, StartedAt: time.Now().UTC(),
	}
}

func TestDelegatePutCreatesThenUpdatesInPlace(t *testing.T) {
	delegates, sessions, l := newDelegateStore(t)
	ctx := context.Background()
	sess := mkSession(t, sessions, contracts.Session{Name: "parent"})

	d := running(sess.ID, "a1")
	if err := delegates.Put(ctx, &d, "runner"); err != nil {
		t.Fatal(err)
	}
	if d.ID == "" || d.CreatedAt.IsZero() || d.UpdatedAt.IsZero() {
		t.Fatalf("store must assign identity: %+v", d)
	}
	id, created := d.ID, d.CreatedAt

	next := d
	next.ID, next.CreatedAt = "", time.Time{} // the runner never knows these
	next.State, next.Reason = contracts.DelegateCompleted, "completed"
	if err := delegates.Put(ctx, &next, "runner"); err != nil {
		t.Fatal(err)
	}
	if next.ID != id || !next.CreatedAt.Equal(created) {
		t.Errorf("identity moved: id %s→%s, created %v→%v", id, next.ID, created, next.CreatedAt)
	}

	list, err := delegates.ForSession(ctx, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].State != contracts.DelegateCompleted {
		t.Fatalf("want one completed record, got %+v", list)
	}
	evs := delegateEvents(t, l, sess.ID)
	if len(evs) != 2 || evs[0].State != contracts.DelegateRunning || evs[1].State != contracts.DelegateCompleted {
		t.Fatalf("want running then completed events, got %+v", evs)
	}
}

func TestDelegatePutUnchangedIsSilent(t *testing.T) {
	delegates, sessions, l := newDelegateStore(t)
	ctx := context.Background()
	sess := mkSession(t, sessions, contracts.Session{Name: "parent"})

	d := running(sess.ID, "a1")
	if err := delegates.Put(ctx, &d, "runner"); err != nil {
		t.Fatal(err)
	}
	// A retry re-publishes the same record — possibly re-decoded, so its
	// times have lost their monotonic reading and may carry another zone.
	again := d
	again.ID = ""
	again.StartedAt = d.StartedAt.In(time.FixedZone("x", 3600))
	if err := delegates.Put(ctx, &again, "runner"); err != nil {
		t.Fatal(err)
	}
	if n := len(delegateEvents(t, l, sess.ID)); n != 1 {
		t.Errorf("an unchanged put must not announce: %d events", n)
	}
}

func TestDelegateListByStateAcrossSessions(t *testing.T) {
	delegates, sessions, _ := newDelegateStore(t)
	ctx := context.Background()
	a := mkSession(t, sessions, contracts.Session{Name: "a"})
	b := mkSession(t, sessions, contracts.Session{Name: "b"})

	for _, d := range []contracts.Delegate{running(a.ID, "x"), running(b.ID, "y"), running(b.ID, "z")} {
		if err := delegates.Put(ctx, &d, "runner"); err != nil {
			t.Fatal(err)
		}
	}
	done := running(b.ID, "z")
	done.State = contracts.DelegateStopped
	if err := delegates.Put(ctx, &done, "runner"); err != nil {
		t.Fatal(err)
	}

	got, err := delegates.List(ctx, contracts.DelegateRunning)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].HarnessID != "x" || got[1].HarnessID != "y" {
		t.Fatalf("running across sessions = %+v", got)
	}
	all, err := delegates.List(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Errorf("unfiltered list = %d records, want 3", len(all))
	}
	var verr *contracts.ValidationError
	if _, err := delegates.List(ctx, "idle"); !errors.As(err, &verr) {
		t.Errorf("an unknown state filter must be a validation error, got %v", err)
	}
}

func TestDelegatePutValidates(t *testing.T) {
	delegates, sessions, _ := newDelegateStore(t)
	ctx := context.Background()
	sess := mkSession(t, sessions, contracts.Session{Name: "parent"})

	bad := running(sess.ID, "a1")
	bad.State = "idle"
	var verr *contracts.ValidationError
	if err := delegates.Put(ctx, &bad, "runner"); !errors.As(err, &verr) {
		t.Errorf("unknown state: want validation error, got %v", err)
	}
	orphan := running("ses_missing", "a1")
	if err := delegates.Put(ctx, &orphan, "runner"); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing session: want ErrNotFound, got %v", err)
	}
}

func TestSessionDeleteRemovesItsDelegates(t *testing.T) {
	delegates, sessions, _ := newDelegateStore(t)
	ctx := context.Background()
	gone := mkSession(t, sessions, contracts.Session{Name: "gone"})
	kept := mkSession(t, sessions, contracts.Session{Name: "kept"})
	for _, d := range []contracts.Delegate{running(gone.ID, "x"), running(kept.ID, "y")} {
		if err := delegates.Put(ctx, &d, "runner"); err != nil {
			t.Fatal(err)
		}
	}
	if err := sessions.Delete(ctx, gone.ID, "user"); err != nil {
		t.Fatal(err)
	}
	got, err := delegates.List(ctx, contracts.DelegateRunning)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].SessionID != kept.ID {
		t.Fatalf("a deleted session's delegates must go with it: %+v", got)
	}
}
