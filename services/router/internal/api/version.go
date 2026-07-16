package api

import "net/http"

// VersionInfo is this Router instance's build version.
type VersionInfo struct {
	Version string `json:"version"`
}

func (s *server) handleVersion(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, VersionInfo{Version: s.version})
}
