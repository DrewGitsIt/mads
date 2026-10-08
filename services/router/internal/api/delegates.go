package api

import (
	"net/http"

	"github.com/drewkidwell/mads/pkg/contracts"
)

// handleListDelegates serves the delegate records (docs/DELEGATES-DESIGN.md),
// filtered by ?state= — the session rail's cold read is state=running, one
// call for every session rather than one per card.
func (s *server) handleListDelegates(w http.ResponseWriter, r *http.Request) {
	list, err := s.data.Delegates(r.Context(), contracts.DelegateState(r.URL.Query().Get("state")))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

// handleListSessionDelegates serves one session's delegates, oldest first.
func (s *server) handleListSessionDelegates(w http.ResponseWriter, r *http.Request) {
	list, err := s.data.SessionDelegates(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}
