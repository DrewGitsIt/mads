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
	db  *sql.DB
	log *store.Log
}

// New returns the Data Service handler backed by db.
func New(db *sql.DB) http.Handler {
	s := &server{db: db, log: store.NewLog(db)}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", s.handleHealth)
	mux.HandleFunc("POST /events", s.handlePostEvent)
	mux.HandleFunc("GET /events", s.handleGetEvents)
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
