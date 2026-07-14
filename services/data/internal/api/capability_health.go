package api

import (
	"net/http"

	"github.com/drewkidwell/mads/services/data/internal/store"
)

// handleSetCapabilityHealth records a capability's health check result
// (spec §12.6). Reason must never carry a credential value (spec §13) — the
// caller (the capability runtime) is responsible for that, this handler only
// stores and forwards what it's given.
func (s *server) handleSetCapabilityHealth(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Healthy bool   `json:"healthy"`
		Reason  string `json:"reason,omitempty"`
	}
	if !decode(w, r, &body) {
		return
	}
	updated, err := store.SetCapabilityHealth(r.Context(), s.caps, r.PathValue("id"), body.Healthy, body.Reason, actor(r))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, updated)
}
