package runner

import (
	"context"
	"log"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/providers/provider"
)

// compactOccupancyThreshold is the fraction of the model's context window
// above which Start requests a native compaction pass immediately after
// resuming, rather than resuming silently. Interactive claude-code offers a
// "resume from summary" prompt at this point; headless -p mode (what mads
// always runs) has no equivalent, so an uncompacted resume of a large
// transcript burns far more of the user's usage limit on the very next turn
// than it needs to.
const compactOccupancyThreshold = 0.10

// shouldCompactOnResume reports whether a resumed session's last-known usage
// occupies more than compactOccupancyThreshold of model's context window.
// The prompt side of a turn (input + cache tokens, excluding output) is the
// right basis for occupancy: it's what the next turn's request will actually
// carry as context, the same approximation the UI's context meter uses
// (ui/src/conversation/Header/ContextMeter.tsx). A nil usage — a session
// that never completed a turn before this process resumed it — never
// triggers: there is nothing to estimate occupancy from.
func shouldCompactOnResume(usage *contracts.Usage, model string) bool {
	if usage == nil {
		return false
	}
	promptTokens := usage.InputTokens + usage.CacheReadInputTokens + usage.CacheCreationInputTokens
	window := provider.ContextWindowForModel(model)
	if window <= 0 {
		return false
	}
	return float64(promptTokens)/float64(window) > compactOccupancyThreshold
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
