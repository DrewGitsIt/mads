package api

import (
	"net/http"

	"github.com/drewkidwell/mads/pkg/contracts"
)

func (s *server) handleListTemplates(w http.ResponseWriter, r *http.Request) {
	templates, err := s.data.Templates(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, templates)
}

func (s *server) handleGetTemplate(w http.ResponseWriter, r *http.Request) {
	t, usedBy, err := s.data.TemplateDetail(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"template": t, "used_by": usedBy})
}

func (s *server) handleCreateTemplate(w http.ResponseWriter, r *http.Request) {
	var t contracts.Template
	if !decode(w, r, &t) {
		return
	}
	if err := s.data.CreateTemplate(r.Context(), &t); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, t)
}

func (s *server) handleUpdateTemplate(w http.ResponseWriter, r *http.Request) {
	var t contracts.Template
	if !decode(w, r, &t) {
		return
	}
	t.ID = r.PathValue("id")
	if err := s.data.UpdateTemplate(r.Context(), &t); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func (s *server) handleDeleteTemplate(w http.ResponseWriter, r *http.Request) {
	if err := s.data.DeleteTemplate(r.Context(), r.PathValue("id")); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func (s *server) handleValidateTemplate(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Source string `json:"source"`
	}
	if !decode(w, r, &body) {
		return
	}
	if err := s.data.ValidateTemplate(r.Context(), body.Source); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"valid": true})
}

// handleValidateAllTemplates proxies the Data Service's one-time-upgrade
// discovery report straight through: dataclient.TemplateValidation already
// mirrors the exact {id,name,valid,errors} shape the UI client is built
// against, so there is nothing to reshape here, only to relay. A store-level
// failure (e.g. the Data Service is unreachable) still goes through
// writeError like every other proxy in this file — only an individual
// template being invalid is "200 with a finding," not the request itself.
func (s *server) handleValidateAllTemplates(w http.ResponseWriter, r *http.Request) {
	results, err := s.data.ValidateAllTemplates(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"results": results})
}
