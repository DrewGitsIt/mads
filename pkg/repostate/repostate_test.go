package repostate

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func run(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.email=t@t", "-c", "user.name=t"}, args...)...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func commit(t *testing.T, dir, name string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(name+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, dir, "add", name)
	run(t, dir, "commit", "-q", "-m", "add "+name)
}

// fixture is a repo on main pushed to a bare origin, plus another clone of
// origin standing in for someone else.
func fixture(t *testing.T) (repo, other string) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	bare, repo, other := filepath.Join(root, "origin.git"), filepath.Join(root, "repo"), filepath.Join(root, "other")
	run(t, root, "init", "-q", "--bare", "-b", "main", bare)
	if err := os.Mkdir(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	run(t, repo, "init", "-q", "-b", "main", ".")
	commit(t, repo, "README.md")
	run(t, repo, "remote", "add", "origin", bare)
	run(t, repo, "push", "-q", "-u", "origin", "main")
	run(t, root, "clone", "-q", bare, other)
	return repo, other
}

func TestCompute(t *testing.T) {
	ctx := context.Background()
	repo, other := fixture(t)
	commit(t, repo, "a.txt")
	commit(t, other, "b.txt")
	run(t, other, "push", "-q", "origin", "main")
	run(t, repo, "fetch", "-q", "origin")

	w, err := Compute(ctx, filepath.Join(repo))
	if err != nil {
		t.Fatal(err)
	}
	if !w.Main || w.Branch != "main" || w.Remote != "origin" || w.RemoteBranch != "main" || !w.Upstream ||
		w.Ahead != 1 || w.Behind != 1 || w.NewBranch || w.Head != run(t, repo, "rev-parse", "HEAD") {
		t.Errorf("main checkout = %+v", w)
	}

	wt := filepath.Join(filepath.Dir(repo), "wt")
	run(t, repo, "worktree", "add", "-q", "-b", "feature", wt)
	commit(t, wt, "f.txt")
	w, err = Compute(ctx, wt)
	if err != nil {
		t.Fatal(err)
	}
	if w.Main || w.Branch != "feature" || !w.NewBranch || w.Ahead != 2 || w.GitDir != filepath.Join(repo, ".git") {
		// Ahead 2: a.txt (not on origin) and f.txt, against origin's main.
		t.Errorf("linked worktree = %+v", w)
	}

	run(t, wt, "checkout", "-q", "--detach")
	if w, err = Compute(ctx, wt); err != nil || !w.Detached || w.Branch != "" {
		t.Errorf("detached = %+v, %v", w, err)
	}
}

func TestCacheInvalidate(t *testing.T) {
	ctx := context.Background()
	repo, _ := fixture(t)
	c := NewCache()
	first, err := c.Get(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	commit(t, repo, "a.txt")
	if again, _ := c.Get(ctx, repo); again.Ahead != 0 || !again.ComputedAt.Equal(first.ComputedAt) {
		t.Errorf("cached = %+v, want the first answer until invalidated", again)
	}
	c.Invalidate(first.GitDir)
	if fresh, _ := c.Get(ctx, repo); fresh.Ahead != 1 {
		t.Errorf("after invalidate = %+v, want ahead 1", fresh)
	}
}

// reports counts onChange calls per git dir.
type reports struct {
	mu sync.Mutex
	n  map[string]int
}

func (r *reports) add(gitDir string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.n[gitDir]++
}

func (r *reports) get(gitDir string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.n[gitDir]
}

// waitFor waits until the count reaches want, then a little longer to see
// it doesn't overshoot.
func (r *reports) waitFor(t *testing.T, gitDir string, want int, what string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for r.get(gitDir) < want && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	time.Sleep(300 * time.Millisecond)
	if got := r.get(gitDir); got != want {
		t.Fatalf("%s: %d reports, want %d", what, got, want)
	}
}

func TestWatcher(t *testing.T) {
	repo, other := fixture(t)
	gitDir := filepath.Join(repo, ".git")
	wt := filepath.Join(filepath.Dir(repo), "wt")
	run(t, repo, "worktree", "add", "-q", "-b", "feature", wt)
	wtGitDir := filepath.Join(gitDir, "worktrees", "wt")

	r := &reports{n: map[string]int{}}
	w := NewWatcher(r.add, Options{Quiet: 100 * time.Millisecond, MaxWait: time.Second, Poll: time.Hour, Safety: time.Hour})
	t.Cleanup(w.Close)
	w.Set([]Spec{{GitDir: gitDir, Worktrees: []string{wtGitDir}}})

	commit(t, repo, "one.txt")
	r.waitFor(t, gitDir, 1, "one commit")

	for _, n := range []string{"a.txt", "b.txt", "c.txt"} {
		commit(t, repo, n)
	}
	r.waitFor(t, gitDir, 2, "three quick commits")

	// The index changes, no branch moves.
	if err := os.WriteFile(filepath.Join(repo, "loose.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, repo, "add", "loose.txt")
	run(t, repo, "status", "--short")
	r.waitFor(t, gitDir, 2, "git add and status")
	run(t, repo, "reset", "-q", "loose.txt")
	r.waitFor(t, gitDir, 2, "unstaging")

	commit(t, other, "theirs.txt")
	run(t, other, "push", "-q", "origin", "main")
	run(t, repo, "fetch", "-q", "origin")
	r.waitFor(t, gitDir, 3, "a fetch moving origin/main")

	run(t, repo, "branch", "fix/deep/branch")
	r.waitFor(t, gitDir, 4, "a branch in new folders")
	run(t, repo, "branch", "-f", "fix/deep/branch", "HEAD~1")
	r.waitFor(t, gitDir, 5, "moving a branch in a folder watched since")

	run(t, wt, "checkout", "-q", "-b", "other")
	r.waitFor(t, gitDir, 6, "a linked worktree switching branch")

	w.Set(nil)
	commit(t, repo, "unwatched.txt")
	r.waitFor(t, gitDir, 6, "after the repo is dropped")
}

func TestWatcherPollsWithoutFileEvents(t *testing.T) {
	repo, _ := fixture(t)
	gitDir := filepath.Join(repo, ".git")
	r := &reports{n: map[string]int{}}
	// As if the file watch had failed: the poll alone.
	w := NewWatcher(r.add, Options{Poll: 100 * time.Millisecond, Safety: time.Hour, pollOnly: true})
	t.Cleanup(w.Close)
	w.Set([]Spec{{GitDir: gitDir}})

	time.Sleep(300 * time.Millisecond)
	if n := r.get(gitDir); n != 0 {
		t.Fatalf("%d reports with nothing changed, want 0", n)
	}
	commit(t, repo, "a.txt")
	r.waitFor(t, gitDir, 1, "a commit seen by the poll")
}

// The service watches what's listed and what's asked about, clears a
// repo's cached facts when it changes, and announces it once.
func TestService(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	listed, _ := fixture(t)
	asked, _ := fixture(t)
	r := &reports{n: map[string]int{}}
	s := newService(ctx, func(context.Context) ([]string, error) { return []string{listed}, nil }, r.add,
		Options{Quiet: 100 * time.Millisecond, MaxWait: time.Second, Poll: time.Hour, Safety: time.Hour}, 50*time.Millisecond)

	before, err := s.Worktree(ctx, listed)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond) // the first reconcile has watched it
	commit(t, listed, "a.txt")
	r.waitFor(t, filepath.Join(listed, ".git"), 1, "a listed repo's commit")
	if after, _ := s.Worktree(ctx, listed); after.Ahead != before.Ahead+1 {
		t.Errorf("ahead after the change = %d, want %d", after.Ahead, before.Ahead+1)
	}

	if _, err := s.Worktree(ctx, asked); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	commit(t, asked, "b.txt")
	r.waitFor(t, filepath.Join(asked, ".git"), 1, "a repo a view asked about")
	if RepoOf(filepath.Join(asked, ".git")) != asked {
		t.Error("RepoOf didn't give the main checkout")
	}
}
