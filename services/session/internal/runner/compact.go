package runner

import (
	"context"
	"errors"
	"log"

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

// maybeRequestCompact asks sess to run a native compaction pass if resuming
// with last-known usage above compactOccupancyThreshold (shouldCompactOnResume).
// A no-op, not an error, when sess doesn't implement provider.Compactor —
// only claude-code's stream-json transport is verified to honor it today
// (providers/claudecode's streamSession.Compact) — so an unsupporting
// provider's resume proceeds exactly as before this trigger existed: no
// error, no status shown.
func (r *Runner) maybeRequestCompact(ctx context.Context, sess provider.Session, usage *contracts.Usage, model string) {
	if !shouldCompactOnResume(usage, model) {
		return
	}
	compactor, ok := sess.(provider.Compactor)
	if !ok {
		return
	}
	if err := compactor.Compact(ctx); err != nil {
		log.Printf("runner: request resume compaction: %v", err)
	}
}

// ErrCompactUnsupported is returned by CompactNow when the active provider
// session doesn't implement provider.Compactor. Distinct from the silent
// no-op maybeRequestCompact does for the same case: an explicit user request
// deserves an explicit "this provider can't" answer.
var ErrCompactUnsupported = errors.New("runner: provider does not support compaction")

// CompactNow runs a native compaction pass on user request (the UI's
// "Compact now" action, POST /compact). Unlike the resume trigger it applies
// no threshold — the user asked. Progress and outcome arrive as EventCompact
// on the stream, same as the resume path; this returns once the request is
// accepted, not once compaction finishes.
func (r *Runner) CompactNow(ctx context.Context) error {
	r.mu.Lock()
	if r.sess == nil {
		r.mu.Unlock()
		return ErrNotReady
	}
	if r.busy {
		r.mu.Unlock()
		return ErrTurnInFlight
	}
	sess := r.sess
	r.mu.Unlock()
	compactor, ok := sess.(provider.Compactor)
	if !ok {
		return ErrCompactUnsupported
	}
	return compactor.Compact(ctx)
}
