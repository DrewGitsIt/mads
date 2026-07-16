package api

import (
	"net/http"
	"net/http/httputil"
	"net/url"
)

// handleSessionOutput and handlePipelineOutput forward the UI's data-plane
// subscription (spec §15) to a running child's live agent-output stream.
// This traffic is never persisted: it is byte-transparent reverse-proxied
// straight from the child's /output to the UI connection and never touches
// s.data (the Data Service is not on this path at all).
func (s *server) handleSessionOutput(w http.ResponseWriter, r *http.Request) {
	addr, ok := s.sup.SessionAddr(r.PathValue("id"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "session not running"})
		return
	}
	s.proxyTo(w, r, addr, "/output")
}

func (s *server) handlePipelineOutput(w http.ResponseWriter, r *http.Request) {
	addr, ok := s.sup.PipelineAddr(r.PathValue("id"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "pipeline not running"})
		return
	}
	s.proxyTo(w, r, addr, "/output")
}

// handleSessionMessages and handleSessionInterrupt forward a conversation
// turn's control plane (send / interrupt) to the running session child's own
// POST /messages and POST /interrupt — the child already implements both
// (services/session/internal/api/api.go); the Router previously had no
// route for either, a documented gap (route_manifest_test.go's
// knownGapRoutes). Request and response bodies pass through unmodified —
// the child owns turn semantics (busy/409, not-ready/503), the Router is
// just the address lookup in front of it.
func (s *server) handleSessionMessages(w http.ResponseWriter, r *http.Request) {
	addr, ok := s.sup.SessionAddr(r.PathValue("id"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "session not running"})
		return
	}
	s.proxyTo(w, r, addr, "/messages")
}

func (s *server) handleSessionInterrupt(w http.ResponseWriter, r *http.Request) {
	addr, ok := s.sup.SessionAddr(r.PathValue("id"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "session not running"})
		return
	}
	s.proxyTo(w, r, addr, "/interrupt")
}

// proxyTo reverse-proxies r to addr, rewriting the path to childPath —
// shared by the streaming /output forwarders above and the JSON /messages
// and /interrupt forwarders (httputil.ReverseProxy handles both request
// shapes identically; it isn't SSE-specific).
func (s *server) proxyTo(w http.ResponseWriter, r *http.Request, addr, childPath string) {
	target := &url.URL{Scheme: "http", Host: addr}
	proxy := httputil.NewSingleHostReverseProxy(target)
	r2 := r.Clone(r.Context())
	r2.URL.Path = childPath
	r2.URL.RawPath = ""
	proxy.ServeHTTP(w, r2)
}
