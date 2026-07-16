// Package registrytest is an in-memory fixture registry server implementing
// the registryclient wire contract, for tests. There is no real remote
// registry anywhere; this is the "local test registry server" work item
// W7.6 requires, and doubles as the executable spec of what a real registry
// would need to serve. No _test.go suffix so other packages' test files can
// import it, matching services/data/datatest.
package registrytest

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/drewkidwell/mads/pkg/registryclient"
)

type key struct {
	Type registryclient.ItemType
	Name string
}

// Server is an in-memory registry implementing the registryclient wire
// contract (GET /index, GET /bundles/{type}/{name}/{version}, POST /bundles).
type Server struct {
	mu    sync.Mutex
	items map[key][]registryclient.Bundle // versions, oldest first
}

// New builds a Server, optionally pre-seeded with bundles. Each seeded
// bundle is published in call order, so repeats of the same (type, name)
// become successive versions.
func New(seed ...registryclient.Bundle) *Server {
	s := &Server{items: map[key][]registryclient.Bundle{}}
	for _, b := range seed {
		s.publish(b)
	}
	return s
}

// Start spins up an httptest.Server over a fresh in-memory registry and
// returns its URL.
func Start(t *testing.T, seed ...registryclient.Bundle) string {
	t.Helper()
	srv := httptest.NewServer(New(seed...))
	t.Cleanup(srv.Close)
	return srv.URL
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /index", s.handleIndex)
	mux.HandleFunc("GET /bundles/{type}/{name}/{version}", s.handleGetBundle)
	mux.HandleFunc("POST /bundles", s.handlePublish)
	mux.ServeHTTP(w, r)
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	q := strings.ToLower(r.URL.Query().Get("q"))
	s.mu.Lock()
	defer s.mu.Unlock()

	out := []registryclient.IndexEntry{}
	for k, versions := range s.items {
		latest := versions[len(versions)-1]
		desc := itemDescription(latest)
		if q != "" && !strings.Contains(strings.ToLower(k.Name), q) && !strings.Contains(strings.ToLower(desc), q) {
			continue
		}
		vs := make([]string, len(versions))
		for i, b := range versions {
			vs[i] = b.Version
		}
		out = append(out, registryclient.IndexEntry{
			Type: k.Type, Name: k.Name, Description: desc, Versions: vs, Latest: vs[len(vs)-1],
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleGetBundle(w http.ResponseWriter, r *http.Request) {
	t := registryclient.ItemType(r.PathValue("type"))
	name := r.PathValue("name")
	version := r.PathValue("version")

	s.mu.Lock()
	defer s.mu.Unlock()
	versions, ok := s.items[key{Type: t, Name: name}]
	if !ok {
		http.NotFound(w, r)
		return
	}
	if version == "latest" {
		writeJSON(w, http.StatusOK, versions[len(versions)-1])
		return
	}
	for _, b := range versions {
		if b.Version == version {
			writeJSON(w, http.StatusOK, b)
			return
		}
	}
	http.NotFound(w, r)
}

func (s *Server) handlePublish(w http.ResponseWriter, r *http.Request) {
	var b registryclient.Bundle
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if verr := b.Validate(); verr != nil {
		writeJSON(w, http.StatusUnprocessableEntity, verr)
		return
	}
	s.mu.Lock()
	out := s.publish(b)
	s.mu.Unlock()
	writeJSON(w, http.StatusCreated, out)
}

// publish appends b as the next version of its (type, name), re-deriving
// Name from the embedded item rather than trusting the request's Name.
// Caller must hold s.mu (or be New's constructor, single-threaded).
func (s *Server) publish(b registryclient.Bundle) registryclient.Bundle {
	b.Name = itemName(b)
	k := key{Type: b.Type, Name: b.Name}
	b.Version = strconv.Itoa(len(s.items[k]) + 1)
	s.items[k] = append(s.items[k], b)
	return b
}

func itemName(b registryclient.Bundle) string {
	switch b.Type {
	case registryclient.ItemAgent:
		if b.Agent != nil {
			return b.Agent.Name
		}
	case registryclient.ItemCapability:
		if b.Capability != nil {
			return b.Capability.Name
		}
	case registryclient.ItemTemplate:
		if b.Template != nil {
			return b.Template.Name
		}
	}
	return b.Name
}

func itemDescription(b registryclient.Bundle) string {
	switch b.Type {
	case registryclient.ItemAgent:
		if b.Agent != nil {
			return b.Agent.Description
		}
	case registryclient.ItemCapability:
		if b.Capability != nil {
			return b.Capability.Description
		}
	case registryclient.ItemTemplate:
		if b.Template != nil {
			return b.Template.Description
		}
	}
	return ""
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
