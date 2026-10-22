package api

import (
	"net/http"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/services/data/internal/store"
)

// accountAuthRoutes serves provider-login sign-in state (docs/AUTH-SIGNIN-
// DESIGN.md). Session runners PUT failed/ok as their turns go; the Router
// PUTs ok when a sign-in finishes and reads the list.
func accountAuthRoutes(mux *http.ServeMux, log *store.Log) {
	auths := store.NewAccountAuths(log)

	mux.HandleFunc("PUT /accounts/auth/failed", func(w http.ResponseWriter, r *http.Request) {
		var f contracts.AccountAuthFailed
		if !decode(w, r, &f) {
			return
		}
		if err := auths.Failed(r.Context(), f, actor(r)); err != nil {
			writeStoreError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	mux.HandleFunc("PUT /accounts/auth/ok", func(w http.ResponseWriter, r *http.Request) {
		var ok contracts.AccountAuthOK
		if !decode(w, r, &ok) {
			return
		}
		if err := auths.OK(r.Context(), ok, actor(r)); err != nil {
			writeStoreError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	mux.HandleFunc("GET /accounts/auth", func(w http.ResponseWriter, r *http.Request) {
		list, err := auths.List(r.Context())
		if err != nil {
			writeStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, list)
	})
}
