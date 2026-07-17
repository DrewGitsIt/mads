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
		}
		if err := links.Create(r.Context(), &link, actor(r)); err != nil {
			writeStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, link)
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
		}
		if err := links.Update(r.Context(), &link, actor(r)); err != nil {
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
