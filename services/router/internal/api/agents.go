package api

import (
	"net/http"

	"github.com/drewkidwell/mads/pkg/agentgen"
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

func (s *server) handleGetAgent(w http.ResponseWriter, r *http.Request) {
	a, err := s.data.Agent(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, a)
}

func (s *server) handleCreateAgent(w http.ResponseWriter, r *http.Request) {
	var a contracts.Agent
	if !decode(w, r, &a) {
		return
	}
	if err := s.data.CreateAgent(r.Context(), &a); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, a)
}

func (s *server) handleUpdateAgent(w http.ResponseWriter, r *http.Request) {
	var a contracts.Agent
	if !decode(w, r, &a) {
		return
	}
	a.ID = r.PathValue("id")
	if err := s.data.UpdateAgent(r.Context(), &a); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, a)
}

func (s *server) handleDeleteAgent(w http.ResponseWriter, r *http.Request) {
	if err := s.data.DeleteAgent(r.Context(), r.PathValue("id")); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// handleDescribeAgent, handleSuggestCapabilities and handleRefineAgent are
// the Library editor's three enrichment actions (spec §9.5). Unlike
// /agents/generate, these are fast, side-effect-free, single-call Fast
// Model requests, so they run synchronously rather than through job.Runner
// — no result event to plumb, the response carries the result directly.

func (s *server) handleDescribeAgent(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Prompt string `json:"prompt"`
	}
	if !decode(w, r, &body) {
		return
	}
	result, err := agentgen.New(s.data, s.jobs.FastModel()).Describe(r.Context(), body.Prompt)
	if err != nil {
		writeErrMsg(w, http.StatusBadGateway, "describe agent: ", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"result": result})
}

func (s *server) handleSuggestCapabilities(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Prompt string `json:"prompt"`
	}
	if !decode(w, r, &body) {
		return
	}
	result, err := agentgen.New(s.data, s.jobs.FastModel()).SuggestCapabilities(r.Context(), body.Prompt)
	if err != nil {
		writeErrMsg(w, http.StatusBadGateway, "suggest capabilities: ", err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *server) handleRefineAgent(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Prompt      string `json:"prompt"`
		Instruction string `json:"instruction"`
	}
	if !decode(w, r, &body) {
		return
	}
	result, err := agentgen.New(s.data, s.jobs.FastModel()).Refine(r.Context(), body.Prompt, body.Instruction)
	if err != nil {
		writeErrMsg(w, http.StatusBadGateway, "refine agent: ", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"result": result})
}
