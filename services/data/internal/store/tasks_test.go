package store

import (
	"context"
	"errors"
	"testing"

	"github.com/drewkidwell/mads/pkg/contracts"
)

func newTaskStore(t *testing.T) (*Tasks, *Log) {
	t.Helper()
	l := openMigratedDB(t)
	return NewTasks(l), l
}

func mkTask(t *testing.T, s *Tasks, task contracts.Task) contracts.Task {
	t.Helper()
	if task.SessionID == "" {
		task.SessionID = "ses_t"
	}
	if task.Source == "" {
		task.Source = "user"
	}
	if err := s.Create(context.Background(), &task, "user"); err != nil {
		t.Fatal(err)
	}
	return task
}

func eventTypes(t *testing.T, l *Log, f Filter) []contracts.EventType {
	t.Helper()
	events, err := l.Query(context.Background(), f)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]contracts.EventType, len(events))
	for i, e := range events {
		out[i] = e.Type
	}
	return out
}

func TestCreateAssignsAndAnnounces(t *testing.T) {
	s, l := newTaskStore(t)
	task := mkTask(t, s, contracts.Task{Title: "Build the thing"})

	if task.ID == "" || task.Status != contracts.StatusReady || task.CreatedAt.IsZero() {
		t.Errorf("create did not fill defaults: %+v", task)
	}
	got := eventTypes(t, l, Filter{Types: []string{"task.created"}})
	if len(got) != 1 {
		t.Errorf("want one task.created event, got %d", len(got))
	}
}

func TestValidationReportsAllAtOnce(t *testing.T) {
	s, _ := newTaskStore(t)
	err := s.Create(context.Background(), &contracts.Task{Status: "someday"}, "user")
	var verr *contracts.ValidationError
	if !errors.As(err, &verr) {
		t.Fatalf("want ValidationError, got %v", err)
	}
	if len(verr.Errors) != 4 { // title, session_id, source, status
		t.Errorf("want 4 errors together, got %d: %v", len(verr.Errors), verr.Errors)
	}
}

func TestDependencyValidation(t *testing.T) {
	s, _ := newTaskStore(t)
	a := mkTask(t, s, contracts.Task{Title: "A"})
	other := mkTask(t, s, contracts.Task{Title: "other-session", SessionID: "ses_other"})

	err := s.Create(context.Background(), &contracts.Task{
		Title: "B", SessionID: "ses_t", Source: "user",
		Dependencies: []string{a.ID, "tsk_missing", other.ID},
	}, "user")
	var verr *contracts.ValidationError
	if !errors.As(err, &verr) {
		t.Fatalf("want ValidationError, got %v", err)
	}
	if len(verr.Errors) != 2 { // missing + cross-session; a.ID is fine
		t.Errorf("want 2 dependency errors, got %v", verr.Errors)
	}
	// The failed create must leave nothing behind (outbox atomicity).
	tasks, err := s.List(context.Background(), TaskQuery{SessionID: "ses_t"})
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 {
		t.Errorf("failed create leaked rows: %d tasks", len(tasks))
	}
}

func TestUpdateEmitsStatusChange(t *testing.T) {
	s, l := newTaskStore(t)
	task := mkTask(t, s, contracts.Task{Title: "A"})

	task.Status = contracts.StatusInProgress
	task.Labels = []string{"backend"}
	if err := s.Update(context.Background(), &task, "pipeline"); err != nil {
		t.Fatal(err)
	}
	got := eventTypes(t, l, Filter{Types: []string{"task.updated", "task.status_changed"}})
	if len(got) != 2 {
		t.Fatalf("want task.updated + task.status_changed, got %v", got)
	}

	// Update without a status change emits only task.updated.
	task.Title = "A renamed"
	if err := s.Update(context.Background(), &task, "user"); err != nil {
		t.Fatal(err)
	}
	got = eventTypes(t, l, Filter{Types: []string{"task.status_changed"}})
	if len(got) != 1 {
		t.Errorf("status_changed emitted without a status change: %v", got)
	}

	fetched, err := s.Get(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if fetched.Title != "A renamed" || len(fetched.Labels) != 1 {
		t.Errorf("update did not persist: %+v", fetched)
	}
}

func TestDraftLifecycle(t *testing.T) {
	s, l := newTaskStore(t)
	ctx := context.Background()
	draft := mkTask(t, s, contracts.Task{Title: "draft A", Draft: true, Source: "synthesizer"})
	discard := mkTask(t, s, contracts.Task{Title: "draft B", Draft: true, Source: "synthesizer"})
	real := mkTask(t, s, contracts.Task{Title: "real"})

	if err := s.Confirm(ctx, draft.ID, "user"); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(ctx, draft.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Draft {
		t.Error("confirm left draft flag set")
	}

	if err := s.Discard(ctx, discard.ID, "user"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(ctx, discard.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("discarded draft still exists: %v", err)
	}

	// Real tasks cannot be confirmed or discarded.
	var verr *contracts.ValidationError
	if err := s.Discard(ctx, real.ID, "user"); !errors.As(err, &verr) {
		t.Errorf("discarding a non-draft should be a validation error, got %v", err)
	}

	types := eventTypes(t, l, Filter{Types: []string{"task.confirmed", "task.discarded"}})
	if len(types) != 2 {
		t.Errorf("want confirmed + discarded events, got %v", types)
	}
}

func TestHandoffsAndNotes(t *testing.T) {
	s, l := newTaskStore(t)
	ctx := context.Background()
	task := mkTask(t, s, contracts.Task{Title: "A"})

	if err := s.AddHandoff(ctx, task.ID, &contracts.Handoff{From: "architect", Content: "plan attached"}, "architect"); err != nil {
		t.Fatal(err)
	}
	if err := s.AddNote(ctx, task.ID, &contracts.Note{Type: contracts.NoteBlocker, Content: "API key missing", Source: "executor"}, "executor"); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Handoffs) != 1 || len(got.Notes) != 1 {
		t.Fatalf("hydration missing sub-records: %+v", got)
	}
	if got.Notes[0].Status != contracts.NoteStatusConfirmed {
		t.Error("user-authored note should default to confirmed")
	}
	if n := eventTypes(t, l, Filter{Types: []string{"note.created"}}); len(n) != 1 {
		t.Errorf("want note.created, got %v", n)
	}

	if err := s.AddNote(ctx, "tsk_missing", &contracts.Note{Type: contracts.NoteContext, Content: "x", Source: "user"}, "user"); !errors.Is(err, ErrNotFound) {
		t.Errorf("note on missing task: want ErrNotFound, got %v", err)
	}
}

func TestImportAtomicity(t *testing.T) {
	s, _ := newTaskStore(t)
	ctx := context.Background()

	// Batch with one invalid task: nothing lands.
	err := s.Import(ctx, []*contracts.Task{
		{Title: "ok", SessionID: "ses_i", Source: "user"},
		{SessionID: "ses_i", Source: "user"}, // no title
	}, "user")
	var verr *contracts.ValidationError
	if !errors.As(err, &verr) {
		t.Fatalf("want ValidationError, got %v", err)
	}
	tasks, _ := s.List(ctx, TaskQuery{SessionID: "ses_i"})
	if len(tasks) != 0 {
		t.Fatalf("failed import leaked %d tasks", len(tasks))
	}

	// Valid batch with intra-batch dependencies.
	a := &contracts.Task{Title: "A", SessionID: "ses_i", Source: "user", ID: NewID("tsk")}
	b := &contracts.Task{Title: "B", SessionID: "ses_i", Source: "user", Dependencies: []string{a.ID}}
	if err := s.Import(ctx, []*contracts.Task{a, b}, "user"); err != nil {
		t.Fatal(err)
	}
	tasks, _ = s.List(ctx, TaskQuery{SessionID: "ses_i"})
	if len(tasks) != 2 {
		t.Fatalf("import landed %d tasks, want 2", len(tasks))
	}
}

func TestListFilters(t *testing.T) {
	s, _ := newTaskStore(t)
	ctx := context.Background()
	mkTask(t, s, contracts.Task{Title: "a", SessionID: "ses_x"})
	mkTask(t, s, contracts.Task{Title: "b", SessionID: "ses_x", Status: contracts.StatusDone})
	mkTask(t, s, contracts.Task{Title: "c", SessionID: "ses_x", Draft: true, Source: "synthesizer"})
	mkTask(t, s, contracts.Task{Title: "d", SessionID: "ses_y"})

	byStatus, _ := s.List(ctx, TaskQuery{SessionID: "ses_x", Status: contracts.StatusDone})
	if len(byStatus) != 1 || byStatus[0].Title != "b" {
		t.Errorf("status filter: %+v", byStatus)
	}
	draft := true
	drafts, _ := s.List(ctx, TaskQuery{SessionID: "ses_x", Draft: &draft})
	if len(drafts) != 1 || drafts[0].Title != "c" {
		t.Errorf("draft filter: %+v", drafts)
	}
	all, _ := s.List(ctx, TaskQuery{})
	if len(all) != 4 {
		t.Errorf("unfiltered list: %d", len(all))
	}
}

func TestCycles(t *testing.T) {
	s, _ := newTaskStore(t)
	ctx := context.Background()

	a := mkTask(t, s, contracts.Task{Title: "A", SessionID: "ses_c"})
	b := mkTask(t, s, contracts.Task{Title: "B", SessionID: "ses_c", Dependencies: []string{a.ID}})
	c := mkTask(t, s, contracts.Task{Title: "C", SessionID: "ses_c", Dependencies: []string{b.ID}})

	cycles, err := s.Cycles(ctx, "ses_c")
	if err != nil {
		t.Fatal(err)
	}
	if len(cycles) != 0 {
		t.Fatalf("acyclic graph reported cycles: %v", cycles)
	}

	// Close the loop: A depends on C. Cycles are legal at rest (spec §3.6) —
	// the update succeeds and the check reports it.
	a.Dependencies = []string{c.ID}
	if err := s.Update(ctx, &a, "user"); err != nil {
		t.Fatalf("cycle-introducing update should succeed at rest: %v", err)
	}
	cycles, err = s.Cycles(ctx, "ses_c")
	if err != nil {
		t.Fatal(err)
	}
	if len(cycles) != 1 || len(cycles[0]) != 3 {
		t.Fatalf("want one 3-task cycle, got %v", cycles)
	}

	// Other sessions are unaffected.
	cycles, _ = s.Cycles(ctx, "ses_elsewhere")
	if len(cycles) != 0 {
		t.Errorf("unrelated session reports cycles: %v", cycles)
	}
}
