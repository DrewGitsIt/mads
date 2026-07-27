package provider

// DefaultContextWindow is the token window size assumed for a model with no
// specific override below — every current Claude model is 200k. This is the
// backend counterpart of ui/src/router/modelDisplay.ts's
// DEFAULT_CONTEXT_WINDOW; the two are duplicated data (the UI's context
// meter and the Runner's resume-compaction trigger, services/session/
// internal/runner/compact.go, each need this in their own process) rather
// than a shared source, so keep them in sync by hand as models change.
const DefaultContextWindow = 200_000

// contextWindows holds per-model overrides, keyed by exact model ID. Empty
// today — every current Claude model is 200k. Add entries here (and to
// modelDisplay.ts's CONTEXT_WINDOWS) as models with non-200k windows appear.
var contextWindows = map[string]int{}

// ContextWindowForModel returns modelID's context window size in tokens,
// falling back to DefaultContextWindow for an unmatched or empty id.
func ContextWindowForModel(modelID string) int {
	if w, ok := contextWindows[modelID]; ok {
		return w
	}
	return DefaultContextWindow
}
