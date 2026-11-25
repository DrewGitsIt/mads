package gitroot

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFindWalksPastMissingDirs(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	for _, d := range []string{".git/objects", ".git/refs"} {
		if err := os.MkdirAll(filepath.Join(repo, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(repo, ".git", "HEAD"), []byte("ref: refs/heads/main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, sure := Find(filepath.Join(repo, "gone", "deeper")); got != repo || !sure {
		t.Errorf("Find(deleted) = %q, %v; want %q, sure", got, sure, repo)
	}
	if got, sure := Find(filepath.Join(root, "elsewhere")); got != "" || !sure {
		t.Errorf("Find(outside) = %q, %v; want \"\", sure", got, sure)
	}
	// An empty .git dir isn't a repo: the walk can't promise git's answer.
	odd := filepath.Join(root, "odd")
	if err := os.MkdirAll(filepath.Join(odd, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got, sure := Find(odd); got != odd || sure {
		t.Errorf("Find(empty .git) = %q, %v; want %q, unsure", got, sure, odd)
	}
}

func TestResolveKeepsTheMissingTail(t *testing.T) {
	root := t.TempDir()
	real, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if got, ok := Resolve(filepath.Join(link, "a", "b")); !ok || got != filepath.Join(real, "a", "b") {
		t.Errorf("Resolve = %q, %v; want %q", got, ok, filepath.Join(real, "a", "b"))
	}
}
