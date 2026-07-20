package api

import (
	"net/http"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/services/data/internal/store"
)

func configRoutes(mux *http.ServeMux, log *store.Log) {
	cfg := store.NewConfig(log)

	mux.HandleFunc("GET /config", func(w http.ResponseWriter, r *http.Request) {
		c, err := cfg.Get(r.Context())
		if err != nil {
			writeStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, c)
	})

	mux.HandleFunc("PUT /config", func(w http.ResponseWriter, r *http.Request) {
		var c contracts.Config
		if !decode(w, r, &c) {
			return
		}
		if err := cfg.Update(r.Context(), &c, actor(r)); err != nil {
			writeStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, c)
	})
}

func profileRoutes(mux *http.ServeMux, log *store.Log) {
	registryRoutes(mux, "profiles", store.NewProfiles(log))

	mux.HandleFunc("POST /profiles/{id}/activate", func(w http.ResponseWriter, r *http.Request) {
		cfg, err := store.ActivateProfile(r.Context(), log, r.PathValue("id"), actor(r))
		if err != nil {
			writeStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, cfg)
	})

	// POST /profiles/capture returns a snapshot of the current Config for the
	// caller to save as a new profile or fold into an update to an existing
	// one (profiles-v2 "save current settings as profile" capture path).
	mux.HandleFunc("POST /profiles/capture", func(w http.ResponseWriter, r *http.Request) {
		snap, err := store.CaptureConfigSnapshot(r.Context(), log)
		if err != nil {
			writeStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, snap)
	})
}
