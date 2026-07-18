package api

import (
	"net/http"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
)

const (
	defaultLeaseTTL = 10 * time.Minute
	minLeaseTTL     = time.Second
	maxLeaseTTL     = time.Hour
)

func (s *server) handleClaimLease(w http.ResponseWriter, r *http.Request) {
	var body struct {
		TTLSeconds int                      `json:"ttl_seconds"`
		Identity   *contracts.LeaseIdentity `json:"identity,omitempty"`
	}
	if r.ContentLength != 0 && !decode(w, r, &body) {
		return
	}
	ttl := defaultLeaseTTL
	if body.TTLSeconds != 0 {
		ttl = time.Duration(body.TTLSeconds) * time.Second
		if ttl < minLeaseTTL || ttl > maxLeaseTTL {
			verr := &contracts.ValidationError{}
			verr.Add("ttl_seconds", "must be between 1 and 3600")
			writeJSON(w, http.StatusUnprocessableEntity, verr)
			return
		}
	}
	lease, err := s.leases.ClaimWithIdentity(r.Context(), r.PathValue("id"), actor(r), ttl, body.Identity)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, lease)
}

// handleListLeases lists every live lease held by the caller's own actor
// (spec: reap-on-launch) — the reclaim candidate set. Scoped implicitly via
// X-Mads-Actor, same as every other lease-mutating route; no query param.
func (s *server) handleListLeases(w http.ResponseWriter, r *http.Request) {
	leases, err := s.leases.ByHolder(r.Context(), actor(r))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, leases)
}

// handleReclaimLease is the proof-or-nothing transition (spec: reap-on-
// launch): the caller has already established, via pkg/procid, that
// identity's process is provably dead.
func (s *server) handleReclaimLease(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Identity contracts.LeaseIdentity `json:"identity"`
		Evidence string                  `json:"evidence"`
	}
	if !decode(w, r, &body) {
		return
	}
	ok, err := s.leases.Reclaim(r.Context(), r.PathValue("id"), actor(r), body.Identity, body.Evidence)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"reclaimed": ok})
}

func (s *server) handleReleaseLease(w http.ResponseWriter, r *http.Request) {
	if err := s.leases.Release(r.Context(), r.PathValue("id"), actor(r)); err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "released"})
}

// handleReleaseAllLeases is the supervision path (spec §15): the Router calls
// it the moment a child process exits, so that child's tasks land blocked
// immediately instead of when their meters run out.
func (s *server) handleReleaseAllLeases(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Holder string `json:"holder"`
		Reason string `json:"reason"`
	}
	if !decode(w, r, &body) {
		return
	}
	if body.Holder == "" {
		verr := &contracts.ValidationError{}
		verr.Add("holder", "required")
		writeJSON(w, http.StatusUnprocessableEntity, verr)
		return
	}
	reason := body.Reason
	if reason == "" {
		reason = "lease released: " + body.Holder + " exited"
	}
	n, err := s.leases.ReleaseAll(r.Context(), body.Holder, reason)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"released": n})
}
