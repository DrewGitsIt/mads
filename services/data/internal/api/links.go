package api

import (
	"fmt"
	"net/http"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/services/data/internal/store"
)

func linkRoutes(mux *http.ServeMux, log *store.Log) {
	links := store.NewLinks(log, store.NewSessions(log))

	mux.HandleFunc("POST /links", func(w http.ResponseWriter, r *http.Request) {
		var req contracts.LinkRequest
		if !decode(w, r, &req) {
			return
		}
		grants, verr := resolveGrants(req)
		if verr != nil {
			writeJSON(w, http.StatusUnprocessableEntity, verr)
			return
		}
		link := contracts.Link{
			GrantingSessionID: req.GrantingSessionID,
			SubjectSessionID:  req.SubjectSessionID,
			Grants:            grants,
			Cadence:           req.Cadence,
		}
		if err := links.Create(r.Context(), &link, actor(r)); err != nil {
			writeStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, link)
	})

	// POST /links/validate is the dry run of staged links on POST /sessions
	// (the Router calls it before the new session exists, so one endpoint of
	// each entry is contracts.NewSessionPlaceholder). Runs the same grant
	// resolution and store rules as POST /links but writes nothing, and
	// reports problems as 200 {"errors":[...]} (empty when the links would be
	// accepted) rather than 422 — a failed check is the answer, not a failure.
	mux.HandleFunc("POST /links/validate", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Links []contracts.LinkRequest `json:"links"`
		}
		if !decode(w, r, &req) {
			return
		}
		var v contracts.ValidationError
		checks := make([]store.StagedLinkCheck, 0, len(req.Links))
		for i, lr := range req.Links {
			field := fmt.Sprintf("links[%d]", i)
			grants, verr := resolveGrants(lr)
			if verr != nil {
				for _, fe := range verr.Errors {
					v.Add(field+"."+fe.Field, fe.Message)
				}
				continue
			}
			checks = append(checks, store.StagedLinkCheck{Field: field, Link: contracts.Link{
				GrantingSessionID: lr.GrantingSessionID,
				SubjectSessionID:  lr.SubjectSessionID,
				Grants:            grants,
				Cadence:           lr.Cadence,
			}})
		}
		storeErr, err := links.ValidateStaged(r.Context(), checks)
		if err != nil {
			writeStoreError(w, err)
			return
		}
		if storeErr != nil {
			v.Errors = append(v.Errors, storeErr.Errors...)
		}
		if v.Errors == nil {
			v.Errors = []contracts.FieldError{}
		}
		writeJSON(w, http.StatusOK, v)
	})

	mux.HandleFunc("GET /links/{id}", func(w http.ResponseWriter, r *http.Request) {
		link, err := links.Get(r.Context(), r.PathValue("id"))
		if err != nil {
			writeStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, link)
	})

	mux.HandleFunc("PUT /links/{id}", func(w http.ResponseWriter, r *http.Request) {
		cur, err := links.Get(r.Context(), r.PathValue("id"))
		if err != nil {
			writeStoreError(w, err)
			return
		}
		var req contracts.LinkRequest
		if !decode(w, r, &req) {
			return
		}
		grants, verr := resolveGrants(req)
		if verr != nil {
			writeJSON(w, http.StatusUnprocessableEntity, verr)
			return
		}
		link := contracts.Link{
			ID:                cur.ID,
			GrantingSessionID: cur.GrantingSessionID,
			SubjectSessionID:  cur.SubjectSessionID,
			Grants:            grants,
			Cadence:           req.Cadence,
		}
		if err := links.Update(r.Context(), &link, actor(r)); err != nil {
			writeStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, link)
	})

	// PUT /links/{id}/orchestrate-status is called by the granting session's
	// own runner (services/session/internal/runner), never a user — it
	// records whether the orchestrate grant has actually materialized as
	// live tools (Start-time materialization, or RefreshOrchestrate's
	// deferred live-cycle). The actor is hardcoded to "system" for the same
	// reason as the hand-raiser routes below: this route only ever means one
	// thing regardless of who calls it.
	mux.HandleFunc("PUT /links/{id}/orchestrate-status", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Status contracts.OrchestrateStatus `json:"status"`
			// Error is the reason for a "failed" status; ignored otherwise.
			Error string `json:"error"`
		}
		if !decode(w, r, &req) {
			return
		}
		link, err := links.SetOrchestrateStatus(r.Context(), r.PathValue("id"), req.Status, req.Error, "system")
		if err != nil {
			writeStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, link)
	})

	mux.HandleFunc("DELETE /links/{id}", func(w http.ResponseWriter, r *http.Request) {
		if err := links.Delete(r.Context(), r.PathValue("id"), actor(r)); err != nil {
			writeStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
	})

	mux.HandleFunc("GET /sessions/{id}/links", func(w http.ResponseWriter, r *http.Request) {
		list, err := links.ForSession(r.Context(), r.PathValue("id"))
		if err != nil {
			writeStoreError(w, err)
			return
		}
		if list == nil {
			list = []contracts.Link{}
		}
		writeJSON(w, http.StatusOK, list)
	})

	// Both mailbox routes are Router-internal plumbing (the digest job
	// appends, the signal job and the leader's on-demand pull drain) —
	// never called by the UI directly.
	mux.HandleFunc("POST /links/{id}/digests", func(w http.ResponseWriter, r *http.Request) {
		var d contracts.LinkDigest
		if !decode(w, r, &d) {
			return
		}
		if err := links.AppendDigest(r.Context(), r.PathValue("id"), &d); err != nil {
			writeStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, d)
	})

	mux.HandleFunc("POST /links/{id}/digests/drain", func(w http.ResponseWriter, r *http.Request) {
		drain := links.DrainDigests
		if r.URL.Query().Get("deferred") == "1" {
			drain = links.DrainDeferred
		}
		list, err := drain(r.Context(), r.PathValue("id"))
		if err != nil {
			writeStoreError(w, err)
			return
		}
		if list == nil {
			list = []contracts.LinkDigest{}
		}
		writeJSON(w, http.StatusOK, list)
	})

	// Per-recipient count of pushes deferred while the recipient slept — the
	// Router decorates session reads with it (Session.WaitingUpdates).
	mux.HandleFunc("GET /links/deferred-counts", func(w http.ResponseWriter, r *http.Request) {
		counts, err := links.DeferredCounts(r.Context())
		if err != nil {
			writeStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, counts)
	})

	// Both hand-raiser routes are meant to be called by the Router's
	// Supervisor only (docs/LINKED-SESSIONS-DESIGN.md decision 6) — there's
	// no user-facing UI action that should ever hit these. The actor is
	// hardcoded to "system" rather than read from X-Mads-Actor: this route
	// only ever means one thing regardless of who calls it.
	mux.HandleFunc("POST /sessions/{id}/hand-raiser", func(w http.ResponseWriter, r *http.Request) {
		link, err := links.CreateImplicitHandRaiser(r.Context(), r.PathValue("id"), "system")
		if err != nil {
			writeStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, link)
	})

	mux.HandleFunc("DELETE /sessions/{id}/hand-raiser", func(w http.ResponseWriter, r *http.Request) {
		if err := links.RemoveImplicitHandRaiser(r.Context(), r.PathValue("id"), "system"); err != nil {
			writeStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
	})
}

// resolveGrants turns a LinkRequest into the grant set the store persists —
// preset resolution and the hand-raiser refusal live here, at the API layer,
// not the store: hand_raiser is a SYSTEM-creatable preset only
// (docs/LINKED-SESSIONS-DESIGN.md decision 6, AMENDMENT 2026-07-16), never
// reachable through this user-facing route. The store itself must not
// hardcode this refusal — the not-yet-built L3 implicit-link path needs to
// grant exactly {read,inject} programmatically, bypassing this check.
func resolveGrants(req contracts.LinkRequest) ([]contracts.Grant, *contracts.ValidationError) {
	if req.Preset == "" {
		return req.Grants, nil
	}
	if req.Preset == contracts.PresetHandRaiser {
		v := &contracts.ValidationError{}
		v.Add("preset", "hand_raiser is system-granted only (implicit on pipeline launch) — not creatable via this route")
		return nil, v
	}
	grants, ok := contracts.PresetGrants[req.Preset]
	if !ok {
		v := &contracts.ValidationError{}
		v.Add("preset", fmt.Sprintf("unknown preset %q", req.Preset))
		return nil, v
	}
	return grants, nil
}
