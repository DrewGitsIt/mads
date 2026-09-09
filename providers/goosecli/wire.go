package goosecli

import (
	"encoding/json"
	"strings"

	"github.com/drewkidwell/mads/providers/provider"
)

// The types in this file derive from the ACP v1 spec schema
// (agentclientprotocol.com, pinned by the official
// agent-client-protocol-schema 1.5.0 crate goose vendors) and are verified
// against the goose 1.48.0 captures under testdata/captures/. The spec is
// normative: goose is one conforming agent, and the decoder must survive
// any conforming agent's output — official variants goose never emits
// today (plan, plan_removed, the max_* stop reasons) are still handled or
// skipped silently, never a decode error. goose-specific extension shapes
// are quarantined: every _meta.goose.* field this adapter reads lives in
// gooseMeta, and every _goose/unstable/* method call lives in a named
// helper in session.go, so extension use stays greppable for the T10
// upstream-proposals task.

// rpcMessage is one line of goose's ACP transport: bidirectional JSON-RPC
// 2.0 over newline-delimited stdio. ID is json.RawMessage rather than an
// int because the two directions use different id types — our outgoing
// requests use ints, while goose's server→client requests
// (session/request_permission) carry string UUIDs that a reply must echo
// verbatim.
type rpcMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

// text renders the error for EventError, mapping goose's two structured
// failure surfaces (design doc "Error surfaces to handle") to actionable
// messages: auth_required (code -32000, provider unconfigured) and
// credits_exhausted (-32603 with data{reason, url?}).
func (e *rpcError) text() string {
	if e.Code == -32000 {
		return "goose provider not configured — run `goose configure` (" + e.Message + ")"
	}
	var d struct {
		Reason string `json:"reason"`
		URL    string `json:"url"`
	}
	_ = json.Unmarshal(e.Data, &d)
	if d.Reason == "credits_exhausted" {
		msg := "goose credits exhausted"
		if d.URL != "" {
			msg += " — top up at " + d.URL
		}
		return msg + " (" + e.Message + ")"
	}
	return e.Message
}

// initializeParams — "initialize". fs capabilities are deliberately false:
// advertising them would reroute goose's developer file/shell tools
// through client-side fs/* callbacks this adapter would then have to
// implement. With them false, tools run in-process in goose.
type initializeParams struct {
	ProtocolVersion    int                `json:"protocolVersion"`
	ClientCapabilities clientCapabilities `json:"clientCapabilities"`
}

type clientCapabilities struct {
	FS fsCapabilities `json:"fs"`
}

type fsCapabilities struct {
	ReadTextFile  bool `json:"readTextFile"`
	WriteTextFile bool `json:"writeTextFile"`
}

// mcpServerEntry is one spec stdio MCP server descriptor, threaded from
// StartOptions.MCPServers into session/new and session/load.
type mcpServerEntry struct {
	Name    string        `json:"name"`
	Command string        `json:"command"`
	Args    []string      `json:"args"`
	Env     []envVariable `json:"env"`
}

type envVariable struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// newSessionParams / newSessionResult — "session/new". MCPServers must be
// present even when empty: goose 1.48.0 rejects its absence with -32602
// (pinned: testdata/captures/acp-load-replay.jsonl lines 3-4 for the
// session/load twin of the same requirement).
type newSessionParams struct {
	Cwd        string           `json:"cwd"`
	MCPServers []mcpServerEntry `json:"mcpServers"`
}

type newSessionResult struct {
	SessionID string `json:"sessionId"`
}

// loadSessionParams — "session/load"; the agent replays the session's
// prior turns as session/update notifications before this request's own
// response resolves (the ACP loadSession convention).
type loadSessionParams struct {
	SessionID  string           `json:"sessionId"`
	Cwd        string           `json:"cwd"`
	MCPServers []mcpServerEntry `json:"mcpServers"`
}

// setModeParams — "session/set_mode" (auto/approve/smart_approve/chat).
type setModeParams struct {
	SessionID string `json:"sessionId"`
	ModeID    string `json:"modeId"`
}

// setConfigOptionParams — "session/set_config_option". Value is a flat
// string (pinned: testdata/captures/acp-transcript.jsonl lines 10-12).
type setConfigOptionParams struct {
	SessionID string `json:"sessionId"`
	ConfigID  string `json:"configId"`
	Value     string `json:"value"`
}

// systemPromptParams — "_goose/unstable/session/system-prompt/set", a
// goose extension method (spec-sanctioned underscore prefix). mode
// "append" preserves goose's base prompt, matching claude-code's
// --append-system-prompt semantics. Called only from
// streamSession.setSystemPromptUnstable.
type systemPromptParams struct {
	SessionID string `json:"sessionId"`
	Mode      string `json:"mode"`
	Key       string `json:"key"`
	Text      string `json:"text"`
}

// promptParams / promptResult — "session/prompt". The response (not a
// notification) is what signals a turn's end.
type promptParams struct {
	SessionID string         `json:"sessionId"`
	Prompt    []contentBlock `json:"prompt"`
}

type contentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// promptResult carries one of the five spec stopReason values — end_turn,
// max_tokens, max_turn_requests, refusal, cancelled — all of which the
// adapter handles even though goose emits only three today (see
// streamSession.awaitTurnEnd; an unrecognized string is a plain turn end,
// never a decode error). Usage is a goose extension riding the result
// (pinned: testdata/captures/acp-transcript.jsonl line 85).
type promptResult struct {
	StopReason string     `json:"stopReason"`
	Usage      *wireUsage `json:"usage,omitempty"`
}

type wireUsage struct {
	TotalTokens  int `json:"totalTokens"`
	InputTokens  int `json:"inputTokens"`
	OutputTokens int `json:"outputTokens"`
}

// cancelParams — "session/cancel", sent as a notification (no response).
type cancelParams struct {
	SessionID string `json:"sessionId"`
}

// closeParams — "session/close", best-effort at session shutdown.
type closeParams struct {
	SessionID string `json:"sessionId"`
}

// sessionUpdateParams / sessionUpdate — "session/update" notifications:
// the agent's async narration of a turn (or, during session/load, its
// replay of history). SessionUpdate names which of the v1 schema's 12
// variants this update is; the decoder handles the ones the mapping table
// (design doc "session/update → Event mapping") names and silently skips
// everything else — including plan/plan_removed, official variants goose
// never emits today, and any future addition.
type sessionUpdateParams struct {
	SessionID string          `json:"sessionId"`
	Update    json.RawMessage `json:"update"`
}

// sessionUpdate is the variant-agnostic decode of one update. Content is
// kept raw because the same JSON key holds different shapes per variant: a
// single content block on *_chunk variants, an array of tool-result items
// on tool_call_update.
type sessionUpdate struct {
	SessionUpdate string          `json:"sessionUpdate"`
	Content       json.RawMessage `json:"content,omitempty"`
	ToolCallID    string          `json:"toolCallId,omitempty"`
	Title         string          `json:"title,omitempty"`
	Status        string          `json:"status,omitempty"`
	RawInput      json.RawMessage `json:"rawInput,omitempty"`
	Meta          gooseMeta       `json:"_meta,omitempty"`
	// usage_update-variant fields (pinned: testdata/captures/
	// acp-transcript.jsonl): Used is goose's absolute context-occupancy
	// snapshot, Size the session's REAL context window (e.g. 128000 for a
	// local model the static ContextWindowForModel table would call
	// 200000). Ridden onto the turn-end Usage as ContextTokens/
	// ContextWindow — the same accuracy contract as claude-code's
	// iterations/modelUsage parse (providers/claudecode/wire.go).
	Used int `json:"used,omitempty"`
	Size int `json:"size,omitempty"`
}

// gooseMeta quarantines every _meta.goose.* extension field this adapter
// reads, in one place, so extension dependencies stay greppable (T10).
// Today that is exactly one: the tool_call variant's
// _meta.goose.toolCall{toolName, extensionName} pair, the only stable
// tool identity goose provides (titles are humanized display strings, and
// the ACP kind field is never set on model tool calls).
type gooseMeta struct {
	Goose struct {
		ToolCall struct {
			ToolName      string `json:"toolName"`
			ExtensionName string `json:"extensionName"`
		} `json:"toolCall"`
	} `json:"goose"`
}

// chunkText decodes the *_chunk variants' single content block.
func (u *sessionUpdate) chunkText() string {
	var c contentBlock
	_ = json.Unmarshal(u.Content, &c)
	return c.Text
}

// toolResultText joins tool_call_update's content array — items of
// {type:"content", content:{type:"text", text}} — into one result body.
func (u *sessionUpdate) toolResultText() string {
	var items []struct {
		Content contentBlock `json:"content"`
	}
	_ = json.Unmarshal(u.Content, &items)
	parts := make([]string, 0, len(items))
	for _, it := range items {
		if it.Content.Text != "" {
			parts = append(parts, it.Content.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// toolUse normalizes a tool_call session/update into the provider-neutral
// shape shared by live events and transcript replay. Name is the stable
// _meta.goose.toolCall.toolName (bare "shell", composite
// "todo__todo_write" — naming is per-extension, never synthesized into
// composites here), falling back to the display title for a conforming
// agent that doesn't set the goose extension.
func (u *sessionUpdate) toolUse() *provider.ToolUse {
	name := u.Meta.Goose.ToolCall.ToolName
	if name == "" {
		name = u.Title
	}
	return &provider.ToolUse{
		ID:      u.ToolCallID,
		Name:    name,
		Input:   u.RawInput,
		Path:    toolPath(u.RawInput),
		Mutates: toolMutates(u.Meta.Goose.ToolCall.ExtensionName, u.Meta.Goose.ToolCall.ToolName, u.RawInput),
	}
}

// toolPath digs the touched file out of a tool call's raw input, if any —
// the §5 tool-use attribution signal, same convention as claude-code's.
func toolPath(input json.RawMessage) string {
	var in struct {
		FilePath string `json:"file_path"`
		Path     string `json:"path"`
	}
	_ = json.Unmarshal(input, &in)
	if in.FilePath != "" {
		return in.FilePath
	}
	return in.Path
}

// toolMutates is the §5 file-change signal. goose never sets ACP's kind
// taxonomy on model tool calls, so this is derived from the builtin tool
// table instead: only developer text_editor's writing commands touch the
// file at Path.
func toolMutates(extension, name string, rawInput json.RawMessage) bool {
	if extension != "developer" || name != "text_editor" {
		return false
	}
	switch textEditorCommand(rawInput) {
	case "write", "str_replace", "insert":
		return true
	}
	return false
}

// textEditorCommand extracts developer text_editor's rawInput command
// field — the read/edit discriminator ("view" reads; "write",
// "str_replace", "insert" edit).
func textEditorCommand(rawInput json.RawMessage) string {
	var in struct {
		Command string `json:"command"`
	}
	_ = json.Unmarshal(rawInput, &in)
	return in.Command
}

// requestPermissionParams — "session/request_permission" request params:
// the tool call the agent wants to make, offered alongside options to
// select from. The params carry no _meta (pinned:
// testdata/captures/acp-transcript.jsonl lines 34/44/66), so trust gating
// correlates ToolCall.ToolCallID with the preceding tool_call update's
// gooseMeta — see streamSession.handlePermission.
type requestPermissionParams struct {
	SessionID string             `json:"sessionId"`
	ToolCall  permissionToolCall `json:"toolCall"`
	Options   []permissionOption `json:"options"`
}

type permissionToolCall struct {
	ToolCallID string          `json:"toolCallId"`
	Title      string          `json:"title"`
	RawInput   json.RawMessage `json:"rawInput,omitempty"`
}

// permissionOption is one offered reply. OptionID is an agent-chosen
// opaque string per spec — goose's ids happen to mirror the kind names,
// but that is coincidence, not contract; selection goes by Kind
// (allow_once/allow_always/reject_once/reject_always) only.
type permissionOption struct {
	OptionID string `json:"optionId"`
	Name     string `json:"name"`
	Kind     string `json:"kind"`
}

// selectOption picks the offered option matching the decision: the _once
// kind of the wanted polarity, falling back to the _always kind of the
// same polarity (preferring _once so a denial doesn't persist into a
// later, differently-trusted resume). ok=false when no option of the
// needed polarity exists — the caller replies outcome "cancelled".
func selectOption(options []permissionOption, allow bool) (optionID string, ok bool) {
	once, always := "reject_once", "reject_always"
	if allow {
		once, always = "allow_once", "allow_always"
	}
	fallback := ""
	for _, o := range options {
		switch o.Kind {
		case once:
			return o.OptionID, true
		case always:
			if fallback == "" {
				fallback = o.OptionID
			}
		}
	}
	if fallback != "" {
		return fallback, true
	}
	return "", false
}

// permissionResult / permissionOutcome — the adapter's reply to
// "session/request_permission": {outcome:{outcome:"selected", optionId}}
// or {outcome:{outcome:"cancelled"}}.
type permissionResult struct {
	Outcome permissionOutcome `json:"outcome"`
}

type permissionOutcome struct {
	Outcome  string `json:"outcome"`
	OptionID string `json:"optionId,omitempty"`
}
