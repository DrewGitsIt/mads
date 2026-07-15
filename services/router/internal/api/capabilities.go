package api

import (
	"net/http"

	"github.com/drewkidwell/mads/pkg/contracts"
)

func (s *server) handleListCapabilities(w http.ResponseWriter, r *http.Request) {
	caps, err := s.data.Capabilities(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, caps)
}

func (s *server) handleGetCapability(w http.ResponseWriter, r *http.Request) {
	c, err := s.data.Capability(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, c)
}

func (s *server) handleCreateCapability(w http.ResponseWriter, r *http.Request) {
	var c contracts.Capability
	if !decode(w, r, &c) {
		return
	}
	if err := s.data.CreateCapability(r.Context(), &c); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, c)
}

func (s *server) handleUpdateCapability(w http.ResponseWriter, r *http.Request) {
	var c contracts.Capability
	if !decode(w, r, &c) {
		return
	}
	c.ID = r.PathValue("id")
	if err := s.data.UpdateCapability(r.Context(), &c); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, c)
}

func (s *server) handleDeleteCapability(w http.ResponseWriter, r *http.Request) {
	if err := s.data.DeleteCapability(r.Context(), r.PathValue("id")); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// handleDiagnoseCapability runs a one-off health check plus troubleshooting
// suggestions (spec §12.7) for the Library's Diagnose panel.
func (s *server) handleDiagnoseCapability(w http.ResponseWriter, r *http.Request) {
	health, suggestions, err := s.data.DiagnoseCapability(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"healthy": health.Healthy, "reason": health.Reason, "suggestions": suggestions,
	})
}
