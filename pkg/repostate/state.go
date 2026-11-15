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
	return w, branchFacts(ctx, top, branch, &w)
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
}

type call struct {
	done chan struct{}
	w    contracts.RepoWorktree
	err  error
}

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
	if cl, ok := c.computing[key]; ok {
		c.mu.Unlock()
		<-cl.done
		return cl.w, cl.err
	}
	cl := &call{done: make(chan struct{})}
	c.computing[key] = cl
	epoch := c.epoch
	c.mu.Unlock()

	if branch == "" {
		cl.w, cl.err = Compute(ctx, top)
	} else {
		cl.w, cl.err = ComputeBranch(ctx, top, branch)
	}
	c.mu.Lock()
	delete(c.computing, key)
	if cl.err == nil && c.epoch == epoch {
		c.byPath[key] = cl.w
	}
	c.mu.Unlock()
	close(cl.done)
	return cl.w, cl.err
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
