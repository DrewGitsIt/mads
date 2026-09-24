package claudecode

import "github.com/drewkidwell/mads/providers/provider"

// claudeToolNames holds this adapter's *concrete* tool names, organized by
// the v1 ToolIntent vocabulary. This is the only place in mads that knows
// "shell == Bash" on claude-code; the persona, the runner, the UI, and
// `mads personas` all speak intents. The provider's trust layer (trustArgs)
// still uses the concrete names directly — it's the harness-level
// enforcement (a native restriction); the resolver is the persona-level
// policy (the persona-level allow/deny).

// claudecode's native vocabulary is PascalCase, and there is a 1-to-1 map
// onto the v1 ToolIntent closed set (skill/mcp are handled by mads through
// capability-registry wiring, not through claude-code tool names, so they
// intentionally have no concrete mapping here).
var claudecodeToolMap = provider.ToolIntentMap{
	provider.ToolIntentRead:   {"Read"},
	provider.ToolIntentWrite:  {"Write"},
	provider.ToolIntentEdit:   {"Edit"},
	provider.ToolIntentShell:  {"Bash"},
	provider.ToolIntentSearch: {"Grep", "Glob"},
	provider.ToolIntentFetch:  {"WebFetch"},
}

// ToolIntentResolver declares the harness's concrete tool-name policy for
// the closed v1 ToolIntent vocabulary (the agent-concept redesign §2).
//
// The conformance suite (providers/conformance.CheckToolIntents) asserts
// every intent in this map is in the closed set; providers that claim to
// enforce the "shell" intent (see claudecode's own IntentMatrix,
// IntentDenyCommands: Native) MUST include a ToolIntentShell entry — the
// trust layer's --disallowedTools flag is the native enforcement, but the
// persona-level allow/deny also resolves to --disallowedTools, and the two
// paths must agree about what shell is called on this harness.
func (p *Provider) ToolIntentMap() provider.ToolIntentMap {
	out := make(provider.ToolIntentMap, len(claudecodeToolMap))
	for k, v := range claudecodeToolMap {
		got := make([]string, len(v))
		copy(got, v)
		out[k] = got
	}
	return out
}
