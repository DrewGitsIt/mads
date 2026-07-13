package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

// handleStreamEvents is the UI's single control-plane subscription point
// (spec §15): each request opens its own upstream dataclient.Stream call
// scoped to its own filters, so fan-out (independent subscribers) and
// server-side filtering are the Data Service's job — Router is a
// transparent per-subscriber proxy, not a shared multiplexer. Re-marshalling
// each envelope with its own Seq/Type reproduces the Data Service's
// id:/event:/data: wire format exactly, so a client that reconnects with
// ?since=<last id> resumes without gaps whether it's talking to the Data
// Service directly or through Router.
func (s *server) handleStreamEvents(w http.ResponseWriter, r *http.Request) {
	since, _ := strconv.ParseInt(r.URL.Query().Get("since"), 10, 64)
	sessionID := r.URL.Query().Get("session_id")
	var types []string
	if raw := r.URL.Query().Get("types"); raw != "" {
		types = strings.Split(raw, ",")
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "streaming unsupported"})
		return
	}

	ch, err := s.data.Stream(r.Context(), since, sessionID, types)
	if err != nil {
		writeErrMsg(w, http.StatusBadGateway, "stream: ", err)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	for env := range ch {
		data, err := json.Marshal(env)
		if err != nil {
			return
		}
		if _, err := fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", env.Seq, env.Type, data); err != nil {
			return
		}
		flusher.Flush()
	}
}
