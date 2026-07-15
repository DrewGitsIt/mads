package api

import (
	"net/http"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/providers/provider"
)

func (s *server) handleListSessions(w http.ResponseWriter, r *http.Request) {
	sessions, err := s.data.Sessions(r.Context(), r.URL.Query().Get("activity"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, sessions)
}

func (s *server) handleListWorkspaces(w http.ResponseWriter, r *http.Request) {
	workspaces, err := s.data.Workspaces(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, workspaces)
}

func (s *server) handleListAgents(w http.ResponseWriter, r *http.Request) {
	agents, err := s.data.Agents(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, agents)
}

func (s *server) handleListTasks(w http.ResponseWriter, r *http.Request) {
	tasks, err := s.data.Tasks(r.Context(), r.URL.Query().Get("session_id"), contracts.TaskStatus(r.URL.Query().Get("status")))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, tasks)
}

func (s *server) handleListProviders(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.providers.Names())
}

func (s *server) handleListProviderModels(w http.ResponseWriter, r *http.Request) {
	p, err := s.providers.Get(r.PathValue("name"))
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	discoverer, ok := p.(provider.ModelDiscoverer)
	if !ok {
		writeJSON(w, http.StatusOK, []provider.Model{})
		return
	}
	models, err := discoverer.Models(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, models)
}
