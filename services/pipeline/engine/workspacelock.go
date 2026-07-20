package engine

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
)

// acquireWorkspaceLock takes a blocking, OS-level advisory lock scoped to
// w.primary's shared .git dir (git-common-dir, so every linked worktree
// resolves to the same file — same resolution ensureMergirafDriver already
// does). The in-process landQueue only serializes land attempts within one
// engine; this extends that guarantee across processes, so a CLI `mads run`
// engine and a Router-spawned pipeline child engine, each with their own
// landQueue, still never interleave a land attempt against the same
// workspace (spec: "land attempts must be mutually excluded across engine
// processes").
func acquireWorkspaceLock(ctx context.Context, w *worktrees) (release func(), err error) {
	out, err := w.git(ctx, w.primary, "rev-parse", "--git-common-dir")
	if err != nil {
		return nil, fmt.Errorf("resolve workspace lock path: %w", err)
	}
	gitDir := strings.TrimSpace(out)
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(w.primary, gitDir)
	}
	path := filepath.Join(gitDir, "mads-land.lock")

	closeFn, err := acquireWorkspaceFlock(path)
	if err != nil {
		return nil, fmt.Errorf("acquire workspace lock %s: %w", path, err)
	}
	return func() { _ = closeFn() }, nil
}
