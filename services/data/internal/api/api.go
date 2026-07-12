// Package api is the Data Service's HTTP surface. Every route that mutates
// state goes through the store's outbox (store.Log.WithTx) so the change and
// the events describing it commit together (spec §15).
package api

import (
	"database/sql"
	"encoding/json"
	"log"
	"net/http"

	"github.com/drewkidwell/mads/services/data/internal/store"
)

type server struct {
	db    *sql.DB
	log   *store.Log
	tasks *store.Tasks
}

// New returns the Data Service handler backed by db.
func New(db *sql.DB) http.Handler {
	l := store.NewLog(db)
	s := &server{db: db, log: l, tasks: store.NewTasks(l)}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", s.handleHealth)
	mux.HandleFunc("POST /events", s.handlePostEvent)
	mux.HandleFunc("GET /events", s.handleGetEvents)
	mux.HandleFunc("POST /tasks", s.handleCreateTask)
	mux.HandleFunc("POST /tasks/import", s.handleImportTasks)
	mux.HandleFunc("GET /tasks", s.handleListTasks)
	mux.HandleFunc("GET /tasks/cycles", s.handleTaskCycles)
	mux.HandleFunc("GET /tasks/{id}", s.handleGetTask)
	mux.HandleFunc("PUT /tasks/{id}", s.handleUpdateTask)
	mux.HandleFunc("POST /tasks/{id}/confirm", s.handleConfirmTask)
	mux.HandleFunc("POST /tasks/{id}/discard", s.handleDiscardTask)
	mux.HandleFunc("POST /tasks/{id}/handoffs", s.handleAddHandoff)
	mux.HandleFunc("POST /tasks/{id}/notes", s.handleAddNote)
	return mux
}

func (s *server) handleHealth(w http.ResponseWriter, r *http.Request) {
	status, code := "ok", http.StatusOK
	if err := s.db.PingContext(r.Context()); err != nil {
		status, code = "database unreachable", http.StatusServiceUnavailable
	}
	writeJSON(w, code, map[string]string{"service": "data", "status": status})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("api: encode response: %v", err)
	}
}
