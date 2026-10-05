package runner

import (
	"context"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/sessionwt"
)

// The session's Commit and Land actions (docs/SESSION-WORKTREES-DESIGN.md
// D3, D5, D6) while its child is live: the Router forwards them here
// because only this process knows whether the agent is mid-turn, and only
// it can hold the user's messages back while a Land rewrites the folder.
// The git work itself is sessionwt.Ops, the same code the Router runs for
// an inactive session.

// landHook is a test seam, called once Land holds the landing flag.
var landHook func()

func (r *Runner) worktreeOps() sessionwt.Ops {
	return sessionwt.Ops{Store: r.data, Refresh: r.RefreshWorktreeCounts}
}

// WorktreeStatus is the header/dialog state, with the busy gate filled in
// from this runner. pipeline is the Router's word on an active run.
func (r *Runner) WorktreeStatus(ctx context.Context, pipeline bool) (contracts.WorktreeStatus, error) {
	r.mu.Lock()
	busy := r.busy || r.landing
	r.mu.Unlock()
	return r.worktreeOps().Status(ctx, r.sessionID, sessionwt.Gates{Busy: busy, Pipeline: pipeline})
}

// Commit commits the selected session files (D5). It doesn't wait for the
// agent: committing what is on disk mid-turn is the user's call to make.
// Refusals are *sessionwt.CommitRefusal.
func (r *Runner) Commit(ctx context.Context, req contracts.CommitRequest) (contracts.CommitResult, error) {
	return r.worktreeOps().Commit(context.WithoutCancel(ctx), r.sessionID, req)
}

// UndoCommit takes back the session's last Commit while it is still the
// tip (D5).
func (r *Runner) UndoCommit(ctx context.Context, hash string) (contracts.UndoCommitResult, error) {
	return r.worktreeOps().Undo(context.WithoutCancel(ctx), r.sessionID, hash)
}

// Land lands the session branch into its base (D6). It is refused while a
// turn is in flight; otherwise no turn can start until it finishes —
// messages sent meanwhile queue, and go out once it's done. It runs to the
// end even if the caller goes away: stopping git half-way through a merge
// is the one outcome worse than either result.
func (r *Runner) Land(ctx context.Context, pipeline bool) (contracts.LandResult, error) {
	ctx = context.WithoutCancel(ctx)
	if !r.beginLanding() {
		return r.worktreeOps().Land(ctx, r.sessionID, sessionwt.Gates{Busy: true, Pipeline: pipeline})
	}
	defer r.endLanding()
	if landHook != nil {
		landHook()
	}
	return r.worktreeOps().Land(ctx, r.sessionID, sessionwt.Gates{Pipeline: pipeline})
}

// beginLanding sets the landing flag unless a turn is in flight (or
// another Land holds it).
func (r *Runner) beginLanding() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.busy || r.landing {
		return false
	}
	r.landing = true
	return true
}

// endLanding clears the landing flag and delivers whatever queued behind
// it, as a turn end would have.
func (r *Runner) endLanding() {
	r.mu.Lock()
	r.landing = false
	if !r.busy && !r.closing && len(r.pending) > 0 {
		r.deliverNextLocked()
		return
	}
	r.mu.Unlock()
}
