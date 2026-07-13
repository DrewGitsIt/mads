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
	s.proxyOutput(w, r, addr)
}

func (s *server) handlePipelineOutput(w http.ResponseWriter, r *http.Request) {
	addr, ok := s.sup.PipelineAddr(r.PathValue("id"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "pipeline not running"})
		return
	}
	s.proxyOutput(w, r, addr)
}

func (s *server) proxyOutput(w http.ResponseWriter, r *http.Request, addr string) {
	target := &url.URL{Scheme: "http", Host: addr}
	proxy := httputil.NewSingleHostReverseProxy(target)
	r2 := r.Clone(r.Context())
	r2.URL.Path = "/output"
	r2.URL.RawPath = ""
	proxy.ServeHTTP(w, r2)
}
