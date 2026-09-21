package acprpc

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

// Start launches (or with opts.Resume, reattaches to) an interactive ACP
// session. Lifecycle: spawn, initialize (empty client capabilities, keep
// the advertised capability set), session/new or capability-gated
// session/load, preseeded config options, trust mode, model, system
// prompt — goose-as-client's apply order (provider.rs:1578-1589).
func (e *Engine) Start(ctx context.Context, opts provider.StartOptions) (provider.Session, error) {
	if opts.Mode != provider.ModeACP {
		return nil, fmt.Errorf("mode %q: %w", opts.Mode, provider.ErrUnsupported)
	}
	cmd := exec.Command(e.cfg.Bin, e.cfg.SpawnArgs...)
	cmd.Dir = opts.Cwd
	cmd.Env = e.env(opts.Env)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %s: %w", e.cfg.Bin, err)
	}

	trust := opts.Trust
	if trust == "" {
		trust = provider.TrustFull
	}
	s := &streamSession{
		cfg:       &e.cfg,
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
		return nil, fmt.Errorf("%s acp %s: %w", e.cfg.Bin, step, err)
	}

	initResp, err := s.request(ctx, "initialize", initializeParams{ProtocolVersion: 1})
	if err != nil {
		return fail("initialize", err)
	}
	var initRes initializeResult
	_ = json.Unmarshal(initResp.Result, &initRes)
	caps := initRes.AgentCapabilities
	// sessionCapabilities entries are presence-keyed empty objects, never
	// bools (codex advertises "close": {}, hermes omits the key —
	// claude/codex/hermes *-full.jsonl line 2).
	_, closeOK := caps.SessionCapabilities["close"]
	s.mu.Lock()
	s.closeOK = closeOK
	s.mu.Unlock()

	// mcpServers is required on both session/new and session/load even when
	// empty (goose acp-load-replay.jsonl lines 3-4); http entries ride only
	// when the agent advertised mcpCapabilities.http, sse entries never.
	mcp := mcpEntries(opts.MCPServers, caps.MCPCapabilities.HTTP)

	var sessRes sessionResult
	if opts.Resume != "" {
		// Resume without the loadSession capability fails loudly rather than
		// silently starting a fresh, unrelated session — same stance as
		// goose-as-client's "does not support session/load" error
		// (provider.rs:1614-1622), typed per the Start contract.
		if !caps.LoadSession {
			_ = s.Close()
			return nil, fmt.Errorf("agent does not advertise loadSession; cannot resume session %s: %w", opts.Resume, provider.ErrUnsupported)
		}
		s.mu.Lock()
		s.replaying = true
		s.mu.Unlock()
		resp, err := s.request(ctx, "session/load", loadSessionParams{SessionID: opts.Resume, Cwd: opts.Cwd, MCPServers: mcp})
		s.mu.Lock()
		s.replaying = false
		s.ref = opts.Resume
		s.mu.Unlock()
		if err != nil {
			// resource_not_found on an unknown ref is a real failure, not an
			// ErrUnsupported case — resuming a bad ref must fail loudly.
			return fail("session/load", err)
		}
		_ = json.Unmarshal(resp.Result, &sessRes)
	} else {
		resp, err := s.request(ctx, "session/new", newSessionParams{Cwd: opts.Cwd, MCPServers: mcp})
		if err != nil {
			return fail("session/new", err)
		}
		if err := json.Unmarshal(resp.Result, &sessRes); err != nil {
			return fail("session/new result", err)
		}
		s.mu.Lock()
		s.ref = sessRes.SessionID
		s.mu.Unlock()
	}

	// Config options apply before the mode — goose-as-client's order after
	// both session/new and session/load (provider.rs:1578-1589, 1626-1638).
	for _, po := range e.cfg.PreseedConfigOptions {
		if _, err := s.request(ctx, "session/set_config_option", setConfigOptionParams{SessionID: s.Ref(), ConfigID: po.ID, Value: po.Value}); err != nil {
			return fail("session/set_config_option "+po.ID, err)
		}
	}

	if e.cfg.SendModeAtSessionNew {
		if err := s.applyTrustMode(ctx, trust, sessRes); err != nil {
			return fail("trust mode", err)
		}
	}

	if opts.Model != "" {
		if e.cfg.ModelConfigOptionID == "" {
			_ = s.Close()
			return nil, fmt.Errorf("agent has no model config option; cannot select model %q: %w", opts.Model, provider.ErrUnsupported)
		}
		// A bad model fails Start loudly rather than silently running the
		// default.
		if _, err := s.request(ctx, "session/set_config_option", setConfigOptionParams{SessionID: s.Ref(), ConfigID: e.cfg.ModelConfigOptionID, Value: opts.Model}); err != nil {
			return fail("session/set_config_option "+e.cfg.ModelConfigOptionID, err)
		}
	}

	if opts.SystemPrompt != "" {
		if err := s.applySystemPrompt(ctx, opts.SystemPrompt); err != nil {
			return fail("system prompt", err)
		}
	}

	return s, nil
}

// applyTrustMode resolves TrustModes[trust] and sends it over the
// runtime-detected transport. A missing mapping skips the call for full
// trust but fails limited/read_only loudly — the engine can't even ask
// the agent to be careful, and silence would look like enforcement. A
// mapped id the session result's availableModes doesn't offer also fails
// loudly (goose-as-client's select_mode_id validates the same way,
// provider.rs:1818-1840).
func (s *streamSession) applyTrustMode(ctx context.Context, trust provider.Trust, sessRes sessionResult) error {
	modeID, ok := s.cfg.TrustModes[trust]
	if !ok {
		if trust == provider.TrustFull {
			return nil
		}
		return fmt.Errorf("no vendor mode mapped for trust %q", trust)
	}
	if sessRes.Modes != nil && len(sessRes.Modes.AvailableModes) > 0 {
		offered := false
		for _, m := range sessRes.Modes.AvailableModes {
			if m.ID == modeID {
				offered = true
				break
			}
		}
		if !offered {
			return fmt.Errorf("mapped mode %q for trust %q is not among the agent's available modes", modeID, trust)
		}
	}
	if configID := modeConfigOptionID(sessRes.ConfigOptions); configID != "" {
		_, err := s.request(ctx, "session/set_config_option", setConfigOptionParams{SessionID: s.Ref(), ConfigID: configID, Value: modeID})
		return err
	}
	_, err := s.request(ctx, "session/set_mode", setModeParams{SessionID: s.Ref(), ModeID: modeID})
	return err
}

// applySystemPrompt delivers the agent persona per the SystemPrompt knob.
// The extension-method strategy falls back to FirstSendPrefix on
// method_not_found (-32601) — a vendor dropping its extension must not
// silently drop the persona with it.
func (s *streamSession) applySystemPrompt(ctx context.Context, text string) error {
	switch s.cfg.SystemPrompt.Mode {
	case SystemPromptNone:
		return nil // dropped by declared decision
	case SystemPromptExtensionMethod:
		if s.cfg.SystemPrompt.Method == "" || s.cfg.SystemPrompt.Params == nil {
			return fmt.Errorf("SystemPromptExtensionMethod requires Method and Params")
		}
		resp, err := s.request(ctx, s.cfg.SystemPrompt.Method, s.cfg.SystemPrompt.Params(s.Ref(), text))
		if err == nil {
			return nil
		}
		if resp.Error == nil || resp.Error.Code != -32601 {
			return err
		}
	}
	// FirstSendPrefix (the zero value), and the -32601 fallback above.
	s.mu.Lock()
	s.promptPrefix = text
	s.mu.Unlock()
	return nil
}

// mcpEntries converts StartOptions.MCPServers to spec descriptors, gated
// on the agent's advertised MCP capabilities: stdio always rides; http
// only when mcpCapabilities.http was advertised; sse never (deprecated in
// the spec — none of the four captured agents accepts it: goose and codex
// advertise sse:false, claude's true is untested, hermes omits the key).
// Drops are silent. Stdio Command is shell-split with strings.Fields, the
// MCPServerRef convention (see providers/claudecode's writeMCPConfig).
func mcpEntries(servers []provider.MCPServerRef, httpOK bool) []json.RawMessage {
	entries := make([]json.RawMessage, 0, len(servers))
	for _, srv := range servers {
		switch srv.Type {
		case "", "stdio":
			fields := strings.Fields(srv.Command)
			if len(fields) == 0 {
				continue
			}
			entry := stdioMCPServer{Name: srv.Name, Command: fields[0], Args: fields[1:], Env: []envVariable{}}
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
			raw, err := json.Marshal(entry)
			if err != nil {
				continue
			}
			entries = append(entries, raw)
		case "http":
			if !httpOK {
				continue
			}
			raw, err := json.Marshal(httpMCPServer{Type: "http", Name: srv.Name, URL: srv.URL})
			if err != nil {
				continue
			}
			entries = append(entries, raw)
		}
	}
	return entries
}

// toolCallInfo is the recorded identity of one announced tool call — its
// resolved trust class and raw input, keyed by toolCallId so a later
// session/request_permission (whose params may carry no identity at all,
// goose's case) can be gated.
type toolCallInfo struct {
	class    string
	rawInput json.RawMessage
}

// streamSession owns one agent process. Requests it sends are correlated
// to their responses via pending; session/update notifications keep
// flowing on the same read loop while a request is in flight. The read
// loop is the SOLE writer to events — turn ends are emitted inline from
// handleLine, never from a separate goroutine, so the channel close in
// read's defer can never race a send.
type streamSession struct {
	cfg    *Config
	cmd    *exec.Cmd
	events chan provider.Event

	mu           sync.Mutex
	stdin        io.WriteCloser
	nextID       int
	pending      map[int]chan rpcMessage
	turnID       int // the in-flight session/prompt's request id; 0 = idle
	ref          string
	trust        provider.Trust
	closeOK      bool // agent advertised sessionCapabilities.close
	interrupted  bool // an interrupt was requested this turn
	replaying    bool // session/load is in flight: buffer updates, don't stream them
	replay       []provider.Message
	promptPrefix string // system prompt awaiting the first Send (FirstSendPrefix / -32601 fallback)
	toolCalls    map[string]toolCallInfo
	closed       bool
	// Latest usage_update snapshot (absolute occupancy + real window size),
	// attached to the next turn end's Usage. Agents push these throughout a
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
		if s.turnID != 0 && id == s.turnID {
			s.turnID = 0
			interrupted := s.interrupted
			s.interrupted = false
			ctxUsed, ctxSize := s.ctxUsed, s.ctxSize
			s.mu.Unlock()
			s.finishTurn(m, interrupted, ctxUsed, ctxSize)
			return
		}
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

// finishTurn resolves one session/prompt response into EventTurnEnd,
// mapping all five spec stopReasons: cancelled → Interrupted, refusal →
// EventError then turn end, max_tokens / max_turn_requests / anything
// unrecognized → plain turn end. The `interrupted` flag is load-bearing,
// not belt-and-braces: cancel stop-reasons genuinely diverge across
// agents — claude/hermes resolve "cancelled" but goose and codex resolve
// "end_turn" (goose's cancel token usually kills the stream before the
// stop reason can observe it; pinned in both capture sets) — so the
// engine's own flag is what actually detects interruption.
func (s *streamSession) finishTurn(resp rpcMessage, interrupted bool, ctxUsed, ctxSize int) {
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
		s.events <- provider.Event{Kind: provider.EventError, Text: "agent refused to continue (stopReason refusal)"}
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

// handlePermission gates a session/request_permission on the session's
// trust level and the tool's recorded class, replying by selecting from
// the request's offered options by kind — optionIds are agent-chosen
// opaque strings per spec, never hardcoded. The identity comes from the
// recorded tool_call update with the same toolCallId (goose's request
// params carry none of their own); below full trust an unmatched id
// denies — never silently under-enforce.
func (s *streamSession) handlePermission(id json.RawMessage, params json.RawMessage) {
	var p requestPermissionParams
	_ = json.Unmarshal(params, &p)

	s.mu.Lock()
	info, known := s.toolCalls[p.ToolCall.ToolCallID]
	trust := s.trust
	s.mu.Unlock()

	allow := trust == provider.TrustFull || (known && classAllowed(trust, info.class))

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

// classAllowed is the engine-fixed trust policy over the ACP kind
// taxonomy: read_only allows {read, search, think, fetch}; limited
// additionally allows {edit}; everything else — including "unknown",
// "other", and "execute" — denies below full trust (the kirocli/goose
// unknown-deny precedent: over-denying after an agent renames a tool is
// safe, under-enforcing is not).
func classAllowed(trust provider.Trust, class string) bool {
	switch trust {
	case provider.TrustLimited:
		switch class {
		case "read", "edit", "search", "think", "fetch":
			return true
		}
		return false
	case provider.TrustReadOnly:
		switch class {
		case "read", "search", "think", "fetch":
			return true
		}
		return false
	default: // TrustFull, and "" (never set here — Start normalizes it)
		return true
	}
}

// classMutates derives the §5 file-change signal from the class — the
// taxonomy's writing kinds, replacing per-vendor tool-name tables.
func classMutates(class string) bool {
	switch class {
	case "edit", "delete", "move":
		return true
	}
	return false
}

// classify resolves a tool_call update's trust class per the ToolIdentity
// knob: the wire kind (KindBased, absent → unknown), a ToolClassTable
// lookup on the _meta identity (MetaNamespace), or unconditionally
// unknown (TitleOnly — no stable identity means limited/read_only
// deny-all, never gating on a display string).
func (s *streamSession) classify(u *sessionUpdate) (class, name string) {
	switch s.cfg.ToolIdentity.Mode {
	case ToolIdentityKindBased:
		if u.Kind == "" {
			return "unknown", u.Title
		}
		return u.Kind, u.Title
	case ToolIdentityMetaNamespace:
		meta := decodeNSToolCallMeta(u.Meta, s.cfg.ToolIdentity.Namespace)
		name = meta.ToolName
		if name == "" {
			name = u.Title
		}
		for _, rule := range s.cfg.ToolClassTable {
			if rule.Extension != meta.ExtensionName || rule.Name != meta.ToolName {
				continue
			}
			if c, ok := rule.ByCommand[rawInputCommand(u.RawInput)]; ok {
				return c, name
			}
			if rule.Class != "" {
				return rule.Class, name
			}
			return "unknown", name
		}
		return "unknown", name
	default: // ToolIdentityTitleOnly
		return "unknown", u.Title
	}
}

// toolKindFor maps a trust class onto the provider.ToolKind vocabulary
// for rendering; anything outside it is ToolKindOther, never a guess.
func toolKindFor(class string) provider.ToolKind {
	switch k := provider.ToolKind(class); k {
	case provider.ToolKindRead, provider.ToolKindEdit, provider.ToolKindDelete,
		provider.ToolKindMove, provider.ToolKindSearch, provider.ToolKindExecute,
		provider.ToolKindThink, provider.ToolKindFetch, provider.ToolKindAgent:
		return k
	}
	return provider.ToolKindOther
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
		if replaying {
			return // Message has no thinking role; replay keeps the reply only
		}
		if text := u.chunkText(); text != "" {
			s.events <- provider.Event{Kind: provider.EventThinking, Text: text}
		}
	case "user_message_chunk":
		if !replaying {
			return // live echoes of our own prompt aren't surfaced as events
		}
		if text := u.chunkText(); text != "" {
			s.appendReplay(provider.Message{Role: "user", Text: text})
		}
	case "tool_call":
		class, name := s.classify(&u)
		if u.ToolCallID != "" {
			s.mu.Lock()
			s.toolCalls[u.ToolCallID] = toolCallInfo{class: class, rawInput: u.RawInput}
			s.mu.Unlock()
		}
		tool := &provider.ToolUse{
			ID:      u.ToolCallID,
			Name:    name,
			Input:   u.RawInput,
			Path:    toolPath(u.RawInput),
			Mutates: classMutates(class),
			Kind:    toolKindFor(class),
			Title:   u.Title,
		}
		if replaying {
			s.appendReplay(provider.Message{Role: "tool", Tool: tool})
			return
		}
		s.events <- provider.Event{Kind: provider.EventToolUse, Tool: tool}
	case "tool_call_update":
		// Live updates also arrive in a non-terminal in_progress variant
		// (goose's carry _meta.toolNotification output chunks) — skip
		// everything but the terminal completed/failed pair.
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
		// config_option_update, session_info_update, plan, plan_removed, and
		// anything unrecognized (including future spec additions): skipped
		// silently, never an error.
	}
}

func (s *streamSession) appendReplay(msg provider.Message) {
	s.mu.Lock()
	s.replay = append(s.replay, msg)
	s.mu.Unlock()
}

// ReplayMessages returns the messages buffered by a session/load replay —
// the live-session data source a binding's TranscriptReader reaches via
// an interface assertion on its own `interface{ ReplayMessages()
// []provider.Message }` (the concrete session type is unexported).
func (s *streamSession) ReplayMessages() []provider.Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.replay
}

// beginRequest allocates a request id, registers its response channel, and
// writes the request line for a synchronous (non-prompt) request.
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

// Send begins a turn: session/prompt, whose eventual response the read
// loop resolves into EventTurnEnd (finishTurn) — never a waiting
// goroutine, so a slow turn's session/update chunks stream while the
// prompt is in flight and the events channel has exactly one writer. One
// in-flight prompt per session is a spec obligation, enforced here: a
// Send during an open turn fails immediately.
func (s *streamSession) Send(_ context.Context, text string) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return fmt.Errorf("session %s is closed", s.ref)
	}
	if s.turnID != 0 {
		s.mu.Unlock()
		return fmt.Errorf("session %s already has a prompt in flight", s.ref)
	}
	if s.promptPrefix != "" {
		text = s.promptPrefix + "\n\n" + text
		s.promptPrefix = ""
	}
	s.nextID++
	id := s.nextID
	s.turnID = id
	ref := s.ref
	s.mu.Unlock()

	clearTurn := func() {
		s.mu.Lock()
		if s.turnID == id {
			s.turnID = 0
		}
		s.mu.Unlock()
	}
	raw, err := json.Marshal(promptParams{
		SessionID: ref,
		Prompt:    []contentBlock{{Type: "text", Text: text}},
	})
	if err != nil {
		clearTurn()
		return err
	}
	line, err := json.Marshal(rpcMessage{JSONRPC: "2.0", ID: json.RawMessage(strconv.Itoa(id)), Method: "session/prompt", Params: raw})
	if err != nil {
		clearTurn()
		return err
	}
	if err := s.write(append(line, '\n')); err != nil {
		clearTurn()
		return err
	}
	return nil
}

// Interrupt asks the agent to stop the current response via session/cancel
// — the standard ACP cancel notification, no response expected. The turn
// still closes through its session/prompt response; the flag set here is
// what marks it Interrupted (see finishTurn).
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

// Close ends the session: a best-effort session/close first — only when
// the agent advertised sessionCapabilities.close (it cancels any active
// run and lets the agent persist cleanly; unadvertised, the call would be
// a protocol violation) — then stdin EOF asks the process to exit; a
// laggard is killed.
func (s *streamSession) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	ref := s.ref
	closeOK := s.closeOK
	s.mu.Unlock()

	if ref != "" && closeOK {
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
