package api

import (
	"net/http"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/services/data/internal/store"
)

func sessionRoutes(mux *http.ServeMux, log *store.Log) {
	s := store.NewSessions(log)

	mux.HandleFunc("POST /sessions", func(w http.ResponseWriter, r *http.Request) {
		var sess contracts.Session
		if !decode(w, r, &sess) {
			return
		}
		sess.ID = ""
		if err := s.Create(r.Context(), &sess, actor(r)); err != nil {
			writeStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, sess)
	})

	mux.HandleFunc("GET /sessions", func(w http.ResponseWriter, r *http.Request) {
		list, err := s.List(r.Context(), contracts.SessionActivity(r.URL.Query().Get("activity")))
		if err != nil {
			writeStoreError(w, err)
			return
		}
		if list == nil {
			list = []contracts.Session{}
		}
		writeJSON(w, http.StatusOK, list)
	})

	mux.HandleFunc("GET /sessions/{id}", func(w http.ResponseWriter, r *http.Request) {
		sess, err := s.Get(r.Context(), r.PathValue("id"))
		if err != nil {
			writeStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, sess)
	})

	mux.HandleFunc("PUT /sessions/{id}", func(w http.ResponseWriter, r *http.Request) {
		var sess contracts.Session
		if !decode(w, r, &sess) {
			return
		}
		sess.ID = r.PathValue("id")
		if err := s.Update(r.Context(), &sess, actor(r)); err != nil {
			writeStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, sess)
	})

	mux.HandleFunc("PUT /sessions/{id}/activity", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Activity contracts.SessionActivity `json:"activity"`
		}
		if !decode(w, r, &body) {
			return
		}
		if err := s.SetActivity(r.Context(), r.PathValue("id"), body.Activity, actor(r)); err != nil {
			writeStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	// The user's own "done with this for now" judgement (the rail's ✓), and
	// the send path clearing it. Scoped and atomic for the reason
	// store.Sessions.SetAside documents: both land while the session's own
	// runner is writing the same record.
	mux.HandleFunc("PUT /sessions/{id}/aside", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Aside bool `json:"aside"`
		}
		if !decode(w, r, &body) {
			return
		}
		if err := s.SetAside(r.Context(), r.PathValue("id"), body.Aside, actor(r)); err != nil {
			writeStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	// The Session runner's main-agent transitions: one call mirrors the
	// state onto the record and appends its event, so the rail's cold read
	// and its live stream can never disagree (store.Sessions.SetAgentState).
	mux.HandleFunc("PUT /sessions/{id}/agent-state", func(w http.ResponseWriter, r *http.Request) {
		var state contracts.AgentState
		if !decode(w, r, &state) {
			return
		}
		if err := s.SetAgentState(r.Context(), r.PathValue("id"), state, actor(r)); err != nil {
			writeStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	mux.HandleFunc("POST /sessions/{id}/turn-end", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Usage *contracts.Usage `json:"usage,omitempty"`
			At    time.Time        `json:"at"`
		}
		if !decode(w, r, &body) {
			return
		}
		if err := s.RecordTurnEnd(r.Context(), r.PathValue("id"), body.Usage, body.At, actor(r)); err != nil {
			writeStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	// Scoped isolation-state write (contracts.SessionWorktreePatch): the
	// worktree fields before the provider starts, and D4's counts after.
	mux.HandleFunc("PATCH /sessions/{id}/worktree", func(w http.ResponseWriter, r *http.Request) {
		var patch contracts.SessionWorktreePatch
		if !decode(w, r, &patch) {
			return
		}
		if err := s.SetWorktree(r.Context(), r.PathValue("id"), patch, actor(r)); err != nil {
			writeStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	mux.HandleFunc("DELETE /sessions/{id}", func(w http.ResponseWriter, r *http.Request) {
		if err := s.Delete(r.Context(), r.PathValue("id"), actor(r)); err != nil {
			writeStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
	})

	mux.HandleFunc("POST /sessions/{id}/files", func(w http.ResponseWriter, r *http.Request) {
		var fc contracts.FileChange
		if !decode(w, r, &fc) {
			return
		}
		fc.SessionID = r.PathValue("id")
		if err := s.RecordFileChange(r.Context(), &fc, actor(r)); err != nil {
			writeStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, fc)
	})

	mux.HandleFunc("POST /sessions/{id}/files/shell", func(w http.ResponseWriter, r *http.Request) {
		var body contracts.ShellFileChanges
		if !decode(w, r, &body) {
			return
		}
		res, err := s.RecordShellFileChanges(r.Context(), r.PathValue("id"), body.Changes, actor(r))
		if err != nil {
			writeStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, res)
	})

	mux.HandleFunc("GET /sessions/{id}/files", func(w http.ResponseWriter, r *http.Request) {
		list, err := s.FileChanges(r.Context(), r.PathValue("id"))
		if err != nil {
			writeStoreError(w, err)
			return
		}
		if list == nil {
			list = []contracts.FileChange{}
		}
		writeJSON(w, http.StatusOK, list)
	})

	mux.HandleFunc("POST /sessions/{id}/commits", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			contracts.Commit
			TrailerTaskID    string   `json:"trailer_task_id"`
			HeuristicTaskIDs []string `json:"heuristic_task_ids"`
			Source           string   `json:"source"`
			Files            int      `json:"files"`
			Drafted          bool     `json:"drafted"`
		}
		if !decode(w, r, &body) {
			return
		}
		body.SessionID = r.PathValue("id")
		rec, err := s.RecordCommit(r.Context(), &store.CommitReport{
			Commit:           body.Commit,
			TrailerTaskID:    body.TrailerTaskID,
			HeuristicTaskIDs: body.HeuristicTaskIDs,
			Source:           body.Source,
			Files:            body.Files,
			Drafted:          body.Drafted,
		}, actor(r))
		if err != nil {
			writeStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, rec)
	})

	mux.HandleFunc("DELETE /sessions/{id}/commits/{hash}", func(w http.ResponseWriter, r *http.Request) {
		if err := s.DeleteCommit(r.Context(), r.PathValue("id"), r.PathValue("hash"), actor(r)); err != nil {
			writeStoreError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	mux.HandleFunc("GET /sessions/{id}/commits", func(w http.ResponseWriter, r *http.Request) {
		list, err := s.Commits(r.Context(), r.PathValue("id"))
		if err != nil {
			writeStoreError(w, err)
			return
		}
		if list == nil {
			list = []contracts.Commit{}
		}
		writeJSON(w, http.StatusOK, list)
	})

	leases := store.NewLeases(log)
	mux.HandleFunc("GET /sessions/{id}/lease-active", func(w http.ResponseWriter, r *http.Request) {
		active, err := leases.ActiveForSession(r.Context(), r.PathValue("id"))
		if err != nil {
			writeStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"active": active})
	})
}
