package api

import (
	"net/http"
	"os"
	"sort"
	"strings"
)

// credentialEnvSuffixes flags process env vars that look like secrets worth
// offering the first-run wizard's "import from environment" step (MADS-UX
// §3.9) — a name-only heuristic; values never leave the process this way.
var credentialEnvSuffixes = []string{"API_KEY", "TOKEN", "SECRET", "PASSWORD"}

func looksLikeCredentialName(name string) bool {
	upper := strings.ToUpper(name)
	for _, suffix := range credentialEnvSuffixes {
		if strings.HasSuffix(upper, suffix) {
			return true
		}
	}
	return false
}

// handleListCredentials returns stored env-var NAMES only — values never
// cross the wire (spec §13, MADS-UX §3.7).
func (s *server) handleListCredentials(w http.ResponseWriter, _ *http.Request) {
	if s.creds == nil {
		writeJSON(w, http.StatusOK, []string{})
		return
	}
	names, err := s.creds.Names()
	if err != nil {
		writeErrMsg(w, http.StatusInternalServerError, "credentials: ", err)
		return
	}
	sort.Strings(names)
	writeJSON(w, http.StatusOK, names)
}

// handleSetCredential stores one credential value. The value arrives only
// in this request body, from the local UI to the local store — it is never
// echoed back or logged.
func (s *server) handleSetCredential(w http.ResponseWriter, r *http.Request) {
	if s.creds == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "credential store unavailable"})
		return
	}
	var body struct {
		Name  string `json:"name"`
		Value string `json:"value"`
	}
	if !decode(w, r, &body) {
		return
	}
	if body.Name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name is required"})
		return
	}
	if err := s.creds.Set(body.Name, body.Value); err != nil {
		writeErrMsg(w, http.StatusInternalServerError, "credentials: ", err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"name": body.Name})
}

func (s *server) handleDeleteCredential(w http.ResponseWriter, r *http.Request) {
	if s.creds == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "credential store unavailable"})
		return
	}
	if err := s.creds.Delete(r.PathValue("name")); err != nil {
		writeErrMsg(w, http.StatusInternalServerError, "credentials: ", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// handleCredentialEnvCandidates lists process env-var NAMES that look like
// credentials and aren't already stored — the first-run wizard's import
// picklist. Values never appear in the response.
func (s *server) handleCredentialEnvCandidates(w http.ResponseWriter, _ *http.Request) {
	existing := map[string]bool{}
	if s.creds != nil {
		if names, err := s.creds.Names(); err == nil {
			for _, n := range names {
				existing[n] = true
			}
		}
	}
	var candidates []string
	for _, kv := range os.Environ() {
		name, _, ok := strings.Cut(kv, "=")
		if !ok || existing[name] || !looksLikeCredentialName(name) {
			continue
		}
		candidates = append(candidates, name)
	}
	sort.Strings(candidates)
	if candidates == nil {
		candidates = []string{}
	}
	writeJSON(w, http.StatusOK, candidates)
}

// handleImportCredentials reads the named env vars server-side and stores
// them — values are read from this process's own environment and written
// straight to the local store, never round-tripped through the request or
// response body (spec §13).
func (s *server) handleImportCredentials(w http.ResponseWriter, r *http.Request) {
	if s.creds == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "credential store unavailable"})
		return
	}
	var body struct {
		Names []string `json:"names"`
	}
	if !decode(w, r, &body) {
		return
	}
	imported := []string{}
	for _, name := range body.Names {
		value, ok := os.LookupEnv(name)
		if !ok {
			continue
		}
		if err := s.creds.Set(name, value); err != nil {
			writeErrMsg(w, http.StatusInternalServerError, "credentials: ", err)
			return
		}
		imported = append(imported, name)
	}
	writeJSON(w, http.StatusOK, map[string][]string{"imported": imported})
}

// vaultSourceInfo is a linkable external secret manager's status (MADS-UX
// §3.7 vault link). No vault.Source implementations are registered yet —
// this always returns an empty list until one lands.
type vaultSourceInfo struct {
	Name    string `json:"name"`
	Healthy bool   `json:"healthy"`
}

func (s *server) handleListVaultSources(w http.ResponseWriter, r *http.Request) {
	out := make([]vaultSourceInfo, 0, len(s.vaultSources))
	for _, src := range s.vaultSources {
		healthy, _ := src.Healthy(r.Context())
		out = append(out, vaultSourceInfo{Name: src.Name(), Healthy: healthy})
	}
	writeJSON(w, http.StatusOK, out)
}
