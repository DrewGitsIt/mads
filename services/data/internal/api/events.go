package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/services/data/internal/store"
)

// defaultBatchLimit caps one-shot (non-streaming) reads so a cold UI view
// can't drag the whole log over the wire; clients page with ?since=.
const defaultBatchLimit = 500

func (s *server) handlePostEvent(w http.ResponseWriter, r *http.Request) {
	var env contracts.Envelope
	if err := json.NewDecoder(r.Body).Decode(&env); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON: " + err.Error()})
		return
	}
	var v contracts.ValidationError
	if !env.Type.Valid() {
		v.Add("type", fmt.Sprintf("unregistered event type %q", env.Type))
	}
	if env.Actor == "" {
		v.Add("actor", "required")
	}
	if !v.Empty() {
		writeJSON(w, http.StatusUnprocessableEntity, &v)
		return
	}
	env.Seq = 0 // assigned by the log, never by the caller
	if err := s.log.Append(r.Context(), &env); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusCreated, env)
}

func (s *server) handleGetEvents(w http.ResponseWriter, r *http.Request) {
	f, verr := parseFilter(r)
	if verr != nil {
		writeJSON(w, http.StatusUnprocessableEntity, verr)
		return
	}
	if strings.Contains(r.Header.Get("Accept"), "text/event-stream") {
		s.streamEvents(w, r, f)
		return
	}
	f.Limit = defaultBatchLimit
	events, err := s.log.Query(r.Context(), f)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if events == nil {
		events = []contracts.Envelope{}
	}
	writeJSON(w, http.StatusOK, events)
}

// streamEvents is the live subscription path (spec §15): replay from the
// cursor, then push each new commit as it lands. Each SSE message carries
// the event's seq as its id, so a reconnecting client resumes with
// ?since=<last id> and misses nothing.
func (s *server) streamEvents(w http.ResponseWriter, r *http.Request, f store.Filter) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "streaming unsupported"})
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	cursor := f.Since
	for {
		// Arm the wait before querying: anything committed between the query
		// and the wait still wakes us, so no event can fall in the crack.
		wake := s.log.Wait()

		batch := f
		batch.Since = cursor
		events, err := s.log.Query(r.Context(), batch)
		if err != nil {
			return
		}
		for _, env := range events {
			data, err := json.Marshal(env)
			if err != nil {
				return
			}
			if _, err := fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", env.Seq, env.Type, data); err != nil {
				return
			}
			cursor = env.Seq
		}
		if len(events) > 0 {
			flusher.Flush()
		}

		select {
		case <-r.Context().Done():
			return
		case <-wake:
		}
	}
}

// eventTypeShape is what an event type looks like ("domain.name"), whether or
// not this build has it registered. A read filter accepts any well-formed
// type, and an unregistered one simply matches nothing: the UI is served
// live from ui/dist and routinely ships ahead of a long-running Data
// Service, and it subscribes to every type it wants over ONE shared
// EventSource. Rejecting a single type this build doesn't know yet (as a 422
// here, a 502 through the Router) used to fail that whole stream, and the
// UI's reconnect loop retried it forever with the same list, so every live
// view in the window went quiet at once: the rail froze on stale session
// records, the Pipeline tab never saw a run end. Writes still validate
// against the registry (handlePostEvent); only reads are forgiving. Truly
// malformed filters ("nope", "a.b.c") are still rejected.
var eventTypeShape = regexp.MustCompile(`^[a-z][a-z0-9_]*\.[a-z][a-z0-9_]*$`)

func parseFilter(r *http.Request) (store.Filter, *contracts.ValidationError) {
	var (
		f store.Filter
		v contracts.ValidationError
	)
	q := r.URL.Query()
	if raw := q.Get("since"); raw != "" {
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || n < 0 {
			v.Add("since", "must be a non-negative integer cursor")
		} else {
			f.Since = n
		}
	}
	f.SessionID = q.Get("session_id")
	if raw := q.Get("types"); raw != "" {
		for _, p := range strings.Split(raw, ",") {
			p = strings.TrimSpace(p)
			if p == "" {
				continue
			}
			if !contracts.EventType(p).Valid() && !eventTypeShape.MatchString(p) {
				if dom, ok := strings.CutSuffix(p, ".*"); !ok || dom == "" || strings.Contains(dom, ".") {
					v.Add("types", fmt.Sprintf("%q is neither an event type like %q nor a domain wildcard like %q", p, "task.created", "task.*"))
					continue
				}
			}
			f.Types = append(f.Types, p)
		}
	}
	if !v.Empty() {
		return f, &v
	}
	return f, nil
}
