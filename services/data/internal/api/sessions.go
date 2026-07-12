package api

import (
	"net/http"

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
		}
		if !decode(w, r, &body) {
			return
		}
		body.SessionID = r.PathValue("id")
		rec, err := s.RecordCommit(r.Context(), &store.CommitReport{
			Commit:           body.Commit,
			TrailerTaskID:    body.TrailerTaskID,
			HeuristicTaskIDs: body.HeuristicTaskIDs,
		}, actor(r))
		if err != nil {
			writeStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, rec)
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
}
