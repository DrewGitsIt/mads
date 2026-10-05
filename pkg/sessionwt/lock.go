package sessionwt

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
)

// There are two tiers of lock, both flock(2) files in the repo's shared .git
// directory, so every process — session child, Router, pipeline child —
// resolves the same files from the primary or any linked worktree:
//
//   - The session lock, mads-session-<id>.lock, serializes work scoped to
//     one session's worktree: Ensure, and later Commit, Land's merge of the
//     base into the session branch, pipeline task merges into the session
//     folder, and delete cleanup. None of these write the primary checkout,
//     and git's own ref and worktree-metadata locks already cover a
//     `worktree add` racing a merge in the primary.
//   - The repo lock, mads-land.lock, serializes writes to the primary
//     checkout: the pipeline engine's lands (which took this same file
//     before this package existed), and later Land's merge into the base.
//
// Splitting them is what keeps a session start from waiting behind a land
// that holds the repo lock through its whole gate.
//
// Ordering, to rule out deadlock: an operation that needs both takes the
// session lock first and the repo lock second, never the reverse. Nothing
// holding the repo lock may wait for a session lock.

// LockFile is the repo lock's name inside the shared .git directory.
const LockFile = "mads-land.lock"

// sessionLockFile is the session lock's name for one session.
func sessionLockFile(sessionID string) string { return "mads-session-" + sessionID + ".lock" }

// SessionLockPath is sessionID's lock for whichever repo dir belongs to:
// <git-common-dir>/mads-session-<sessionID>.lock.
func SessionLockPath(ctx context.Context, dir, sessionID string) (string, error) {
	if sessionID == "" || strings.ContainsAny(sessionID, `/\`) {
		return "", fmt.Errorf("sessionwt: invalid session id %q", sessionID)
	}
	common, err := commonDir(ctx, dir)
	if err != nil {
		return "", fmt.Errorf("resolve session lock path: %w", err)
	}
	return filepath.Join(common, sessionLockFile(sessionID)), nil
}

// AcquireSessionLock blocks until it holds sessionID's lock in dir's repo,
// or ctx ends. Same flock semantics as AcquireLock: never acquire it again
// while holding it. If the repo lock is needed too, take it after this one.
func AcquireSessionLock(ctx context.Context, dir, sessionID string) (release func(), err error) {
	path, err := SessionLockPath(ctx, dir, sessionID)
	if err != nil {
		return nil, err
	}
	closeFn, err := AcquireFlock(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("acquire session lock %s: %w", path, err)
	}
	return func() { _ = closeFn() }, nil
}

// WithSessionLock runs fn holding sessionID's lock. fn may take the repo
// lock (that is the permitted order) but must not take this one again.
func WithSessionLock(ctx context.Context, dir, sessionID string, fn func() error) error {
	release, err := AcquireSessionLock(ctx, dir, sessionID)
	if err != nil {
		return err
	}
	defer release()
	return fn()
}

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
// safe to call from any goroutine. Take it only for writes to the primary
// checkout, and after the session lock when both are needed.
//
// flock(2) locks belong to an open file description, not a process: a second
// AcquireLock for the same repo from the same process blocks exactly like one
// from another process (the session lock behaves the same). A holder must
// never acquire again — call the lock-free functions ("caller holds the ...
// lock") inside instead, or it deadlocks against itself.
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

// WithLock runs fn holding dir's repo lock. fn must not take the repo lock
// again, nor any session lock (see the ordering rule above).
func WithLock(ctx context.Context, dir string, fn func() error) error {
	release, err := AcquireLock(ctx, dir)
	if err != nil {
		return err
	}
	defer release()
	return fn()
}
