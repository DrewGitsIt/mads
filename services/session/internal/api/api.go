// Package api is the Session Service's HTTP surface (spec §15): one process,
// one session, so nothing here is scoped by :id — every route acts on the
// single runner.Runner this process owns.
package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/providers/provider"
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
	mux.HandleFunc("GET /queue", s.handleQueue)
	mux.HandleFunc("DELETE /queue/{id}", s.handleCancelQueued)
	mux.HandleFunc("POST /inject", s.handleInject)
	mux.HandleFunc("POST /interrupt", s.handleInterrupt)
	mux.HandleFunc("POST /compact", s.handleCompact)
	mux.HandleFunc("POST /merge", s.handleMerge)
	mux.HandleFunc("GET /output", s.handleStream)
	mux.HandleFunc("POST /model", s.handleSetModel)
	mux.HandleFunc("POST /transport", s.handleTransport)
	mux.HandleFunc("GET /transport", s.handleTransportStatus)
	mux.HandleFunc("POST /input", s.handleTerminalInput)
	mux.HandleFunc("POST /resize", s.handleTerminalResize)
	mux.HandleFunc("GET /files", s.handleFiles)
	mux.HandleFunc("GET /commits", s.handleCommits)
	mux.HandleFunc("GET /transcript", s.handleTranscript)
	mux.HandleFunc("POST /skills/{name}/inject", s.handleInjectSkill)
	mux.HandleFunc("GET /capabilities/{name}/diagnose", s.handleDiagnoseCapability)
	mux.HandleFunc("POST /orchestrate/refresh", s.handleOrchestrateRefresh)
	mux.HandleFunc("POST /permission-hook", s.handlePermissionHook)
	mux.HandleFunc("GET /permission-requests", s.handlePermissionRequests)
	mux.HandleFunc("POST /permission-requests/{id}/resolve", s.handleResolvePermission)
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
		// Queue asks for a message sent mid-turn to wait behind the turn
		// instead of being refused with 409 (runner.SendOrQueue).
		Queue bool `json:"queue"`
	}
	if !decode(w, r, &body) {
		return
	}
	if !body.Queue {
		if err := s.runner.Send(r.Context(), body.Text); err != nil {
			writeRunnerError(w, err)
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]string{"status": "accepted"})
		return
	}
	queued, err := s.runner.SendOrQueue(r.Context(), body.Text)
	if err != nil {
		writeRunnerError(w, err)
		return
	}
	if queued != nil {
		writeJSON(w, http.StatusAccepted, map[string]any{"status": "queued", "queued": queued})
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "accepted"})
}

// handleQueue lists the user's messages queued behind the in-flight turn.
func (s *server) handleQueue(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"queued": s.runner.QueuedMessages()})
}

// handleCancelQueued drops one queued message before it's delivered; 404
// when it isn't queued (anymore — it may have just gone out).
func (s *server) handleCancelQueued(w http.ResponseWriter, r *http.Request) {
	if !s.runner.CancelQueued(r.PathValue("id")) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "message is not queued"})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleInject delivers a linked session's message into this session's
// conversation (docs/LINKED-SESSIONS-DESIGN.md decisions 1-2). The Router
// has already resolved the Attribution server-side before forwarding here —
// this handler trusts it as given, same as it trusts the Router's other
// proxied writes.
func (s *server) handleInject(w http.ResponseWriter, r *http.Request) {
	var body contracts.InjectRequest
	if !decode(w, r, &body) {
		return
	}
	if err := s.runner.Inject(r.Context(), body.Text, body.Attribution); err != nil {
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

// handleCompact runs a native context-compaction pass on user request (the
// UI's "Compact now" action). Accepted means started — progress and outcome
// arrive as compact events on /output, mirroring the resume-triggered pass.
func (s *server) handleCompact(w http.ResponseWriter, r *http.Request) {
	if err := s.runner.CompactNow(r.Context()); err != nil {
		writeRunnerError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "accepted"})
}

// handleMerge attempts to merge the session's isolated worktree branch back
// into target_branch (the primary's current branch if omitted). Both a
// clean merge and a conflicted merge answer 200 — a conflict is a
// structured, success-shaped response the UI branches on via
// result.conflicted, not a failure status (spec §6, §17).
func (s *server) handleMerge(w http.ResponseWriter, r *http.Request) {
	var body struct {
		TargetBranch string `json:"target_branch"`
	}
	if !decode(w, r, &body) {
		return
	}
	result, err := s.runner.Merge(r.Context(), body.TargetBranch)
	if err != nil {
		writeRunnerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// handleStream is the data plane's live event feed (spec §15): every
// provider event for this session's lifetime, fanned out from the runner's
// hub. There is no replay/cursor here for chat events — that's the Data
// Service's job for anything durable (transcript, activity); this is the
// ephemeral stream a connected UI rides while it's open. Terminal (PTY)
// output is the one exception: SubscribeTerminal replays the hub's bounded
// PTY ring buffer as a single leading EventPTYOutput before live events, so
// a newly attached terminal (reattach, or first attach after PTY bytes
// already flowed) gets scrollback instead of only bytes from here on —
// scoped to that event kind, so chat-mode consumers are unaffected.
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

	replay, ch, unsubscribe := s.runner.SubscribeTerminal()
	defer unsubscribe()

	var seq int64
	writeEvent := func(ev provider.Event) bool {
		data, err := json.Marshal(ev)
		if err != nil {
			return false
		}
		seq++
		if _, err := fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", seq, ev.Kind, data); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}

	if len(replay) > 0 {
		if !writeEvent(provider.Event{Kind: provider.EventPTYOutput, Text: string(replay)}) {
			return
		}
	}

	for {
		select {
		case <-r.Context().Done():
			return
		case ev, ok := <-ch:
			if !ok {
				return
			}
			if !writeEvent(ev) {
				return
			}
		}
	}
}

// handleSetModel changes the model the live provider process runs. The
// Router has already persisted the new model on the session record before
// forwarding here (services/router/internal/api/sessions.go) — this handler
// is only responsible for the process that is running right now.
//
// queued=true means a turn was in flight and the switch was parked until it
// ends, which is the deliberate contract: a model change never interrupts an
// agent mid-turn. The Router passes that flag straight through so the UI can
// say so rather than claiming the model already changed.
func (s *server) handleSetModel(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Model string `json:"model"`
	}
	if !decode(w, r, &body) {
		return
	}
	if strings.TrimSpace(body.Model) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "model must not be empty"})
		return
	}
	queued, err := s.runner.SetModel(r.Context(), body.Model)
	if err != nil {
		writeSwitchError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"model": body.Model, "queued": queued})
}

// handleTransport switches the session's single transport between chat
// (ACP) and terminal (PTY) mode on the same provider ref (spec: one
// conversation, two transports, single-writer handoff). Simultaneous
// mirroring is out of scope — this always tears down the old transport's
// provider process before starting the new one.
func (s *server) handleTransport(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Mode string `json:"mode"`
	}
	if !decode(w, r, &body) {
		return
	}
	var mode provider.Mode
	switch body.Mode {
	case "chat":
		mode = provider.ModeACP
	case "terminal":
		mode = provider.ModePTY
	default:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unknown mode: " + body.Mode})
		return
	}
	ref, err := s.runner.SwitchTransport(r.Context(), mode)
	if err != nil {
		writeSwitchError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"mode": body.Mode, "ref": ref})
}

// handleTransportStatus reports the transport mode currently live on this
// session — read straight off the runner (Mode()), not a separately
// tracked/cached value, since SwitchTransport is the only writer and the
// runner is the single source of truth. Lets the UI initialize its
// Chat/Terminal toggle to whatever's actually running on mount, without
// ever having to fire a switch just to find out.
func (s *server) handleTransportStatus(w http.ResponseWriter, r *http.Request) {
	mode := "chat"
	if s.runner.Mode() == provider.ModePTY {
		mode = "terminal"
	}
	writeJSON(w, http.StatusOK, map[string]string{"mode": mode})
}

// handleTerminalInput writes raw terminal input to the session's current
// transport. The body is the raw bytes to write, unwrapped — same
// pass-through contract the Router's proxy already uses for every other
// route, and it avoids forcing arbitrary terminal byte sequences through a
// JSON string.
func (s *server) handleTerminalInput(w http.ResponseWriter, r *http.Request) {
	data, err := io.ReadAll(r.Body)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "read body: " + err.Error()})
		return
	}
	if err := s.runner.TerminalWrite(data); err != nil {
		writeRunnerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleTerminalResize propagates a terminal resize to the session's
// current transport.
func (s *server) handleTerminalResize(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Cols int `json:"cols"`
		Rows int `json:"rows"`
	}
	if !decode(w, r, &body) {
		return
	}
	if err := s.runner.TerminalResize(body.Cols, body.Rows); err != nil {
		writeRunnerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleOrchestrateRefresh re-evaluates this session's held orchestrate
// grants against its currently running provider process (docs/
// LINKED-SESSIONS-DESIGN.md: dynamic orchestrate materialization) — the
// Router calls this fire-and-forget after a link create/update/delete
// notifies this session mid-conversation (services/router/internal/api/
// links.go, notifyOrchestrateRefresh). Answers 202 regardless of whether the
// refresh actually cycled anything: the link record's own OrchestrateStatus,
// not this response, is the durable signal the rail UI reads.
func (s *server) handleOrchestrateRefresh(w http.ResponseWriter, r *http.Request) {
	if err := s.runner.RefreshOrchestrate(r.Context()); err != nil {
		writeRunnerError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "accepted"})
}

// handleFiles lists every file this session has touched (spec §5), read
// straight through from the Data Service — the durable record, not a
// local cache.
func (s *server) handleFiles(w http.ResponseWriter, r *http.Request) {
	files, err := s.runner.Files(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, files)
}

// handleCommits lists every commit this session has produced, with task
// linkage resolved by the Data Service (spec §5).
func (s *server) handleCommits(w http.ResponseWriter, r *http.Request) {
	commits, err := s.runner.Commits(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, commits)
}

// handleTranscript answers this session's live conversation straight from
// its bound provider instance (runner.Transcript) — the Router proxies here
// for any session with a running child, since the Router's own
// provider.Registry has no visibility into this process's provider session.
func (s *server) handleTranscript(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.runner.Transcript(r.Context()))
}

// handleInjectSkill sends a declared skill's content into the session as a
// turn (spec §12.4) — the system-directed and /command injection paths both
// resolve to this one endpoint.
func (s *server) handleInjectSkill(w http.ResponseWriter, r *http.Request) {
	if err := s.runner.InjectSkill(r.Context(), r.PathValue("name")); err != nil {
		writeRunnerError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "accepted"})
}

// handleDiagnoseCapability runs a live health check (spec §12.7) — not a
// cached read — self-healing the recorded status as a side effect.
func (s *server) handleDiagnoseCapability(w http.ResponseWriter, r *http.Request) {
	healthy, reason, suggestions, err := s.runner.DiagnoseCapability(r.Context(), r.PathValue("name"))
	if err != nil {
		writeRunnerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"healthy":     healthy,
		"reason":      reason,
		"suggestions": suggestions,
	})
}

// handlePermissionHook is the callback the claude PreToolUse hook's `curl`
// command blocks on (providers/claudecode/permission.go builds the exact
// command line). The request body is claude's own PreToolUse JSON, piped
// through unmodified by the hook's stdin — only tool_name/tool_input are
// read here, the rest (session_id, transcript_path, cwd, permission_mode)
// isn't needed since this process already knows all of that about itself.
// The response body IS the hook's required stdout verbatim (the shell
// command prints curl's output straight through), so its shape is
// hookSpecificOutput.permissionDecision exactly, not this API's usual
// {"error":...}/{"status":...} envelope.
func (s *server) handlePermissionHook(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ToolName  string          `json:"tool_name"`
		ToolInput json.RawMessage `json:"tool_input"`
	}
	if !decode(w, r, &body) {
		return
	}
	decision := s.runner.RequestPermission(body.ToolName, body.ToolInput)
	// hookEventName is part of claude's hookSpecificOutput contract for
	// PreToolUse (the repo's own SessionStart hook includes its equivalent —
	// see providers/claudecode/complete_test.go). Omitting it risks claude
	// treating the output as malformed and falling back to its default
	// ask-with-nobody-to-answer, i.e. a silent indefinite stall.
	resp := map[string]any{
		"hookSpecificOutput": map[string]string{
			"hookEventName":      "PreToolUse",
			"permissionDecision": decision,
		},
	}
	if decision == "deny" {
		resp["systemMessage"] = "Permission request was denied (or expired awaiting a response) via the chat UI."
	}
	writeJSON(w, http.StatusOK, resp)
}

// handlePermissionRequests answers every still-pending permission request
// for this session — a chat client reconciles this against its live-stream
// state on transcript refetch, so a still-open card survives a reload
// (acceptance criterion 3).
func (s *server) handlePermissionRequests(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.runner.PermissionRequests())
}

// handleResolvePermission answers a pending permission request from the chat
// UI's Allow/Deny card.
func (s *server) handleResolvePermission(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Decision string `json:"decision"`
	}
	if !decode(w, r, &body) {
		return
	}
	if !s.runner.ResolvePermission(r.PathValue("id"), body.Decision) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no pending permission request with that id"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
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
	case errors.Is(err, runner.ErrNoWorktree):
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
	case errors.Is(err, runner.ErrSwitchBusy):
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
	case errors.Is(err, runner.ErrNotPTYMode):
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
	case errors.Is(err, runner.ErrCompactUnsupported):
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
	default:
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
}

// writeSwitchError maps SwitchTransport's outcomes onto POST /transport's
// error contract, which — unlike writeRunnerError's plain {"error":...} —
// carries a machine-readable code and a retryable hint so the UI can offer
// the right recovery action (interrupt-then-switch for busy, retry/reopen
// for a failed resume) without string-matching the message.
func writeSwitchError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, runner.ErrNotReady):
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": err.Error(), "code": "not_ready", "retryable": true})
	case errors.Is(err, runner.ErrSwitchBusy):
		writeJSON(w, http.StatusConflict, map[string]any{"error": err.Error(), "code": "busy", "retryable": true})
	default:
		// SwitchTransport wraps the provider's Start error for anything
		// past the busy/not-ready gates — the old process is already dead
		// at this point, so the caller's only paths forward are retrying
		// the switch or reopening the mode it just left.
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error(), "code": "resume_failed", "retryable": true})
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
