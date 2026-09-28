package goosecli

import "github.com/drewkidwell/mads/providers/provider"

// goose's built-in developer tools have a 1-to-1 map onto the v1
// ToolIntent closed set (goose is the "reference" vocabulary the v1
// closed set was named after). skill/mcp map to the mads
// capability-registry path and intentionally have no goose built-in
// mapping (the runner wires those via opts.MCPServers, not tool names).
var gooseToolMap = provider.ToolIntentMap{
	provider.ToolIntentRead:   {"read"},
	provider.ToolIntentWrite:  {"write"},
	provider.ToolIntentEdit:   {"edit"},
	provider.ToolIntentShell:  {"shell"},
	provider.ToolIntentSearch: {"search"},
	provider.ToolIntentFetch:  {"fetch"},
}

// ToolIntentMap declares the harness's concrete tool-name policy for the
// persona-level allow/deny vocabulary (the agent-concept redesign §2).
//
// goose's IntentMatrix declares IntentDenyCommands: Unsupported — goose
// has no native command-restriction surface of its own; the runner falls
// back to enforcing the Persona's Denylist via the system prompt (the
// goosecli "AppendSystemPrompt" strategy) rather than a tool-level block.
// The map here still names the tools *so that* the persona config can
// express intent-level denials and so the conformance suite can verify
// this adapter's honesty about which intents it maps (all six of the
// v1 tool intents, except skill/mcp which are mads capability-registry
// concerns).
func (p *Provider) ToolIntentMap() provider.ToolIntentMap {
	out := make(provider.ToolIntentMap, len(gooseToolMap))
	for k, v := range gooseToolMap {
		got := make([]string, len(v))
		copy(got, v)
		out[k] = got
	}
	return out
}
