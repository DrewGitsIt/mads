package acprpc

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/drewkidwell/mads/pkg/procreap"
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
	// The engine has no fork mapping (ACP session/fork is unstable and no
	// shipped adapter declares BehaviorBranching): fail rather than let
	// session/load resume the original in place.
	if opts.Fork && opts.Resume != "" {
		return nil, fmt.Errorf("fork on resume: %w", provider.ErrUnsupported)
	}
	// goose (and ACP in general) require an absolute cwd in session/new
	// (goose 1.51: -32602 "cwd must be an absolute path"); claude-code and
	// kiro tolerate a relative one. Absolutize here so every caller gets a
	// spec-valid cwd regardless of the harness — a relative cwd is a
	// client-side concern, not the agent's. A cwd of "" or "." means "the
	// process's own dir", which is absolute by construction once resolved.
	cwd := opts.Cwd
	if cwd == "" || !filepath.IsAbs(cwd) {
		if abs, err := filepath.Abs(cwd); err == nil {
			cwd = abs
		}
	}
	cmd := exec.Command(e.cfg.Bin, e.cfg.SpawnArgs...)
	cmd.Dir = cwd
	cmd.Env = e.env(opts.Env)
	if e.cfg.OwnProcessTree {
		ownGroup(cmd)
	}
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
		readDone:  make(chan struct{}),
		stdin:     stdin,
		events:    make(chan provider.Event, 256),
		pending:   make(map[int]chan rpcMessage),
		trust:     trust,
		toolCalls: make(map[string]toolCallInfo),
		asker:     opts.Permissions,
		asks:      make(map[string]*permAsk),
		settle:    opts.Keyed,
	}
	if e.cfg.OwnProcessTree {
		s.reaper = procreap.New(cmd.Process.Pid)
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
	_, deleteOK := caps.SessionCapabilities["delete"]
	s.mu.Lock()
	s.closeOK = closeOK
	s.deleteOK = deleteOK
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
		resp, err := s.request(ctx, "session/load", loadSessionParams{SessionID: opts.Resume, Cwd: cwd, MCPServers: mcp})
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
		s.mu.Lock()
		s.configOptions = sessRes.ConfigOptions
		s.models = sessRes.Models
		s.mu.Unlock()
	} else {
		resp, err := s.request(ctx, "session/new", newSessionParams{Cwd: cwd, MCPServers: mcp})
		if err != nil {
			return fail("session/new", err)
		}
		if err := json.Unmarshal(resp.Result, &sessRes); err != nil {
			return fail("session/new result", err)
		}
		s.mu.Lock()
		s.ref = sessRes.SessionID
		s.configOptions = sessRes.ConfigOptions
		s.models = sessRes.Models
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
		method, params, err := s.modelSetter(e, opts.Model)
		if err != nil {
			_ = s.Close()
			return nil, err
		}
		// A bad model fails Start loudly rather than silently running the
		// default.
		if _, err := s.request(ctx, method, params); err != nil {
			return fail(method, err)
		}
	}

	if opts.SystemPrompt != "" {
		if err := s.applySystemPrompt(ctx, opts.SystemPrompt); err != nil {
			return fail("system prompt", err)
		}
	}

	return s, nil
}

// discard deletes the session server-side, for a throwaway the user should
// never see. Best-effort and capability-gated, like Close's session/close:
// an agent without sessionCapabilities.delete simply keeps it. Only
// DiscoverModels calls this — session/close alone leaves the session
// persisted (goose keeps it in sessions.db and lists it in `goose session
// list`), so a per-boot discovery pass would otherwise deposit one junk
// session per startup, forever.
func (s *streamSession) discard(ctx context.Context) {
	s.mu.Lock()
	ok, ref := s.deleteOK, s.ref
	s.mu.Unlock()
	if !ok || ref == "" {
		return
	}
	_, _ = s.request(ctx, "session/delete", closeParams{SessionID: ref})
}

// modelSetter picks the call that can actually select id, and must stay in
// lockstep with DiscoverModels: discovery reads the two model surfaces in
// this same order, so an id the picker offered is always settable by the
// setter chosen here. Sending an availableModels id to
// session/set_config_option (or the reverse) is rejected by the agent —
// codex, which ships both, shares no ids between them.
func (s *streamSession) modelSetter(e *Engine, id string) (string, any, error) {
	s.mu.Lock()
	models := s.models
	s.mu.Unlock()
	if models != nil && len(models.AvailableModels) > 0 {
		return "session/set_model", setModelParams{SessionID: s.Ref(), ModelID: id}, nil
	}
	if e.cfg.ModelConfigOptionID != "" {
		return "session/set_config_option", setConfigOptionParams{SessionID: s.Ref(), ConfigID: e.cfg.ModelConfigOptionID, Value: id}, nil
	}
	return "", nil, fmt.Errorf("agent has no model surface; cannot select model %q: %w", id, provider.ErrUnsupported)
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
// resolved trust class, tool name (classify's, the same Name its
// EventToolUse carried) and raw input, keyed by toolCallId so a later
// session/request_permission (whose params may carry no identity at all,
// goose's case) can be gated and, when forwarded, named.
type toolCallInfo struct {
	class    string
	name     string
	rawInput json.RawMessage
}

// permAsk is one session/request_permission forwarded to the asker and not
// yet answered, keyed in streamSession.asks by idKey(id). Whoever deletes
// the entry under s.mu — the asker's goroutine with a decision, or a
// withdrawal (session/cancel, $/cancel_request, process exit, Close) —
// owns the request's single reply; the loser finds it gone and does
// nothing. That delete is the whole exactly-once arbitration.
type permAsk struct {
	id      json.RawMessage
	options []permissionOption
	cancel  context.CancelFunc
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
	// readDone closes when the agent's stdout ends; reaper is set for an
	// OwnProcessTree agent (closeTree).
	readDone chan struct{}
	reaper   *procreap.Reaper

	mu      sync.Mutex
	stdin   io.WriteCloser
	nextID  int
	pending map[int]chan rpcMessage
	turnID  int // the in-flight session/prompt's request id; 0 = idle
	ref     string
	trust   provider.Trust
	closeOK bool // agent advertised sessionCapabilities.close
	// deleteOK — agent advertised sessionCapabilities.delete. Only the
	// DiscoverModels probe uses it: a real session is closed, never
	// deleted, because its transcript is the user's.
	deleteOK    bool
	interrupted bool // an interrupt was requested this turn
	// compacting — the in-flight turn is Compact's: reported as an
	// EventCompact start (on its first update) / done (at its response)
	// pair, its ack text kept out of the stream. compactStarted marks the
	// start as sent.
	compacting     bool
	compactStarted bool
	// compactPre/compactAt: occupancy and wall-clock when a compaction turn
	// began — the marker recorded in the transcript when it succeeds.
	compactPre int
	compactAt  time.Time
	replaying  bool // session/load is in flight: buffer updates, don't stream them
	replay     []provider.Message
	// transcript + lastAgentMsgID — the session's own record of its
	// conversation, in wire order (session/load's replayed history first,
	// then every live turn). agent_message_chunk fragments sharing one
	// messageId are coalesced into a single transcript entry (see
	// recordAgentChunk) so Transcript() renders a reply as one message, not
	// one line per streamed fragment.
	transcript     []provider.Message
	lastAgentMsgID string
	promptPrefix   string // system prompt awaiting the first Send (FirstSendPrefix / -32601 fallback)
	// configOptions is the session/new-or-load result's configOptions list,
	// retained for DiscoverModels (the model entry's options/currentValue).
	configOptions []configOption
	// models is the session/new-or-load result's ACP model selector, nil
	// for an agent that offers only the configOption. Retained for the same
	// reason as configOptions, and additionally to decide which setter
	// selectModel may use — an id only this surface knows must go through
	// session/set_model.
	models    *sessionModels
	toolCalls map[string]toolCallInfo
	// asker is StartOptions.Permissions — who answers the agent's
	// permission requests at limited trust (handlePermission); nil keeps
	// the engine's own class-based answer. asks holds the requests
	// currently forwarded to it (permAsk).
	asker  provider.PermissionAsker
	asks   map[string]*permAsk
	closed bool
	// Latest usage_update snapshot (absolute occupancy + real window size),
	// attached to the next turn end's Usage. Agents push these throughout a
	// turn (and a post-compaction one can legitimately DROP below the prior
	// value); last-write-wins is exactly right for "occupancy now".
	ctxUsed int
	ctxSize int
	// settle — the session asked for EventSettle (StartOptions.Keyed);
	// cfg.Settle says where. item is the message the latest text or
	// thinking chunk belonged to (Config.Keys); anonSeq numbers the
	// messages of chunks that carried no messageId. Both are read-loop
	// state, guarded by mu like the rest.
	settle  bool
	item    itemState
	anonSeq int
}

// itemState tracks the open message for KeyModeMessageRun: its id (with
// the created stamp, when the strategy reads one), the current run and the
// kind ("text" or "thinking") of that run. open is false after a boundary
// — a tool call, a tool result, a turn's start or end — so the next chunk
// opens a new message even under the same messageId.
type itemState struct {
	open  bool
	msgID string // the wire messageId, "" for an anonymous message
	id    string // the key's message part
	kind  string
	run   int
	// Under KeyStrategy.ThinkingApart: the message's text and thinking
	// runs so far, each counted on its own.
	texts, thoughts int
}

// read is the sole owner of the events channel: it decodes stdout lines
// and closes the channel when the process's stdout ends. A close without a
// preceding turn end is the process-died signal (contract in
// providers/provider). Any request still awaiting a response when that
// happens is unblocked (channel closed, not sent to) so its caller can
// tell the difference from a real answer.
func (s *streamSession) read(stdout io.Reader) {
	defer func() {
		defer close(s.readDone)
		// The agent is gone: nobody is left to receive an answer, so
		// every forwarded ask is withdrawn without a reply.
		s.withdrawAsks(false)
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
			compacting, started := s.compacting, s.compactStarted
			pre, at := s.compactPre, s.compactAt
			s.compacting, s.compactStarted = false, false
			ctxUsed, ctxSize := s.ctxUsed, s.ctxSize
			s.mu.Unlock()
			if compacting {
				if !started {
					s.events <- compactStart()
				}
				done := compactDone(m, interrupted)
				if done.Compact.Success {
					s.recordCompaction(pre, ctxUsed, at)
				}
				s.events <- done
			}
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
	case m.Method == "$/cancel_request":
		var p cancelRequestParams
		if json.Unmarshal(m.Params, &p) == nil && len(p.RequestID) > 0 {
			s.cancelAsk(p.RequestID)
		}
	case m.Method == "session/update":
		s.handleUpdate(m.Params)
	}
}

// finishTurn resolves one session/prompt response into EventTurnEnd,
// mapping all five spec stopReasons: cancelled → Interrupted, refusal →
// EventError then turn end, max_tokens / max_turn_requests / anything
// unrecognized → plain turn end. The `interrupted` flag is load-bearing,
// not belt-and-braces: cancel stop-reasons genuinely diverge across
// agents and versions — claude/hermes and goose 1.50.1 resolve
// "cancelled" (goosecli acp-transcript.jsonl lines 36-55), but codex and
// goose 1.48.0 resolve "end_turn" (1.48's cancel token usually killed the
// stream before the stop reason could observe it;
// acp-transcript-1.48.0.jsonl lines 86-105) — so the engine's own flag is
// what reliably detects interruption.
func (s *streamSession) finishTurn(resp rpcMessage, interrupted bool, ctxUsed, ctxSize int) {
	s.endItem()
	if s.cfg.Settle.PromptResponse {
		s.emitSettle("")
	}
	if resp.Error != nil {
		// auth_required (-32000), credits_exhausted, "session already has
		// active run" on a concurrent prompt, generic stream failures — all
		// surface as EventError + TurnEnd; see rpcError.text. The failure
		// is recorded in the transcript too, as an agent entry marked
		// IsError, so the user's message reads as not delivered rather than
		// unanswered — the shape claude-code's own transcript gives it.
		text := resp.Error.text()
		s.mu.Lock()
		s.transcript = append(s.transcript, provider.Message{Role: "agent", Text: text, Timestamp: time.Now().UTC(), IsError: true})
		s.lastAgentMsgID = ""
		s.mu.Unlock()
		s.events <- provider.Event{Kind: provider.EventError, Text: text}
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

func compactStart() provider.Event {
	return provider.Event{Kind: provider.EventCompact, Compact: &provider.CompactStatus{Phase: "start"}}
}

// compactDone resolves a compaction turn's session/prompt response into the
// closing EventCompact, ahead of the turn end finishTurn still emits. ACP
// gives no compaction verdict of its own: a clean end_turn is success, an
// error response or an interrupt is not.
func compactDone(resp rpcMessage, interrupted bool) provider.Event {
	st := &provider.CompactStatus{Phase: "done", Success: true}
	var pr promptResult
	_ = json.Unmarshal(resp.Result, &pr)
	switch {
	case resp.Error != nil:
		st.Success, st.Error = false, resp.Error.text()
	case interrupted || pr.StopReason == "cancelled":
		st.Success, st.Error = false, "compaction interrupted"
	}
	return provider.Event{Kind: provider.EventCompact, Compact: st}
}

// handlePermission answers a session/request_permission. Who decides
// depends on trust — "follow the harness" (providers/provider
// PermissionAsker): mads asks the user only when the agent itself asked.
//
//   - full: allow, always. The agent shouldn't be asking at all (its full
//     mode, e.g. goose "auto", never does); if it does anyway, nobody is
//     there to answer, and full means "don't ask me".
//   - read_only: the engine's class gate decides alone (classAllowed) —
//     the hard guarantee never depends on a person clicking Deny, and an
//     allowed read needs no card either.
//   - limited with an asker (StartOptions.Permissions — the chat
//     transport): the request goes to the user (forwardPermission), except
//     that with PermissionAsksEveryCall (goose's approve mode) the safe
//     read/search/think/fetch classes are allowed here without a card.
//     Unknown identities are forwarded too: the agent asked, the user
//     sees what it asked about and decides.
//   - limited without an asker: the class gate, as before asking existed —
//     execute/other/unknown deny, never stall on an ask nobody answers.
//   - auto_review: exactly limited's two cases, except that every ask is
//     forwarded unfiltered even with PermissionAsksEveryCall. That knob
//     describes the agent's limited-trust mode; at auto_review the agent
//     runs its own reviewer (goose's smart_approve: read-only-annotated
//     tools allowed, an extra LLM call judging the rest), so whatever
//     still reaches session/request_permission is what the reviewer
//     escalated — the user decides it, nothing is second-guessed here. An
//     agent without a reviewer never sees auto_review: the provider runs
//     such a session as limited (provider.EffectiveTrust).
//
// The identity preferably comes from the recorded tool_call update with
// the same toolCallId (goose's request params carry none of their own);
// when no announced call matches, the request's own embedded toolCall — a
// spec ToolCallUpdate that may carry kind/_meta of its own (kiro-cli asks
// before announcing) — is classified through the same ToolIdentity knob.
// Either path without identity classifies unknown, which the class gate
// denies below full trust — never silently under-enforce.
//
// Every engine-made reply selects from the request's offered options by
// kind — optionIds are agent-chosen opaque strings per spec, never
// hardcoded — and is written from the read loop at once; only the
// forwarded path leaves the request open, on its own goroutine.
func (s *streamSession) handlePermission(id json.RawMessage, params json.RawMessage) {
	var p requestPermissionParams
	_ = json.Unmarshal(params, &p)

	s.mu.Lock()
	info, known := s.toolCalls[p.ToolCall.ToolCallID]
	trust := s.trust
	asker := s.asker
	s.mu.Unlock()

	class, name := info.class, info.name
	if !known {
		class, name = s.classify(&sessionUpdate{
			Title:    p.ToolCall.Title,
			Kind:     p.ToolCall.Kind,
			RawInput: p.ToolCall.RawInput,
			Meta:     p.ToolCall.Meta,
		})
	}

	// The safe classes are exactly read_only's allowed set. Only limited's
	// mode asks about everything (PermissionAsksEveryCall); auto_review's
	// asks are already a reviewer's escalations.
	answerSafeLocally := trust == provider.TrustLimited && s.cfg.PermissionAsksEveryCall &&
		classAllowed(provider.TrustReadOnly, class)
	asksUser := trust == provider.TrustLimited || trust == provider.TrustAutoReview
	if asksUser && asker != nil && !answerSafeLocally {
		input := p.ToolCall.RawInput
		if len(input) == 0 {
			input = info.rawInput
		}
		s.forwardPermission(id, p.Options, asker, provider.PermissionAsk{
			ToolUseID: p.ToolCall.ToolCallID,
			Tool:      name,
			Input:     input,
			// ParentToolUseID, AgentID, Reason stay empty: no captured ACP
			// agent puts subagent identity or an ask reason on the wire
			// (claude-agent-acp, codex, hermes, goose — testdata/captures;
			// claude's _meta.permission carries only a title).
		})
		return
	}

	allow := trust == provider.TrustFull || classAllowed(trust, class)
	outcome := permissionOutcome{Outcome: "cancelled"}
	if optionID, ok := selectOption(p.Options, allow); ok {
		outcome = permissionOutcome{Outcome: "selected", OptionID: optionID}
	}
	s.replyPermission(id, outcome)
}

// forwardPermission hands one request to the asker on its own goroutine —
// never the read loop, which must keep delivering session/update lines (and
// a later $/cancel_request) while the user thinks — and replies with the
// offered _once option matching the decision (selectOnceOption). The
// asker's ctx is cancelled when the request is withdrawn first
// (withdrawAsks, cancelAsk); a decision arriving after that finds its
// permAsk gone and is discarded, per the PermissionAsker contract.
func (s *streamSession) forwardPermission(id json.RawMessage, options []permissionOption, asker provider.PermissionAsker, ask provider.PermissionAsk) {
	key := idKey(id)
	ctx, cancel := context.WithCancel(context.Background())
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		cancel()
		return // Close already withdrew every ask; nobody reads a reply now
	}
	s.asks[key] = &permAsk{id: id, options: options, cancel: cancel}
	s.mu.Unlock()

	go func() {
		decision := asker(ctx, ask)
		s.mu.Lock()
		pa, ok := s.asks[key]
		if ok {
			delete(s.asks, key)
		}
		s.mu.Unlock()
		cancel()
		if !ok {
			return // withdrawn while the user was deciding
		}
		outcome := permissionOutcome{Outcome: "cancelled"}
		if optionID, found := selectOnceOption(pa.options, decision.Allow); found {
			outcome = permissionOutcome{Outcome: "selected", OptionID: optionID}
		}
		// decision.Message has nowhere to go: ACP's permission response
		// carries only the outcome. The agent reports the refused call
		// itself (goose: a failed tool_call_update), and the Runner has
		// already marked the call blocked.
		s.replyPermission(pa.id, outcome)
	}()
}

// cancelAsk handles the agent's $/cancel_request for a forwarded
// permission request: the agent stopped waiting, so the asker's ctx is
// cancelled and the request gets its one response — the RequestCancelled
// error the RFD prescribes. A requestId naming no open ask (already
// answered, or a request of some other kind) is ignored.
func (s *streamSession) cancelAsk(requestID json.RawMessage) {
	key := idKey(requestID)
	s.mu.Lock()
	pa, ok := s.asks[key]
	if ok {
		delete(s.asks, key)
	}
	s.mu.Unlock()
	if !ok {
		return
	}
	pa.cancel()
	line, err := json.Marshal(rpcMessage{JSONRPC: "2.0", ID: pa.id, Error: &rpcError{Code: errRequestCancelled, Message: "Request cancelled"}})
	if err != nil {
		return
	}
	_ = s.write(append(line, '\n'))
}

// withdrawAsks withdraws every forwarded ask at once, cancelling each
// asker's ctx. reply says whether the agent still expects answers: the
// spec obliges a client that sends session/cancel to answer every pending
// session/request_permission with outcome "cancelled" (Interrupt); a dead
// process or a closing session gets nothing (read's exit, Close).
func (s *streamSession) withdrawAsks(reply bool) {
	s.mu.Lock()
	asks := make([]*permAsk, 0, len(s.asks))
	for key, pa := range s.asks {
		asks = append(asks, pa)
		delete(s.asks, key)
	}
	s.mu.Unlock()
	for _, pa := range asks {
		pa.cancel()
		if reply {
			s.replyPermission(pa.id, permissionOutcome{Outcome: "cancelled"})
		}
	}
}

// replyPermission writes one session/request_permission response.
func (s *streamSession) replyPermission(id json.RawMessage, outcome permissionOutcome) {
	result, _ := json.Marshal(permissionResult{Outcome: outcome})
	line, err := json.Marshal(rpcMessage{JSONRPC: "2.0", ID: id, Result: result})
	if err != nil {
		return
	}
	_ = s.write(append(line, '\n'))
}

// classAllowed is the engine-fixed trust policy over the ACP kind
// taxonomy: read_only allows {read, search, think, fetch}; limited (and
// auto_review, whose reviewer is the agent's, not the engine's)
// additionally allows {edit}; everything else — including "unknown",
// "other", and "execute" — denies below full trust (the kirocli/goose
// unknown-deny precedent: over-denying after an agent renames a tool is
// safe, under-enforcing is not).
func classAllowed(trust provider.Trust, class string) bool {
	switch trust {
	case provider.TrustLimited, provider.TrustAutoReview:
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
		return ClassFromTable(s.cfg.ToolClassTable, meta.ExtensionName, meta.ToolName, u.RawInput), name
	default: // ToolIdentityTitleOnly
		return "unknown", u.Title
	}
}

// ClassFromTable is a ToolClassTable lookup (ToolIdentityMetaNamespace): the
// rule for this extension and tool, by command where it has ByCommand
// entries; "unknown" when no rule names it. Exported so an adapter's store
// reader classifies a stored call exactly as the live one was.
func ClassFromTable(table []ToolClassRule, extension, toolName string, rawInput json.RawMessage) string {
	for _, rule := range table {
		if rule.Extension != extension || rule.Name != toolName {
			continue
		}
		if c, ok := rule.ByCommand[rawInputCommand(rawInput)]; ok {
			return c
		}
		if rule.Class != "" {
			return rule.Class
		}
		return "unknown"
	}
	return "unknown"
}

// ToolKindFor is toolKindFor, for an adapter's store reader.
func ToolKindFor(class string) provider.ToolKind { return toolKindFor(class) }

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
	startCompact := s.compacting && !s.compactStarted && !replaying
	if startCompact {
		s.compactStarted = true
	}
	s.mu.Unlock()
	if startCompact {
		s.events <- compactStart()
	}

	switch u.SessionUpdate {
	case "agent_message_chunk":
		if text := u.chunkText(); text != "" {
			// Buffer every streamed assistant-text fragment — regardless of
			// whether this is a session/load replay or a live turn — into s.transcript,
			// the session's own running record of its conversation (see
			// Transcript() below), coalescing fragments that share one messageId
			// into one entry. Without this, a live turn's reply text only ever
			// reaches the one-shot events channel: a subscriber not attached at
			// the moment the chunks stream loses it permanently (the hub's
			// non-PTY frames have no replay, by design), and providers with no
			// on-disk transcript of their own (goose's ACP path, kiro) have no
			// TranscriptReader fallback either — the W7.8 gap
			// docs/GOOSE-ADAPTER-DESIGN.md flags. claude-code is unaffected:
			// its TranscriptReader already reads a persisted .jsonl, but
			// buffering here is harmless and keeps the engine self-consistent
			// for any agent that rides it.
			s.mu.Lock()
			compacting := s.compacting
			s.mu.Unlock()
			if !compacting || replaying {
				s.recordAgentChunk(text, u.MessageID)
			}
			// A compaction turn's only text is the agent's ack ("Compaction
			// complete" from goose) — the EventCompact pair already says so.
			if replaying || compacting {
				return
			}
			select {
			case s.events <- provider.Event{Kind: provider.EventText, Key: s.chunkKey("text", &u), Text: text}:
			default:
			}
		}
	case "agent_thought_chunk":
		if replaying {
			return // Message has no thinking role; replay keeps the reply only
		}
		if text := u.chunkText(); text != "" {
			s.events <- provider.Event{Kind: provider.EventThinking, Key: s.chunkKey("thinking", &u), Text: text}
		}
	case "user_message_chunk":
		s.mu.Lock()
		echo := s.compacting && !replaying // the agent echoing our own "/compact"
		s.mu.Unlock()
		if text := u.chunkText(); text != "" && !echo {
			s.appendReplay(provider.Message{Role: "user", Text: text})
		}
		if !replaying {
			return // live echoes of our own prompt aren't surfaced as events
		}
	case "tool_call":
		// A tool may outlive the agent in a session of its own: remember
		// it shortly after it has had time to start (OwnProcessTree).
		if s.reaper != nil && !replaying {
			s.reaper.SnapshotSoon(300 * time.Millisecond)
		}
		class, name := s.classify(&u)
		if u.ToolCallID != "" {
			s.mu.Lock()
			s.toolCalls[u.ToolCallID] = toolCallInfo{class: class, name: name, rawInput: u.RawInput}
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
		s.appendReplay(provider.Message{Role: "tool", Tool: tool})
		if replaying {
			return
		}
		key := s.toolKey(u.ToolCallID)
		s.events <- provider.Event{Kind: provider.EventToolUse, Key: key, Tool: tool}
		if s.cfg.Settle.ToolCall {
			s.emitSettle(key)
		}
	case "tool_call_update":
		// Live updates also arrive in a non-terminal in_progress variant
		// (goose's carry _meta.toolNotification output chunks) — skip
		// everything but the terminal completed/failed pair.
		if u.Status != "completed" && u.Status != "failed" {
			return
		}
		isError := u.Status == "failed"
		text := u.toolResultText()
		s.appendReplay(provider.Message{Role: "tool_result", Text: text, IsError: isError})
		if replaying {
			return
		}
		key := s.toolKey(u.ToolCallID)
		s.events <- provider.Event{
			Kind:    provider.EventToolResult,
			Key:     key,
			Tool:    &provider.ToolUse{ID: u.ToolCallID},
			Text:    text,
			IsError: isError,
		}
		if s.cfg.Settle.ToolDone {
			s.emitSettle(key)
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

// recordCompaction appends the transcript entry for a successful compaction
// pass — the same Role "compact" marker claude-code's transcript carries.
// ACP has no summary text to carry, and the trigger isn't known here (the
// Runner's near-limit resume uses the same path as the user), so only the
// occupancy before/after and the duration are filled.
func (s *streamSession) recordCompaction(pre, post int, began time.Time) {
	m := &provider.CompactMarker{PreTokens: pre, PostTokens: post}
	if !began.IsZero() {
		m.DurationMS = time.Since(began).Milliseconds()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.transcript = append(s.transcript, provider.Message{Role: "compact", Compact: m, Timestamp: time.Now().UTC()})
	s.lastAgentMsgID = ""
}

// chunkKey is Event.Key for a live text or thinking chunk under
// Config.Keys (see KeyModeMessageRun); "" when the strategy emits none.
func (s *streamSession) chunkKey(kind string, u *sessionUpdate) string {
	if s.cfg.Keys.Mode != KeyModeMessageRun {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	it := &s.item
	switch {
	// Same message: still open, same messageId (both empty for an
	// anonymous one). The created stamp isn't compared — it moves on within
	// one message; the first chunk's is the one the message keeps.
	case it.open && it.msgID == u.MessageID:
		if it.kind != kind {
			it.run++
			it.kind = kind
			it.countRun()
		}
	default:
		id := u.MessageID
		if id == "" {
			s.anonSeq++
			id = fmt.Sprintf("acp-anon-%d", s.anonSeq)
		} else if ns := s.cfg.Keys.CreatedNamespace; ns != "" {
			if created := decodeNSCreated(u.Meta, ns); created != "" {
				id += "@" + created
			}
		}
		*it = itemState{open: true, msgID: u.MessageID, id: id, kind: kind}
		it.countRun()
	}
	if s.cfg.Keys.ThinkingApart {
		if kind == "thinking" {
			return it.id + ":thinking:" + strconv.Itoa(it.thoughts-1)
		}
		return it.id + ":" + strconv.Itoa(it.texts-1)
	}
	return it.id + ":" + strconv.Itoa(it.run)
}

// countRun counts the run just opened for KeyStrategy.ThinkingApart.
func (it *itemState) countRun() {
	if it.kind == "thinking" {
		it.thoughts++
	} else {
		it.texts++
	}
}

// toolKey is Event.Key for a tool call and its result: the toolCallId,
// under any key strategy.
func (s *streamSession) toolKey(toolCallID string) string {
	if s.cfg.Keys.Mode == KeyModeNone {
		return ""
	}
	s.endItem() // a tool call or result ends the message before it
	return toolCallID
}

// endItem closes the open message (a boundary: see itemState).
func (s *streamSession) endItem() {
	s.mu.Lock()
	s.item.open = false
	s.mu.Unlock()
}

// emitSettle sends an EventSettle for a session that asked for them. Like
// every event, from the read loop only.
func (s *streamSession) emitSettle(key string) {
	if !s.settle {
		return
	}
	s.events <- provider.Event{Kind: provider.EventSettle, Key: key}
}

func (s *streamSession) appendReplay(msg provider.Message) {
	s.mu.Lock()
	s.replay = append(s.replay, msg)
	s.transcript = append(s.transcript, msg)
	if msg.Role == "agent" {
		s.lastAgentMsgID = "" // a non-chunk-sourced agent entry resets the coalescing window
	}
	s.mu.Unlock()
}

// recordAgentChunk appends text to s.transcript as one entry per messageId —
// a session/update variant's "messageId" field (see sessionUpdate.MessageID)
// is a logical message id an agent stamps on every streamed fragment of one
// reply (goose's agent_message_chunk updates in
// providers/goosecli/testdata/captures/acp-transcript.jsonl lines 23-32 all
// share messageId "chatcmpl-885"), so chunks that agree on one are coalesced
// into the entry started by the first one rather than appended as separate
// lines. Chunks with no/empty messageId (or a changing one) open a fresh
// entry, same as appendReplay below. Guarded by s.mu like appendReplay.
func (s *streamSession) recordAgentChunk(text, msgID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if msgID != "" && len(s.transcript) > 0 && s.transcript[len(s.transcript)-1].Role == "agent" && s.lastAgentMsgID == msgID {
		s.transcript[len(s.transcript)-1].Text += text
	} else {
		s.transcript = append(s.transcript, provider.Message{Role: "agent", Text: text})
		s.lastAgentMsgID = msgID
	}
}

// ReplayMessages returns the messages observed so far (the session/load
// replay, if this session resumed an existing ref, plus any live turns
// since) — the live-session data source a binding's TranscriptReader
// reaches via an interface assertion on its own `interface{
// ReplayMessages() []provider.Message }` (the concrete session type is
// unexported).
func (s *streamSession) ReplayMessages() []provider.Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]provider.Message{}, s.transcript...)
}

// Transcript returns this session's full conversation — the replayed history
// (if this session resumed an existing ref) plus every live turn's user
// prompt and assistant text//tool traffic observed since, in wire order.
// This is the ACP-specific half of the fix for the goose-assistant-reply
// gap docs/GOOSE-ADAPTER-DESIGN.md (W7.8) flags: providers built on this
// engine (goose's ACP path, kiro) have no on-disk transcript file of their
// own the way claude-code's TranscriptReader reads, so a binding that
// serves GET /transcript needs exactly this — the session's own record —
// not just its one-shot, loss-prone events channel.
func (s *streamSession) Transcript() []provider.Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]provider.Message{}, s.transcript...)
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
	return s.prompt(text, false)
}

// Compact implements provider.Compactor by prompting the agent's own
// compaction command (Config.CompactCommand) — ACP has no compaction
// method, only the slash command agents advertise. The turn is reported as
// an EventCompact pair rather than as a reply (see compacting). A pending
// FirstSendPrefix system prompt is left for the next real Send: prefixed
// onto "/compact" the agent would read it as a message, not the command.
func (s *streamSession) Compact(_ context.Context, instructions string) error {
	if s.cfg.CompactCommand == "" {
		return fmt.Errorf("agent has no compaction command: %w", provider.ErrUnsupported)
	}
	text := s.cfg.CompactCommand
	if instructions = strings.TrimSpace(instructions); instructions != "" {
		if !s.cfg.CompactFocus {
			return fmt.Errorf("agent's compaction takes no instructions: %w", provider.ErrUnsupported)
		}
		text += " " + instructions
	}
	return s.prompt(text, true)
}

func (s *streamSession) prompt(text string, compact bool) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return fmt.Errorf("session %s is closed", s.ref)
	}
	if s.turnID != 0 {
		s.mu.Unlock()
		return fmt.Errorf("session %s already has a prompt in flight: %w", s.ref, provider.ErrTurnInFlight)
	}
	if s.promptPrefix != "" && !compact {
		text = s.promptPrefix + "\n\n" + text
		s.promptPrefix = ""
	}
	s.nextID++
	id := s.nextID
	s.turnID = id
	s.item.open = false // a turn's first chunk opens a new message
	s.compacting, s.compactStarted = compact, false
	ref := s.ref
	if compact {
		// The "/compact" prompt and the agent's ack are plumbing, not
		// conversation: a success lands in the transcript as one compact
		// marker (recordCompaction), a failure as nothing.
		s.compactPre, s.compactAt = s.ctxUsed, time.Now()
	} else {
		s.transcript = append(s.transcript, provider.Message{Role: "user", Text: text, Timestamp: time.Now().UTC()})
	}
	s.mu.Unlock()

	clearTurn := func() {
		s.mu.Lock()
		if s.turnID == id {
			s.turnID = 0
			s.compacting, s.compactStarted = false, false
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
// what marks it Interrupted (see finishTurn). A permission request still
// waiting on the user is withdrawn with it: the spec requires the client
// to answer every pending session/request_permission with outcome
// "cancelled" once it has sent session/cancel, and the asker's ctx is
// cancelled so the user's card closes.
func (s *streamSession) Interrupt(context.Context) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return fmt.Errorf("session %s is closed", s.ref)
	}
	s.interrupted = true
	ref := s.ref
	s.mu.Unlock()
	err := s.notify("session/cancel", cancelParams{SessionID: ref})
	s.withdrawAsks(true)
	return err
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

	// Remember the agent's current descendants before it is asked to go:
	// once it exits they reparent to init (OwnProcessTree; the agent is
	// unreaped here, so its tree is still its own).
	if s.reaper != nil {
		s.reaper.Snapshot()
	}

	// Nobody will answer an open ask once the session is going away; the
	// agent learns of it through session/close (which cancels its run) or
	// the process ending, so no reply is owed.
	s.withdrawAsks(false)

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
	if s.reaper != nil {
		closeTree(s.cmd, 3*time.Second, s.readDone, s.reaper)
		return nil
	}
	closeWait(s.cmd, 3*time.Second)
	return nil
}
