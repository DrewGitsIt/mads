package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
)

// handleSessionOutput and handlePipelineOutput forward the UI's data-plane
// subscription (spec §15) to a running child's live agent-output stream.
// This traffic is never persisted: it is byte-transparent reverse-proxied
// straight from the child's /output to the UI connection and never touches
// s.data (the Data Service is not on this path at all).
func (s *server) handleSessionOutput(w http.ResponseWriter, r *http.Request) {
	addr, ok := s.sup.SessionAddr(r.PathValue("id"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "session not running"})
		return
	}
	s.proxyTo(w, r, addr, "/output")
}

func (s *server) handlePipelineOutput(w http.ResponseWriter, r *http.Request) {
	addr, ok := s.sup.PipelineAddr(r.PathValue("id"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "pipeline not running"})
		return
	}
	s.proxyTo(w, r, addr, "/output")
}

// handleSessionMessages and handleSessionInterrupt forward a conversation
// turn's control plane (send / interrupt) to the running session child's own
// POST /messages and POST /interrupt — the child already implements both
// (services/session/internal/api/api.go); the Router previously had no
// route for either, a documented gap (route_manifest_test.go's
// knownGapRoutes). Request and response bodies pass through unmodified once
// a child is addressed — the child owns turn semantics (busy/409,
// not-ready/503), the Router is just the address lookup in front of it.
// handleSessionMessages differs from handleSessionInterrupt in one respect:
// if no child is tracked, it first wakes the session (see ensureSession,
// sessions.go) rather than 404ing — a composer send should never surface
// "session not running" to the UI.
func (s *server) handleSessionMessages(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	// Before the wake: a send naming a missing attachment fails fast
	// rather than starting a child for a message that can't go out.
	if !s.resolveMessageAttachments(w, r, id) {
		return
	}
	addr, err := s.ensureSession(r.Context(), id)
	if err != nil {
		writeWakeError(w, err)
		return
	}
	s.clearAsideOnSend(r.Context(), id)
	s.proxyTo(w, r, addr, "/messages")
}

// clearAsideOnSend retires the user's "set aside" judgement because the user
// just contradicted it.
//
// Set aside means "I am done with this for now" (contracts.SessionTagAside),
// and the rail folds the card away on the strength of that. Sending the
// session a message is the user saying the opposite, so the tag goes — the
// card is not merely surfaced for the duration of the turn. Surfacing would
// have been the smaller change and it is the wrong one twice over: the card
// would drop back into the fold the moment the turn ended, taking the answer
// with it, and the ✓ would keep reading pressed on a session sitting in the
// main list. A judgement the user has revised should not survive as view
// state the system keeps re-applying.
//
// This is the one place in the system that can tell the difference. A user
// message arrives at POST /messages; everything the system starts on its own
// — a linked session's push, a digest, a task-terminal notice — arrives at
// the child's POST /inject with an attribution attached (runner.Inject). So
// an unprompted turn leaves a set-aside session exactly where it is, which
// is the whole point of the fold.
//
// Placed after the wake, so a send to a session that cannot start leaves the
// record alone; and best-effort, because a tag is never worth failing the
// user's message over. The store no-ops when the tag isn't there, so the
// common send writes nothing.
func (s *server) clearAsideOnSend(ctx context.Context, id string) {
	if err := s.data.SetSessionAside(ctx, id, false); err != nil {
		log.Printf("router: clear aside on send for %s: %v", id, err)
	}
}

// handleSessionQueue and handleSessionCancelQueued forward the composer's
// queued-message list and its per-message remove to the running child's own
// GET /queue and DELETE /queue/{id}. The queue is the child's in-memory
// state, so a session with no tracked child has nothing queued by
// construction — an empty list, not a 404, like the permission-request list
// below; cancelling against one is still a 404.
func (s *server) handleSessionQueue(w http.ResponseWriter, r *http.Request) {
	addr, ok := s.sup.SessionAddr(r.PathValue("id"))
	if !ok {
		writeJSON(w, http.StatusOK, map[string]any{"queued": []any{}})
		return
	}
	s.proxyTo(w, r, addr, "/queue")
}

func (s *server) handleSessionCancelQueued(w http.ResponseWriter, r *http.Request) {
	addr, ok := s.sup.SessionAddr(r.PathValue("id"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "session not running"})
		return
	}
	s.proxyTo(w, r, addr, "/queue/"+r.PathValue("qid"))
}

// handleSessionCompact forwards the UI's "Compact now" action to the running
// session child's own POST /compact. It wakes a dormant session first, like
// handleSessionMessages rather than handleSessionInterrupt: the action's
// whole point is trimming a large conversation before the next turn, which
// applies at least as much to a session that isn't currently running. The
// optional body ({"instructions": "..."}, /compact's focus text) passes
// through to the child untouched.
func (s *server) handleSessionCompact(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	addr, err := s.ensureSession(r.Context(), id)
	if err != nil {
		writeWakeError(w, err)
		return
	}
	s.proxyTo(w, r, addr, "/compact")
}

func (s *server) handleSessionInterrupt(w http.ResponseWriter, r *http.Request) {
	addr, ok := s.sup.SessionAddr(r.PathValue("id"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "session not running"})
		return
	}
	s.proxyTo(w, r, addr, "/interrupt")
}

// handleSessionTransport, handleSessionInput, and handleSessionResize
// forward the chat/terminal transport-switch control plane to the running
// session child's own POST /transport, POST /input, and POST /resize
// (services/session/internal/api/api.go). Pure reverse-proxies, same as
// /messages and /interrupt above — no status-code special-casing, so the
// child's 409 (mid-turn switch refused) and 502 (resume failed) responses
// pass through untouched.
func (s *server) handleSessionTransport(w http.ResponseWriter, r *http.Request) {
	addr, ok := s.sup.SessionAddr(r.PathValue("id"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "session not running"})
		return
	}
	s.proxyTo(w, r, addr, "/transport")
}

// handleGetSessionTransport reports the session's current live transport
// mode, read straight from the running child (GET /transport,
// services/session's Runner.Mode() — the only writer of that state, via
// SwitchTransport). A session with no tracked child has no transport
// history to report at all, so this answers "chat" — the UI's own
// default — instead of 404ing; unlike every other proxy in this file, a
// missing child is not an error here.
func (s *server) handleGetSessionTransport(w http.ResponseWriter, r *http.Request) {
	addr, ok := s.sup.SessionAddr(r.PathValue("id"))
	if !ok {
		writeJSON(w, http.StatusOK, map[string]string{"mode": "chat"})
		return
	}
	s.proxyTo(w, r, addr, "/transport")
}

func (s *server) handleSessionInput(w http.ResponseWriter, r *http.Request) {
	addr, ok := s.sup.SessionAddr(r.PathValue("id"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "session not running"})
		return
	}
	s.proxyTo(w, r, addr, "/input")
}

func (s *server) handleSessionResize(w http.ResponseWriter, r *http.Request) {
	addr, ok := s.sup.SessionAddr(r.PathValue("id"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "session not running"})
		return
	}
	s.proxyTo(w, r, addr, "/resize")
}

// handleSessionPermissionRequests and handleSessionResolvePermission forward
// the chat permission-card control plane to the running session child's own
// GET /permission-requests and POST /permission-requests/{id}/resolve
// (services/session/internal/api/api.go). A session with no tracked child
// has no pending requests to report by construction (the hook that would
// have created one only runs from a live provider process), so — unlike the
// plain proxies above — an absent child answers an empty list rather than
// 404ing; resolving against one is still an error, since there is nothing
// live to answer.
func (s *server) handleSessionPermissionRequests(w http.ResponseWriter, r *http.Request) {
	addr, ok := s.sup.SessionAddr(r.PathValue("id"))
	if !ok {
		writeJSON(w, http.StatusOK, []any{})
		return
	}
	s.proxyTo(w, r, addr, "/permission-requests")
}

func (s *server) handleSessionResolvePermission(w http.ResponseWriter, r *http.Request) {
	addr, ok := s.sup.SessionAddr(r.PathValue("id"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "session not running"})
		return
	}
	s.proxyTo(w, r, addr, "/permission-requests/"+r.PathValue("reqId")+"/resolve")
}

// proxyTo reverse-proxies r to addr, rewriting the path to childPath —
// shared by the streaming /output forwarders above and the JSON /messages
// and /interrupt forwarders (httputil.ReverseProxy handles both request
// shapes identically; it isn't SSE-specific).
func (s *server) proxyTo(w http.ResponseWriter, r *http.Request, addr, childPath string) {
	target := &url.URL{Scheme: "http", Host: addr}
	proxy := httputil.NewSingleHostReverseProxy(target)
	r2 := r.Clone(r.Context())
	r2.URL.Path = childPath
	r2.URL.RawPath = ""
	proxy.ServeHTTP(w, r2)
}

// postJSONToChild sends body as a fresh JSON POST to addr+path, rather than
// byte-transparently proxying r — some routes (e.g. link inject) need the
// Router to transform the request server-side (resolving Attribution)
// before it reaches the child, which proxyTo's pass-through can't do.
//
//nolint:unused // no caller since links-land; orchestrate.go and job/digest.go still name it as the POST counterpart, so its owner removes or reuses it
func (s *server) postJSONToChild(w http.ResponseWriter, r *http.Request, addr, path string, body any) {
	raw, err := json.Marshal(body)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, "http://"+addr+path, bytes.NewReader(raw))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	defer func() { _ = resp.Body.Close() }()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}
