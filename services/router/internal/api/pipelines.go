package api

import (
	"log"
	"net/http"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/services/router/internal/supervisor"
)

func (s *server) handleStartPipeline(w http.ResponseWriter, r *http.Request) {
	var body struct {
		SessionID  string `json:"session_id"`
		TemplateID string `json:"template_id"`
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

	runID := supervisor.NewRunID()
	addr, err := s.sup.SpawnPipeline(r.Context(), runID, body.SessionID)
	if err != nil {
		writeErrMsg(w, http.StatusBadGateway, "spawn pipeline: ", err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"run_id": runID, "addr": addr})
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
