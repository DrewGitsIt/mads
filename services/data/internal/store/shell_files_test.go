package store

import (
	"context"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
)

// A shell command's change is the session's own tool use — attributed even
// working in place — unless another session's tools recorded the same path
// inside the change's window, in which case it's left to them.
func TestRecordShellFileChanges(t *testing.T) {
	s, tasks, _ := newSessionStore(t)
	ctx := context.Background()
	me := mkSession(t, s, contracts.Session{Isolation: contracts.IsolationCurrentDir})
	other := mkSession(t, s, contracts.Session{Isolation: contracts.IsolationCurrentDir})
	task := mkTask(t, tasks, contracts.Task{Title: "T", SessionID: other.ID})

	t0 := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	record := func(sessionID, taskID, path string, at time.Time) {
		t.Helper()
		// A local offset, as the runner writes, against UTC windows: the
		// comparison has to be on times, not on the stored strings.
		if err := s.RecordFileChange(ctx, &contracts.FileChange{
			SessionID: sessionID, TaskID: taskID, Path: path, Change: "modified",
			Origin: contracts.OriginToolUse, Timestamp: at.In(time.FixedZone("MDT", -6*3600)),
		}, sessionID); err != nil {
			t.Fatal(err)
		}
	}
	record(other.ID, "", "/repo/theirs.go", t0.Add(2*time.Second))    // inside the window
	record(other.ID, "", "/repo/earlier.go", t0.Add(-time.Minute))    // before it
	record(other.ID, task.ID, "/repo/task.go", t0.Add(2*time.Second)) // a task worktree's row
	record(me.ID, "", "/repo/mine-too.go", t0.Add(2*time.Second))     // my own tool edit

	change := func(path string) contracts.ShellFileChange {
		return contracts.ShellFileChange{
			Repo: "/repo", Path: path, Change: "modified",
			Timestamp: t0.Add(3 * time.Second), Since: t0, Until: t0.Add(5 * time.Second),
		}
	}
	res, err := s.RecordShellFileChanges(ctx, me.ID, []contracts.ShellFileChange{
		change("/repo/theirs.go"), change("/repo/earlier.go"), change("/repo/task.go"),
		change("/repo/mine-too.go"), change("/repo/fresh.go"),
	}, me.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Contested) != 1 || res.Contested[0] != "/repo/theirs.go" {
		t.Errorf("contested = %v, want only /repo/theirs.go", res.Contested)
	}
	var got []string
	for _, fc := range res.Recorded {
		got = append(got, fc.Path)
		if fc.Origin != contracts.OriginToolUse || !fc.Attributed {
			t.Errorf("%s: origin %q attributed %v, want attributed tool_use", fc.Path, fc.Origin, fc.Attributed)
		}
		if !fc.Timestamp.Equal(t0.Add(3 * time.Second)) {
			t.Errorf("%s: timestamp %v, want the change's own time", fc.Path, fc.Timestamp)
		}
	}
	want := []string{"/repo/earlier.go", "/repo/task.go", "/repo/mine-too.go", "/repo/fresh.go"}
	if len(got) != len(want) {
		t.Fatalf("recorded %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("recorded %v, want %v", got, want)
		}
	}

	rows, err := s.FileChanges(ctx, me.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1+len(want) {
		t.Errorf("ledger has %d rows, want %d", len(rows), 1+len(want))
	}
}

func TestRecordShellFileChangesValidates(t *testing.T) {
	s, _, _ := newSessionStore(t)
	me := mkSession(t, s, contracts.Session{Isolation: contracts.IsolationCurrentDir})
	_, err := s.RecordShellFileChanges(context.Background(), me.ID, []contracts.ShellFileChange{
		{Path: "/repo/a.go", Change: "renamed"},
	}, me.ID)
	if err == nil {
		t.Fatal("an unknown change kind was accepted")
	}
	rows, _ := s.FileChanges(context.Background(), me.ID)
	if len(rows) != 0 {
		t.Errorf("a refused batch still wrote %d rows", len(rows))
	}
}
