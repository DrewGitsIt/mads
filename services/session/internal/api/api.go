// Package api is the Session Service's HTTP surface (spec §15): one process,
// one session, so nothing here is scoped by :id — every route acts on the
// single runner.Runner this process owns.
package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"

	"github.com/drewkidwell/mads/services/session/internal/runner"
)

type server struct {
	runner *runner.Runner
}

// New returns the Session Service's HTTP handler.
func New(r *runner.Runner) http.Handler {
	s := &server{runner: r}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", s.handleHealth)
	mux.HandleFunc("POST /messages", s.handleSendMessage)
	mux.HandleFunc("POST /interrupt", s.handleInterrupt)
	mux.HandleFunc("GET /stream", s.handleStream)
	return mux
}

// handleHealth always answers 200 regardless of runner state — it is a
// process-liveness probe, not a session-readiness one. Callers that need to
// know whether the session is ready to accept turns learn that from
// POST /messages's 503, not from here.
func (s *server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"service": "session", "status": "ok"})
}

func (s *server) handleSendMessage(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Text string `json:"text"`
	}
	if !decode(w, r, &body) {
		return
	}
	if err := s.runner.Send(r.Context(), body.Text); err != nil {
		writeRunnerError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "accepted"})
}

func (s *server) handleInterrupt(w http.ResponseWriter, r *http.Request) {
	if err := s.runner.Interrupt(r.Context()); err != nil {
		writeRunnerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleStream is the data plane's live event feed (spec §15): every
// provider event for this session's lifetime, fanned out from the runner's
// hub. There is no replay/cursor here — that's the Data Service's job for
// anything durable (transcript, activity); this is the ephemeral stream a
// connected UI rides while it's open.
func (s *server) handleStream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "streaming unsupported"})
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	ch, unsubscribe := s.runner.Subscribe()
	defer unsubscribe()

	var seq int64
	for {
		select {
		case <-r.Context().Done():
			return
		case ev, ok := <-ch:
			if !ok {
				return
			}
			data, err := json.Marshal(ev)
			if err != nil {
				return
			}
			seq++
			if _, err := fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", seq, ev.Kind, data); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

// writeRunnerError maps the runner's sentinel errors onto this API's
// contract: not-ready (Start hasn't finished, or failed) is a retryable
// 503, a turn already in flight is a 409 the UI's composer-disabled state
// should never actually trigger, and everything else is unexpected.
func writeRunnerError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, runner.ErrNotReady):
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": err.Error()})
	case errors.Is(err, runner.ErrTurnInFlight):
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
	default:
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("api: encode response: %v", err)
	}
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON: " + err.Error()})
		return false
	}
	return true
}
