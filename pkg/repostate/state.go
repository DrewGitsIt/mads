// Package repostate keeps facts about a repo's branches — what each
// checkout is on, what a push would publish, what the remote has that the
// branch doesn't — once per repo, current, for every session working there
// (docs/COMMIT-LAND-PUSH-DESIGN.md D10). The facts are computed on demand
// and cached; a watch on the git files that record where branches point
// (Watcher) clears a repo's entries the moment one moves, from any tool.
package repostate

import (
	"context"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/sessionwt"
)

// GitDir is the absolute shared git dir of the repo holding path (the same
// for its main checkout and every linked worktree).
func GitDir(ctx context.Context, path string) (string, error) {
	out, err := sessionwt.Git(ctx, path, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return "", err
	}
	return filepath.Clean(strings.TrimSpace(out)), nil
}

// worktreeGitDir is the checkout's own git dir: the shared one for the main
// checkout, <shared>/worktrees/<name> for a linked worktree.
func worktreeGitDir(ctx context.Context, path string) (string, error) {
	out, err := sessionwt.Git(ctx, path, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return "", err
	}
	return filepath.Clean(strings.TrimSpace(out)), nil
}

// Compute reads the branch facts of the checkout holding path. Offline:
// the remote side is as of the last fetch.
func Compute(ctx context.Context, path string) (contracts.RepoWorktree, error) {
	top, err := sessionwt.Toplevel(ctx, path)
	if err != nil {
		return contracts.RepoWorktree{}, err
	}
	gitDir, err := GitDir(ctx, top)
	if err != nil {
		return contracts.RepoWorktree{}, err
	}
	w := contracts.RepoWorktree{Path: top, GitDir: gitDir, ComputedAt: time.Now().UTC()}
	if own, err := worktreeGitDir(ctx, top); err == nil {
		w.Main = own == gitDir
	}
	if head, err := sessionwt.Git(ctx, top, "rev-parse", "-q", "--verify", "HEAD"); err == nil {
		w.Head = strings.TrimSpace(head)
	}
	branch, err := sessionwt.CurrentBranch(ctx, top)
	if err != nil {
		w.Detached = true
		return w, nil
	}
	if err := branchFacts(ctx, top, branch, &w); err != nil {
		return contracts.RepoWorktree{}, err
	}
	if !w.Main {
		if b, ok := sessionwt.BaseOf(ctx, top, branch); ok {
			w.Base, w.BaseSource, w.Fork = b.Base, b.Source, b.Fork
			if w.ToLand, err = sessionwt.CountToLand(ctx, top, b.Base, branch); err != nil {
				return contracts.RepoWorktree{}, err
			}
		}
	}
	return w, nil
}

// ComputeBranch is Compute for branch in the repo holding path, whether or
// not that checkout has it checked out — an isolated session's base in the
// user's checkout, which a push after Land publishes.
func ComputeBranch(ctx context.Context, path, branch string) (contracts.RepoWorktree, error) {
	top, err := sessionwt.Toplevel(ctx, path)
	if err != nil {
		return contracts.RepoWorktree{}, err
	}
	gitDir, err := GitDir(ctx, top)
	if err != nil {
		return contracts.RepoWorktree{}, err
	}
	w := contracts.RepoWorktree{Path: top, GitDir: gitDir, ComputedAt: time.Now().UTC()}
	if own, err := worktreeGitDir(ctx, top); err == nil {
		w.Main = own == gitDir
	}
	head, err := sessionwt.Git(ctx, top, "rev-parse", "-q", "--verify", "refs/heads/"+branch+"^{commit}")
	if err != nil {
		return contracts.RepoWorktree{}, err
	}
	w.Head = strings.TrimSpace(head)
	return w, branchFacts(ctx, top, branch, &w)
}

// branchFacts fills w's branch, remote and ahead/behind for branch.
func branchFacts(ctx context.Context, top, branch string, w *contracts.RepoWorktree) error {
	var err error
	w.Branch = branch
	w.Remote, w.RemoteBranch, w.Upstream = sessionwt.RemoteFor(ctx, top, branch)
	if w.Remote == "" {
		return nil
	}
	if w.Ahead, err = sessionwt.CountUnpushed(ctx, top, branch); err != nil {
		return err
	}
	remoteRef := "refs/remotes/" + w.Remote + "/" + w.RemoteBranch
	if _, err := sessionwt.Git(ctx, top, "rev-parse", "-q", "--verify", remoteRef); err != nil {
		w.NewBranch = true
		return nil
	}
	w.Behind, err = countCommits(ctx, top, "refs/heads/"+branch+".."+remoteRef)
	return err
}

func countCommits(ctx context.Context, dir, rng string) (int, error) {
	out, err := sessionwt.Git(ctx, dir, "rev-list", "--count", rng)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(out))
}

// Cache keeps Compute's answers per checkout until the repo changes.
// Safe for concurrent use. The zero value is not usable; use NewCache.
type Cache struct {
	mu     sync.Mutex
	byPath map[string]contracts.RepoWorktree // checkout top (or top + branch) → its facts
	// computing collapses concurrent misses for one checkout into one
	// Compute.
	computing map[string]*call
	// epoch counts invalidations: a Compute that started before one is
	// returned but not cached, since it may have read the repo mid-change.
	epoch uint64
	// computeFn stands in for Compute/ComputeBranch in tests; nil is them.
	computeFn func(ctx context.Context, top, branch string) (contracts.RepoWorktree, error)
}

type call struct {
	done chan struct{}
	w    contracts.RepoWorktree
	err  error
	// waiters counts the callers still waiting on done (guarded by
	// Cache.mu); the last to give up cancels the compute.
	waiters int
	cancel  context.CancelFunc
}

// computeTimeout bounds one shared compute, which runs detached from any
// one caller's context: callers come and go, the compute answers them all.
const computeTimeout = 2 * time.Minute

// NewCache returns an empty cache.
func NewCache() *Cache {
	return &Cache{byPath: map[string]contracts.RepoWorktree{}, computing: map[string]*call{}}
}

// Get is Compute for path, from the cache when the repo hasn't changed
// since.
func (c *Cache) Get(ctx context.Context, path string) (contracts.RepoWorktree, error) {
	return c.get(ctx, path, "")
}

// GetBranch is ComputeBranch, cached the same way.
func (c *Cache) GetBranch(ctx context.Context, path, branch string) (contracts.RepoWorktree, error) {
	return c.get(ctx, path, branch)
}

// get serves a hit from the cache and collapses concurrent misses for one
// key into one compute. That compute is shared, so it runs under its own
// context (computeTimeout), not the first caller's: one caller cancelling
// (a view switched away) returns that caller at once with its ctx's error
// but doesn't fail the others. Once every waiter has gone, the compute is
// cancelled, which kills its git commands.
func (c *Cache) get(ctx context.Context, path, branch string) (contracts.RepoWorktree, error) {
	top, err := sessionwt.Toplevel(ctx, path)
	if err != nil {
		return contracts.RepoWorktree{}, err
	}
	key := top
	if branch != "" {
		key += "\x00" + branch
	}
	c.mu.Lock()
	if w, ok := c.byPath[key]; ok {
		c.mu.Unlock()
		return w, nil
	}
	cl, ok := c.computing[key]
	if !ok {
		cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), computeTimeout)
		cl = &call{done: make(chan struct{}), cancel: cancel}
		c.computing[key] = cl
		go c.compute(cctx, cl, key, top, branch, c.epoch)
	}
	cl.waiters++
	c.mu.Unlock()

	select {
	case <-cl.done:
		return cl.w, cl.err
	case <-ctx.Done():
		c.mu.Lock()
		cl.waiters--
		last := cl.waiters == 0
		if last && c.computing[key] == cl {
			// Nobody is left to answer: a later ask starts afresh.
			delete(c.computing, key)
		}
		c.mu.Unlock()
		if last {
			cl.cancel()
		}
		return contracts.RepoWorktree{}, ctx.Err()
	}
}

func (c *Cache) compute(ctx context.Context, cl *call, key, top, branch string, epoch uint64) {
	defer cl.cancel()
	switch {
	case c.computeFn != nil:
		cl.w, cl.err = c.computeFn(ctx, top, branch)
	case branch == "":
		cl.w, cl.err = Compute(ctx, top)
	default:
		cl.w, cl.err = ComputeBranch(ctx, top, branch)
	}
	c.mu.Lock()
	if c.computing[key] == cl {
		delete(c.computing, key)
	}
	if cl.err == nil && ctx.Err() == nil && c.epoch == epoch {
		c.byPath[key] = cl.w
	}
	c.mu.Unlock()
	close(cl.done)
}

// Invalidate drops every cached checkout of the repo whose shared git dir
// is gitDir. A Compute running meanwhile still answers its caller but
// isn't cached.
func (c *Cache) Invalidate(gitDir string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.epoch++
	for top, w := range c.byPath {
		if w.GitDir == gitDir {
			delete(c.byPath, top)
		}
	}
}
