package api

import (
	"log"
	"net/http"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/pipelinerun"
	"github.com/drewkidwell/mads/services/router/internal/supervisor"
)

// handleStartPipeline validates everything the pipeline child would
// otherwise fail on after already being spawned — no workspace, no primary
// repo, an invalid template, or no ready work — so "201 + silence" can't
// happen: a request that would leave an orphan child listening for no
// reason gets a 4xx with a human-readable reason instead.
func (s *server) handleStartPipeline(w http.ResponseWriter, r *http.Request) {
	var body struct {
		SessionID  string `json:"session_id"`
		TemplateID string `json:"template_id"`
		// ExcludeTaskIDs are tasks the run leaves out: never launched, and
		// anything depending on them is reported skipped. Their statuses
		// are untouched.
		ExcludeTaskIDs []string `json:"exclude_task_ids"`
	}
	if !decode(w, r, &body) {
		return
	}
	var verr contracts.ValidationError
	if body.SessionID == "" {
		verr.Add("session_id", "required")
	}
	if body.TemplateID == "" {
		verr.Add("template_id", "required")
	}
	if !verr.Empty() {
		writeJSON(w, http.StatusBadRequest, verr)
		return
	}

	ctx := r.Context()
	sess, err := s.data.Session(ctx, body.SessionID)
	if err != nil {
		writeError(w, err)
		return
	}
	if sess.Workspace == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "session has no workspace — pick one before starting"})
		return
	}
	ws, err := s.data.Workspace(ctx, sess.Workspace)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "session workspace " + sess.Workspace + " not found: " + err.Error()})
		return
	}
	if _, ok := ws.PrimaryRepo(); !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "workspace " + ws.Name + " has no primary repo configured"})
		return
	}
	if _, _, err := pipelinerun.LoadTemplate(ctx, s.data, body.TemplateID); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	tasks, err := s.data.Tasks(ctx, body.SessionID, "")
	if err != nil {
		writeError(w, err)
		return
	}
	inSession := make(map[string]bool, len(tasks))
	for _, t := range tasks {
		inSession[t.ID] = true
	}
	excluded := make(map[string]bool, len(body.ExcludeTaskIDs))
	for _, id := range body.ExcludeTaskIDs {
		if !inSession[id] {
			verr.Add("exclude_task_ids", "unknown task "+id+" in session "+body.SessionID)
		}
		excluded[id] = true
	}
	if !verr.Empty() {
		writeJSON(w, http.StatusBadRequest, verr)
		return
	}
	ready := false
	for _, t := range tasks {
		if t.Status == contracts.StatusReady && !t.Draft && !excluded[t.ID] {
			ready = true
			break
		}
	}
	if !ready {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "session has no ready tasks"})
		return
	}

	runID := supervisor.NewRunID()
	addr, err := s.sup.SpawnPipeline(ctx, runID, body.SessionID, body.TemplateID, body.ExcludeTaskIDs)
	if err != nil {
		writeErrMsg(w, http.StatusBadGateway, "spawn pipeline: ", err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"run_id": runID, "addr": addr})
}

// handleRetryTask resets a blocked/failed task to ready and bumps its retry
// count. The live scheduler (engine.go's "live work queue") picks it up on
// its next scan if a run is still in progress; if the run already finished,
// retry has no effect until a new run is launched — that's expected
// pipeline behavior, not something this handler works around.
func (s *server) handleRetryTask(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	task, err := s.data.Task(r.Context(), id)
	if err != nil {
		writeError(w, err)
		return
	}
	task.Status = contracts.StatusReady
	task.RetryCount++
	if err := s.data.UpdateTask(r.Context(), &task); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, task)
}

func (s *server) handleCancelPipeline(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("id")

	// Look up sessionID before terminating: TerminatePipeline's exit handling
	// forgets runID as soon as the child exits, and PipelineCancelled's
	// envelope needs a SessionID.
	sessionID, ok := s.sup.PipelineSessionID(runID)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "pipeline run not found: " + runID})
		return
	}
	if err := s.sup.TerminatePipeline(r.Context(), runID); err != nil {
		writeErrMsg(w, http.StatusBadGateway, "cancel pipeline: ", err)
		return
	}
	if err := s.data.Append(r.Context(), &contracts.Envelope{
		Type:      contracts.PipelineCancelled,
		SessionID: sessionID,
		Payload:   mustJSON(map[string]any{"run_id": runID}),
	}); err != nil {
		log.Printf("api: append pipeline.cancelled for %s: %v", runID, err)
	}
	w.WriteHeader(http.StatusNoContent)
}
