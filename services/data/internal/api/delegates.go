package api

import (
	"net/http"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/services/data/internal/store"
)

// delegateRoutes serves the delegate store (docs/DELEGATES-DESIGN.md). The
// Session runner is the only writer (PUT, one whole record per change); the
// Router reads.
func delegateRoutes(mux *http.ServeMux, log *store.Log) {
	delegates := store.NewDelegates(log)

	mux.HandleFunc("PUT /sessions/{id}/delegates/{harnessId}", func(w http.ResponseWriter, r *http.Request) {
		var d contracts.Delegate
		if !decode(w, r, &d) {
			return
		}
		// The path is the key; a body that disagrees is overruled, not
		// trusted to write somewhere else.
		d.SessionID, d.HarnessID = r.PathValue("id"), r.PathValue("harnessId")
		if err := delegates.Put(r.Context(), &d, actor(r)); err != nil {
			writeStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, d)
	})

	mux.HandleFunc("GET /sessions/{id}/delegates", func(w http.ResponseWriter, r *http.Request) {
		list, err := delegates.ForSession(r.Context(), r.PathValue("id"))
		if err != nil {
			writeStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, list)
	})

	// ?state= filters (the rail's cold read is state=running).
	mux.HandleFunc("GET /delegates", func(w http.ResponseWriter, r *http.Request) {
		list, err := delegates.List(r.Context(), contracts.DelegateState(r.URL.Query().Get("state")))
		if err != nil {
			writeStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, list)
	})
}
