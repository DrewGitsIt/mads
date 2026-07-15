package api

import (
	"net/http"

	"github.com/drewkidwell/mads/pkg/contracts"
)

// handleMetricsCost answers GET /metrics/cost, proxying the Data Service's
// own route (spec §8). session_id is optional; empty means every session.
func (s *server) handleMetricsCost(w http.ResponseWriter, r *http.Request) {
	m, err := s.data.MetricsCost(r.Context(), r.URL.Query().Get("session_id"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, m)
}

// handleMetricsAgentLeaderboard answers GET /metrics/agents: each agent's
// usage, success rate, and retries, system-wide.
func (s *server) handleMetricsAgentLeaderboard(w http.ResponseWriter, r *http.Request) {
	entries, err := s.data.MetricsAgentLeaderboard(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	if entries == nil {
		entries = []contracts.AgentLeaderboardEntry{}
	}
	writeJSON(w, http.StatusOK, entries)
}

// handleMetricsTaskOutcomes answers GET /metrics/tasks: the confirmed-task
// status distribution. session_id is optional; empty means every session.
func (s *server) handleMetricsTaskOutcomes(w http.ResponseWriter, r *http.Request) {
	out, err := s.data.MetricsTaskOutcomes(r.Context(), r.URL.Query().Get("session_id"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handleMetricsCapabilityUsage answers GET /metrics/capabilities: every
// registered capability's invocation count, including never-used ones.
func (s *server) handleMetricsCapabilityUsage(w http.ResponseWriter, r *http.Request) {
	out, err := s.data.MetricsCapabilityUsage(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	if out == nil {
		out = []contracts.CapabilityUsage{}
	}
	writeJSON(w, http.StatusOK, out)
}

// handleMetricsPipelineRuns answers GET /metrics/pipelines: reconstructed
// pipeline run history. session_id is optional; empty means every session.
func (s *server) handleMetricsPipelineRuns(w http.ResponseWriter, r *http.Request) {
	out, err := s.data.MetricsPipelineRuns(r.Context(), r.URL.Query().Get("session_id"))
	if err != nil {
		writeError(w, err)
		return
	}
	if out == nil {
		out = []contracts.PipelineRun{}
	}
	writeJSON(w, http.StatusOK, out)
}
