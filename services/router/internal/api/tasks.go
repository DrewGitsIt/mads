package api

import (
	"net/http"
	"strconv"

	"github.com/drewkidwell/mads/pkg/contracts"
)

// This file is a pure pass-through over dataclient's task/note/file-change
// methods — no Router-side state, same shape as sessions.go. The Data
// Service already owns all validation and persistence (services/data/internal
// /api/tasks.go); Router just proxies the wire shape and maps errors onto its
// own convention (writeError, not the Data Service's writeStoreError).

func (s *server) handleCreateTask(w http.ResponseWriter, r *http.Request) {
	var t contracts.Task
	if !decode(w, r, &t) {
		return
	}
	if err := s.data.CreateTask(r.Context(), &t); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, t)
}

func (s *server) handleImportTasks(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Tasks []contracts.Task `json:"tasks"`
	}
	if !decode(w, r, &body) {
		return
	}
	tasks, err := s.data.ImportTasks(r.Context(), body.Tasks)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"tasks": tasks})
}

func (s *server) handleListTasks(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	tasks, err := s.data.Tasks(r.Context(), q.Get("session_id"), contracts.TaskStatus(q.Get("status")))
	if err != nil {
		writeError(w, err)
		return
	}
	if tasks == nil {
		tasks = []contracts.Task{}
	}
	writeJSON(w, http.StatusOK, tasks)
}

func (s *server) handleTaskCycles(w http.ResponseWriter, r *http.Request) {
	cycles, err := s.data.Cycles(r.Context(), r.URL.Query().Get("session_id"))
	if err != nil {
		writeError(w, err)
		return
	}
	if cycles == nil {
		cycles = [][]string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"cycles": cycles})
}

func (s *server) handleGetTask(w http.ResponseWriter, r *http.Request) {
	t, err := s.data.Task(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

// handleUpdateTask is a full-replace PUT (spec: the Data Service has no
// PATCH for tasks) — the UI must send the entire Task object, not a partial
// diff, or every unset field is nulled out server-side.
func (s *server) handleUpdateTask(w http.ResponseWriter, r *http.Request) {
	var t contracts.Task
	if !decode(w, r, &t) {
		return
	}
	t.ID = r.PathValue("id")
	if err := s.data.UpdateTask(r.Context(), &t); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func (s *server) handleConfirmTask(w http.ResponseWriter, r *http.Request) {
	if err := s.data.ConfirmTask(r.Context(), r.PathValue("id")); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "confirmed"})
}

func (s *server) handleDiscardTask(w http.ResponseWriter, r *http.Request) {
	if err := s.data.DiscardTask(r.Context(), r.PathValue("id")); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "discarded"})
}

func (s *server) handleAddNote(w http.ResponseWriter, r *http.Request) {
	var n contracts.Note
	if !decode(w, r, &n) {
		return
	}
	if err := s.data.AddNote(r.Context(), r.PathValue("id"), &n); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, n)
}

func (s *server) handleConfirmNote(w http.ResponseWriter, r *http.Request) {
	noteID, err := parseNoteID(w, r)
	if err != nil {
		return
	}
	if err := s.data.ConfirmNote(r.Context(), r.PathValue("id"), noteID); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "confirmed"})
}

func (s *server) handleDiscardNote(w http.ResponseWriter, r *http.Request) {
	noteID, err := parseNoteID(w, r)
	if err != nil {
		return
	}
	if err := s.data.DiscardNote(r.Context(), r.PathValue("id"), noteID); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "discarded"})
}

func parseNoteID(w http.ResponseWriter, r *http.Request) (int64, error) {
	noteID, err := strconv.ParseInt(r.PathValue("noteId"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid note id"})
		return 0, err
	}
	return noteID, nil
}

func (s *server) handleSessionFiles(w http.ResponseWriter, r *http.Request) {
	changes, err := s.data.FileChanges(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	if changes == nil {
		changes = []contracts.FileChange{}
	}
	writeJSON(w, http.StatusOK, changes)
}
