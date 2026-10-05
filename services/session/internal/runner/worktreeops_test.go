package runner

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/providers/fake"
	"github.com/drewkidwell/mads/providers/provider"
	"github.com/drewkidwell/mads/services/data/datatest"
)

// gitOut/gitRun mirror services/pipeline/engine/worktree_test.go's helpers —
// same style for setting up and inspecting real git repos in temp dirs.
func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	_ = gitOut(t, dir, args...)
}

func initRepo(t *testing.T, dir string) {
	t.Helper()
	gitRun(t, dir, "init", "-q", "-b", "main", ".")
	// Repo-local identity, not just -c flags on the commit below: Commit
	// and Land commit from production code with the user's own identity,
	// and CI runners carry no global git identity (git then exits 128,
	// "Committer identity unknown"). A worktree shares its parent repo's
	// config, so this covers the session worktree too.
	gitRun(t, dir, "config", "user.email", "t@t")
	gitRun(t, dir, "config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("init\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, dir, "add", ".")
	gitRun(t, dir, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "-m", "init")
}

// newIsolatedRunner starts a Runner wired for worktree isolation and returns
// it alongside its Data Service client, ready for a test to drive Commit
// and Land.
func newIsolatedRunner(t *testing.T, cwd, worktreeRoot string) (*Runner, *dataclient.Client, contracts.Session) {
	t.Helper()
	ctx := context.Background()
	base := datatest.Start(t)
	sess := newSession(t, base)
	data := dataclient.New(base, sess.ID)
	reg := provider.NewRegistry()
	if err := reg.Register(fake.New()); err != nil {
		t.Fatalf("register fake: %v", err)
	}
	r := New(sess.ID, data, reg)
	r.Cwd = cwd
	r.WorktreeRoot = worktreeRoot
	if err := r.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = r.Shutdown(ctx) })
	got, err := data.Session(ctx, sess.ID)
	if err != nil {
		t.Fatalf("load session: %v", err)
	}
	return r, data, got
}

// eventTypes returns the ordered list of event types recorded for the
// session, restricted to want if non-empty.
func eventTypes(t *testing.T, data *dataclient.Client, sessionID string, want ...string) []string {
	t.Helper()
	evs, err := data.Events(context.Background(), 0, sessionID, want)
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	types := make([]string, len(evs))
	for i, ev := range evs {
		types[i] = string(ev.Type)
	}
	return types
}

// Commit and Land through the live runner: the work lands in the user's
// checkout, the folder stays, and the record says nothing is left.
func TestRunnerCommitAndLand(t *testing.T) {
	ctx := context.Background()
	cwd := t.TempDir()
	initRepo(t, cwd)
	r, data, sess := newIsolatedRunner(t, cwd, t.TempDir())
	if sess.WorktreeDir == "" {
		t.Fatalf("session not isolated: %+v", sess)
	}

	writeTestFile(t, filepath.Join(sess.WorktreeDir, "feature.txt"), "f\n")
	r.recordFileChange("feature.txt")
	status, err := r.WorktreeStatus(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(status.Files) != 1 || status.Files[0].Path != "feature.txt" || status.LandBlocked == nil {
		t.Fatalf("status = %+v", status)
	}
	if _, err := r.Commit(ctx, contracts.CommitRequest{Message: "Feature"}); err != nil {
		t.Fatal(err)
	}
	res, err := r.Land(ctx, false)
	if err != nil || res.Outcome != contracts.LandLanded {
		t.Fatalf("land = %+v, %v", res, err)
	}
	if _, err := os.Stat(filepath.Join(cwd, "feature.txt")); err != nil {
		t.Error("work didn't land in the checkout")
	}
	if _, err := os.Stat(sess.WorktreeDir); err != nil {
		t.Error("Land removed the session folder")
	}
	got, err := data.Session(ctx, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.UncommittedCount != 0 || got.UnlandedCount != 0 || got.WorktreeDir != sess.WorktreeDir {
		t.Errorf("record after land = %+v", got)
	}
	if types := eventTypes(t, data, sess.ID, "merge.*"); strings.Join(types, ",") != "merge.attempted,merge.completed" {
		t.Errorf("merge events = %v", types)
	}
}

// Land is refused mid-turn, and a message sent while it runs waits behind
// it, then goes out on its own once it's done.
func TestLandQueuesMessagesAndRefusesMidTurn(t *testing.T) {
	ctx := context.Background()
	cwd := t.TempDir()
	initRepo(t, cwd)
	r, _, sess := newIsolatedRunner(t, cwd, t.TempDir())
	writeTestFile(t, filepath.Join(sess.WorktreeDir, "feature.txt"), "f\n")
	r.recordFileChange("feature.txt")
	if _, err := r.Commit(ctx, contracts.CommitRequest{Message: "Feature"}); err != nil {
		t.Fatal(err)
	}

	r.mu.Lock()
	r.busy = true
	r.mu.Unlock()
	if res, err := r.Land(ctx, false); err != nil || res.Reason != contracts.LandBusy {
		t.Fatalf("land mid-turn = %+v, %v; want refused busy", res, err)
	}
	r.mu.Lock()
	r.busy = false
	r.mu.Unlock()

	ch, unsubscribe := r.Subscribe()
	defer unsubscribe()
	var queued *contracts.QueuedMessage
	landHook = func() {
		var err error
		if queued, err = r.SendOrQueue(ctx, "echo after land"); err != nil {
			t.Errorf("send during land: %v", err)
		}
		if err := r.Send(ctx, "echo direct"); !errors.Is(err, ErrTurnInFlight) {
			t.Errorf("plain send during land = %v, want ErrTurnInFlight", err)
		}
	}
	t.Cleanup(func() { landHook = nil })
	res, err := r.Land(ctx, false)
	if err != nil || res.Outcome != contracts.LandLanded {
		t.Fatalf("land = %+v, %v", res, err)
	}
	if queued == nil {
		t.Fatal("a message sent during Land was not queued")
	}
	if got := drainText(t, ch); got != "you said after land" {
		t.Errorf("delivered after land = %q", got)
	}
	if q := r.QueuedMessages(); len(q) != 0 {
		t.Errorf("queue after delivery = %+v", q)
	}
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
