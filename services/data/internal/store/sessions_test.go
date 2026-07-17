package store

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/drewkidwell/mads/pkg/contracts"
)

func newSessionStore(t *testing.T) (*Sessions, *Tasks, *Log) {
	t.Helper()
	l := openMigratedDB(t)
	return NewSessions(l), NewTasks(l), l
}

func mkSession(t *testing.T, s *Sessions, sess contracts.Session) contracts.Session {
	t.Helper()
	if sess.Name == "" {
		sess.Name = "test session"
	}
	if err := s.Create(context.Background(), &sess, "user"); err != nil {
		t.Fatal(err)
	}
	return sess
}

func TestSessionLifecycleEvents(t *testing.T) {
	s, _, l := newSessionStore(t)
	ctx := context.Background()
	sess := mkSession(t, s, contracts.Session{Name: "Add auth", Workspace: "mads-core"})
	if sess.Activity != contracts.SessionActive || sess.Isolation != contracts.IsolationWorktree {
		t.Fatalf("defaults not applied: %+v", sess)
	}

	sess.Name = "Add OAuth"
	if err := s.Update(ctx, &sess, "user"); err != nil {
		t.Fatal(err)
	}
	sess.Activity = contracts.SessionInactive
	sess.Summary = "Implemented the OAuth flow."
	if err := s.Update(ctx, &sess, "system"); err != nil {
		t.Fatal(err)
	}
	sess.Provider = "claude-code" // binding at first message (spec §11.5)
	if err := s.Update(ctx, &sess, "system"); err != nil {
		t.Fatal(err)
	}

	types := eventTypes(t, l, Filter{SessionID: sess.ID, Types: []string{"session.*"}})
	want := []contracts.EventType{
		contracts.SessionCreated, contracts.SessionRenamed,
		contracts.SessionActivityChanged, contracts.SessionUpdated,
	}
	if len(types) != len(want) {
		t.Fatalf("events %v, want %v", types, want)
	}
	for i := range want {
		if types[i] != want[i] {
			t.Errorf("event %d: %s, want %s", i, types[i], want[i])
		}
	}
}

func TestSessionDeleteCascades(t *testing.T) {
	s, tasks, l := newSessionStore(t)
	ctx := context.Background()
	sess := mkSession(t, s, contracts.Session{})
	task := mkTask(t, tasks, contracts.Task{Title: "T", SessionID: sess.ID})
	leases := NewLeases(l)
	if _, err := leases.Claim(ctx, task.ID, "executor", 60000000000); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordFileChange(ctx, &contracts.FileChange{
		SessionID: sess.ID, Path: "a.go", Change: "modified", Origin: contracts.OriginToolUse,
	}, "executor"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RecordCommit(ctx, &CommitReport{Commit: contracts.Commit{
		Hash: "abc123", SessionID: sess.ID, Message: "change things",
	}}, "executor"); err != nil {
		t.Fatal(err)
	}

	if err := s.Delete(ctx, sess.ID, "user"); err != nil {
		t.Fatal(err)
	}

	if _, err := s.Get(ctx, sess.ID); !errors.Is(err, ErrNotFound) {
		t.Error("session survived delete")
	}
	if _, err := tasks.Get(ctx, task.ID); !errors.Is(err, ErrNotFound) {
		t.Error("task survived session delete (spec §1: hard delete)")
	}
	for _, table := range []string{"file_changes", "commits", "task_leases", "task_deps", "task_notes"} {
		var n int
		if err := l.db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Errorf("%s: %d rows survived the cascade", table, n)
		}
	}
	// The event log is append-only: history survives deletion (spec §8).
	types := eventTypes(t, l, Filter{SessionID: sess.ID, Types: []string{"session.deleted"}})
	if len(types) != 1 {
		t.Errorf("session.deleted not announced: %v", types)
	}
}

// Links are the one exception to this file's cascade rule (session deletion
// breaks its links, never cascades to them — REBUILD-SPEC §9 /
// docs/LINKED-SESSIONS-DESIGN.md "Unchanged from §1.12"): a link outliving
// a deleted endpoint is a dangling reference by design, not something to
// clean up here.
func TestSessionDeleteDoesNotCascadeToLinks(t *testing.T) {
	s, _, l := newSessionStore(t)
	ctx := context.Background()
	links := NewLinks(l, s)
	leader := mkSession(t, s, contracts.Session{Name: "leader"})
	subject := mkSession(t, s, contracts.Session{Name: "subject"})

	link := contracts.Link{
		GrantingSessionID: leader.ID, SubjectSessionID: subject.ID,
		Grants: []contracts.Grant{contracts.GrantRead},
	}
	if err := links.Create(ctx, &link, "user"); err != nil {
		t.Fatal(err)
	}

	if err := s.Delete(ctx, subject.ID, "user"); err != nil {
		t.Fatal(err)
	}

	got, err := links.Get(ctx, link.ID)
	if err != nil {
		t.Fatalf("link did not survive subject session deletion: %v", err)
	}
	if got.SubjectSessionID != subject.ID {
		t.Errorf("surviving link's subject_session_id = %q, want %q (dangling reference by design)", got.SubjectSessionID, subject.ID)
	}
}

// The §5 attribution rule (decided): isolation mode × origin.
func TestAttributionTable(t *testing.T) {
	cases := []struct {
		isolation string
		origin    contracts.ChangeOrigin
		want      bool
	}{
		{contracts.IsolationWorktree, contracts.OriginToolUse, true},
		{contracts.IsolationWorktree, contracts.OriginCommit, true},
		{contracts.IsolationWorktree, contracts.OriginObserved, true}, // the worktree exists only for this session
		{contracts.IsolationCurrentDir, contracts.OriginToolUse, true},
		{contracts.IsolationCurrentDir, contracts.OriginCommit, true},
		{contracts.IsolationCurrentDir, contracts.OriginObserved, false}, // ambient: recorded, not attributed
	}
	for _, c := range cases {
		if got := contracts.AttributedChange(c.isolation, c.origin); got != c.want {
			t.Errorf("AttributedChange(%s, %s) = %v, want %v", c.isolation, c.origin, got, c.want)
		}
	}

	// And end-to-end through the store: the session's isolation decides.
	s, _, _ := newSessionStore(t)
	ctx := context.Background()
	for i, c := range cases {
		sess := mkSession(t, s, contracts.Session{Name: fmt.Sprintf("s%d", i), Isolation: c.isolation})
		fc := contracts.FileChange{SessionID: sess.ID, Path: "x.go", Change: "modified", Origin: c.origin}
		if err := s.RecordFileChange(ctx, &fc, "any"); err != nil {
			t.Fatal(err)
		}
		if fc.Attributed != c.want {
			t.Errorf("case %d: stored attribution %v, want %v", i, fc.Attributed, c.want)
		}
	}
}

// The commit-linkage chain (spec §3, §5), rung by rung.
func TestCommitLinkageChain(t *testing.T) {
	s, tasks, l := newSessionStore(t)
	ctx := context.Background()
	sess := mkSession(t, s, contracts.Session{})
	trailerTask := mkTask(t, tasks, contracts.Task{Title: "trailer target", SessionID: sess.ID})
	activeTask := mkTask(t, tasks, contracts.Task{Title: "active task", SessionID: sess.ID})
	heurTask := mkTask(t, tasks, contracts.Task{Title: "heuristic match", SessionID: sess.ID})

	// Rung 1: an explicit trailer wins over everything.
	sess.ActiveTaskID = activeTask.ID
	if err := s.Update(ctx, &sess, "user"); err != nil {
		t.Fatal(err)
	}
	c1, err := s.RecordCommit(ctx, &CommitReport{
		Commit:        contracts.Commit{Hash: "c1", SessionID: sess.ID, Message: "fix"},
		TrailerTaskID: trailerTask.ID, HeuristicTaskIDs: []string{heurTask.ID},
	}, "executor")
	if err != nil {
		t.Fatal(err)
	}
	if c1.TaskID != trailerTask.ID || c1.LinkedVia != contracts.LinkedViaTrailer {
		t.Errorf("rung 1: %+v", c1)
	}

	// Rung 2: no trailer (or a bogus one) → the session's active task.
	c2, err := s.RecordCommit(ctx, &CommitReport{
		Commit:        contracts.Commit{Hash: "c2", SessionID: sess.ID, Message: "more"},
		TrailerTaskID: "tsk_bogus",
	}, "executor")
	if err != nil {
		t.Fatal(err)
	}
	if c2.TaskID != activeTask.ID || c2.LinkedVia != contracts.LinkedViaSessionContext {
		t.Errorf("rung 2: %+v", c2)
	}

	// Rung 3: no trailer, no active task → heuristics.
	sess.ActiveTaskID = ""
	if err := s.Update(ctx, &sess, "user"); err != nil {
		t.Fatal(err)
	}
	c3, err := s.RecordCommit(ctx, &CommitReport{
		Commit:           contracts.Commit{Hash: "c3", SessionID: sess.ID, Message: "touch"},
		HeuristicTaskIDs: []string{"tsk_gone", heurTask.ID},
	}, "executor")
	if err != nil {
		t.Fatal(err)
	}
	if c3.TaskID != heurTask.ID || c3.LinkedVia != contracts.LinkedViaHeuristic {
		t.Errorf("rung 3: %+v", c3)
	}

	// Rung 4: nothing resolves → orphan flag, never dropped.
	c4, err := s.RecordCommit(ctx, &CommitReport{
		Commit: contracts.Commit{Hash: "c4", SessionID: sess.ID, Message: "mystery"},
	}, "executor")
	if err != nil {
		t.Fatal(err)
	}
	if !c4.Orphan || c4.TaskID != "" {
		t.Errorf("rung 4: %+v", c4)
	}

	// Events: created for all four, linked for the three that resolved.
	created := eventTypes(t, l, Filter{Types: []string{"commit.created"}})
	linked := eventTypes(t, l, Filter{Types: []string{"commit.linked"}})
	if len(created) != 4 || len(linked) != 3 {
		t.Errorf("events: %d created (want 4), %d linked (want 3)", len(created), len(linked))
	}

	all, err := s.Commits(ctx, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 4 {
		t.Errorf("stored %d commits, want 4", len(all))
	}
}
