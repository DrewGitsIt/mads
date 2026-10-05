// Package sessionwt owns the git mechanics of session isolation
// (docs/SESSION-WORKTREES-DESIGN.md): one worktree and one branch per
// session, found by a deterministic path and name rather than by whatever the
// session record says, plus the two tiers of lock (a per-session lock and the
// per-repo land lock) every process that changes them takes first.
//
// It lives in pkg, not inside one service, because three processes do this
// work (D3): the session child at start and resume, the Router for inactive
// sessions, and the pipeline child for task merges. They serialize against
// each other only if they share the same lock files and one idea of where a
// session's folder is.
//
// Functions that change the repository assume the caller holds a lock (see
// lock.go for the two tiers and their order): Ensure and CommitStray the
// session lock; Merge and Remove, which write the primary, the session lock
// and then the repo lock. Functions that only read (IsGitRepo,
// CurrentBranch, Prefix, Count*) take no lock.
package sessionwt

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// BranchPrefix is the namespace of session branches. The pipeline's task
// branches live beside it under mads/, so nothing here may assume every
// mads/* branch is a session's.
const BranchPrefix = "mads/session/"

// Branch is the deterministic branch name for a session.
func Branch(sessionID string) string { return BranchPrefix + sessionID }

// Path is the deterministic folder for a session under root.
func Path(root, sessionID string) string { return filepath.Join(root, sessionID) }

// ErrDetachedHEAD is returned by CurrentBranch when the primary has no branch
// checked out — there is nothing to name as the session's Land target, so the
// caller doesn't isolate (D1).
var ErrDetachedHEAD = errors.New("sessionwt: primary is on a detached HEAD")

// Git runs one git command in dir. Commits it makes carry the mads-session
// safety identity, never the user's config: only the automatic paths
// (CommitStray, Merge) commit through here, and the user's own commits go
// through their own identity elsewhere.
func Git(ctx context.Context, dir string, args ...string) (string, error) {
	return git(ctx, dir, nil, args...)
}

func git(ctx context.Context, dir string, env []string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=mads-session", "GIT_AUTHOR_EMAIL=session@mads.local",
		"GIT_COMMITTER_NAME=mads-session", "GIT_COMMITTER_EMAIL=session@mads.local")
	cmd.Env = append(cmd.Env, env...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("git %s: %v\n%s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

// IsGitRepo reports whether dir is inside a git repository. Isolation is
// git-only (spec §17): a session opened on a plain folder runs in place, never
// fails.
func IsGitRepo(ctx context.Context, dir string) bool {
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "--git-dir")
	cmd.Dir = dir
	return cmd.Run() == nil
}

// CurrentBranch is the short name of the branch checked out in dir — the
// value recorded as Session.WorktreeBase at creation. A detached HEAD returns
// ErrDetachedHEAD.
func CurrentBranch(ctx context.Context, dir string) (string, error) {
	out, err := Git(ctx, dir, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil {
		// symbolic-ref exits 1 with no output when HEAD isn't a symref; any
		// other failure (not a repo, git missing) is a real error.
		if strings.TrimSpace(out) == "" && IsGitRepo(ctx, dir) {
			return "", ErrDetachedHEAD
		}
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// Prefix is dir's path relative to the top of its working tree ("" at the
// top, "sub/dir/" below it). A session asked to start in a repo subdirectory
// runs in the same subdirectory of its worktree.
func Prefix(ctx context.Context, dir string) (string, error) {
	out, err := Git(ctx, dir, "rev-parse", "--show-prefix")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// Toplevel is the absolute root of dir's working tree — what ledger paths are
// made relative to before counting (SessionFiles, CountUncommitted).
func Toplevel(ctx context.Context, dir string) (string, error) {
	out, err := Git(ctx, dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// commonDir is the absolute path of dir's shared .git directory — the same
// for the main checkout and every linked worktree, which is what makes it the
// right home for a per-repo lock.
func commonDir(ctx context.Context, dir string) (string, error) {
	out, err := Git(ctx, dir, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// samePath compares two absolute paths after resolving symlinks where they
// exist, so a temp dir reached through /var and git's /private/var spelling
// of it (macOS) still compare equal. A path that doesn't exist resolves
// through its parent.
func samePath(a, b string) bool {
	return resolve(a) == resolve(b)
}

func resolve(p string) string {
	p = filepath.Clean(p)
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	if r, err := filepath.EvalSymlinks(filepath.Dir(p)); err == nil {
		return filepath.Join(r, filepath.Base(p))
	}
	return p
}
