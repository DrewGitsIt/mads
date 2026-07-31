package agentselect

import "github.com/drewkidwell/mads/pkg/contracts"

// EffectiveCapabilities returns the deduplicated union of a workspace's
// ambient capability names and a task's additive deltas (owner decision:
// required capabilities live at both levels — workspace facts are true of
// every task there; task deltas are work-specific additions. There is no
// subtraction: the effective requirement is always the union).
func EffectiveCapabilities(task contracts.Task, workspace contracts.Workspace) []string {
	seen := make(map[string]struct{}, len(workspace.Capabilities)+len(task.RequiredCapabilities))
	var out []string
	for _, name := range workspace.Capabilities {
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	for _, name := range task.RequiredCapabilities {
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	return out
}

// ExpandCapabilities resolves capability names against the live registry
// into the {Tools, MCPServers, Skills} slices a Request wants, grouping by
// Capability.Type. Names with no matching registry entry are dropped —
// write-time validation (services/data) keeps requirement refs inside the
// registry at rest, but a capability can be deleted after a task/workspace
// references it, so this stays defensive rather than erroring.
func ExpandCapabilities(names []string, registry []contracts.Capability) (tools, mcpServers, skills []string) {
	byName := make(map[string]contracts.Capability, len(registry))
	for _, c := range registry {
		byName[c.Name] = c
	}
	for _, name := range names {
		c, ok := byName[name]
		if !ok {
			continue
		}
		switch c.Type {
		case contracts.CapabilityMCP:
			mcpServers = append(mcpServers, name)
		case contracts.CapabilitySkill:
			skills = append(skills, name)
		}
	}
	return tools, mcpServers, skills
}
