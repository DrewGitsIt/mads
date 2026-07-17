package api

import (
	"net/http"

	"github.com/drewkidwell/mads/pkg/contracts"
)

// requireLinkedSessions 404s while the linked-sessions flag is off — every
// route this feature exposes is gated identically
// (docs/LINKED-SESSIONS-DESIGN.md: "Everything ships behind the
// linked-sessions flag"). Gating lives only in the Router; the Data Service
// stays a flag-agnostic persistence layer, consistent with how no other
// feature flag is enforced below the Router today.
func (s *server) requireLinkedSessions(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cfg, err := s.data.Config(r.Context())
		if err != nil {
			writeError(w, err)
			return
		}
		if !cfg.FeatureFlags.LinkedSessions {
			http.NotFound(w, r)
			return
		}
		next(w, r)
	}
}

func (s *server) handleCreateLink(w http.ResponseWriter, r *http.Request) {
	var req contracts.LinkRequest
	if !decode(w, r, &req) {
		return
	}
	link, err := s.data.CreateLink(r.Context(), req)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, link)
}

func (s *server) handleGetLink(w http.ResponseWriter, r *http.Request) {
	link, err := s.data.Link(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, link)
}

func (s *server) handleUpdateLink(w http.ResponseWriter, r *http.Request) {
	var req contracts.LinkRequest
	if !decode(w, r, &req) {
		return
	}
	link, err := s.data.UpdateLink(r.Context(), r.PathValue("id"), req)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, link)
}

func (s *server) handleDeleteLink(w http.ResponseWriter, r *http.Request) {
	if err := s.data.DeleteLink(r.Context(), r.PathValue("id")); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func (s *server) handleListSessionLinks(w http.ResponseWriter, r *http.Request) {
	list, err := s.data.LinksForSession(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}
