package api

import (
	"context"
	"net/http"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/services/data/internal/store"
)

const (
	defaultLeaseTTL = 10 * time.Minute
	minLeaseTTL     = time.Second
	maxLeaseTTL     = time.Hour
)

// leaseHeader carries a lease token (docs/PIPELINE-LEASES-DESIGN.md A2):
// proof of possession for renew, release and writes to a leased task.
const leaseHeader = "X-Mads-Lease"

// leaseCtx is the request context carrying its X-Mads-Lease token, for
// store calls that write to a task (checkAndRenew reads it).
func leaseCtx(r *http.Request) context.Context {
	return store.WithLeaseToken(r.Context(), r.Header.Get(leaseHeader))
}

// handleClaimLease takes a free task's lease. Body (all optional):
// {holder, ttl_seconds, identity}. holder defaults to the caller's actor
// and must equal it or start with "<actor>:" (422 otherwise). A live
// lease, whoever holds it, is 409. The response is the lease plus its
// token, the only time the token is ever returned.
func (s *server) handleClaimLease(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Holder     string                   `json:"holder"`
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
	lease, err := s.leases.ClaimWith(r.Context(), r.PathValue("id"), store.ClaimRequest{
		Actor: actor(r), Holder: body.Holder, TTL: ttl, Identity: body.Identity,
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, lease)
}

// handleRenewLease extends a live lease for the token in X-Mads-Lease. Any
// failure to match (gone, expired, another token) is 409 with code
// "lease_lost". It never creates a lease.
func (s *server) handleRenewLease(w http.ResponseWriter, r *http.Request) {
	lease, err := s.leases.Renew(r.Context(), r.PathValue("id"), r.Header.Get(leaseHeader))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, lease)
}

// handleListLeases lists live leases for reap-on-launch. With ?kind=k it
// lists every lease whose holder is k or starts with "k:"; k must be the
// caller's own actor (422 otherwise). Without kind it lists the leases
// held by exactly the caller's actor.
func (s *server) handleListLeases(w http.ResponseWriter, r *http.Request) {
	var (
		leases []contracts.Lease
		err    error
	)
	if kind := r.URL.Query().Get("kind"); kind != "" {
		if kind != actor(r) {
			verr := &contracts.ValidationError{}
			verr.Add("kind", "must be the caller's actor")
			writeJSON(w, http.StatusUnprocessableEntity, verr)
			return
		}
		leases, err = s.leases.ByKind(r.Context(), kind)
	} else {
		leases, err = s.leases.ByHolder(r.Context(), actor(r))
	}
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if leases == nil {
		leases = []contracts.Lease{}
	}
	writeJSON(w, http.StatusOK, leases)
}

// handleReclaimLease is the proof-or-nothing transition (spec: reap-on-
// launch): the caller has already established, via pkg/procid, that
// identity's process is provably dead. The caller's actor must be the
// lease holder's kind. Tokenless: the dead holder's token died with it.
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

// handleReleaseLease drops a lease for the token in X-Mads-Lease. A wrong
// or missing token is 409; an already-free task is a no-op 200.
func (s *server) handleReleaseLease(w http.ResponseWriter, r *http.Request) {
	if err := s.leases.Release(r.Context(), r.PathValue("id"), r.Header.Get(leaseHeader)); err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "released"})
}

// handleReleaseAllLeases is the supervision path (spec §15): the Router calls
// it the moment a child process exits, so that child's leases go
// immediately instead of when their meters run out. Holder-matched and
// tokenless; task status is untouched.
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
