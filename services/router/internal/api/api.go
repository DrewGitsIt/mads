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
	"github.com/drewkidwell/mads/pkg/credstore"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/pkg/preflight"
	"github.com/drewkidwell/mads/pkg/vault"
	"github.com/drewkidwell/mads/providers/provider"
	"github.com/drewkidwell/mads/services/router/internal/job"
	"github.com/drewkidwell/mads/services/router/internal/supervisor"
)

type server struct {
	data         *dataclient.Client
	sup          *supervisor.Supervisor
	jobs         *job.Runner
	providers    *provider.Registry
	creds        *credstore.Store
	preflight    preflight.Checker
	vaultRefs    *vault.RefStore
	vaultSources []vault.Source
}

// New returns the Router's HTTP handler. creds may be nil (credential
// endpoints degrade to 503); a nil preflightChecker defaults to
// preflight.StubChecker{} — W7.3's real implementation does not exist yet.
// vaultRefs may be nil (vault-ref endpoints degrade to 503); vaultSources may
// be nil or empty (GET /credentials/vault-sources reports no sources).
func New(data *dataclient.Client, sup *supervisor.Supervisor, jobs *job.Runner, providers *provider.Registry, creds *credstore.Store, preflightChecker preflight.Checker, vaultRefs *vault.RefStore, vaultSources []vault.Source) http.Handler {
	if preflightChecker == nil {
		preflightChecker = preflight.StubChecker{}
	}
	s := &server{data: data, sup: sup, jobs: jobs, providers: providers, creds: creds, preflight: preflightChecker, vaultRefs: vaultRefs, vaultSources: vaultSources}
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
	mux.HandleFunc("GET /agents/{id}", s.handleGetAgent)
	mux.HandleFunc("POST /agents", s.handleCreateAgent)
	mux.HandleFunc("PUT /agents/{id}", s.handleUpdateAgent)
	mux.HandleFunc("DELETE /agents/{id}", s.handleDeleteAgent)
	mux.HandleFunc("POST /agents/describe", s.handleDescribeAgent)
	mux.HandleFunc("POST /agents/suggest-capabilities", s.handleSuggestCapabilities)
	mux.HandleFunc("POST /agents/refine", s.handleRefineAgent)
	mux.HandleFunc("GET /capabilities", s.handleListCapabilities)
	mux.HandleFunc("GET /capabilities/{id}", s.handleGetCapability)
	mux.HandleFunc("POST /capabilities", s.handleCreateCapability)
	mux.HandleFunc("PUT /capabilities/{id}", s.handleUpdateCapability)
	mux.HandleFunc("DELETE /capabilities/{id}", s.handleDeleteCapability)
	mux.HandleFunc("POST /capabilities/{id}/diagnose", s.handleDiagnoseCapability)
	mux.HandleFunc("GET /workspaces/{id}", s.handleGetWorkspace)
	mux.HandleFunc("POST /workspaces", s.handleCreateWorkspace)
	mux.HandleFunc("PUT /workspaces/{id}", s.handleUpdateWorkspace)
	mux.HandleFunc("DELETE /workspaces/{id}", s.handleDeleteWorkspace)
	mux.HandleFunc("GET /templates", s.handleListTemplates)
	mux.HandleFunc("GET /templates/{id}", s.handleGetTemplate)
	mux.HandleFunc("POST /templates", s.handleCreateTemplate)
	mux.HandleFunc("PUT /templates/{id}", s.handleUpdateTemplate)
	mux.HandleFunc("DELETE /templates/{id}", s.handleDeleteTemplate)
	mux.HandleFunc("POST /templates/validate", s.handleValidateTemplate)
	mux.HandleFunc("POST /retros", s.handleStartRetro)
	mux.HandleFunc("POST /jobs/{id}/cancel", s.handleCancelJob)
	mux.HandleFunc("POST /retros/{id}/follow-up-session", s.handleRetroFollowUpSession)
	mux.HandleFunc("GET /retros", s.handleListRetros)
	mux.HandleFunc("GET /retros/{id}", s.handleGetRetro)
	mux.HandleFunc("POST /retros/{id}/dpas/{dpaId}/accept", s.handleAcceptDPA)
	mux.HandleFunc("POST /retros/{id}/dpas/{dpaId}/reject", s.handleRejectDPA)
	mux.HandleFunc("POST /tasks", s.handleCreateTask)
	mux.HandleFunc("POST /tasks/import", s.handleImportTasks)
	mux.HandleFunc("GET /tasks", s.handleListTasks)
	mux.HandleFunc("GET /tasks/cycles", s.handleTaskCycles)
	mux.HandleFunc("GET /tasks/{id}", s.handleGetTask)
	mux.HandleFunc("PUT /tasks/{id}", s.handleUpdateTask)
	mux.HandleFunc("POST /tasks/{id}/confirm", s.handleConfirmTask)
	mux.HandleFunc("POST /tasks/{id}/discard", s.handleDiscardTask)
	mux.HandleFunc("POST /tasks/{id}/notes", s.handleAddNote)
	mux.HandleFunc("POST /tasks/{id}/notes/{noteId}/confirm", s.handleConfirmNote)
	mux.HandleFunc("POST /tasks/{id}/notes/{noteId}/discard", s.handleDiscardNote)
	mux.HandleFunc("GET /sessions/{id}/files", s.handleSessionFiles)
	mux.HandleFunc("GET /sessions", s.handleListSessions)
	mux.HandleFunc("GET /workspaces", s.handleListWorkspaces)
	mux.HandleFunc("GET /agents", s.handleListAgents)
	mux.HandleFunc("POST /tasks/{id}/retry", s.handleRetryTask)
	mux.HandleFunc("GET /providers", s.handleListProviders)
	mux.HandleFunc("GET /providers/behaviors", s.handleListProviderBehaviors)
	mux.HandleFunc("GET /providers/{name}/models", s.handleListProviderModels)
	mux.HandleFunc("GET /config", s.handleGetConfig)
	mux.HandleFunc("PUT /config", s.handleUpdateConfig)
	mux.HandleFunc("GET /profiles", s.handleListProfiles)
	mux.HandleFunc("POST /profiles", s.handleCreateProfile)
	mux.HandleFunc("PUT /profiles/{id}", s.handleUpdateProfile)
	mux.HandleFunc("DELETE /profiles/{id}", s.handleDeleteProfile)
	mux.HandleFunc("POST /profiles/{id}/activate", s.handleActivateProfile)
	mux.HandleFunc("GET /preflight", s.handlePreflight)
	mux.HandleFunc("GET /credentials", s.handleListCredentials)
	mux.HandleFunc("POST /credentials", s.handleSetCredential)
	mux.HandleFunc("DELETE /credentials/{name}", s.handleDeleteCredential)
	mux.HandleFunc("GET /credentials/env-candidates", s.handleCredentialEnvCandidates)
	mux.HandleFunc("POST /credentials/import", s.handleImportCredentials)
	mux.HandleFunc("GET /credentials/vault-sources", s.handleListVaultSources)
	mux.HandleFunc("GET /credentials/vault-refs", s.handleListVaultRefs)
	mux.HandleFunc("POST /credentials/vault-refs", s.handleSetVaultRef)
	mux.HandleFunc("DELETE /credentials/vault-refs/{name}", s.handleDeleteVaultRef)
	mux.HandleFunc("GET /metrics/cost", s.handleMetricsCost)
	mux.HandleFunc("GET /metrics/agents", s.handleMetricsAgentLeaderboard)
	mux.HandleFunc("GET /metrics/tasks", s.handleMetricsTaskOutcomes)
	mux.HandleFunc("GET /metrics/capabilities", s.handleMetricsCapabilityUsage)
	mux.HandleFunc("GET /metrics/pipelines", s.handleMetricsPipelineRuns)
	return withCORS(mux)
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
