// Package api is the Data Service's HTTP surface. Wave-1 work items add
// routes here as they land; every route that mutates state must go through
// the store's outbox so the change and its event commit together (spec §15).
package api

import (
	"database/sql"
	"encoding/json"
	"log"
	"net/http"
)

// New returns the Data Service handler backed by db.
func New(db *sql.DB) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		status, code := "ok", http.StatusOK
		if err := db.PingContext(r.Context()); err != nil {
			status, code = "database unreachable", http.StatusServiceUnavailable
		}
		writeJSON(w, code, map[string]string{"service": "data", "status": status})
	})
	return mux
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("api: encode response: %v", err)
	}
}
