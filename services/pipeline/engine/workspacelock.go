package engine

import (
	"context"

	"github.com/drewkidwell/mads/pkg/sessionwt"
)

// acquireWorkspaceLock takes the repo lock for w.primary
// (<git-common-dir>/mads-land.lock, pkg/sessionwt), so every linked worktree
// resolves to the same file. The in-process landQueue only serializes land
// attempts within one engine; this extends that guarantee across processes,
// so a CLI `mads run` engine and a Router-spawned pipeline child engine, each
// with their own landQueue, still never interleave a land attempt against
// the same workspace (spec: "land attempts must be mutually excluded across
// engine processes"). The session service and the Router take the same lock
// for session worktree work, so they serialize against land attempts too.
func acquireWorkspaceLock(ctx context.Context, w *worktrees) (release func(), err error) {
	return sessionwt.AcquireLock(ctx, w.primary)
}
