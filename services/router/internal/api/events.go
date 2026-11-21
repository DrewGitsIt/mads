package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/drewkidwell/mads/pkg/contracts"
)

// handleEvents dispatches GET /events to the streaming (SSE) path or the
// batch (JSON) path based on Accept, mirroring the Data Service's own
// dispatch (services/data/internal/api/events.go handleGetEvents) — an
// EventSource always sends "Accept: text/event-stream"; a plain fetch()
// doesn't, so the two transports can share one route (Pipeline tab
// HANDOFF.md §2).
func (s *server) handleEvents(w http.ResponseWriter, r *http.Request) {
	if strings.Contains(r.Header.Get("Accept"), "text/event-stream") {
		s.handleStreamEvents(w, r)
		return
	}
	since, _ := strconv.ParseInt(r.URL.Query().Get("since"), 10, 64)
	sessionID := r.URL.Query().Get("session_id")
	var types []string
	if raw := r.URL.Query().Get("types"); raw != "" {
		types = strings.Split(raw, ",")
	}
	events, err := s.data.Events(r.Context(), since, sessionID, types)
	if err != nil {
		writeError(w, err)
		return
	}
	if meshHidesRegistry(r) {
		kept := events[:0]
		for _, env := range events {
			if !registryEvent(env.Type) {
				kept = append(kept, redactDevice(env))
			}
		}
		events = kept
	}
	if events == nil {
		events = []contracts.Envelope{}
	}
	writeJSON(w, http.StatusOK, events)
}

// meshHidesRegistry reports whether r was relayed by this node's Mesh
// Service for a device below admin. Such a device doesn't see this host's
// device registry in the event log: peer.* carry its devices' names, and
// device.* each paired device's fingerprint and grant (and refusals), which
// would let a viewing device rebuild who may reach this host and how
// (docs/MESH-AUTH-DESIGN.md, A5: /peers itself is never forwarded). The
// Mesh Service stamps the device's level (contracts.MeshLevelHeader) and
// overwrites any the caller sent; without the relay mark the header means
// nothing.
func meshHidesRegistry(r *http.Request) bool {
	if _, relayed := r.Header[contracts.MeshRelayHeader]; !relayed {
		return false
	}
	return contracts.GrantLevel(r.Header.Get(contracts.MeshLevelHeader)) != contracts.GrantAdmin
}

// registryEvent is an event of the device registry: peer.* and device.*,
// and mesh.* (this host's Mesh Service: its address, its settings).
func registryEvent(t contracts.EventType) bool {
	d := t.Domain()
	return d == "peer" || d == "device" || d == "mesh"
}

// redactDevice hides which paired device did something from a device
// below admin: an actor "device:<name>/<short fingerprint>" (the Mesh
// Service's, D8) reads as just "device". The name is this host's label for
// another device, and the fingerprint identifies it: the registry such a
// device doesn't see.
func redactDevice(env contracts.Envelope) contracts.Envelope {
	if strings.HasPrefix(env.Actor, "device:") {
		env.Actor = "device"
	}
	return env
}

// handleEventsHead proxies the Data Service's GET /events/head: {"seq": N},
// the log's current max seq. The UI's event bus reads it before its first
// connect and streams with ?since=N, so the stream carries only what happens
// from then on (ui/src/router/events.ts).
func (s *server) handleEventsHead(w http.ResponseWriter, r *http.Request) {
	head, err := s.data.EventsHead(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int64{"seq": head})
}

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

	hide := meshHidesRegistry(r)
	for env := range ch {
		if hide && registryEvent(env.Type) {
			continue
		}
		if hide {
			env = redactDevice(env)
		}
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
