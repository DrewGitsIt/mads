package api

import (
	"context"
	"log"
	"net/http"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/services/router/internal/job"
)

// handleStartRetro spawns a retro job over the given sessions (spec §7). The
// retro ID is minted before Spawn runs so it can be handed back immediately
// and so every retro.* event the job body emits carries it for correlation.
func (s *server) handleStartRetro(w http.ResponseWriter, r *http.Request) {
	var body struct {
		SessionIDs       []string `json:"session_ids"`
		UserObservations string   `json:"user_observations"`
	}
	if !decode(w, r, &body) {
		return
	}
	if len(body.SessionIDs) == 0 {
		var verr contracts.ValidationError
		verr.Add("session_ids", "required")
		writeJSON(w, http.StatusBadRequest, verr)
		return
	}

	retroID := job.NewRetroID()
	jobID, err := s.jobs.Spawn(r.Context(), job.RetroKind, "", job.GenerateRetro(retroID, job.RetroRequest{
		SessionIDs: body.SessionIDs, UserObservations: body.UserObservations,
	}))
	if err != nil {
		writeErrMsg(w, http.StatusBadGateway, "spawn retro: ", err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"job_id": jobID, "retro_id": retroID})
}

// handleCancelJob cancels any running job by ID — generic, not retro-specific,
// so any future cancellable job kind gets it for free.
func (s *server) handleCancelJob(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !s.jobs.Cancel(id) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "job not found or already finished: " + id})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleRetroFollowUpSession turns a retro's non-rejected DPAs into a new
// session's tasks (spec §7): one task per DPA, traceable back to it via
// DPAID. Mirrors handleCreateSession's spawn-then-rollback-on-failure
// pattern.
func (s *server) handleRetroFollowUpSession(w http.ResponseWriter, r *http.Request) {
	retroID := r.PathValue("id")
	retro, err := s.data.Retro(r.Context(), retroID)
	if err != nil {
		writeError(w, err)
		return
	}

	var tasks []contracts.Task
	for _, dpa := range retro.DPAs {
		if dpa.Status == contracts.DPARejected {
			continue
		}
		tasks = append(tasks, contracts.Task{
			Title:              dpa.Problem,
			Description:        "Root cause: " + dpa.RootCause + "\n\nEvidence: " + dpa.Evidence,
			AcceptanceCriteria: dpa.SuggestedAction,
			Source:             "retro",
			DPAID:              dpa.ID,
		})
	}
	if len(tasks) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "no accepted DPAs to create tasks from"})
		return
	}

	sess := contracts.Session{Name: "Retro follow-up: " + retroID, Tags: []string{"retro"}, RetroID: retroID}
	if err := s.data.CreateSession(r.Context(), &sess); err != nil {
		writeError(w, err)
		return
	}
	addr, err := s.sup.SpawnSession(r.Context(), sess.ID)
	if err != nil {
		_ = s.data.DeleteSession(context.Background(), sess.ID)
		writeErrMsg(w, http.StatusBadGateway, "spawn session: ", err)
		return
	}

	for i := range tasks {
		tasks[i].SessionID = sess.ID
	}
	created, err := s.data.ImportTasks(r.Context(), tasks)
	if err != nil {
		writeError(w, err)
		return
	}

	// Log-and-continue: the session and tasks already exist and are the
	// valuable part of this response; failing to link them back to the retro
	// isn't worth failing the whole request over.
	if err := s.data.SetRetroFollowUpSession(r.Context(), retroID, sess.ID); err != nil {
		log.Printf("api: link retro %s to follow-up session %s: %v", retroID, sess.ID, err)
	}

	writeJSON(w, http.StatusCreated, map[string]any{"session": sess, "addr": addr, "tasks": created})
}
