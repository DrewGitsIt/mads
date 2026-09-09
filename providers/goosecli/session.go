package goosecli

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/drewkidwell/mads/providers/provider"
)

// startACP launches goose's structured transport: `goose acp`, a
// standards-compliant JSON-RPC 2.0 agent over stdio (official
// agent-client-protocol Rust SDK, schema v1). Lifecycle per
// docs/GOOSE-ADAPTER-DESIGN.md "ACP session lifecycle": initialize with fs
// capabilities false, session/new or session/load, explicit
// session/set_mode from Trust, session/set_config_option for the model,
// then the unstable system-prompt append.
func (p *Provider) startACP(ctx context.Context, opts provider.StartOptions) (provider.Session, error) {
	args := []string{"acp"}
	for _, b := range p.cfg.Builtins {
		args = append(args, "--with-builtin", b)
	}
	args = append(args, p.cfg.ExtraArgs...)
	cmd := exec.Command(p.cfg.Bin, args...)
	cmd.Dir = opts.Cwd
	cmd.Env = p.env(opts.Env)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %s acp: %w", p.cfg.Bin, err)
	}

	trust := opts.Trust
	if trust == "" {
		trust = provider.TrustFull
	}
	s := &streamSession{
		cmd:       cmd,
		stdin:     stdin,
		events:    make(chan provider.Event, 256),
		pending:   make(map[int]chan rpcMessage),
		trust:     trust,
		toolCalls: make(map[string]toolCallInfo),
	}
	go s.read(stdout)

	fail := func(step string, err error) (provider.Session, error) {
		_ = s.Close()
		return nil, fmt.Errorf("goose acp %s: %w", step, err)
	}

	if _, err := s.request(ctx, "initialize", initializeParams{ProtocolVersion: 1}); err != nil {
		return fail("initialize", err)
	}

	// mcpServers is required on both session/new and session/load even when
	// empty — 1.48.0 rejects its absence with -32602 (pinned:
	// testdata/captures/acp-load-replay.jsonl lines 3-4).
	mcp := mcpEntries(opts.MCPServers)

	if opts.Resume != "" {
		s.mu.Lock()
		s.replaying = true
		s.mu.Unlock()
		_, err := s.request(ctx, "session/load", loadSessionParams{SessionID: opts.Resume, Cwd: opts.Cwd, MCPServers: mcp})
		s.mu.Lock()
		s.replaying = false
		s.ref = opts.Resume
		s.mu.Unlock()
		if err != nil {
			// resource_not_found on an unknown ref is a real failure, not an
			// ErrUnsupported case — resuming a bad ref must fail loudly.
			return fail("session/load", err)
		}
	} else {
		resp, err := s.request(ctx, "session/new", newSessionParams{Cwd: opts.Cwd, MCPServers: mcp})
		if err != nil {
			return fail("session/new", err)
		}
		var result newSessionResult
		if err := json.Unmarshal(resp.Result, &result); err != nil {
			return fail("session/new result", err)
		}
		s.mu.Lock()
		s.ref = result.SessionID
		s.mu.Unlock()
	}

	// Always set the mode explicitly — a full-trust session must not inherit
	// a user config default of "approve" (which would stall headless on a
	// permission request nobody answers), and a resumed session's persisted
	// mode must not override this session's trust level.
	modeID := "auto"
	if trust == provider.TrustLimited || trust == provider.TrustReadOnly {
		modeID = "approve"
	}
	if _, err := s.request(ctx, "session/set_mode", setModeParams{SessionID: s.Ref(), ModeID: modeID}); err != nil {
		return fail("session/set_mode", err)
	}

	if opts.Model != "" {
		// A bad model fails Start loudly rather than silently running the
		// default.
		if _, err := s.request(ctx, "session/set_config_option", setConfigOptionParams{SessionID: s.Ref(), ConfigID: "model", Value: opts.Model}); err != nil {
			return fail("session/set_config_option model", err)
		}
	}

	if opts.SystemPrompt != "" {
		resp, err := s.setSystemPromptUnstable(ctx, opts.SystemPrompt)
		if err != nil {
			if resp.Error == nil || resp.Error.Code != -32601 {
				return fail("system prompt", err)
			}
			// method_not_found: a future goose dropped the unstable method.
			// Fall back to prefixing the first Send with the prompt text —
			// claude-code-era emulation, better than dropping the agent
			// definition on the floor.
			s.mu.Lock()
			s.promptPrefix = opts.SystemPrompt
			s.mu.Unlock()
		}
	}

	return s, nil
}

// setSystemPromptUnstable calls _goose/unstable/session/system-prompt/set
// — one of the goose extension methods this adapter depends on, kept in a
// named helper so every _goose/unstable/* use stays greppable (T10).
func (s *streamSession) setSystemPromptUnstable(ctx context.Context, text string) (rpcMessage, error) {
	return s.request(ctx, "_goose/unstable/session/system-prompt/set",
		systemPromptParams{SessionID: s.Ref(), Mode: "append", Key: "mads-agent", Text: text})
}

// mcpEntries converts StartOptions.MCPServers to spec stdio descriptors.
// Command is shell-split with strings.Fields, the MCPServerRef convention
// (see providers/claudecode's writeMCPConfig).
func mcpEntries(servers []provider.MCPServerRef) []mcpServerEntry {
	entries := make([]mcpServerEntry, 0, len(servers))
	for _, srv := range servers {
		fields := strings.Fields(srv.Command)
		if len(fields) == 0 {
			continue
		}
		entry := mcpServerEntry{Name: srv.Name, Command: fields[0], Args: fields[1:], Env: []envVariable{}}
		if entry.Args == nil {
			entry.Args = []string{}
		}
		for _, kv := range srv.Env {
			k, v, ok := strings.Cut(kv, "=")
			if !ok {
				continue
			}
			entry.Env = append(entry.Env, envVariable{Name: k, Value: v})
		}
		entries = append(entries, entry)
	}
	return entries
}

// toolCallInfo is the stable identity of one announced tool call —
// recorded from the tool_call update's gooseMeta so a later
// session/request_permission (whose params carry no _meta) can be gated on
// the (extension, tool) pair.
type toolCallInfo struct {
	extension string
	name      string
	rawInput  json.RawMessage
}

// streamSession owns one goose acp process. Requests it sends are
// correlated to their responses via pending; session/update notifications
// keep flowing on the same read loop while a request is in flight.
type streamSession struct {
	cmd    *exec.Cmd
	events chan provider.Event

	mu           sync.Mutex
	stdin        io.WriteCloser
	nextID       int
	pending      map[int]chan rpcMessage
	ref          string
	trust        provider.Trust
	interrupted  bool // an interrupt was requested this turn
	replaying    bool // session/load is in flight: buffer updates, don't stream them
	replay       []provider.Message
	promptPrefix string // system prompt awaiting the first Send (unstable-method fallback)
	toolCalls    map[string]toolCallInfo
	closed       bool
	// Latest usage_update snapshot (absolute occupancy + real window size),
	// attached to the next turn end's Usage. Goose pushes these throughout a
	// turn (and a post-compaction one can legitimately DROP below the prior
	// value); last-write-wins is exactly right for "occupancy now".
	ctxUsed int
	ctxSize int
}

// read is the sole owner of the events channel: it decodes stdout lines
// and closes the channel when the process's stdout ends. A close without a
// preceding turn end is the process-died signal (contract in
// providers/provider). Any request still awaiting a response when that
// happens is unblocked (channel closed, not sent to) so its caller can
// tell the difference from a real answer.
func (s *streamSession) read(stdout io.Reader) {
	defer func() {
		s.mu.Lock()
		for id, ch := range s.pending {
			close(ch)
			delete(s.pending, id)
		}
		s.mu.Unlock()
		close(s.events)
	}()
	r := bufio.NewReaderSize(stdout, 64*1024)
	for {
		raw, err := r.ReadBytes('\n')
		if len(raw) > 0 {
			s.handleLine(raw)
		}
		if err != nil {
			return
		}
	}
}

func (s *streamSession) handleLine(raw []byte) {
	var m rpcMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return // not a protocol line; stay tolerant
	}
	switch {
	case m.Method == "" && len(m.ID) > 0: // a response to one of our requests
		var id int
		if err := json.Unmarshal(m.ID, &id); err != nil {
			return // our ids are always ints; anything else isn't ours
		}
		s.mu.Lock()
		ch, ok := s.pending[id]
		if ok {
			delete(s.pending, id)
		}
		s.mu.Unlock()
		if ok {
			ch <- m
		}
	case m.Method == "session/request_permission" && len(m.ID) > 0:
		s.handlePermission(m.ID, m.Params)
	case m.Method == "session/update":
		s.handleUpdate(m.Params)
	}
}

// handlePermission gates a session/request_permission on the session's
// trust level and the tool's identity, replying by selecting from the
// request's offered options by kind — optionIds are agent-chosen opaque
// strings per spec, never hardcoded (goose's ids merely mirror the kind
// names today). The request params carry no _meta, so the tool's
// (extension, name) pair comes from the recorded tool_call update with the
// same toolCallId; an unmatched id denies — never silently under-enforce.
func (s *streamSession) handlePermission(id json.RawMessage, params json.RawMessage) {
	var p requestPermissionParams
	_ = json.Unmarshal(params, &p)

	s.mu.Lock()
	info, known := s.toolCalls[p.ToolCall.ToolCallID]
	s.mu.Unlock()

	rawInput := p.ToolCall.RawInput
	if len(rawInput) == 0 {
		rawInput = info.rawInput
	}
	allow := known && s.toolAllowed(info.extension, info.name, rawInput)

	outcome := permissionOutcome{Outcome: "cancelled"}
	if optionID, ok := selectOption(p.Options, allow); ok {
		outcome = permissionOutcome{Outcome: "selected", OptionID: optionID}
	}
	result, _ := json.Marshal(permissionResult{Outcome: outcome})
	line, err := json.Marshal(rpcMessage{JSONRPC: "2.0", ID: id, Result: result})
	if err != nil {
		return
	}
	_ = s.write(append(line, '\n'))
}

// toolAllowed classifies the tool call against the session's trust level.
// Unknown tools deny under limited and read_only (kirocli precedent) — the
// table is a point-in-time snapshot of goose's builtins, and unknown-deny
// keeps drift safe (it may over-deny after a goose rename, never
// under-enforce).
func (s *streamSession) toolAllowed(extension, name string, rawInput json.RawMessage) bool {
	kind := gooseToolKind(extension, name, rawInput)
	switch s.trust {
	case provider.TrustLimited:
		switch kind {
		case "read", "edit", "search", "think", "fetch":
			return true
		}
		return false
	case provider.TrustReadOnly:
		switch kind {
		case "read", "search", "think", "fetch":
			return true
		}
		return false
	default: // TrustFull, and "" (never set here — startACP normalizes it)
		return true
	}
}

// gooseToolKind maps a goose builtin tool call onto ACP's kind taxonomy —
// necessary because goose never sets kind on model tool calls (its
// tool_calls/conversion.rs leaves it unset; upstream-fixable seam). The
// table snapshots goose 1.48.0's verified builtin names (design doc "Trust
// enforcement"): the ACP toolName is bare "shell", never a synthesized
// developer__shell composite; text_editor splits on its rawInput command
// (view reads, write/str_replace/insert edit); todo's composite-named
// tools only touch session-scoped todo state. Everything else — including
// an unrecognized text_editor command — is "unknown", which denies.
func gooseToolKind(extension, name string, rawInput json.RawMessage) string {
	switch {
	case extension == "developer" && name == "shell":
		return "execute"
	case extension == "developer" && name == "text_editor":
		switch textEditorCommand(rawInput) {
		case "view":
			return "read"
		case "write", "str_replace", "insert":
			return "edit"
		}
		return "unknown"
	case extension == "todo" && (name == "todo__todo_read" || name == "todo__todo_write"):
		return "think"
	}
	return "unknown"
}

func (s *streamSession) handleUpdate(raw json.RawMessage) {
	var p sessionUpdateParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return
	}
	var u sessionUpdate
	if err := json.Unmarshal(p.Update, &u); err != nil {
		return
	}

	s.mu.Lock()
	replaying := s.replaying
	s.mu.Unlock()

	switch u.SessionUpdate {
	case "agent_message_chunk":
		text := u.chunkText()
		if text == "" {
			return
		}
		if replaying {
			s.appendReplay(provider.Message{Role: "agent", Text: text})
			return
		}
		s.events <- provider.Event{Kind: provider.EventText, Text: text}
	case "agent_thought_chunk":
		// Dropped in v1: no thinking kind in the platform vocabulary.
	case "user_message_chunk":
		if !replaying {
			return // live echoes of our own prompt aren't surfaced as events
		}
		if text := u.chunkText(); text != "" {
			s.appendReplay(provider.Message{Role: "user", Text: text})
		}
	case "tool_call":
		if u.ToolCallID != "" {
			s.mu.Lock()
			s.toolCalls[u.ToolCallID] = toolCallInfo{
				extension: u.Meta.Goose.ToolCall.ExtensionName,
				name:      u.Meta.Goose.ToolCall.ToolName,
				rawInput:  u.RawInput,
			}
			s.mu.Unlock()
		}
		tool := u.toolUse()
		if replaying {
			s.appendReplay(provider.Message{Role: "tool", Tool: tool})
			return
		}
		s.events <- provider.Event{Kind: provider.EventToolUse, Tool: tool}
	case "tool_call_update":
		// Live updates also arrive in a non-terminal in_progress variant
		// carrying _meta.toolNotification output chunks — skip everything
		// but the terminal completed/failed pair.
		if u.Status != "completed" && u.Status != "failed" {
			return
		}
		isError := u.Status == "failed"
		text := u.toolResultText()
		if replaying {
			s.appendReplay(provider.Message{Role: "tool_result", Text: text, IsError: isError})
			return
		}
		s.events <- provider.Event{
			Kind:    provider.EventToolResult,
			Tool:    &provider.ToolUse{ID: u.ToolCallID},
			Text:    text,
			IsError: isError,
		}
	case "usage_update":
		// Not an Event of its own — snapshotted and ridden onto the next
		// turn end's Usage as ContextTokens/ContextWindow, which is what the
		// platform persists and the UI's context meter reads. Used=0 lines
		// (goose emits them at session start) are kept too: 0 occupancy is a
		// real snapshot, and ContextTokens=0 just means "unknown/none yet"
		// downstream, which is accurate there.
		s.mu.Lock()
		s.ctxUsed, s.ctxSize = u.Used, u.Size
		s.mu.Unlock()
	default:
		// available_commands_update, current_mode_update,
		// config_option_update, session_info_update: ignored in v1. plan,
		// plan_removed, and anything unrecognized: official (or future)
		// variants goose never emits today — skip silently, never error.
	}
}

func (s *streamSession) appendReplay(msg provider.Message) {
	s.mu.Lock()
	s.replay = append(s.replay, msg)
	s.mu.Unlock()
}

// drainReplay returns the messages buffered by a session/load replay — the
// live-session data source for the Transcript task (export remains the
// primary source).
//
//nolint:unused // forward-built for the Transcript task (goose-adapter-tasks.yaml), its only caller, which lands next
func (s *streamSession) drainReplay() []provider.Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.replay
}

// beginRequest allocates a request id, registers its response channel, and
// writes the request line. The caller decides whether to wait
// synchronously (request) or asynchronously (Send, so a slow turn's
// mid-stream events can be observed before the response arrives).
func (s *streamSession) beginRequest(method string, params any) (chan rpcMessage, error) {
	raw, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, fmt.Errorf("session %s is closed", s.ref)
	}
	s.nextID++
	id := s.nextID
	ch := make(chan rpcMessage, 1)
	s.pending[id] = ch
	s.mu.Unlock()

	line, err := json.Marshal(rpcMessage{JSONRPC: "2.0", ID: json.RawMessage(strconv.Itoa(id)), Method: method, Params: raw})
	if err != nil {
		s.mu.Lock()
		delete(s.pending, id)
		s.mu.Unlock()
		return nil, err
	}
	if err := s.write(append(line, '\n')); err != nil {
		s.mu.Lock()
		delete(s.pending, id)
		s.mu.Unlock()
		return nil, err
	}
	return ch, nil
}

// request sends a request and blocks for its response. On a JSON-RPC error
// the response is returned alongside the error so callers can inspect the
// code (the system-prompt fallback keys on method_not_found).
func (s *streamSession) request(ctx context.Context, method string, params any) (rpcMessage, error) {
	ch, err := s.beginRequest(method, params)
	if err != nil {
		return rpcMessage{}, err
	}
	select {
	case resp, ok := <-ch:
		if !ok {
			return rpcMessage{}, fmt.Errorf("%s: session closed before response", method)
		}
		if resp.Error != nil {
			return resp, fmt.Errorf("%s: %s", method, resp.Error.Message)
		}
		return resp, nil
	case <-ctx.Done():
		return rpcMessage{}, ctx.Err()
	}
}

// notify sends a fire-and-forget message (session/cancel): no id, no
// response expected.
func (s *streamSession) notify(method string, params any) error {
	raw, err := json.Marshal(params)
	if err != nil {
		return err
	}
	line, err := json.Marshal(rpcMessage{JSONRPC: "2.0", Method: method, Params: raw})
	if err != nil {
		return err
	}
	return s.write(append(line, '\n'))
}

func (s *streamSession) Ref() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ref
}

func (s *streamSession) Mode() provider.Mode { return provider.ModeACP }

func (s *streamSession) Events() <-chan provider.Event { return s.events }

// Send begins a turn: session/prompt, sent as a request whose eventual
// response is what signals EventTurnEnd. It must not block waiting for
// that response — a slow turn's session/update chunks need to reach
// Events() while the prompt is still in flight — so the wait happens in a
// goroutine.
func (s *streamSession) Send(_ context.Context, text string) error {
	s.mu.Lock()
	if s.promptPrefix != "" {
		text = s.promptPrefix + "\n\n" + text
		s.promptPrefix = ""
	}
	ref := s.ref
	s.mu.Unlock()
	ch, err := s.beginRequest("session/prompt", promptParams{
		SessionID: ref,
		Prompt:    []contentBlock{{Type: "text", Text: text}},
	})
	if err != nil {
		return err
	}
	go s.awaitTurnEnd(ch)
	return nil
}

// awaitTurnEnd resolves one session/prompt into EventTurnEnd, mapping all
// five spec stopReasons even though goose emits three today: cancelled →
// Interrupted, refusal → EventError then turn end, max_tokens /
// max_turn_requests / anything unrecognized → plain turn end. The `||
// interrupted` arm is load-bearing, not belt-and-braces: captured cancels
// resolve stopReason "end_turn" (goose's cancel token usually kills the
// stream before prompt_stop_reason can observe it), so the adapter's own
// flag is what actually detects interruption.
func (s *streamSession) awaitTurnEnd(ch chan rpcMessage) {
	resp, ok := <-ch
	if !ok {
		return // session died mid-turn; read()'s defer is closing events too
	}
	s.mu.Lock()
	interrupted := s.interrupted
	s.interrupted = false
	ctxUsed, ctxSize := s.ctxUsed, s.ctxSize
	s.mu.Unlock()

	if resp.Error != nil {
		// auth_required (-32000), credits_exhausted, "session already has
		// active run" on a concurrent prompt, generic stream failures — all
		// surface as EventError + TurnEnd; see rpcError.text.
		s.events <- provider.Event{Kind: provider.EventError, Text: resp.Error.text()}
		s.events <- provider.Event{Kind: provider.EventTurnEnd, Interrupted: interrupted}
		return
	}
	var pr promptResult
	_ = json.Unmarshal(resp.Result, &pr)
	switch pr.StopReason {
	case "cancelled":
		interrupted = true
	case "refusal":
		s.events <- provider.Event{Kind: provider.EventError, Text: "goose refused to continue (stopReason refusal)"}
	}
	end := provider.Event{Kind: provider.EventTurnEnd, Interrupted: interrupted}
	if pr.Usage != nil {
		end.Usage = &provider.Usage{InputTokens: pr.Usage.InputTokens, OutputTokens: pr.Usage.OutputTokens}
	}
	// usage_update's absolute occupancy + real window ride the turn end even
	// when the prompt result carries no token counts of its own — the
	// context meter needs the snapshot either way.
	if ctxUsed > 0 || ctxSize > 0 {
		if end.Usage == nil {
			end.Usage = &provider.Usage{}
		}
		end.Usage.ContextTokens = ctxUsed
		end.Usage.ContextWindow = ctxSize
	}
	s.events <- end
}

// Interrupt asks the agent to stop the current response via session/cancel
// — the standard ACP cancel notification, no response expected. The turn
// still closes through its session/prompt response.
func (s *streamSession) Interrupt(context.Context) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return fmt.Errorf("session %s is closed", s.ref)
	}
	s.interrupted = true
	ref := s.ref
	s.mu.Unlock()
	return s.notify("session/cancel", cancelParams{SessionID: ref})
}

func (s *streamSession) write(line []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return fmt.Errorf("session %s is closed", s.ref)
	}
	_, err := s.stdin.Write(line)
	return err
}

// Close ends the session: a best-effort session/close first (it cancels
// any active run and lets goose persist cleanly; a dead process just
// errors, ignored), then stdin EOF asks the process to exit; a laggard is
// killed.
func (s *streamSession) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	ref := s.ref
	s.mu.Unlock()

	if ref != "" {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		_, _ = s.request(ctx, "session/close", closeParams{SessionID: ref})
		cancel()
	}

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	_ = s.stdin.Close()
	s.mu.Unlock()
	closeWait(s.cmd, 3*time.Second)
	return nil
}
