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

// The rail's cold read with no fade asks for what ended after now: only the
// live ones. A completed delegate still waiting on work it started is live
// however long ago its run ended; a plain completed one of the same age is
// not. And a change to WaitingOn alone is a real change (sameDelegate
// compares JSON), so clearing it announces.
func TestDelegateLiveOrEndedAfterIncludesWaiting(t *testing.T) {
	delegates, sessions, l := newDelegateStore(t)
	ctx := context.Background()
	sess := mkSession(t, sessions, contracts.Session{Name: "parent"})
	ended := time.Now().Add(-time.Hour).UTC()
	finished := func(harnessID string, w *contracts.DelegateWaiting) contracts.Delegate {
		d := running(sess.ID, harnessID)
		d.State, d.EndedAt, d.WaitingOn = contracts.DelegateCompleted, &ended, w
		return d
	}
	for _, d := range []contracts.Delegate{
		running(sess.ID, "live"),
		finished("waiting", &contracts.DelegateWaiting{Subagents: 1}),
		finished("done", nil),
	} {
		if err := delegates.Put(ctx, &d, "runner"); err != nil {
			t.Fatal(err)
		}
	}
	got, err := delegates.LiveOrEndedAfter(ctx, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, d := range got {
		ids = append(ids, d.HarnessID)
	}
	if len(ids) != 2 || ids[0] != "live" || ids[1] != "waiting" {
		t.Errorf("live or ended after now = %v, want [live waiting]", ids)
	}
	if got[1].WaitingOn == nil || got[1].WaitingOn.Subagents != 1 {
		t.Errorf("waiting_on did not round-trip: %+v", got[1].WaitingOn)
	}

	cleared := finished("waiting", nil)
	if err := delegates.Put(ctx, &cleared, "runner"); err != nil {
		t.Fatal(err)
	}
	evs := delegateEvents(t, l, sess.ID)
	if last := evs[len(evs)-1]; len(evs) != 4 || last.HarnessID != "waiting" || last.WaitingOn != nil {
		t.Errorf("clearing waiting_on must announce: %d events, last %+v", len(evs), last)
	}

	bad := finished("bad", &contracts.DelegateWaiting{Processes: -1})
	var verr *contracts.ValidationError
	if err := delegates.Put(ctx, &bad, "runner"); !errors.As(err, &verr) {
		t.Errorf("negative waiting count: want validation error, got %v", err)
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
