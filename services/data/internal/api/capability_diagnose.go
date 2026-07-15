package api

import (
	"net/http"

	"github.com/drewkidwell/mads/pkg/capability"
)

// handleDiagnoseCapability runs a one-off health check plus troubleshooting
// suggestions (spec §12.7) for the Library's Diagnose panel — a heavier,
// on-demand sibling of handleSetCapabilityHealth that doesn't persist
// anything. Called with no session context, so a local-command MCP
// capability's "running" state is always reported false (see
// pkg/capability.Health) — accurate for skills and remote MCP, a documented
// limitation for local-command MCP.
func (s *server) handleDiagnoseCapability(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("id")
	cap, err := s.caps.Get(r.Context(), key)
	if err != nil {
		cap, err = s.caps.GetByName(r.Context(), key)
	}
	if err != nil {
		writeStoreError(w, err)
		return
	}
	healthy, reason := capability.Health(r.Context(), *cap, false)
	writeJSON(w, http.StatusOK, map[string]any{
		"healthy":     healthy,
		"reason":      reason,
		"suggestions": capability.Diagnose(*cap, healthy, reason),
	})
}
