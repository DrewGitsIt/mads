package api

import (
	"net/http"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/services/data/internal/store"
)

func retroRoutes(mux *http.ServeMux, log *store.Log) {
	s := store.NewRetros(log)

	mux.HandleFunc("POST /retros", func(w http.ResponseWriter, r *http.Request) {
		var retro contracts.Retro
		if !decode(w, r, &retro) {
			return
		}
		if err := s.Create(r.Context(), &retro); err != nil {
			writeStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, retro)
	})

	mux.HandleFunc("GET /retros", func(w http.ResponseWriter, r *http.Request) {
		list, err := s.List(r.Context())
		if err != nil {
			writeStoreError(w, err)
			return
		}
		if list == nil {
			list = []contracts.Retro{}
		}
		writeJSON(w, http.StatusOK, list)
	})

	mux.HandleFunc("GET /retros/{id}", func(w http.ResponseWriter, r *http.Request) {
		retro, err := s.Get(r.Context(), r.PathValue("id"))
		if err != nil {
			writeStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, retro)
	})

	mux.HandleFunc("POST /retros/{id}/dpas/{dpaId}/accept", func(w http.ResponseWriter, r *http.Request) {
		if err := s.AcceptDPA(r.Context(), r.PathValue("id"), r.PathValue("dpaId")); err != nil {
			writeStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "accepted"})
	})

	mux.HandleFunc("POST /retros/{id}/dpas/{dpaId}/reject", func(w http.ResponseWriter, r *http.Request) {
		if err := s.RejectDPA(r.Context(), r.PathValue("id"), r.PathValue("dpaId")); err != nil {
			writeStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "rejected"})
	})

	mux.HandleFunc("PATCH /retros/{id}/dpas/{dpaId}", func(w http.ResponseWriter, r *http.Request) {
		var edit contracts.DPAEdit
		if !decode(w, r, &edit) {
			return
		}
		dpa, err := s.EditDPA(r.Context(), r.PathValue("id"), r.PathValue("dpaId"), edit)
		if err != nil {
			writeStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, dpa)
	})

	mux.HandleFunc("POST /retros/{id}/follow-up", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			SessionID string `json:"session_id"`
		}
		if !decode(w, r, &body) {
			return
		}
		if err := s.SetFollowUpSession(r.Context(), r.PathValue("id"), body.SessionID); err != nil {
			writeStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "linked"})
	})
}
