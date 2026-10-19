package engine

import (
	"context"
	"fmt"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/procid"
)

// reapDeadLeases is the launch-time recovery path (spec: reap-on-launch,
// docs/PIPELINE-LEASES-DESIGN.md A4). Called once, before the scheduler's
// first scan: for every live lease of this engine's kind (every
// "pipeline:<runID>" holder, any run, any session — so proof must pin down
// machine, boot, pid and start ticks, not just "some pipeline process
// claimed this"), if this engine can PROVE the holder's process is gone,
// delete the lease with an evidence event. Task status is untouched: an
// in_progress task whose lease is reclaimed is Interrupted, and picking it
// up is a later run's scheduler's job (B-R3), not this one's. Anything not
// provable — including this engine's own identity being unreadable — is
// left untouched; the TTL reaper remains the fallback, unchanged.
// Best-effort throughout: any error here just means fewer leases got
// reclaimed early, never a failed launch.
func (e *engine) reapDeadLeases(ctx context.Context) {
	if e.identity == (contracts.LeaseIdentity{}) {
		return // couldn't establish our own identity: nothing is provable
	}
	leases, err := e.cfg.Data.LeasesOfKind(ctx, e.cfg.Actor)
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
			l.Holder, l.Identity.PID, verdict.Reason)
		_, _ = e.cfg.Data.ReclaimLease(ctx, l.TaskID, *l.Identity, evidence)
	}
}
