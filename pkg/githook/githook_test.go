package githook

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func run(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// From a linked worktree git reports the common dir as an absolute path.
// The hook must land in the shared hooks dir, and nothing may appear inside
// the worktree — a stray file there reads as uncommitted session work.
func TestEnsureInstalledFromLinkedWorktree(t *testing.T) {
	ctx := context.Background()
	primary := t.TempDir()
	run(t, primary, "init", "-q", "-b", "main", ".")
	run(t, primary, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "--allow-empty", "-m", "init")
	wt := filepath.Join(t.TempDir(), "wt")
	run(t, primary, "worktree", "add", "-q", "-b", "side", wt)

	if err := EnsureInstalled(ctx, wt); err != nil {
		t.Fatalf("EnsureInstalled: %v", err)
	}
	if _, err := os.Stat(filepath.Join(primary, ".git", "hooks", "post-commit")); err != nil {
		t.Errorf("hook not in the shared hooks dir: %v", err)
	}
	out, err := exec.Command("git", "-C", wt, "status", "--porcelain", "--untracked-files=all").Output()
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 0 {
		t.Errorf("worktree is dirty after install:\n%s", out)
	}
}

// From the primary checkout the common dir is relative (".git") — still works.
func TestEnsureInstalledFromPrimary(t *testing.T) {
	primary := t.TempDir()
	run(t, primary, "init", "-q", "-b", "main", ".")
	if err := EnsureInstalled(context.Background(), primary); err != nil {
		t.Fatalf("EnsureInstalled: %v", err)
	}
	if _, err := os.Stat(filepath.Join(primary, ".git", "hooks", "post-commit")); err != nil {
		t.Errorf("hook missing: %v", err)
	}
}
