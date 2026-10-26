package runner

import (
	"log"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/providers/provider"
)

// stopGrace is how long a Stop waits for the provider's own turn end before
// the runner ends the turn itself. An interrupt that reaches a running turn
// ends it well inside this; one that reaches a harness with nothing running
// gets no answer at all, and that is the case this exists for: the runner's
// view of the turn has drifted from the harness's (a turn end it never saw),
// and without it Stop would be a button that does nothing while the session
// reads "working" for good and every message queues behind a turn that is
// already over (ses_359adef5b1c66ed5, 2026-10-25).
const stopGrace = 10 * time.Second

// stopCheck is one Stop's watchdog firing: the turn epoch Interrupt saw.
type stopCheck struct{ epoch uint64 }

// armStopWatch starts the watchdog for a Stop that saw epoch. Its firing goes
// to the pump, which is what ends turns, so a forced end can't interleave
// with a real one. The send never blocks: a full channel already holds a
// check that covers this one.
func (r *Runner) armStopWatch(epoch uint64) {
	grace := r.stopGrace
	if grace == 0 {
		grace = stopGrace
	}
	time.AfterFunc(grace, func() {
		select {
		case r.stopChecks <- stopCheck{epoch: epoch}:
		default:
		}
	})
}

// stuckAfterStop reports whether a Stop's watchdog should end the turn: sess
// is still the live session, no turn has started or ended since the Stop
// (epoch), and the session still reads as mid-turn — busy, or reporting
// working from activity the runner never started a turn for.
func (r *Runner) stuckAfterStop(sess provider.Session, c stopCheck) bool {
	r.mu.Lock()
	stale := r.sess != sess || r.closing || r.turnEpoch != c.epoch
	busy := r.busy
	r.mu.Unlock()
	if stale {
		return false
	}
	r.agent.mu.Lock()
	working := r.agent.state == contracts.AgentWorking
	r.agent.mu.Unlock()
	return busy || working
}

// forcedTurnEnd is the turn end the pump handles in place of one the
// provider never sent, after logging why.
func (r *Runner) forcedTurnEnd() provider.Event {
	log.Printf("runner: %s: no turn end after Stop; ending the turn", r.sessionID)
	return provider.Event{Kind: provider.EventTurnEnd, Interrupted: true}
}
