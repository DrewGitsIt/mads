package api

import (
	"net/http"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/services/data/internal/store"
)

func proposalRoutes(mux *http.ServeMux, log *store.Log, tasks *store.Tasks) {
	s := store.NewProposals(log, tasks)

	mux.HandleFunc("POST /proposals", func(w http.ResponseWriter, r *http.Request) {
		var p contracts.Proposal
		if !decode(w, r, &p) {
			return
		}
		p.ID = "" // assigned by the store
		if err := s.Create(r.Context(), &p); err != nil {
			writeStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, p)
	})

	mux.HandleFunc("GET /proposals", func(w http.ResponseWriter, r *http.Request) {
		sessionID := r.URL.Query().Get("session_id")
		if sessionID == "" {
			verr := &contracts.ValidationError{}
			verr.Add("session_id", "required")
			writeJSON(w, http.StatusUnprocessableEntity, verr)
			return
		}
		list, err := s.List(r.Context(), sessionID)
		if err != nil {
			writeStoreError(w, err)
			return
		}
		if list == nil {
			list = []contracts.Proposal{}
		}
		writeJSON(w, http.StatusOK, list)
	})

	mux.HandleFunc("GET /proposals/{id}", func(w http.ResponseWriter, r *http.Request) {
		p, err := s.Get(r.Context(), r.PathValue("id"))
		if err != nil {
			writeStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, p)
	})

	mux.HandleFunc("POST /proposals/{id}/approve", func(w http.ResponseWriter, r *http.Request) {
		p, err := s.Approve(r.Context(), r.PathValue("id"))
		if err != nil {
			writeStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, p)
	})

	mux.HandleFunc("POST /proposals/{id}/dismiss", func(w http.ResponseWriter, r *http.Request) {
		p, err := s.Dismiss(r.Context(), r.PathValue("id"))
		if err != nil {
			writeStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, p)
	})
}
