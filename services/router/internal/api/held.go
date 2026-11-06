package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/services/router/internal/job"
)

// Held link updates (docs/HELD-UPDATES-DESIGN.md): pushes deferred while a
// session slept. The conversation lists them, the user can discard (and
// Undo) them, deliver them on their own ("Send updates only"), or — the
// default — send a message, which carries them in the same turn.

// handleHeldUpdates lists the session's held and discarded updates.
func (s *server) handleHeldUpdates(w http.ResponseWriter, r *http.Request) {
	held, err := s.data.HeldUpdates(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, held)
}

// handleDiscardHeld and handleRestoreHeld soft-delete and restore held
// updates by id, as the user (the audit event's actor).
func (s *server) handleDiscardHeld(w http.ResponseWriter, r *http.Request) {
	s.setHeldDiscarded(w, r, true)
}

func (s *server) handleRestoreHeld(w http.ResponseWriter, r *http.Request) {
	s.setHeldDiscarded(w, r, false)
}

func (s *server) setHeldDiscarded(w http.ResponseWriter, r *http.Request, discard bool) {
	var body contracts.LinkDigestIDs
	if !decode(w, r, &body) {
		return
	}
	data := s.data.As("user")
	set := data.RestoreLinkDigests
	if discard {
		set = data.DiscardLinkDigests
	}
	n, err := set(r.Context(), body.IDs)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, contracts.LinkDigestsChanged{Changed: n})
}

// handleDeliverHeld is "Send updates only": the held updates go to the
// agent as one forced turn of their own, the way FlushDeferred delivers
// them, waking the session if it sleeps. Under the wake lock the drain
// happens before the wake, as a waking send's does, so the start hook finds
// nothing and the delivery is this handler's to report. 202
// {"delivered": n}; n is 0 when nothing was held.
func (s *server) handleDeliverHeld(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := s.data.Session(r.Context(), id); err != nil {
		writeError(w, err)
		return
	}
	deps := job.Deps{Data: s.data, Resolver: s.sup}
	unlock := s.lockWake(id)
	defer unlock()
	held, err := job.DrainHeld(r.Context(), s.data, id)
	if err != nil {
		writeError(w, err)
		return
	}
	if _, tracked := s.sup.SessionAddr(id); !tracked {
		if _, err := s.wakeSession(r.Context(), id); err != nil {
			held.Requeue(r.Context(), s.data)
			writeWakeError(w, err)
			return
		}
	}
	n, err := job.DeliverHeld(r.Context(), deps, id, held)
	if err != nil {
		writeErrMsg(w, http.StatusBadGateway, "deliver held updates: ", err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]int{"delivered": n})
}

// sendWaking forwards a send to a session with no child (the caller holds
// its wake lock): drain the held updates, wake, and forward the message
// with them attached. Whatever goes wrong before the child accepts the
// message — the wake, the forward, a refusal — puts the drained updates
// back (Requeue), as FlushDeferred does on a failed delivery.
func (s *server) sendWaking(w http.ResponseWriter, r *http.Request, id string, body map[string]json.RawMessage) {
	var held *job.Held
	if body != nil {
		h, err := job.DrainHeld(r.Context(), s.data, id)
		if err != nil {
			// Not worth failing the message over: the updates stay held and
			// the start hook delivers them as their own turn.
			log.Printf("router: drain held updates for %s: %v", id, err)
		} else {
			held = h
		}
	}
	// Detached from the request: a client that gives up mid-send cancels
	// r.Context(), and the drained updates must still go back.
	requeue := func() { held.Requeue(context.WithoutCancel(r.Context()), s.data) }
	addr, err := s.wakeSession(r.Context(), id)
	if err != nil {
		requeue()
		writeWakeError(w, err)
		return
	}
	_, forged := body["attached"]
	delete(body, "attached")
	parts := held.Parts(false)
	if len(parts) > 0 {
		body["attached"] = mustJSON(parts)
	}
	if (forged || len(parts) > 0) && !setMessageBody(w, r, body) {
		requeue()
		return
	}
	s.clearAsideOnSend(r.Context(), id)
	rec := &statusRecorder{ResponseWriter: w}
	s.proxyTo(rec, r, addr, "/messages")
	if rec.status < 200 || rec.status > 299 {
		requeue()
	}
}

// messageBody reads a POST /messages body as a JSON object, leaving r.Body
// readable again. nil (and true) for a body that isn't one: the child
// answers a malformed body, and nothing is attached to it.
func (s *server) messageBody(w http.ResponseWriter, r *http.Request) (map[string]json.RawMessage, bool) {
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		writeErrMsg(w, http.StatusBadRequest, "read message: ", err)
		return nil, false
	}
	r.Body = io.NopCloser(bytes.NewReader(raw))
	var body map[string]json.RawMessage
	if json.Unmarshal(raw, &body) != nil || body == nil {
		return nil, true
	}
	return body, true
}

// setMessageBody replaces r's body with body, re-encoded.
func setMessageBody(w http.ResponseWriter, r *http.Request, body map[string]json.RawMessage) bool {
	raw, err := json.Marshal(body)
	if err != nil {
		writeErrMsg(w, http.StatusInternalServerError, "", err)
		return false
	}
	r.Body = io.NopCloser(bytes.NewReader(raw))
	r.ContentLength = int64(len(raw))
	r.Header.Del("Content-Length")
	return true
}

// statusRecorder remembers the status a proxied response was written with.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	if s.status == 0 {
		s.status = code
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	return s.ResponseWriter.Write(b)
}

// Flush keeps the recorder transparent to httputil.ReverseProxy's flushing.
func (s *statusRecorder) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
