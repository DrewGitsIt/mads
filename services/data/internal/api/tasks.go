package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/services/data/internal/store"
)

// actor identifies who is making a mutating request ("user", "synthesizer",
// "pipeline", an agent name). Defaults to "user" — the UI's case.
func actor(r *http.Request) string {
	if a := r.Header.Get("X-Mads-Actor"); a != "" {
		return a
	}
	return "user"
}

// writeStoreError maps store errors onto the API contract: ValidationError →
// 422 with every problem listed (spec §10.3), LeaseHeldError → 409 code
// "lease_held", ErrLeaseLost → 409 code "lease_lost", ErrNotFound → 404,
// else 500.
func writeStoreError(w http.ResponseWriter, err error) {
	var (
		verr *contracts.ValidationError
		lerr *store.LeaseHeldError
	)
	switch {
	case errors.As(err, &verr):
		writeJSON(w, http.StatusUnprocessableEntity, verr)
	case errors.As(err, &lerr):
		writeJSON(w, http.StatusConflict, map[string]string{"error": lerr.Error(), "code": "lease_held", "holder": lerr.Holder})
	case errors.Is(err, store.ErrLeaseLost):
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error(), "code": "lease_lost"})
	case errors.Is(err, store.ErrNotFound):
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
	default:
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON: " + err.Error()})
		return false
	}
	return true
}

func (s *server) handleCreateTask(w http.ResponseWriter, r *http.Request) {
	var t contracts.Task
	if !decode(w, r, &t) {
		return
	}
	t.ID = "" // assigned by the store
	if err := s.tasks.Create(r.Context(), &t, actor(r)); err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, t)
}

func (s *server) handleImportTasks(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Tasks []*contracts.Task `json:"tasks"`
	}
	if !decode(w, r, &body) {
		return
	}
	if len(body.Tasks) == 0 {
		verr := &contracts.ValidationError{}
		verr.Add("tasks", "at least one task required")
		writeJSON(w, http.StatusUnprocessableEntity, verr)
		return
	}
	if err := s.tasks.Import(r.Context(), body.Tasks, actor(r)); err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, body)
}

func (s *server) handleListTasks(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	tq := store.TaskQuery{SessionID: q.Get("session_id")}
	if raw := q.Get("status"); raw != "" {
		st := contracts.TaskStatus(raw)
		if !st.Valid() {
			verr := &contracts.ValidationError{}
			verr.Add("status", "unknown status "+raw)
			writeJSON(w, http.StatusUnprocessableEntity, verr)
			return
		}
		tq.Status = st
	}
	if raw := q.Get("draft"); raw != "" {
		d := raw == "true"
		tq.Draft = &d
	}
	tasks, err := s.tasks.List(r.Context(), tq)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if tasks == nil {
		tasks = []contracts.Task{}
	}
	writeJSON(w, http.StatusOK, tasks)
}

func (s *server) handleGetTask(w http.ResponseWriter, r *http.Request) {
	t, err := s.tasks.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func (s *server) handleUpdateTask(w http.ResponseWriter, r *http.Request) {
	var t contracts.Task
	if !decode(w, r, &t) {
		return
	}
	t.ID = r.PathValue("id")
	if err := s.tasks.Update(leaseCtx(r), &t, actor(r)); err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func (s *server) handleConfirmTask(w http.ResponseWriter, r *http.Request) {
	if err := s.tasks.Confirm(leaseCtx(r), r.PathValue("id"), actor(r)); err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "confirmed"})
}

func (s *server) handleDiscardTask(w http.ResponseWriter, r *http.Request) {
	if err := s.tasks.Discard(leaseCtx(r), r.PathValue("id"), actor(r)); err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "discarded"})
}

func (s *server) handleAddHandoff(w http.ResponseWriter, r *http.Request) {
	var h contracts.Handoff
	if !decode(w, r, &h) {
		return
	}
	if err := s.tasks.AddHandoff(leaseCtx(r), r.PathValue("id"), &h, actor(r)); err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, h)
}

func (s *server) handleAddNote(w http.ResponseWriter, r *http.Request) {
	var n contracts.Note
	if !decode(w, r, &n) {
		return
	}
	if err := s.tasks.AddNote(leaseCtx(r), r.PathValue("id"), &n, actor(r)); err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, n)
}

func (s *server) handleConfirmNote(w http.ResponseWriter, r *http.Request) {
	noteID, err := strconv.ParseInt(r.PathValue("noteId"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid note id"})
		return
	}
	if err := s.tasks.ConfirmNote(leaseCtx(r), r.PathValue("id"), noteID, actor(r)); err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "confirmed"})
}

func (s *server) handleDiscardNote(w http.ResponseWriter, r *http.Request) {
	noteID, err := strconv.ParseInt(r.PathValue("noteId"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid note id"})
		return
	}
	if err := s.tasks.DiscardNote(leaseCtx(r), r.PathValue("id"), noteID, actor(r)); err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "discarded"})
}

func (s *server) handleTaskCycles(w http.ResponseWriter, r *http.Request) {
	sessionID := r.URL.Query().Get("session_id")
	if sessionID == "" {
		verr := &contracts.ValidationError{}
		verr.Add("session_id", "required")
		writeJSON(w, http.StatusUnprocessableEntity, verr)
		return
	}
	cycles, err := s.tasks.Cycles(r.Context(), sessionID)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if cycles == nil {
		cycles = [][]string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"cycles": cycles})
}
