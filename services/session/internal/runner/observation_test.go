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

// A fake-provider session whose scripted tool writes a file yields the
// correct touched-file set through the session's own read path (spec §5).
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
	if files[0].Change != "added" {
		t.Errorf("change = %q, want added", files[0].Change)
	}
	if files[0].Origin != contracts.OriginToolUse {
		t.Errorf("origin = %q, want %q", files[0].Origin, contracts.OriginToolUse)
	}

	if _, err := os.Stat(filepath.Join(r.Cwd, "foo.txt")); err != nil {
		t.Errorf("scripted write didn't land on disk: %v", err)
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
}
