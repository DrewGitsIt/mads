package agentselect

import "github.com/drewkidwell/mads/pkg/contracts"

// BuildRequest is the single place that turns work context (a task's
// title/description/labels, or a session's opening prompt used as
// description with an empty title) plus a resolved capability-name list
// into a Request — the shared population path pipeline agent:auto stages
// and interactive session-start agent auto both go through (owner
// decision, 2026-07-30: one inference mechanism feeding one Request
// population path, two call sites — never two divergent heuristics for
// "which agent do I need"). capabilities is expanded against registry via
// ExpandCapabilities; callers decide what that list contains (a task's own
// EffectiveCapabilities, or inference's suggested names).
func BuildRequest(taskID, sessionID, stage, title, description string, labels, capabilities []string, registry []contracts.Capability) Request {
	tools, mcpServers, skills := ExpandCapabilities(capabilities, registry)
	return Request{
		TaskID:      taskID,
		SessionID:   sessionID,
		Stage:       stage,
		Title:       title,
		Description: description,
		Labels:      labels,

		RequiredTools:      tools,
		RequiredMCPServers: mcpServers,
		RequiredSkills:     skills,
	}
}
