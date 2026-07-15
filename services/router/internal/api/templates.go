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
