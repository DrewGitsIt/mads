package api

import (
	"errors"
	"net/http"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/providers/provider"
	"github.com/drewkidwell/mads/services/router/internal/modelcatalog"
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

// providerInfo is one row of the Settings Providers panel's capability
// matrix table (MADS-UX §11): the provider's declared support per behavior,
// plus its declared enforcement level per portable permission intent
// (Intents is nil for a provider that doesn't implement IntentEnforcer —
// it has taken no stance on any intent).
type providerInfo struct {
	Name    string                `json:"name"`
	Matrix  provider.Matrix       `json:"matrix"`
	Intents provider.IntentMatrix `json:"intents,omitempty"`
}

func (s *server) handleListProviders(w http.ResponseWriter, r *http.Request) {
	names := s.providers.Names()
	out := make([]providerInfo, 0, len(names))
	for _, name := range names {
		p, err := s.providers.Get(name)
		if err != nil {
			continue
		}
		info := providerInfo{Name: name, Matrix: p.Matrix()}
		if enforcer, ok := p.(provider.IntentEnforcer); ok {
			info.Intents = enforcer.IntentMatrix()
		}
		out = append(out, info)
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
		writeJSON(w, http.StatusOK, contracts.ModelsResponse{Models: []contracts.Model{}})
		return
	}
	if s.models != nil {
		writeCatalogEntry(w, s.models.Get(r.Context(), r.PathValue("name")))
		return
	}
	// No cache configured: the pre-cache path, discovery inline.
	models, err := discoverer.Models(r.Context())
	if err != nil {
		if errors.Is(err, provider.ErrDiscoveryUnavailable) {
			writeJSON(w, http.StatusOK, contracts.ModelsResponse{Models: []contracts.Model{}, Detail: err.Error()})
			return
		}
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, contracts.ModelsResponse{Models: toContractModels(models)})
}

// handleRefreshProviderModels forces a re-read of one provider's catalog.
// The affordance exists because catalogs change for reasons the Router
// cannot observe — a CLI upgraded, an account logged in, `goose configure`
// switching the backing provider and with it the whole model list — and
// guessing at those beats waiting for a TTL only by accident.
func (s *server) handleRefreshProviderModels(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	p, err := s.providers.Get(name)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	if _, ok := p.(provider.ModelDiscoverer); !ok {
		writeJSON(w, http.StatusOK, contracts.ModelsResponse{Models: []contracts.Model{}})
		return
	}
	if s.models == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "model catalog not configured"})
		return
	}
	writeCatalogEntry(w, s.models.Refresh(r.Context(), name))
}

// writeCatalogEntry renders a cache entry, preserving the pre-cache wire
// contract exactly: an expected discovery failure is 200 with an empty
// list plus Detail (never an error status), and only an UNEXPECTED failure
// 500s, so a real bug stays visible instead of degrading into an empty
// picker.
func writeCatalogEntry(w http.ResponseWriter, e modelcatalog.Entry) {
	if e.Err != nil {
		writeError(w, e.Err)
		return
	}
	resp := contracts.ModelsResponse{
		Models: toContractModels(e.Models),
		Detail: e.Detail,
		State:  string(e.State),
	}
	if !e.FetchedAt.IsZero() {
		resp.FetchedAt = e.FetchedAt.UTC().Format(time.RFC3339)
	}
	writeJSON(w, http.StatusOK, resp)
}

func toContractModels(models []provider.Model) []contracts.Model {
	out := make([]contracts.Model, len(models))
	for i, m := range models {
		out[i] = contracts.Model{ID: m.ID, Name: m.Name, Default: m.Default}
	}
	return out
}
