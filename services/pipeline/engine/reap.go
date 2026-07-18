package engine

import (
	"context"
	"fmt"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/procid"
)

// reapDeadLeases is the launch-time recovery path (spec: reap-on-launch).
// Called once, before the scheduler's first scan: for every lease this
// engine's own actor currently holds (on any session — the same default
// actor "pipeline" is shared across every `mads run` invocation on a
// machine, which is exactly why proof must pin down pid + start-ticks,
// not just "some pipeline process claimed this"), if this engine can
// PROVE the original holder's process is gone, release the lease and
// requeue the task in_progress -> ready with an evidence event. Anything
// not provable — including this engine's own identity being unreadable —
// is left untouched; the TTL reaper remains the fallback, unchanged.
// Best-effort throughout: any error here just means fewer leases got
// reclaimed early, never a failed launch.
func (e *engine) reapDeadLeases(ctx context.Context) {
	if e.identity == (contracts.LeaseIdentity{}) {
		return // couldn't establish our own identity: nothing is provable
	}
	leases, err := e.cfg.Data.Leases(ctx)
	if err != nil {
		return
	}
	for _, l := range leases {
		if l.Identity == nil {
			continue // rule 4: pre-migration/non-Linux rows are never provable
		}
		verdict, provable := procid.Evaluate(e.identity, *l.Identity, procid.Default)
		if !provable || !verdict.Dead {
			continue
		}
		evidence := fmt.Sprintf("lease reclaimed: holder %s pid %d provably dead (%s)",
			e.cfg.Actor, l.Identity.PID, verdict.Reason)
		_, _ = e.cfg.Data.ReclaimLease(ctx, l.TaskID, *l.Identity, evidence)
	}
}
