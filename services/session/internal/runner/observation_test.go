package runner

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/providers/fake"
	"github.com/drewkidwell/mads/providers/provider"
	"github.com/drewkidwell/mads/services/data/datatest"
)

// A fake-provider session whose scripted tools write one file and read
// another yields exactly the written file through the session's own read
// path (spec §5) — a read-only tool use carries a Path too, and recording
// it as a change is what once inflated a session's file count 40%.
func TestFilesTracksToolUsePaths(t *testing.T) {
	ctx := context.Background()
	base := datatest.Start(t)
	sess := newSession(t, base)
	data := dataclient.New(base, sess.ID)

	f, err := fake.NewWithScenario(fake.Scenario{
		Name: "writes",
		Rules: []fake.Rule{
			{Match: "write it", Reply: "done", Tools: []fake.ToolStep{
				{Name: "write_file", Path: "foo.txt", Content: "hello"},
				// Path but no Content: the fake's read shape (executeTool
				// skips it) — must not appear in the touched-file set.
				{Name: "read_file", Path: "bar.txt"},
			}},
		},
	})
	if err != nil {
		t.Fatalf("scenario: %v", err)
	}
	reg := provider.NewRegistry()
	if err := reg.Register(f); err != nil {
		t.Fatalf("register fake: %v", err)
	}

	r := New(sess.ID, data, reg)
	r.Cwd = t.TempDir()
	if err := r.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = r.Shutdown(ctx) })

	ch, unsubscribe := r.Subscribe()
	defer unsubscribe()

	if err := r.Send(ctx, "write it"); err != nil {
		t.Fatalf("send: %v", err)
	}
	drainText(t, ch)

	files, err := r.Files(ctx)
	if err != nil {
		t.Fatalf("files: %v", err)
	}
	if len(files) != 1 || files[0].Path != "foo.txt" {
		t.Fatalf("touched files = %+v, want exactly [foo.txt]", files)
	}
	// "modified", not "added": the fake executes each scripted tool before
	// emitting its tool_use event, so the created file already exists when
	// recordFileChange stats it — the documented post-execution limitation.
	// Providers that announce tool_use pre-execution (claudecode) classify
	// a created file "added"; TestFileChangeClassification covers that
	// ordering directly.
	if files[0].Change != "modified" {
		t.Errorf("change = %q, want modified", files[0].Change)
	}
	if files[0].Origin != contracts.OriginToolUse {
		t.Errorf("origin = %q, want %q", files[0].Origin, contracts.OriginToolUse)
	}
	if files[0].Repo != r.Cwd {
		t.Errorf("repo = %q, want the session cwd %q", files[0].Repo, r.Cwd)
	}

	if _, err := os.Stat(filepath.Join(r.Cwd, "foo.txt")); err != nil {
		t.Errorf("scripted write didn't land on disk: %v", err)
	}
}

// Classification reflects reality, not sighting order (spec §5): a
// pre-existing file's first touch is "modified", only a path absent at
// observation time is "added" (later touches stay "modified" — the UI's
// collapseChanges reads first=="added" as created-in-session), and a
// touched path gone by turn end is swept as "deleted". Drives
// recordFileChange/sweepDeletedFiles directly to control the
// observation-vs-execution ordering the fake provider can't (it executes
// before emitting).
func TestFileChangeClassification(t *testing.T) {
	ctx := context.Background()
	base := datatest.Start(t)
	sess := newSession(t, base)
	data := dataclient.New(base, sess.ID)

	r := New(sess.ID, data, provider.NewRegistry())
	dir := t.TempDir()
	r.Cwd = dir

	if err := os.WriteFile(filepath.Join(dir, "pre.txt"), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}

	r.recordFileChange("pre.txt") // existed before the session touched it
	r.recordFileChange("new.txt") // absent at observation: the tool is about to create it
	if err := os.WriteFile(filepath.Join(dir, "new.txt"), []byte("n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r.recordFileChange("new.txt") // later touch of a created file
	if err := os.Remove(filepath.Join(dir, "pre.txt")); err != nil {
		t.Fatal(err)
	}
	r.sweepDeletedFiles() // turn end: pre.txt is gone

	files, err := r.Files(ctx)
	if err != nil {
		t.Fatalf("files: %v", err)
	}
	want := []struct{ path, change string }{
		{"pre.txt", "modified"},
		{"new.txt", "added"},
		{"new.txt", "modified"},
		{"pre.txt", "deleted"},
	}
	if len(files) != len(want) {
		t.Fatalf("ledger = %+v, want %d rows", files, len(want))
	}
	for i, w := range want {
		if files[i].Path != w.path || files[i].Change != w.change {
			t.Errorf("row %d = %s %q, want %s %q", i, files[i].Path, files[i].Change, w.path, w.change)
		}
	}

	// A second sweep must not repeat the deletion; a recreated path reads
	// as created again rather than inheriting the pre-deletion verdict.
	r.sweepDeletedFiles()
	r.recordFileChange("pre.txt")
	files, err = r.Files(ctx)
	if err != nil {
		t.Fatalf("files: %v", err)
	}
	if len(files) != len(want)+1 {
		t.Fatalf("ledger after resweep+retouch = %+v, want %d rows", files, len(want)+1)
	}
	if last := files[len(files)-1]; last.Path != "pre.txt" || last.Change != "added" {
		t.Errorf("retouch after deletion = %s %q, want pre.txt added", last.Path, last.Change)
	}
}

// A commit made in the session's worktree is reported by the installed
// post-commit hook to the Data Service and appears in the session's commit
// list, linked via the Task trailer (spec §5) — the same reporting contract
// the pipeline engine uses (services/pipeline/engine/worktree_test.go).
func TestCommitInSessionWorktreeReportsLinkage(t *testing.T) {
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("no curl on PATH (the hook depends on it)")
	}
	ctx := context.Background()
	base := datatest.Start(t)
	sess := newSession(t, base)
	data := dataclient.New(base, sess.ID)

	task := contracts.Task{SessionID: sess.ID, Title: "hooked", Source: "user"}
	if err := data.CreateTask(ctx, &task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	dir := t.TempDir()
	gitRun(t, dir, "init", "-q")

	reg := provider.NewRegistry()
	if err := reg.Register(fake.New()); err != nil {
		t.Fatalf("register fake: %v", err)
	}
	r := New(sess.ID, data, reg)
	r.Cwd = dir
	if err := r.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = r.Shutdown(ctx) })

	if err := os.WriteFile(filepath.Join(dir, "work.txt"), []byte("w"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "-m",
		"do the work\n\nTask: "+task.ID)

	var commits []contracts.Commit
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var err error
		commits, err = r.Commits(ctx)
		if err == nil && len(commits) > 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if len(commits) != 1 {
		t.Fatalf("hook never reported the commit, got %+v", commits)
	}
	got := commits[0]
	if got.SessionID != sess.ID {
		t.Errorf("session_id = %q, want %q", got.SessionID, sess.ID)
	}
	if got.TaskID != task.ID || got.LinkedVia != contracts.LinkedViaTrailer {
		t.Errorf("linkage: %+v", got)
	}
	if got.Message != "do the work" {
		t.Errorf("message: %q", got.Message)
	}
	wantRepo, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	gotRepo, err := filepath.EvalSymlinks(got.Repo)
	if err != nil {
		t.Fatalf("Repo %q does not resolve: %v", got.Repo, err)
	}
	if gotRepo != wantRepo {
		t.Errorf("Repo = %q (resolved %q), want %q", got.Repo, gotRepo, wantRepo)
	}
}
