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
// for their own writes to the primary checkout (merge-back, Land), so those
// serialize against land attempts too; work inside a session's worktree
// takes only that session's lock and never waits on a land.
func acquireWorkspaceLock(ctx context.Context, w *worktrees) (release func(), err error) {
	return sessionwt.AcquireLock(ctx, w.primary)
}
