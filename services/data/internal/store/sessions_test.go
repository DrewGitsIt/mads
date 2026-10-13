package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

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

// An import supplies the conversation's real start time; an ordinary create
// (zero CreatedAt) is stamped now, and an update never moves it.
func TestSessionCreateHonorsSuppliedCreatedAt(t *testing.T) {
	s, _, _ := newSessionStore(t)
	ctx := context.Background()
	past := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)

	imported := mkSession(t, s, contracts.Session{CreatedAt: past})
	if !imported.CreatedAt.Equal(past) {
		t.Errorf("CreatedAt = %v, want the supplied %v", imported.CreatedAt, past)
	}
	if imported.UpdatedAt.Before(past.Add(24 * time.Hour)) {
		t.Errorf("UpdatedAt = %v, want now", imported.UpdatedAt)
	}
	ordinary := mkSession(t, s, contracts.Session{})
	if time.Since(ordinary.CreatedAt) > time.Minute {
		t.Errorf("ordinary CreatedAt = %v, want now", ordinary.CreatedAt)
	}

	imported.CreatedAt = time.Now()
	imported.Name = "renamed"
	if err := s.Update(ctx, &imported, "user"); err != nil {
		t.Fatal(err)
	}
	if !imported.CreatedAt.Equal(past) {
		t.Errorf("CreatedAt after update = %v, want it kept at %v", imported.CreatedAt, past)
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

// TestSetAgentStateMirrorsAndAnnounces: the record and the event have to
// land together — a cold GET /sessions is the only thing a page load
// mid-turn has to go on, and it must agree with the stream.
func TestSetAgentStateMirrorsAndAnnounces(t *testing.T) {
	s, _, l := newSessionStore(t)
	ctx := context.Background()
	sess := mkSession(t, s, contracts.Session{Name: "Add auth"})
	before, err := s.Get(ctx, sess.ID)
	if err != nil {
		t.Fatal(err)
	}

	working := contracts.AgentState{
		State: contracts.AgentWorking,
		Label: "Running bash · make check",
		Since: time.Date(2026, 9, 28, 14, 5, 0, 0, time.UTC),
	}
	if err := s.SetAgentState(ctx, sess.ID, working, "session"); err != nil {
		t.Fatal(err)
	}

	got, err := s.Get(ctx, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.AgentState == nil {
		t.Fatal("agent state not mirrored onto the record")
	}
	if got.AgentState.State != working.State || got.AgentState.Label != working.Label ||
		!got.AgentState.Since.Equal(working.Since) {
		t.Errorf("mirrored state = %+v, want %+v", *got.AgentState, working)
	}
	// A tool-label transition is not a change to the session's user-visible
	// state: bumping UpdatedAt would reshuffle the list several times a turn
	// and restart the archive clock SweepExpiredShadows reads off it.
	if !got.UpdatedAt.Equal(before.UpdatedAt) {
		t.Errorf("UpdatedAt moved: %v, want %v", got.UpdatedAt, before.UpdatedAt)
	}

	types := eventTypes(t, l, Filter{SessionID: sess.ID, Types: []string{"session.agent_state"}})
	if len(types) != 1 {
		t.Fatalf("agent_state events = %v, want exactly one", types)
	}

	if err := s.SetAgentState(ctx, sess.ID, contracts.AgentState{State: "bogus"}, "session"); err == nil {
		t.Error("expected a validation error for an unknown agent state")
	}
}

// TestRecordTurnEndStampsLastTurnAt: last_turn_at advances on turn end and
// on nothing else — the whole reason it exists beside UpdatedAt, which every
// rename and summary write bumps.
func TestRecordTurnEndStampsLastTurnAt(t *testing.T) {
	s, _, _ := newSessionStore(t)
	ctx := context.Background()
	sess := mkSession(t, s, contracts.Session{Name: "Add auth"})

	if cur, err := s.Get(ctx, sess.ID); err != nil {
		t.Fatal(err)
	} else if cur.LastTurnAt != nil {
		t.Errorf("LastTurnAt = %v on a session with no turns yet, want nil", cur.LastTurnAt)
	}

	turnEnd := time.Date(2026, 9, 28, 14, 6, 12, 0, time.UTC)
	usage := &contracts.Usage{InputTokens: 1200, ContextTokens: 48_000}
	if err := s.RecordTurnEnd(ctx, sess.ID, usage, turnEnd, "session"); err != nil {
		t.Fatal(err)
	}
	cur, err := s.Get(ctx, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if cur.LastTurnAt == nil || !cur.LastTurnAt.Equal(turnEnd) {
		t.Fatalf("LastTurnAt = %v, want %v", cur.LastTurnAt, turnEnd)
	}
	if cur.LastUsage == nil || cur.LastUsage.ContextTokens != 48_000 {
		t.Errorf("LastUsage = %+v, want the turn's accounting", cur.LastUsage)
	}

	// Rename, summary and activity writes: none of them is a turn.
	cur.Name = "Add OAuth"
	cur.Summary = "Implemented the OAuth flow."
	if err := s.Update(ctx, cur, "user"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetActivity(ctx, sess.ID, contracts.SessionInactive, "system"); err != nil {
		t.Fatal(err)
	}
	after, err := s.Get(ctx, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.LastTurnAt == nil || !after.LastTurnAt.Equal(turnEnd) {
		t.Errorf("LastTurnAt = %v after rename/summary/activity writes, want %v", after.LastTurnAt, turnEnd)
	}
	if !after.UpdatedAt.After(*after.LastTurnAt) {
		t.Errorf("UpdatedAt %v should have moved past the turn, LastTurnAt %v", after.UpdatedAt, after.LastTurnAt)
	}

	// A turn that reported no usage still ended: the stamp advances, the
	// previous accounting survives.
	later := turnEnd.Add(time.Minute)
	if err := s.RecordTurnEnd(ctx, sess.ID, nil, later, "session"); err != nil {
		t.Fatal(err)
	}
	if cur, err := s.Get(ctx, sess.ID); err != nil {
		t.Fatal(err)
	} else if cur.LastTurnAt == nil || !cur.LastTurnAt.Equal(later) {
		t.Errorf("LastTurnAt = %v after a usage-less turn, want %v", cur.LastTurnAt, later)
	} else if cur.LastUsage == nil || cur.LastUsage.ContextTokens != 48_000 {
		t.Errorf("LastUsage = %+v, want the earlier turn's accounting preserved", cur.LastUsage)
	}
}

// TestSessionUpdateKeepsRunnerFields is the lost-update case the scoped
// writers exist for: a client PUTs a whole record it read before the runner
// wrote its turn state. The stale snapshot must not stamp a finished turn
// back over a live one.
func TestSessionUpdateKeepsRunnerFields(t *testing.T) {
	s, _, _ := newSessionStore(t)
	ctx := context.Background()
	sess := mkSession(t, s, contracts.Session{Name: "Add auth"})

	stale, err := s.Get(ctx, sess.ID) // read before the runner writes
	if err != nil {
		t.Fatal(err)
	}

	turnEnd := time.Date(2026, 9, 28, 14, 6, 12, 0, time.UTC)
	if err := s.RecordTurnEnd(ctx, sess.ID, nil, turnEnd, "session"); err != nil {
		t.Fatal(err)
	}
	working := contracts.AgentState{State: contracts.AgentWorking, Label: "Thinking…", Since: turnEnd}
	if err := s.SetAgentState(ctx, sess.ID, working, "session"); err != nil {
		t.Fatal(err)
	}

	stale.Name = "Add OAuth" // ...and only now writes back
	if err := s.Update(ctx, stale, "user"); err != nil {
		t.Fatal(err)
	}

	got, err := s.Get(ctx, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "Add OAuth" {
		t.Errorf("name = %q, want the rename to have landed", got.Name)
	}
	if got.LastTurnAt == nil || !got.LastTurnAt.Equal(turnEnd) {
		t.Errorf("LastTurnAt = %v, want %v — a stale PUT cleared it", got.LastTurnAt, turnEnd)
	}
	if got.AgentState == nil || got.AgentState.State != contracts.AgentWorking {
		t.Errorf("AgentState = %+v, want the live working state", got.AgentState)
	}
}

// TestSetWorktreeScopedWrite: the isolation fields land through their own
// scoped write, an unchanged recount is not announced, and a stale full PUT
// can't erase the worktree or roll the counts back.
func TestSetWorktreeScopedWrite(t *testing.T) {
	s, _, l := newSessionStore(t)
	ctx := context.Background()
	sess := mkSession(t, s, contracts.Session{Name: "Isolated"})
	stale, err := s.Get(ctx, sess.ID)
	if err != nil {
		t.Fatal(err)
	}

	dir, branch, base, two, three, four := "/wt/x", "mads/session/x", "main", 2, 3, 4
	patch := contracts.SessionWorktreePatch{
		WorktreeDir: &dir, WorktreeBranch: &branch, WorktreeBase: &base,
		UncommittedCount: &two, UnlandedCount: &three, OutsideCount: &four,
	}
	if err := s.SetWorktree(ctx, sess.ID, patch, "session"); err != nil {
		t.Fatal(err)
	}
	updates := func() int {
		return len(eventTypes(t, l, Filter{SessionID: sess.ID, Types: []string{"session.updated"}}))
	}
	n := updates()
	if err := s.SetWorktree(ctx, sess.ID, patch, "session"); err != nil {
		t.Fatal(err)
	}
	if updates() != n {
		t.Error("an unchanged patch announced session.updated")
	}

	stale.Name = "Renamed"
	if err := s.Update(ctx, stale, "user"); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(ctx, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "Renamed" || got.WorktreeDir != dir || got.WorktreeBranch != branch || got.WorktreeBase != base ||
		got.UncommittedCount != 2 || got.UnlandedCount != 3 || got.OutsideCount != 4 {
		t.Errorf("after stale PUT: %+v, want the rename and every isolation field", got)
	}

	zero, empty := 0, ""
	if err := s.SetWorktree(ctx, sess.ID, contracts.SessionWorktreePatch{UncommittedCount: &zero, WorktreeDir: &empty}, "session"); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Get(ctx, sess.ID); got.UncommittedCount != 0 || got.WorktreeDir != "" || got.UnlandedCount != 3 || got.WorktreeBranch != branch || got.OutsideCount != 4 {
		t.Errorf("partial patch: %+v, want uncommitted and dir cleared, the rest kept", got)
	}
	// A changed outside count alone is news: the header's Land depends on it.
	n = updates()
	if err := s.SetWorktree(ctx, sess.ID, contracts.SessionWorktreePatch{OutsideCount: &zero}, "session"); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Get(ctx, sess.ID); got.OutsideCount != 0 || updates() != n+1 {
		t.Errorf("outside patch: %+v, %d updates (was %d), want it cleared and announced", got, updates(), n)
	}
}

// An undone Commit drops its record and says so on the event log; a hash
// the session never recorded (or another session's) is not found.
func TestDeleteCommit(t *testing.T) {
	s, _, l := newSessionStore(t)
	ctx := context.Background()
	sess := mkSession(t, s, contracts.Session{})
	other := mkSession(t, s, contracts.Session{})
	for _, c := range []contracts.Commit{
		{Hash: "keep", SessionID: sess.ID, Message: "a"},
		{Hash: "undo", SessionID: sess.ID, Message: "b"},
		{Hash: "theirs", SessionID: other.ID, Message: "c"},
	} {
		if _, err := s.RecordCommit(ctx, &CommitReport{Commit: c}, "test"); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.DeleteCommit(ctx, sess.ID, "theirs", "test"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delete another session's commit: err = %v, want ErrNotFound", err)
	}
	if err := s.DeleteCommit(ctx, sess.ID, "undo", "test"); err != nil {
		t.Fatal(err)
	}
	got, err := s.Commits(ctx, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Hash != "keep" {
		t.Errorf("commits after undo = %+v, want only keep", got)
	}
	evs, err := l.Query(ctx, Filter{SessionID: sess.ID, Types: []string{string(contracts.CommitUndone)}})
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 1 || !strings.Contains(string(evs[0].Payload), `"undo"`) {
		t.Errorf("commit.undone events = %+v", evs)
	}
}

// A commit mads made itself says so on commit.created — source, subject,
// file count, drafted — so the conversation can draw its card; a
// hook-reported commit carries none of it.
func TestRecordCommitProvenance(t *testing.T) {
	s, _, l := newSessionStore(t)
	ctx := context.Background()
	sess := mkSession(t, s, contracts.Session{})
	if _, err := s.RecordCommit(ctx, &CommitReport{
		Commit: contracts.Commit{Hash: "mine", SessionID: sess.ID, Message: "Fix the thing"},
		Source: contracts.CommitSourceCommit, Files: 3, Drafted: true,
	}, "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RecordCommit(ctx, &CommitReport{
		Commit: contracts.Commit{Hash: "agent", SessionID: sess.ID, Message: "Agent's own"},
	}, "test"); err != nil {
		t.Fatal(err)
	}
	evs, err := l.Query(ctx, Filter{SessionID: sess.ID, Types: []string{string(contracts.CommitCreated)}})
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 2 {
		t.Fatalf("commit.created events = %d, want 2", len(evs))
	}
	var mine, agent map[string]any
	_ = json.Unmarshal(evs[0].Payload, &mine)
	_ = json.Unmarshal(evs[1].Payload, &agent)
	if mine["source"] != "commit" || mine["message"] != "Fix the thing" || mine["files"] != float64(3) || mine["drafted"] != true {
		t.Errorf("mads commit payload = %v", mine)
	}
	if _, ok := agent["source"]; ok {
		t.Errorf("hook commit payload = %v, want no source", agent)
	}
}

// TestSessionEffectiveTrustNeverStored: effective_trust is the Router's
// derivation from the provider's declaration, recomputed on every read; a
// decorated record written back (Create or Update) must not freeze it into
// the row, or a provider that later grows a reviewer would keep reporting
// the old downgrade. auto_review itself is a valid level to store.
func TestSessionEffectiveTrustNeverStored(t *testing.T) {
	s, _, _ := newSessionStore(t)
	ctx := context.Background()
	sess := mkSession(t, s, contracts.Session{Trust: contracts.TrustAutoReview, EffectiveTrust: contracts.TrustLimited})
	got, err := s.Get(ctx, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Trust != contracts.TrustAutoReview || got.EffectiveTrust != "" {
		t.Fatalf("after create: trust %q, effective %q — want auto_review stored, effective dropped", got.Trust, got.EffectiveTrust)
	}
	got.EffectiveTrust = contracts.TrustLimited
	if err := s.Update(ctx, got, "user"); err != nil {
		t.Fatal(err)
	}
	again, err := s.Get(ctx, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if again.EffectiveTrust != "" {
		t.Fatalf("after update: effective %q stored, want dropped", again.EffectiveTrust)
	}
}

// TestSetAsideScopedWrite: the user's "done with this for now" judgement
// lands through its own scoped write, an unchanged set is not announced, and
// a stale full PUT racing it can't resurrect a tag the user just cleared.
func TestSetAsideScopedWrite(t *testing.T) {
	s, _, l := newSessionStore(t)
	ctx := context.Background()
	sess := mkSession(t, s, contracts.Session{Name: "Parked", Tags: []string{"retro"}})
	updates := func() int {
		return len(eventTypes(t, l, Filter{SessionID: sess.ID, Types: []string{"session.updated"}}))
	}

	if err := s.SetAside(ctx, sess.ID, true, "user"); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(ctx, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.IsAside() {
		t.Fatalf("tags = %v, want the aside tag", got.Tags)
	}
	// Tags are shared ground — provenance must survive the user's judgement.
	if len(got.Tags) != 2 || got.Tags[0] != "retro" {
		t.Errorf("tags = %v, want retro kept alongside aside", got.Tags)
	}

	// The rail's list subscribes to session.updated; without it the fold
	// closes only on the browser that clicked.
	n := updates()
	if n == 0 {
		t.Fatal("setting a session aside announced no session.updated")
	}
	if err := s.SetAside(ctx, sess.ID, true, "user"); err != nil {
		t.Fatal(err)
	}
	if updates() != n {
		t.Error("an unchanged SetAside announced session.updated")
	}

	// The send path's reason for being able to clear unconditionally: a
	// session that was never set aside writes nothing at all.
	fresh := mkSession(t, s, contracts.Session{Name: "Never parked"})
	before, err := s.Get(ctx, fresh.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetAside(ctx, fresh.ID, false, "router"); err != nil {
		t.Fatal(err)
	}
	after, err := s.Get(ctx, fresh.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !after.UpdatedAt.Equal(before.UpdatedAt) || after.Tags != nil {
		t.Errorf("clearing an unset tag touched the record: %+v", after)
	}

	// The lost-update case the scoped writer exists for. A client reads the
	// record while the session is parked, the user sends it a message (which
	// clears the tag), and only then does the stale PUT land.
	stale, err := s.Get(ctx, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetAside(ctx, sess.ID, false, "router"); err != nil {
		t.Fatal(err)
	}
	stale.Name = "Picked back up"
	if err := s.Update(ctx, stale, "user"); err != nil {
		t.Fatal(err)
	}
	final, err := s.Get(ctx, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if final.Name != "Picked back up" {
		t.Errorf("name = %q, want the rename to have landed", final.Name)
	}
	if final.IsAside() {
		t.Errorf("tags = %v — a stale PUT put the session back in the fold", final.Tags)
	}
}

func TestSetRailRankScopedWrite(t *testing.T) {
	s, _, l := newSessionStore(t)
	ctx := context.Background()
	sess := mkSession(t, s, contracts.Session{Name: "Dragged"})
	before, err := s.Get(ctx, sess.ID)
	if err != nil {
		t.Fatal(err)
	}

	if err := s.SetRailRank(ctx, sess.ID, 1234.5, "user"); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(ctx, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.RailRank == nil || *got.RailRank != 1234.5 {
		t.Fatalf("rail_rank = %v, want 1234.5", got.RailRank)
	}
	// A position in one view is not a change to the session: the archive
	// clock reads UpdatedAt.
	if !got.UpdatedAt.Equal(before.UpdatedAt) {
		t.Error("SetRailRank bumped updated_at")
	}
	if n := len(eventTypes(t, l, Filter{SessionID: sess.ID, Types: []string{"session.updated"}})); n == 0 {
		t.Error("SetRailRank announced no session.updated")
	}

	// A full-record PUT from a writer that read before the drag must not
	// put the card back where it was.
	stale := *before
	stale.Summary = "runner wrote this"
	if err := s.Update(ctx, &stale, "runner"); err != nil {
		t.Fatal(err)
	}
	got, err = s.Get(ctx, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.RailRank == nil || *got.RailRank != 1234.5 {
		t.Fatalf("rail_rank after a stale PUT = %v, want 1234.5 kept", got.RailRank)
	}

	if err := s.SetRailRank(ctx, sess.ID, math.NaN(), "user"); err == nil {
		t.Error("SetRailRank accepted NaN")
	}
}

func TestBringingBackFromAsideRanksOnTop(t *testing.T) {
	s, _, _ := newSessionStore(t)
	ctx := context.Background()
	sess := mkSession(t, s, contracts.Session{Name: "Picked up again"})
	if err := s.SetRailRank(ctx, sess.ID, 1, "user"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetAside(ctx, sess.ID, true, "user"); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(ctx, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.RailRank == nil || *got.RailRank != 1 {
		t.Fatalf("setting aside moved the rank: %v", got.RailRank)
	}

	start := float64(time.Now().UnixMilli())
	if err := s.SetAside(ctx, sess.ID, false, "user"); err != nil {
		t.Fatal(err)
	}
	got, err = s.Get(ctx, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	// Ranked "now", which is above every start time already in the list.
	if got.RailRank == nil || *got.RailRank < start {
		t.Fatalf("rail_rank after bring-back = %v, want >= %v", got.RailRank, start)
	}
}
