package runner

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/providers/provider"
)

// compactOccupancyThreshold is the fraction of the model's context window
// above which Start requests a native compaction pass immediately after
// resuming, rather than resuming silently. This is near-limit protection
// only — the user-visible "compact now?" choice lives in the UI (which reads
// the same last_usage and offers a banner well before this point), so the
// server must not preempt it except when the next turn genuinely risks
// overflowing the window. It was originally 0.10, computed from the
// per-turn billing aggregate (~N× real occupancy on multi-tool turns) —
// together those made nearly every session bounce (resume, trust change,
// restart) silently compact.
const compactOccupancyThreshold = 0.85

// contextOccupancy returns usage's context occupancy as a fraction of
// model's window, or 0 when unknowable. ContextTokens (the final API call's
// prompt side, wire.go's iterations breakdown) is the only trusted
// numerator. The summed token fields are a per-turn billing aggregate —
// ~N× real occupancy on multi-tool turns — and a LastUsage persisted before
// ContextTokens existed carries only that aggregate, so summing it here made
// waking a dormant session read e.g. 267212/200000 = 1.34 and auto-compact
// it. No ContextTokens means occupancy is unknown, and unknown must never
// compact: the aggregate is a UI better-than-nothing at most, never a
// trigger. The provider-reported window beats the static table for the same
// reason (a local 128k model would otherwise be judged against 200k).
func contextOccupancy(usage *contracts.Usage, model string) float64 {
	if usage == nil || usage.ContextTokens == 0 {
		return 0
	}
	window := usage.ContextWindow
	if window <= 0 {
		window = provider.ContextWindowForModel(model)
	}
	if window <= 0 {
		return 0
	}
	return float64(usage.ContextTokens) / float64(window)
}

// shouldCompactOnResume reports whether a resumed session's last-known usage
// occupies more than compactOccupancyThreshold of model's context window.
// A nil usage — a session that never completed a turn before this process
// resumed it — never triggers, and neither does an aggregate-only usage
// (persisted before ContextTokens existed): contextOccupancy reports both
// as unknown rather than trusting the inflated billing sum.
func shouldCompactOnResume(usage *contracts.Usage, model string) bool {
	return contextOccupancy(usage, model) > compactOccupancyThreshold
}

// resumeCompactor returns sess's Compactor when resuming with last-known
// usage above compactOccupancyThreshold (shouldCompactOnResume), nil
// otherwise — including when sess doesn't implement provider.Compactor at
// all, so an unsupporting provider's resume proceeds exactly as before this
// trigger existed: no error, no status shown. Start asks before it installs
// the session, so the compaction turn can be marked in flight in the same
// lock hold that makes the session reachable (beginCompactLocked) — a
// message arriving in between would otherwise go straight to a harness
// that's about to be (or already is) compacting.
func resumeCompactor(sess provider.Session, usage *contracts.Usage, model string) provider.Compactor {
	if !shouldCompactOnResume(usage, model) {
		return nil
	}
	compactor, _ := sess.(provider.Compactor)
	return compactor
}

// requestResumeCompact runs the near-limit resume compaction Start decided
// on (resumeCompactor) and already marked in flight (beginCompactLocked).
// "" — the harness's default pass; focus text is only ever the user's.
// ErrUnsupported is a shared engine's agent without a compaction command
// (acprpc's CompactCommand unset): the same silent no-op as no Compactor at
// all. Either way a pass that never started gives its turn back.
func (r *Runner) requestResumeCompact(ctx context.Context, compactor provider.Compactor) {
	err := compactor.Compact(ctx, "")
	if err == nil {
		return
	}
	if !errors.Is(err, provider.ErrUnsupported) {
		log.Printf("runner: request resume compaction: %v", err)
	}
	r.abortCompact()
}

// beginCompactLocked marks a runner-requested compaction pass (CompactNow,
// or Start's near-limit trigger) as the in-flight turn. It is one, to the
// harness: claude-code runs /compact as a turn ending in a result line, and
// acprpc sends a real session/prompt and refuses any other prompt until it
// answers. Left unmarked, a message sent meanwhile went SendOrQueue → not
// busy → Send → the harness's "already has a prompt in flight" → a 500 and
// a lost message; marked, it queues behind the pass exactly as behind any
// turn, and onTurnEnd — the pass's own EventTurnEnd — delivers it. The
// usage tally is told too, so the pass's tokens count but it doesn't count
// as a turn of conversation (countTurn). Must be called with r.mu held; the
// caller marks the agent working (markAgentWorking("/compact") labels it
// Compacting) after unlocking.
func (r *Runner) beginCompactLocked() {
	r.busy = true
	r.turnStartedAt, r.interrupted = time.Now(), false
	r.tally.setCompactTurn(true)
}

// abortCompact undoes beginCompactLocked for a pass the harness refused to
// start: no turn is coming to end it, so this is its turn end — idle, and
// anything that queued in the meantime goes out now.
func (r *Runner) abortCompact() {
	r.tally.setCompactTurn(false)
	r.markAgentIdle()
	r.mu.Lock()
	r.busy = false
	if len(r.pending) > 0 && !r.landing && r.sess != nil {
		r.deliverNextLocked()
		return
	}
	r.mu.Unlock()
}

// ErrCompactUnsupported is returned by CompactNow when the active provider
// session doesn't implement provider.Compactor. Distinct from the silent
// no-op the resume trigger does for the same case: an explicit user request
// deserves an explicit "this provider can't" answer.
var ErrCompactUnsupported = errors.New("runner: provider does not support compaction")

// CompactNow runs a native compaction pass on user request (the UI's
// "Compact now" action and /compact, POST /compact). Unlike the resume
// trigger it applies no threshold — the user asked. Instructions, when
// non-empty, steer the summary (the /compact focus text); a harness that
// can't take them refuses rather than compacting without them, surfaced as
// ErrCompactUnsupported like a harness with no compaction at all. Progress
// and outcome arrive as EventCompact on the stream, same as the resume
// path; this returns once the request is accepted, not once compaction
// finishes. The pass is the session's in-flight turn until its turn end
// (beginCompactLocked), so it refuses while another turn runs, and messages
// sent while it runs queue behind it.
func (r *Runner) CompactNow(ctx context.Context, instructions string) error {
	r.mu.Lock()
	if r.sess == nil {
		r.mu.Unlock()
		return ErrNotReady
	}
	if r.busy || r.landing {
		r.mu.Unlock()
		return ErrTurnInFlight
	}
	compactor, ok := r.sess.(provider.Compactor)
	if !ok {
		r.mu.Unlock()
		return ErrCompactUnsupported
	}
	r.beginCompactLocked()
	r.mu.Unlock()
	r.markAgentWorking("/compact")
	if err := compactor.Compact(ctx, instructions); err != nil {
		r.abortCompact()
		if errors.Is(err, provider.ErrUnsupported) {
			return fmt.Errorf("%w: %v", ErrCompactUnsupported, err)
		}
		return err
	}
	return nil
}
