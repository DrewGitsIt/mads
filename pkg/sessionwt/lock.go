package sessionwt

import (
	"context"
	"fmt"
	"path/filepath"
)

// LockFile is the per-repo lock's name inside the shared .git directory. The
// pipeline engine has taken this same file around its task merges since
// before this package existed; session and Router work take it too, so every
// process that changes a repo's worktrees, branches or checkout serializes
// against every other one (D3).
const LockFile = "mads-land.lock"

// LockPath is the repo lock for whichever repo dir belongs to:
// <git-common-dir>/mads-land.lock, so a primary checkout and all of its linked
// worktrees resolve to the same file.
func LockPath(ctx context.Context, dir string) (string, error) {
	common, err := commonDir(ctx, dir)
	if err != nil {
		return "", fmt.Errorf("resolve repo lock path: %w", err)
	}
	return filepath.Join(common, LockFile), nil
}

// AcquireLock blocks until it holds dir's repo lock, or ctx ends. release is
// safe to call from any goroutine.
//
// flock(2) locks belong to an open file description, not a process: a second
// AcquireLock for the same repo from the same process blocks exactly like one
// from another process. A holder must never acquire again — call the
// lock-free functions ("caller holds the repo lock") inside instead, or it
// deadlocks against itself.
func AcquireLock(ctx context.Context, dir string) (release func(), err error) {
	path, err := LockPath(ctx, dir)
	if err != nil {
		return nil, err
	}
	closeFn, err := AcquireFlock(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("acquire repo lock %s: %w", path, err)
	}
	return func() { _ = closeFn() }, nil
}

// WithLock runs fn holding dir's repo lock. fn must not take the lock again
// (see AcquireLock).
func WithLock(ctx context.Context, dir string, fn func() error) error {
	release, err := AcquireLock(ctx, dir)
	if err != nil {
		return err
	}
	defer release()
	return fn()
}
