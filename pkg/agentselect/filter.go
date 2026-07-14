package agentselect

import "github.com/drewkidwell/mads/pkg/contracts"

// hasCapabilities reports whether agent declares a superset of the task's
// required tools, MCP servers, and skills (spec §9: fast set-intersection).
func hasCapabilities(req Request, agent contracts.Agent) bool {
	return supersetOf(agent.Capabilities.Tools, req.RequiredTools) &&
		supersetOf(agent.Capabilities.MCPServers, req.RequiredMCPServers) &&
		supersetOf(agent.Capabilities.Skills, req.RequiredSkills)
}

func supersetOf(have, need []string) bool {
	if len(need) == 0 {
		return true
	}
	set := make(map[string]struct{}, len(have))
	for _, h := range have {
		set[h] = struct{}{}
	}
	for _, n := range need {
		if _, ok := set[n]; !ok {
			return false
		}
	}
	return true
}

// eliminationReason applies the capability filter then the quadrant filter,
// in that order (spec §9: two-stage filter, capability checked first). An
// agent with no declared quadrant (the system synthesizer/agent-builder
// agents) never matches any quadrant and is always eliminated here.
func eliminationReason(req Request, quadrant contracts.Quadrant, agent contracts.Agent) string {
	if !hasCapabilities(req, agent) {
		return "capability"
	}
	if agent.Quadrant != quadrant {
		return "quadrant"
	}
	return ""
}
