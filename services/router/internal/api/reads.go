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

func (s *server) handleListAgents(w http.ResponseWriter, r *http.Request) {
	agents, err := s.data.Agents(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, agents)
}

// providerInfo is one row of the Settings Providers panel's capability
// matrix table (MADS-UX §11): the provider's declared support per behavior.
type providerInfo struct {
	Name   string          `json:"name"`
	Matrix provider.Matrix `json:"matrix"`
}

func (s *server) handleListProviders(w http.ResponseWriter, r *http.Request) {
	names := s.providers.Names()
	out := make([]providerInfo, 0, len(names))
	for _, name := range names {
		p, err := s.providers.Get(name)
		if err != nil {
			continue
		}
		out = append(out, providerInfo{Name: name, Matrix: p.Matrix()})
	}
	writeJSON(w, http.StatusOK, out)
}

// behaviorInfo is one row of the matrix legend: the behavior and the
// platform's requirement level for it (spec §11).
type behaviorInfo struct {
	Behavior provider.Behavior `json:"behavior"`
	Level    provider.Level    `json:"level"`
}

func (s *server) handleListProviderBehaviors(w http.ResponseWriter, _ *http.Request) {
	behaviors := provider.Behaviors()
	out := make([]behaviorInfo, 0, len(behaviors))
	for _, b := range behaviors {
		out = append(out, behaviorInfo{Behavior: b, Level: provider.Requirement(b)})
	}
	writeJSON(w, http.StatusOK, out)
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
