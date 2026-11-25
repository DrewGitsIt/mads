package api

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// A packed stash (after pack-refs) still moves the fingerprint when its
// last entry is dropped: the drop edits only packed-refs and the reflog.
func TestStashRefMovesWithAPackedStashDrop(t *testing.T) {
	dir := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git("add", "a.txt")
	git("commit", "-q", "-m", "a")
	gitDir := filepath.Join(dir, ".git")
	if got := stashRef(gitDir); got != "" {
		t.Fatalf("no stash: %q, want empty", got)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("b\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git("stash", "push", "-q")
	pushed := stashRef(gitDir)
	if pushed == "" {
		t.Fatal("after a stash push: empty")
	}
	git("pack-refs", "--all")
	if _, err := os.Stat(filepath.Join(gitDir, "refs", "stash")); err == nil {
		t.Skip("this git keeps refs/stash loose after pack-refs")
	}
	packed := stashRef(gitDir)
	git("stash", "drop", "-q")
	if dropped := stashRef(gitDir); dropped == packed {
		t.Errorf("fingerprint after dropping a packed stash = %q, unchanged", dropped)
	}
}
