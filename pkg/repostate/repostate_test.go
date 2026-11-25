package repostate

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
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
	if w.Main || w.Branch != "feature" || !w.NewBranch || w.Ahead != 2 || w.GitDir != filepath.Join(repo, ".git") ||
		w.Base != "main" || w.BaseSource != "default" || w.ToLand != 1 || w.Fork == "" {
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

// A panicking callback is logged, not fatal, and later changes are still
// reported.
func TestWatcherSurvivesAPanickingCallback(t *testing.T) {
	repo, _ := fixture(t)
	gitDir := filepath.Join(repo, ".git")
	r := &reports{n: map[string]int{}}
	var once sync.Once
	w := NewWatcher(func(g string) {
		r.add(g)
		once.Do(func() { panic("boom") })
	}, Options{Quiet: 100 * time.Millisecond, MaxWait: time.Second, Poll: time.Hour, Safety: time.Hour})
	t.Cleanup(w.Close)
	w.Set([]Spec{{GitDir: gitDir}})

	commit(t, repo, "one.txt")
	r.waitFor(t, gitDir, 1, "the change whose callback panics")
	commit(t, repo, "two.txt")
	r.waitFor(t, gitDir, 2, "the next change")
}

// TestCacheSharedComputeOutlivesOneCaller: two callers wait on one compute;
// the first cancelling returns it at once without failing the second, and
// only when the last waiter leaves is the compute's context cancelled.
func TestCacheSharedComputeOutlivesOneCaller(t *testing.T) {
	repo, _ := fixture(t)
	c := NewCache()
	release := make(chan struct{})
	started := make(chan context.Context, 4)
	var calls sync.WaitGroup
	c.computeFn = func(ctx context.Context, top, _ string) (contracts.RepoWorktree, error) {
		started <- ctx
		select {
		case <-release:
			return contracts.RepoWorktree{Path: top, Branch: "main"}, nil
		case <-ctx.Done():
			return contracts.RepoWorktree{}, ctx.Err()
		}
	}

	ctxA, cancelA := context.WithCancel(context.Background())
	type answer struct {
		w   contracts.RepoWorktree
		err error
	}
	a, b := make(chan answer, 1), make(chan answer, 1)
	calls.Add(2)
	go func() { defer calls.Done(); w, err := c.Get(ctxA, repo); a <- answer{w, err} }()
	computeCtx := <-started
	go func() { defer calls.Done(); w, err := c.Get(context.Background(), repo); b <- answer{w, err} }()
	waitWaiters(t, c, 2)

	cancelA()
	if got := <-a; !errors.Is(got.err, context.Canceled) {
		t.Fatalf("cancelled caller = %+v, want context.Canceled", got)
	}
	if computeCtx.Err() != nil {
		t.Fatal("one caller's cancel cancelled the shared compute")
	}
	close(release)
	if got := <-b; got.err != nil || got.w.Branch != "main" {
		t.Fatalf("other caller = %+v, want the compute's answer", got)
	}
	calls.Wait()
	if len(started) != 0 {
		t.Fatalf("%d extra computes, want one shared", len(started))
	}
	// And it was cached for the next ask.
	if w, err := c.Get(context.Background(), repo); err != nil || w.Branch != "main" || len(started) != 0 {
		t.Fatalf("after = %+v, %v; want a cache hit", w, err)
	}
}

// TestCacheLastCallerCancelStopsCompute: when every waiter has gone, the
// compute is cancelled (its git commands die with it) and nothing is cached.
func TestCacheLastCallerCancelStopsCompute(t *testing.T) {
	repo, _ := fixture(t)
	c := NewCache()
	started := make(chan context.Context, 4)
	c.computeFn = func(ctx context.Context, _ string, _ string) (contracts.RepoWorktree, error) {
		started <- ctx
		<-ctx.Done()
		return contracts.RepoWorktree{}, ctx.Err()
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := c.Get(ctx, repo); done <- err }()
	computeCtx := <-started
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	select {
	case <-computeCtx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("compute still running after its last waiter left")
	}
	// A later ask starts a fresh compute rather than joining the dead one.
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	go func() { _, _ = c.Get(ctx2, repo) }()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("no fresh compute after the cancelled one")
	}
}

func waitWaiters(t *testing.T, c *Cache, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		c.mu.Lock()
		got := 0
		for _, cl := range c.computing {
			got += cl.waiters
		}
		c.mu.Unlock()
		if got == n {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("waiters never reached %d", n)
}

// TestCacheHitSpawnsNoGit: the key is the checkout's top, found by walking
// the filesystem (sessionwt.Toplevel), so a hit costs no git process; it
// used to cost a `git rev-parse --show-toplevel` per ask.
func TestCacheHitSpawnsNoGit(t *testing.T) {
	repo, _ := fixture(t)
	c := NewCache()
	if _, err := c.Get(context.Background(), filepath.Join(repo)); err != nil {
		t.Fatal(err)
	}
	real, err := exec.LookPath("git")
	if err != nil {
		t.Skip("no git")
	}
	bin := t.TempDir()
	log := filepath.Join(bin, "spawns.log")
	script := "#!/bin/sh\necho \"$*\" >> '" + log + "'\nexec '" + real + "' \"$@\"\n"
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	for range 20 {
		if _, err := c.Get(context.Background(), repo); err != nil {
			t.Fatal(err)
		}
	}
	if raw, _ := os.ReadFile(log); len(raw) != 0 {
		t.Errorf("cache hits spawned git:\n%s", raw)
	}
}
