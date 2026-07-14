package api

import (
	"net/http"

	"github.com/drewkidwell/mads/pkg/contracts"
)

// handleMetricsCost answers GET /metrics/cost (spec §8): usage.recorded
// events sliced by day/model, session, and pipeline run.
func (s *server) handleMetricsCost(w http.ResponseWriter, r *http.Request) {
	m, err := s.metrics.Cost(r.Context(), r.URL.Query().Get("session_id"))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, m)
}

// handleMetricsAgentLeaderboard answers GET /metrics/agents (spec §8): each
// agent's usage, success rate, and retries.
func (s *server) handleMetricsAgentLeaderboard(w http.ResponseWriter, r *http.Request) {
	entries, err := s.metrics.AgentLeaderboard(r.Context())
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if entries == nil {
		entries = []contracts.AgentLeaderboardEntry{}
	}
	writeJSON(w, http.StatusOK, entries)
}

// handleMetricsTaskOutcomes answers GET /metrics/tasks (spec §8): the
// confirmed-task status distribution.
func (s *server) handleMetricsTaskOutcomes(w http.ResponseWriter, r *http.Request) {
	out, err := s.metrics.TaskOutcomes(r.Context(), r.URL.Query().Get("session_id"))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handleMetricsCapabilityUsage answers GET /metrics/capabilities (spec §8):
// every registered capability's invocation count, including never-used ones.
func (s *server) handleMetricsCapabilityUsage(w http.ResponseWriter, r *http.Request) {
	out, err := s.metrics.CapabilityUsage(r.Context())
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if out == nil {
		out = []contracts.CapabilityUsage{}
	}
	writeJSON(w, http.StatusOK, out)
}

// handleMetricsPipelineRuns answers GET /metrics/pipelines (spec §8):
// reconstructed pipeline run history.
func (s *server) handleMetricsPipelineRuns(w http.ResponseWriter, r *http.Request) {
	out, err := s.metrics.PipelineRuns(r.Context(), r.URL.Query().Get("session_id"))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if out == nil {
		out = []contracts.PipelineRun{}
	}
	writeJSON(w, http.StatusOK, out)
}
