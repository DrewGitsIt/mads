package api

import (
	"context"
	"net/http"

	"github.com/drewkidwell/mads/pkg/contracts"
)

func (s *server) handleGetSession(w http.ResponseWriter, r *http.Request) {
	sess, err := s.data.Session(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, sess)
}

func (s *server) handleCreateSession(w http.ResponseWriter, r *http.Request) {
	var sess contracts.Session
	if !decode(w, r, &sess) {
		return
	}
	sess.ID = ""
	if err := s.resolveSessionDefaults(r.Context(), &sess); err != nil {
		writeError(w, err)
		return
	}
	if _, err := s.providers.Get(sess.Provider); err != nil {
		writeErrMsg(w, http.StatusBadRequest, "create session: ", err)
		return
	}
	if err := s.data.CreateSession(r.Context(), &sess); err != nil {
		writeError(w, err)
		return
	}
	addr, err := s.sup.SpawnSession(r.Context(), sess.ID)
	if err != nil {
		// Best-effort rollback so a failed spawn doesn't leave an orphaned
		// session record with no child behind it. Uses a background context:
		// the record still needs cleaning up even if the request that asked
		// for it is gone.
		_ = s.data.DeleteSession(context.Background(), sess.ID)
		writeErrMsg(w, http.StatusBadGateway, "spawn session: ", err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"session": sess, "addr": addr})
}

func (s *server) handleTerminateSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.sup.TerminateSession(r.Context(), id); err != nil {
		writeErrMsg(w, http.StatusBadGateway, "terminate session: ", err)
		return
	}
	if err := s.data.DeleteSession(r.Context(), id); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) handleBounceSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	addr, err := s.sup.BounceSession(r.Context(), id)
	if err != nil {
		writeErrMsg(w, http.StatusBadGateway, "bounce session: ", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"addr": addr})
}
