package api

import (
	"net/http"
	"os"
	"sort"
	"strings"

	"github.com/drewkidwell/mads/pkg/vault"
)

// knownCredentialEnvVars/knownCredentialEnvVarPrefixes are the declared
// allowlist for the env-import candidate scan (spec §13.7) — deliberately
// not a heuristic suffix match: a dragnet risks surfacing unrelated process
// env vars that happen to end in TOKEN/SECRET but were never meant for mads
// (e.g. a CI runner's internal vars). Names never leave the process via this
// scan — only names, never values, are returned to the caller.
var knownCredentialEnvVars = []string{
	"ANTHROPIC_API_KEY", "OPENAI_API_KEY",
	"GITHUB_TOKEN", "GH_TOKEN", "GITLAB_TOKEN",
}
var knownCredentialEnvVarPrefixes = []string{
	"AWS_", // AWS_ACCESS_KEY_ID, AWS_SECRET_ACCESS_KEY, AWS_SESSION_TOKEN
	"AZURE_",
	"GOOGLE_", // GOOGLE_API_KEY, GOOGLE_APPLICATION_CREDENTIALS
}

func looksLikeCredentialName(name string) bool {
	upper := strings.ToUpper(name)
	for _, known := range knownCredentialEnvVars {
		if upper == known {
			return true
		}
	}
	for _, prefix := range knownCredentialEnvVarPrefixes {
		if strings.HasPrefix(upper, prefix) {
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

// envCandidatesResponse is the import picklist plus the allowlist that
// produced it, so the UI can say what it looked for — an empty picklist
// otherwise reads as "nothing found" with no hint of what "anything" was.
// Prefixes are rendered with a trailing "*" (e.g. "AWS_*").
type envCandidatesResponse struct {
	Candidates []string `json:"candidates"`
	LookedFor  []string `json:"looked_for"`
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
	lookedFor := append([]string{}, knownCredentialEnvVars...)
	for _, prefix := range knownCredentialEnvVarPrefixes {
		lookedFor = append(lookedFor, prefix+"*")
	}
	writeJSON(w, http.StatusOK, envCandidatesResponse{Candidates: candidates, LookedFor: lookedFor})
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
// §3.7 vault link, spec §13.8). Error carries the actionable
// install/configure guidance from Source.Healthy when unhealthy — never a
// secret, just diagnostic text.
type vaultSourceInfo struct {
	Name    string `json:"name"`
	Healthy bool   `json:"healthy"`
	Error   string `json:"error,omitempty"`
}

func (s *server) handleListVaultSources(w http.ResponseWriter, r *http.Request) {
	out := make([]vaultSourceInfo, 0, len(s.vaultSources))
	for _, src := range s.vaultSources {
		healthy, err := src.Healthy(r.Context())
		info := vaultSourceInfo{Name: src.Name(), Healthy: healthy}
		if err != nil {
			info.Error = err.Error()
		}
		out = append(out, info)
	}
	writeJSON(w, http.StatusOK, out)
}

// vaultRefInfo is a vault-backed credential's pointer — source + path, never
// a value, so unlike a credential this is safe to echo back over the wire
// (spec §13.8).
type vaultRefInfo struct {
	Name   string `json:"name"`
	Source string `json:"source"`
	Path   string `json:"path"`
}

func (s *server) handleListVaultRefs(w http.ResponseWriter, _ *http.Request) {
	if s.vaultRefs == nil {
		writeJSON(w, http.StatusOK, []vaultRefInfo{})
		return
	}
	names, err := s.vaultRefs.Names()
	if err != nil {
		writeErrMsg(w, http.StatusInternalServerError, "vault-refs: ", err)
		return
	}
	sort.Strings(names)
	out := make([]vaultRefInfo, 0, len(names))
	for _, name := range names {
		ref, ok, err := s.vaultRefs.Get(name)
		if err != nil || !ok {
			continue
		}
		out = append(out, vaultRefInfo{Name: name, Source: ref.Source, Path: ref.Path})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *server) handleSetVaultRef(w http.ResponseWriter, r *http.Request) {
	if s.vaultRefs == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "vault ref store unavailable"})
		return
	}
	var body vaultRefInfo
	if !decode(w, r, &body) {
		return
	}
	if body.Name == "" || body.Source == "" || body.Path == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name, source, and path are required"})
		return
	}
	if err := s.vaultRefs.Set(body.Name, vault.Ref{Source: body.Source, Path: body.Path}); err != nil {
		writeErrMsg(w, http.StatusInternalServerError, "vault-refs: ", err)
		return
	}
	writeJSON(w, http.StatusCreated, body)
}

func (s *server) handleDeleteVaultRef(w http.ResponseWriter, r *http.Request) {
	if s.vaultRefs == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "vault ref store unavailable"})
		return
	}
	if err := s.vaultRefs.Delete(r.PathValue("name")); err != nil {
		writeErrMsg(w, http.StatusInternalServerError, "vault-refs: ", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}
