package api

import (
	"net/http"

	"github.com/drewkidwell/mads/pkg/agentselect"
	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/services/router/internal/job"
)

// handleGenerateAgent spawns an agent-generation job (spec §9.5–9.6): either
// task-context-driven (title set — the auto-select-found-no-match fallback)
// or NL-description-driven (the Library editor's "describe what you want"
// path). The draft is delivered asynchronously as an agent.draft_generated
// event — callers observe it via GET /events, the same spawn-then-observe
// pattern as every other job.
func (s *server) handleGenerateAgent(w http.ResponseWriter, r *http.Request) {
	var body struct {
		SessionID          string   `json:"session_id"`
		TaskID             string   `json:"task_id"`
		Title              string   `json:"title"`
		Description        string   `json:"description"`
		Labels             []string `json:"labels"`
		RequiredTools      []string `json:"required_tools"`
		RequiredMCPServers []string `json:"required_mcp_servers"`
		RequiredSkills     []string `json:"required_skills"`
	}
	if !decode(w, r, &body) {
		return
	}

	var fn job.Func
	switch {
	case body.Title != "":
		fn = job.GenerateAgentFromTask(agentselect.Request{
			TaskID: body.TaskID, SessionID: body.SessionID,
			Title: body.Title, Description: body.Description, Labels: body.Labels,
			RequiredTools: body.RequiredTools, RequiredMCPServers: body.RequiredMCPServers,
			RequiredSkills: body.RequiredSkills,
		}, body.SessionID, body.TaskID)
	case body.Description != "":
		fn = job.GenerateAgentFromDescription(body.Description, body.SessionID)
	default:
		var verr contracts.ValidationError
		verr.Add("title", "title or description is required")
		writeJSON(w, http.StatusBadRequest, verr)
		return
	}

	id, err := s.jobs.Spawn(r.Context(), job.AgentGenerationKind, body.SessionID, fn)
	if err != nil {
		writeErrMsg(w, http.StatusBadGateway, "spawn agent generation: ", err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"job_id": id})
}
