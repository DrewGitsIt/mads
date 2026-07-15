package api

import (
	"net/http"

	"github.com/drewkidwell/mads/pkg/contracts"
)

func (s *server) handleGetWorkspace(w http.ResponseWriter, r *http.Request) {
	ws, err := s.data.Workspace(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, ws)
}

func (s *server) handleCreateWorkspace(w http.ResponseWriter, r *http.Request) {
	var ws contracts.Workspace
	if !decode(w, r, &ws) {
		return
	}
	if err := s.data.CreateWorkspace(r.Context(), &ws); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, ws)
}

func (s *server) handleUpdateWorkspace(w http.ResponseWriter, r *http.Request) {
	var ws contracts.Workspace
	if !decode(w, r, &ws) {
		return
	}
	ws.ID = r.PathValue("id")
	if err := s.data.UpdateWorkspace(r.Context(), &ws); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, ws)
}

func (s *server) handleDeleteWorkspace(w http.ResponseWriter, r *http.Request) {
	if err := s.data.DeleteWorkspace(r.Context(), r.PathValue("id")); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}
