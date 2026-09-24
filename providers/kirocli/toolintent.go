package kirocli

import "github.com/drewkidwell/mads/providers/provider"

// kiro-cli's concrete tool vocabulary is less stable than claude-code or
// goose's — the mapping below is best-effort against the tool set kiro
// currently exposes (read_file / write_file / edit_file / run_command /
// search / fetch_url). If kiro renames a tool, update this map and the
// conformance assertion (CheckToolIntents) will pass as long as the
// intent set is unchanged.
var kiroToolMap = provider.ToolIntentMap{
	provider.ToolIntentRead:   {"read_file"},
	provider.ToolIntentWrite:  {"write_file"},
	provider.ToolIntentEdit:   {"edit_file"},
	provider.ToolIntentShell:  {"run_command"},
	provider.ToolIntentSearch: {"search"},
	provider.ToolIntentFetch:  {"fetch_url"},
}

// ToolIntentMap declares the harness's concrete tool-name policy for the
// persona-level allow/deny vocabulary (agent-concept redesign §2).
func (p *Provider) ToolIntentMap() provider.ToolIntentMap {
	out := make(provider.ToolIntentMap, len(kiroToolMap))
	for k, v := range kiroToolMap {
		got := make([]string, len(v))
		copy(got, v)
		out[k] = got
	}
	return out
}
