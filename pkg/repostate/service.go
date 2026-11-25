package repostate

import (
	"context"
	"log"
	"path/filepath"
	"sync"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
)

// Lister names the checkouts sessions work in: the repos to keep current.
// The Router's is the gitstate service's kept sessions, from memory.
type Lister func(ctx context.Context) ([]string, error)

// Service is the Router's repo state: a Cache kept current by a Watcher
// over the repos sessions work in (its Lister), and anything recently asked
// about. Each settled change clears the repo's entries and is announced
// once. Safe for concurrent use.
type Service struct {
	cache    *Cache
	watcher  *Watcher
	list     Lister
	announce func(gitDir string)
	every    time.Duration

	mu    sync.Mutex
	asked map[string]time.Time // checkout → when a view last asked about it
	where map[string]location  // checkout → its git dirs (they don't change)
	kick  chan struct{}
}

type location struct{ gitDir, ownGitDir string }

// askedFor is how long a checkout someone asked about stays watched
// without an active session in it.
const askedFor = 30 * time.Minute

// NewService starts keeping repo state current until ctx ends: it lists
// the checkouts to keep current every 30 s (and right away, and on
// Reconcile), watches their repos, and calls announce with the shared git dir of each repo that
// changed, after clearing its cache entries.
func NewService(ctx context.Context, list Lister, announce func(gitDir string)) *Service {
	return newService(ctx, list, announce, Options{}, 30*time.Second)
}

func newService(ctx context.Context, list Lister, announce func(gitDir string), opts Options, every time.Duration) *Service {
	s := &Service{cache: NewCache(), list: list, announce: announce, every: every,
		asked: map[string]time.Time{}, where: map[string]location{}, kick: make(chan struct{}, 1)}
	s.watcher = NewWatcher(s.changed, opts)
	go s.reconcileLoop(ctx)
	return s
}

func (s *Service) changed(gitDir string) {
	s.cache.Invalidate(gitDir)
	s.announce(gitDir)
}

// Worktree is the branch facts of the checkout holding path, current to
// the last settled change. Asking also keeps that repo watched for a
// while.
func (s *Service) Worktree(ctx context.Context, path string) (contracts.RepoWorktree, error) {
	s.noteAsked(path)
	return s.cache.Get(ctx, path)
}

// Branch is Worktree for a branch the checkout needn't have checked out.
func (s *Service) Branch(ctx context.Context, path, branch string) (contracts.RepoWorktree, error) {
	s.noteAsked(path)
	return s.cache.GetBranch(ctx, path, branch)
}

// Reconcile asks for the watched repos to be listed again soon, after the
// lister's answer moved (a session's repo set changed). Never blocks.
func (s *Service) Reconcile() {
	select {
	case s.kick <- struct{}{}:
	default:
	}
}

func (s *Service) noteAsked(path string) {
	s.mu.Lock()
	_, known := s.asked[path]
	s.asked[path] = time.Now()
	s.mu.Unlock()
	if !known {
		select {
		case s.kick <- struct{}{}:
		default:
		}
	}
}

func (s *Service) reconcileLoop(ctx context.Context) {
	defer s.watcher.Close()
	t := time.NewTicker(s.every)
	defer t.Stop()
	for {
		guard("reconciling watched repos", func() { s.reconcile(ctx) })
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-s.kick:
		}
	}
}

// reconcile watches exactly the repos of the listed checkouts and
// of checkouts asked about lately.
func (s *Service) reconcile(ctx context.Context) {
	paths, err := s.list(ctx)
	if err != nil && ctx.Err() == nil {
		log.Printf("repostate: list the checkouts to watch: %v", err)
	}
	now := time.Now()
	s.mu.Lock()
	for p, at := range s.asked {
		if now.Sub(at) > askedFor {
			delete(s.asked, p)
			continue
		}
		paths = append(paths, p)
	}
	s.mu.Unlock()

	byGitDir := map[string]*Spec{}
	for _, p := range paths {
		loc, ok := s.locate(ctx, p)
		if !ok {
			continue
		}
		spec := byGitDir[loc.gitDir]
		if spec == nil {
			spec = &Spec{GitDir: loc.gitDir}
			byGitDir[loc.gitDir] = spec
		}
		if loc.ownGitDir != loc.gitDir {
			spec.Worktrees = append(spec.Worktrees, loc.ownGitDir)
		}
	}
	specs := make([]Spec, 0, len(byGitDir))
	for _, sp := range byGitDir {
		specs = append(specs, *sp)
	}
	s.watcher.Set(specs)
}

// locate is the git dirs of the checkout at path, remembered once found.
func (s *Service) locate(ctx context.Context, path string) (location, bool) {
	path = filepath.Clean(path)
	s.mu.Lock()
	loc, ok := s.where[path]
	s.mu.Unlock()
	if ok {
		return loc, true
	}
	gitDir, err := GitDir(ctx, path)
	if err != nil {
		return location{}, false // gone, or not a repo: try again next time
	}
	own, err := worktreeGitDir(ctx, path)
	if err != nil {
		return location{}, false
	}
	loc = location{gitDir: gitDir, ownGitDir: own}
	s.mu.Lock()
	s.where[path] = loc
	s.mu.Unlock()
	return loc, true
}

// RepoOf is the main checkout's top for a shared git dir, for
// repo.changed's payload: the parent of a ".git" dir, else what git says.
func RepoOf(gitDir string) string {
	if filepath.Base(gitDir) == ".git" {
		return filepath.Dir(gitDir)
	}
	return gitDir
}
