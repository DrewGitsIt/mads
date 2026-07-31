package agentselect

import (
	"fmt"
	"strings"

	"github.com/drewkidwell/mads/pkg/contracts"
)

// missingCapabilities names every required tool/MCP server/skill agent
// doesn't declare — the detail behind a "capability" elimination (spec §9),
// surfaced in ScoreBreakdown.Eliminated and stage-failure reasons so a user
// learns what to add to which agent instead of seeing a bare failure.
func missingCapabilities(req Request, agent contracts.Agent) []string {
	var missing []string
	missing = append(missing, missingFrom(agent.Capabilities.Tools, req.RequiredTools)...)
	missing = append(missing, missingFrom(agent.Capabilities.MCPServers, req.RequiredMCPServers)...)
	missing = append(missing, missingFrom(agent.Capabilities.Skills, req.RequiredSkills)...)
	return missing
}

func missingFrom(have, need []string) []string {
	if len(need) == 0 {
		return nil
	}
	set := make(map[string]struct{}, len(have))
	for _, h := range have {
		set[h] = struct{}{}
	}
	var missing []string
	for _, n := range need {
		if _, ok := set[n]; !ok {
			missing = append(missing, n)
		}
	}
	return missing
}

// eliminationReason applies the capability filter then the quadrant filter,
// in that order (spec §9: two-stage filter, capability checked first). An
// agent with no declared quadrant (the system synthesizer/agent-builder
// agents) never matches any quadrant and is always eliminated here.
func eliminationReason(req Request, quadrant contracts.Quadrant, agent contracts.Agent) string {
	if missing := missingCapabilities(req, agent); len(missing) > 0 {
		return fmt.Sprintf("capability (missing %s)", strings.Join(missing, ", "))
	}
	if agent.Quadrant != quadrant {
		return "quadrant"
	}
	return ""
}
