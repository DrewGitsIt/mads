// Package api is the Router's HTTP surface (spec §15) — the UI's single
// entry point for session and pipeline lifecycle. Every mutation goes
// through the Supervisor (child process spawn/kill) and the Data Service
// (via dataclient); the Router holds no state of its own beyond the
// Supervisor's in-memory child registry.
package api

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/providers/provider"
	"github.com/drewkidwell/mads/services/router/internal/job"
	"github.com/drewkidwell/mads/services/router/internal/supervisor"
)

type server struct {
	data      *dataclient.Client
	sup       *supervisor.Supervisor
	jobs      *job.Runner
	providers *provider.Registry
}

// New returns the Router's HTTP handler.
func New(data *dataclient.Client, sup *supervisor.Supervisor, jobs *job.Runner, providers *provider.Registry) http.Handler {
	s := &server{data: data, sup: sup, jobs: jobs, providers: providers}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", s.handleHealth)
	mux.HandleFunc("POST /sessions", s.handleCreateSession)
	mux.HandleFunc("POST /sessions/{id}/terminate", s.handleTerminateSession)
	mux.HandleFunc("POST /sessions/{id}/bounce", s.handleBounceSession)
	mux.HandleFunc("POST /pipelines", s.handleStartPipeline)
	mux.HandleFunc("POST /pipelines/{id}/cancel", s.handleCancelPipeline)
	mux.HandleFunc("GET /events", s.handleStreamEvents)
	mux.HandleFunc("GET /sessions/{id}/output", s.handleSessionOutput)
	mux.HandleFunc("GET /pipelines/{id}/output", s.handlePipelineOutput)
	mux.HandleFunc("POST /agents/generate", s.handleGenerateAgent)
	mux.HandleFunc("POST /retros", s.handleStartRetro)
	mux.HandleFunc("POST /jobs/{id}/cancel", s.handleCancelJob)
	mux.HandleFunc("POST /retros/{id}/follow-up-session", s.handleRetroFollowUpSession)
	mux.HandleFunc("GET /sessions", s.handleListSessions)
	mux.HandleFunc("GET /workspaces", s.handleListWorkspaces)
	mux.HandleFunc("GET /agents", s.handleListAgents)
	mux.HandleFunc("GET /tasks", s.handleListTasks)
	mux.HandleFunc("POST /tasks/{id}/retry", s.handleRetryTask)
	mux.HandleFunc("GET /providers", s.handleListProviders)
	mux.HandleFunc("GET /providers/{name}/models", s.handleListProviderModels)
	return mux
}

func (s *server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"service": "router", "status": "ok"})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("api: encode response: %v", err)
	}
}

func writeErrMsg(w http.ResponseWriter, code int, format string, err error) {
	writeJSON(w, code, map[string]string{"error": format + err.Error()})
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON: " + err.Error()})
		return false
	}
	return true
}

// writeError maps dataclient's typed errors onto the Router's API contract.
// Deliberately not the Data Service's own convention (422 for validation
// failures, per apierror.go) — the Router treats a rejected request the same
// whether the rejection came from its own decoding or from a proxied
// dataclient call, so validation failures are a flat 400 here.
func writeError(w http.ResponseWriter, err error) {
	var verr *contracts.ValidationError
	switch {
	case errors.As(err, &verr):
		writeJSON(w, http.StatusBadRequest, verr)
	case errors.Is(err, dataclient.ErrNotFound):
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
	case errors.Is(err, dataclient.ErrConflict):
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
	default:
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
}

func mustJSON(v any) json.RawMessage {
	raw, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return raw
}
