package acprpc

import (
	"encoding/json"
	"strings"
)

// The types in this file derive from the ACP v1 spec schema
// (agentclientprotocol.com, pinned by the official
// agent-client-protocol-schema crate) and are verified against committed
// captures from four agents: goose 1.48.0
// (providers/goosecli/testdata/captures/), claude-agent-acp 0.75.1,
// codex-acp 1.10.0, and hermes 0.21.1 (testdata/captures/ here). The spec
// is normative: each agent is one conforming implementation, and the
// decoder must survive any conforming agent's output — official variants
// a given agent never emits are still handled or skipped silently, never
// a decode error. Vendor extension shapes are quarantined: the ONLY _meta
// field the engine ever reads is nsToolCallMeta, gated behind the
// ToolIdentityMetaNamespace knob.

// rpcMessage is one line of the ACP transport: bidirectional JSON-RPC 2.0
// over newline-delimited stdio. ID is json.RawMessage rather than an int
// because the two directions use different id types — our outgoing
// requests use ints, while agent→client requests
// (session/request_permission) may carry string UUIDs a reply must echo
// verbatim (goose does: acp-transcript.jsonl line 34).
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

// text renders the error for EventError, mapping the two structured
// failure surfaces the goose captures pinned to actionable messages:
// auth_required (code -32000, ACP's reserved auth error — the agent's
// backing provider isn't configured) and credits_exhausted (-32603 with
// data{reason, url?}). The vocabulary is goose's but harmless elsewhere;
// any other error passes through as its own message.
func (e *rpcError) text() string {
	if e.Code == -32000 {
		return "agent authentication required (" + e.Message + ")"
	}
	var d struct {
		Reason string `json:"reason"`
		URL    string `json:"url"`
	}
	_ = json.Unmarshal(e.Data, &d)
	if d.Reason == "credits_exhausted" {
		msg := "agent credits exhausted"
		if d.URL != "" {
			msg += " — top up at " + d.URL
		}
		return msg + " (" + e.Message + ")"
	}
	return e.Message
}

// initializeParams — "initialize". ClientCapabilities is deliberately the
// empty object: advertising fs or terminal capabilities reroutes the
// agent's own file/shell tools through client-side callbacks this engine
// would then have to implement; with none advertised, tools run in the
// agent's process. goose-as-client does the same (provider.rs:1536-1560).
type initializeParams struct {
	ProtocolVersion    int      `json:"protocolVersion"`
	ClientCapabilities struct{} `json:"clientCapabilities"`
}

// initializeResult carries the capability set every later gate reads.
// Shape pinned by claude-agent-acp-full.jsonl line 2 / codex-acp-full.jsonl
// line 2 / hermes-acp-full.jsonl line 2.
type initializeResult struct {
	AgentCapabilities agentCapabilities `json:"agentCapabilities"`
}

// agentCapabilities — the three fields the engine gates on. Entries in
// sessionCapabilities are presence-keyed EMPTY OBJECTS, not bools (codex
// advertises "close": {}; hermes has no close key at all — the pinned
// capture lines above), so the decode keeps the raw map and tests keys.
type agentCapabilities struct {
	LoadSession         bool                       `json:"loadSession"`
	MCPCapabilities     mcpCapabilities            `json:"mcpCapabilities"`
	SessionCapabilities map[string]json.RawMessage `json:"sessionCapabilities"`
}

type mcpCapabilities struct {
	HTTP bool `json:"http"`
	SSE  bool `json:"sse"`
}

// stdioMCPServer / httpMCPServer are the two spec MCP server descriptor
// shapes the engine sends. Stdio carries no type field and always includes
// args/env — the shape goose 1.48.0 accepts (acp-transcript.jsonl line 3).
// The two are marshaled separately (mcpEntries) so stdio's always-present
// arrays and http's url never contaminate each other.
type stdioMCPServer struct {
	Name    string        `json:"name"`
	Command string        `json:"command"`
	Args    []string      `json:"args"`
	Env     []envVariable `json:"env"`
}

type httpMCPServer struct {
	Type string `json:"type"`
	Name string `json:"name"`
	URL  string `json:"url"`
}

type envVariable struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// newSessionParams — "session/new". MCPServers must be present even when
// empty: goose 1.48.0 rejects its absence with -32602 (pinned:
// providers/goosecli/testdata/captures/acp-load-replay.jsonl lines 3-4
// for the session/load twin of the same requirement).
type newSessionParams struct {
	Cwd        string            `json:"cwd"`
	MCPServers []json.RawMessage `json:"mcpServers"`
}

// loadSessionParams — "session/load"; the agent replays the session's
// prior turns as session/update notifications before this request's own
// response resolves (the ACP loadSession convention). MCPServers is
// required here too — same pinned evidence as session/new.
type loadSessionParams struct {
	SessionID  string            `json:"sessionId"`
	Cwd        string            `json:"cwd"`
	MCPServers []json.RawMessage `json:"mcpServers"`
}

// sessionResult decodes both session/new and session/load results: the
// sessionId (new only), plus the modes and configOptions the mode
// transport detection and mode validation read. Pinned by
// claude-agent-acp-full.jsonl line 6 / codex-acp-full.jsonl line 5 /
// hermes-acp-full.jsonl line 4 (hermes: no configOptions at all) and the
// goose acp-transcript.jsonl line 4.
type sessionResult struct {
	SessionID     string         `json:"sessionId"`
	Modes         *sessionModes  `json:"modes,omitempty"`
	ConfigOptions []configOption `json:"configOptions,omitempty"`
	Models        *sessionModels `json:"models,omitempty"`
}

// sessionModels is ACP's own model selector, the standards-track sibling of
// the `model` configOption: agents that offer it pair it with
// session/set_model rather than session/set_config_option. The two surfaces
// are independent and an agent may ship either, both, or neither —
// hermes-acp-full.jsonl line 4 has only this one (70 models, no
// configOptions at all), claude-agent-acp-full.jsonl line 6 and the goose
// acp-transcript.jsonl line 4 have only the configOption, and
// codex-acp-full.jsonl line 5 has both.
//
// Where both exist they do not agree, and this one is the finer-grained of
// the two: codex lists 15 models here (the gpt-5.6-terra[low|medium|high]
// reasoning-effort variants) against 3 coarse families in the configOption,
// and only this surface can name the live selection — currentModelId is
// "gpt-5.6-terra[medium]" where the configOption's currentValue flattens to
// "gpt-5.6-terra".
type sessionModels struct {
	CurrentModelID  string      `json:"currentModelId,omitempty"`
	AvailableModels []modelInfo `json:"availableModels,omitempty"`
}

// modelInfo is one entry of sessionModels. Note the key is `modelId`, not
// the configOption's `value` — the two surfaces have separate shapes as
// well as separate setters.
type modelInfo struct {
	ModelID     string `json:"modelId"`
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
}

type sessionModes struct {
	CurrentModeID  string     `json:"currentModeId"`
	AvailableModes []modeInfo `json:"availableModes"`
}

type modeInfo struct {
	ID string `json:"id"`
}

// configOption is one session-result config option. Beyond ID and Category
// (the mode-transport detection), the model option's CurrentValue and
// Options are what DiscoverModels reads — shape pinned by
// providers/goosecli/testdata/captures/acp-transcript.jsonl line 4
// ({"id":"model","name":"Model","type":"select","currentValue":"qwen3.8",
// "options":[{"value":…,"name":…},…]}). Additive decode: agents that omit
// the fields simply leave them zero.
type configOption struct {
	ID           string               `json:"id"`
	Name         string               `json:"name,omitempty"`
	Category     string               `json:"category,omitempty"`
	CurrentValue string               `json:"currentValue,omitempty"`
	Options      []configOptionChoice `json:"options,omitempty"`
}

// configOptionChoice is one selectable value of a select-typed configOption.
type configOptionChoice struct {
	Value string `json:"value"`
	Name  string `json:"name"`
}

// modeConfigOptionID returns the id of the configOption carrying the
// session mode, empty when none — the runtime mode-transport detection
// (goose-as-client provider.rs:718-747): a category "mode" option means
// mode rides session/set_config_option, else session/set_mode. v1 reads
// the session/new-or-load result only; config_option_update refreshes are
// skipped (the transport is not expected to change mid-session).
func modeConfigOptionID(options []configOption) string {
	for _, o := range options {
		if o.Category == "mode" {
			return o.ID
		}
	}
	return ""
}

// setModeParams — "session/set_mode".
type setModeParams struct {
	SessionID string `json:"sessionId"`
	ModeID    string `json:"modeId"`
}

// setConfigOptionParams — "session/set_config_option". Value is a flat
// string (pinned: providers/goosecli/testdata/captures/
// acp-transcript.jsonl lines 10-12).
type setConfigOptionParams struct {
	SessionID string `json:"sessionId"`
	ConfigID  string `json:"configId"`
	Value     string `json:"value"`
}

// setModelParams — "session/set_model", the setter paired with the
// sessionModels selector. An id discovered from availableModels must be set
// through this call; the configOption setter would reject it.
type setModelParams struct {
	SessionID string `json:"sessionId"`
	ModelID   string `json:"modelId"`
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
// max_tokens, max_turn_requests, refusal, cancelled — all handled (see
// finishTurn; an unrecognized string is a plain turn end, never a decode
// error). Usage is an extension riding the result (goose:
// acp-transcript.jsonl line 85); agents without it just leave Usage nil.
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

// closeParams — "session/close", best-effort at session shutdown, sent
// only when the agent advertised sessionCapabilities.close.
type closeParams struct {
	SessionID string `json:"sessionId"`
}

// sessionUpdateParams / sessionUpdate — "session/update" notifications:
// the agent's async narration of a turn (or, during session/load, its
// replay of history). SessionUpdate names which of the v1 schema's 12
// variants this update is; the decoder handles the mapped ones and
// silently skips everything else — including official variants a given
// agent never emits, and any future addition.
type sessionUpdateParams struct {
	SessionID string          `json:"sessionId"`
	Update    json.RawMessage `json:"update"`
}

// sessionUpdate is the variant-agnostic decode of one update. Content is
// kept raw because the same JSON key holds different shapes per variant: a
// single content block on *_chunk variants, an array of tool-result items
// on tool_call_update. Kind is the spec tool-kind taxonomy field on
// tool_call variants — set by claude/codex/hermes, never by goose
// (testdata/captures/README.md "Tool identity varies"). Meta stays raw:
// the only extension read is decodeNSToolCallMeta, behind the
// ToolIdentityMetaNamespace knob.
type sessionUpdate struct {
	SessionUpdate string          `json:"sessionUpdate"`
	Content       json.RawMessage `json:"content,omitempty"`
	ToolCallID    string          `json:"toolCallId,omitempty"`
	Title         string          `json:"title,omitempty"`
	Kind          string          `json:"kind,omitempty"`
	Status        string          `json:"status,omitempty"`
	RawInput      json.RawMessage `json:"rawInput,omitempty"`
	// MessageID groups streamed *_chunk updates into one logical message —
	// goose (acp-transcript.jsonl lines 72-82, acp-load-replay.jsonl line
	// 8) carries it on agent_message_chunk/agent_thought_chunk/user_
	// message_chunk. Used to coalesce one assistant turn's many small text
	// fragments into a single Transcript() entry rather than one line per
	// streamed word.
	MessageID string          `json:"messageId,omitempty"`
	Meta      json.RawMessage `json:"_meta,omitempty"`
	// usage_update-variant fields (goose: acp-transcript.jsonl line 84):
	// Used is the agent's absolute context-occupancy snapshot, Size the
	// session's REAL context window (e.g. 128000 for a local model a static
	// table would call 200000). Ridden onto the turn-end Usage as
	// ContextTokens/ContextWindow.
	Used int `json:"used,omitempty"`
	Size int `json:"size,omitempty"`
}

// nsToolCallMeta quarantines the engine's ONLY _meta read, in one
// greppable type: the tool_call variant's _meta.<ns>.toolCall{toolName,
// extensionName} pair — the sole stable tool identity goose provides
// (titles are humanized display strings, and goose never sets the ACP
// kind field on model tool calls; claude puts a comparable name under
// _meta.claudeCode). The namespace is the ToolIdentityMetaNamespace knob;
// decode is one map hop on it, so no vendor key is ever hardcoded here.
type nsToolCallMeta struct {
	ToolName      string `json:"toolName"`
	ExtensionName string `json:"extensionName"`
}

func decodeNSToolCallMeta(raw json.RawMessage, ns string) nsToolCallMeta {
	var outer map[string]struct {
		ToolCall nsToolCallMeta `json:"toolCall"`
	}
	_ = json.Unmarshal(raw, &outer)
	return outer[ns].ToolCall
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

// rawInputCommand extracts a tool call's rawInput "command" field — the
// ToolClassRule.ByCommand discriminator (goose's text_editor: "view"
// reads; "write"/"str_replace"/"insert" edit).
func rawInputCommand(rawInput json.RawMessage) string {
	var in struct {
		Command string `json:"command"`
	}
	_ = json.Unmarshal(rawInput, &in)
	return in.Command
}

// requestPermissionParams — "session/request_permission" request params:
// the tool call the agent wants to make, offered alongside options to
// select from. goose's params carry no _meta (acp-transcript.jsonl lines
// 34/44/66), so trust gating correlates ToolCall.ToolCallID with the
// preceding tool_call update's recorded identity — see handlePermission.
type requestPermissionParams struct {
	SessionID string             `json:"sessionId"`
	ToolCall  permissionToolCall `json:"toolCall"`
	Options   []permissionOption `json:"options"`
}

// permissionToolCall is the spec ToolCallUpdate embedded in the request:
// every field but toolCallId is optional, and an agent that asks BEFORE
// announcing the tool_call (kiro-cli's shape) carries the identity here
// instead — Kind/Meta feed handlePermission's fallback classification when
// no announced call matches the id.
type permissionToolCall struct {
	ToolCallID string          `json:"toolCallId"`
	Title      string          `json:"title"`
	Kind       string          `json:"kind,omitempty"`
	RawInput   json.RawMessage `json:"rawInput,omitempty"`
	Meta       json.RawMessage `json:"_meta,omitempty"`
}

// permissionOption is one offered reply. OptionID is an agent-chosen
// opaque string per spec — goose's ids happen to mirror the kind names,
// but that is coincidence, not contract (codex/hermes ids do not, and
// hermes offers only two options with no _always pair —
// testdata/captures/README.md); selection goes by Kind
// (allow_once/allow_always/reject_once/reject_always) only, exactly as
// goose-as-client does (acp/common.rs:70-107).
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

// permissionResult / permissionOutcome — the engine's reply to
// "session/request_permission": {outcome:{outcome:"selected", optionId}}
// or {outcome:{outcome:"cancelled"}}.
type permissionResult struct {
	Outcome permissionOutcome `json:"outcome"`
}

type permissionOutcome struct {
	Outcome  string `json:"outcome"`
	OptionID string `json:"optionId,omitempty"`
}
