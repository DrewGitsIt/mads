package api

import (
	"context"
	"errors"
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
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) handleResumeSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	sess, err := s.data.Session(r.Context(), id)
	if err != nil {
		writeError(w, err)
		return
	}
	if sess.Activity == contracts.SessionActive {
		writeErrMsg(w, http.StatusConflict, "resume session: ", errors.New("session is already active"))
		return
	}
	if _, tracked := s.sup.SessionAddr(id); tracked {
		writeErrMsg(w, http.StatusConflict, "resume session: ", errors.New("session already has a running child"))
		return
	}
	addr, err := s.sup.SpawnSession(r.Context(), id)
	if err != nil {
		writeErrMsg(w, http.StatusBadGateway, "resume session: ", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"addr": addr})
}

func (s *server) handleDeleteSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	sess, err := s.data.Session(r.Context(), id)
	if err != nil {
		writeError(w, err)
		return
	}
	if sess.Activity == contracts.SessionActive {
		writeErrMsg(w, http.StatusConflict, "delete session: ", errors.New("session is active; close it before deleting"))
		return
	}
	if _, tracked := s.sup.SessionAddr(id); tracked {
		writeErrMsg(w, http.StatusConflict, "delete session: ", errors.New("session has a running child; close it before deleting"))
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
