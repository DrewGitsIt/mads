package runner

import (
	"context"
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
	// Repo-local identity, not just -c flags on the commit below: Merge's
	// own `git merge` writes a merge commit from production code that has
	// no -c flags of its own, and CI runners carry no global git identity
	// (git then exits 128, "Committer identity unknown"). A worktree shares
	// its parent repo's config, so this covers the session worktree too.
	gitRun(t, dir, "config", "user.email", "t@t")
	gitRun(t, dir, "config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("init\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, dir, "add", ".")
	gitRun(t, dir, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "-m", "init")
}

// newIsolatedRunner starts a Runner wired for worktree isolation and returns
// it alongside its Data Service client, ready for a test to drive Merge.
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

// The first acceptance criterion: a clean merge round-trips against a real
// git repo — work lands on the trunk, the worktree and branch are cleaned
// up, and merge.attempted/merge.completed land in the log.
func TestMergeCleanRoundTrip(t *testing.T) {
	ctx := context.Background()
	cwd := t.TempDir()
	initRepo(t, cwd)
	worktreeRoot := t.TempDir()

	r, data, sess := newIsolatedRunner(t, cwd, worktreeRoot)

	if sess.WorktreeDir == "" || sess.WorktreeBranch == "" {
		t.Fatalf("session missing worktree fields after start: %+v", sess)
	}
	wantBranch := "mads/session/" + sess.ID
	if sess.WorktreeBranch != wantBranch {
		t.Errorf("branch = %q, want %q", sess.WorktreeBranch, wantBranch)
	}
	if _, err := os.Stat(sess.WorktreeDir); err != nil {
		t.Fatalf("worktree dir missing: %v", err)
	}

	// Simulate uncommitted agent work in the worktree — commitStray must
	// pick this up at merge time.
	if err := os.WriteFile(filepath.Join(sess.WorktreeDir, "work.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	result, err := r.Merge(ctx, "")
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	if result.Conflicted {
		t.Fatalf("unexpected conflict: %+v", result)
	}

	// Work landed on the trunk.
	if _, err := os.Stat(filepath.Join(cwd, "work.txt")); err != nil {
		t.Errorf("work.txt missing from merged trunk: %v", err)
	}
	if status := gitOut(t, cwd, "status", "--porcelain"); strings.TrimSpace(status) != "" {
		t.Errorf("trunk dirty after merge:\n%s", status)
	}

	// Worktree and branch cleaned up.
	if wt := gitOut(t, cwd, "worktree", "list"); strings.Contains(wt, "mads/session") {
		t.Errorf("worktree survived cleanup:\n%s", wt)
	}
	if branches := gitOut(t, cwd, "branch", "--list", "mads/session/*"); strings.TrimSpace(branches) != "" {
		t.Errorf("session branch survived cleanup:\n%s", branches)
	}

	// Session record cleared.
	after, err := data.Session(ctx, sess.ID)
	if err != nil {
		t.Fatalf("reload session: %v", err)
	}
	if after.WorktreeDir != "" || after.WorktreeBranch != "" {
		t.Errorf("session still flagged unmerged: %+v", after)
	}

	got := eventTypes(t, data, sess.ID, "merge.attempted", "merge.conflicted", "merge.completed")
	want := []string{"merge.attempted", "merge.completed"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("events = %v, want %v", got, want)
	}
}

// The second acceptance criterion: a conflicted merge aborts cleanly (trunk
// stays clean), returns structured conflict data naming the files, keeps
// the branch, and emits merge.conflicted.
func TestMergeConflicted(t *testing.T) {
	ctx := context.Background()
	cwd := t.TempDir()
	initRepo(t, cwd)
	if err := os.WriteFile(filepath.Join(cwd, "shared.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, cwd, "add", "shared.txt")
	gitRun(t, cwd, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "-m", "add shared")
	worktreeRoot := t.TempDir()

	r, data, sess := newIsolatedRunner(t, cwd, worktreeRoot)

	// The session's worktree diverges from the trunk on the same line.
	if err := os.WriteFile(filepath.Join(sess.WorktreeDir, "shared.txt"), []byte("worktree version\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// The trunk moves on independently, so the merge collides.
	if err := os.WriteFile(filepath.Join(cwd, "shared.txt"), []byte("trunk version\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, cwd, "add", "shared.txt")
	gitRun(t, cwd, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "-m", "diverge trunk")
	trunkHead := strings.TrimSpace(gitOut(t, cwd, "rev-parse", "HEAD"))

	result, err := r.Merge(ctx, "")
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	if !result.Conflicted {
		t.Fatalf("expected conflict, got: %+v", result)
	}
	if result.Branch != sess.WorktreeBranch {
		t.Errorf("result branch = %q, want %q", result.Branch, sess.WorktreeBranch)
	}
	if len(result.Files) != 1 || result.Files[0] != "shared.txt" {
		t.Errorf("result files = %v, want [shared.txt]", result.Files)
	}

	// The abort left the trunk clean, HEAD unchanged, holding its own version.
	if status := gitOut(t, cwd, "status", "--porcelain"); strings.TrimSpace(status) != "" {
		t.Errorf("trunk dirty after aborted merge:\n%s", status)
	}
	if head := strings.TrimSpace(gitOut(t, cwd, "rev-parse", "HEAD")); head != trunkHead {
		t.Errorf("trunk HEAD moved: %s, want %s", head, trunkHead)
	}
	raw, err := os.ReadFile(filepath.Join(cwd, "shared.txt"))
	if err != nil || string(raw) != "trunk version\n" {
		t.Errorf("trunk shared.txt: %q, %v", raw, err)
	}

	// The worktree and branch survive for manual merge-back.
	if _, err := os.Stat(sess.WorktreeDir); err != nil {
		t.Errorf("worktree dir gone: %v", err)
	}
	if branches := gitOut(t, cwd, "branch", "--list", "mads/session/*"); !strings.Contains(branches, sess.WorktreeBranch) {
		t.Errorf("session branch is gone:\n%s", branches)
	}

	after, err := data.Session(ctx, sess.ID)
	if err != nil {
		t.Fatalf("reload session: %v", err)
	}
	if after.WorktreeDir == "" || after.WorktreeBranch == "" {
		t.Errorf("session lost its unmerged flag: %+v", after)
	}

	got := eventTypes(t, data, sess.ID, "merge.attempted", "merge.conflicted", "merge.completed")
	want := []string{"merge.attempted", "merge.conflicted"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("events = %v, want %v", got, want)
	}
}

// A session whose Cwd isn't a git repo must start normally with isolation
// silently skipped, not fail — worktree isolation is git-repo-only (spec
// §17), never a hard requirement for a plain document folder.
func TestMergeSkipsIsolationOnNonGitCwd(t *testing.T) {
	cwd := t.TempDir() // no git init
	worktreeRoot := t.TempDir()

	_, _, sess := newIsolatedRunner(t, cwd, worktreeRoot)

	if sess.WorktreeDir != "" || sess.WorktreeBranch != "" {
		t.Errorf("non-git cwd should not get worktree fields: %+v", sess)
	}
}
