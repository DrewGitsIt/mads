package kirocli

import (
	"encoding/json"

	"github.com/drewkidwell/mads/providers/provider"
)

// rpcMessage is one line of kiro-cli's ACP transport: genuine bidirectional
// JSON-RPC 2.0 over newline-delimited stdio, not claude-code's one-direction
// stream-json. ID is set on requests and their responses, absent on
// notifications — that's how a decoded line is told apart below.
type rpcMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *int            `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// newSessionParams / newSessionResult — "session/new".
type newSessionParams struct {
	Cwd   string `json:"cwd"`
	Model string `json:"modelId,omitempty"`
}

type newSessionResult struct {
	SessionID string `json:"sessionId"`
}

// loadSessionParams — "session/load"; the agent replays the session's prior
// turns as "session/update" notifications before this request's own
// response resolves (the ACP loadSession convention this design commits to
// — see providers/kirocli/README.md's transcript caveat).
type loadSessionParams struct {
	SessionID string `json:"sessionId"`
	Cwd       string `json:"cwd,omitempty"`
}

// promptParams / promptResult — "session/prompt". The response (not a
// notification) is what signals a turn's end.
type promptParams struct {
	SessionID string        `json:"sessionId"`
	Prompt    []promptBlock `json:"prompt"`
}

type promptBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type promptResult struct {
	StopReason string `json:"stopReason"`
}

// cancelParams — "session/cancel", sent as a notification (no response).
type cancelParams struct {
	SessionID string `json:"sessionId"`
}

// sessionUpdateParams / sessionUpdate — "session/update" notifications: the
// agent's async narration of a turn (or, during session/load, its replay of
// history). SessionUpdate names which variant this update is.
type sessionUpdateParams struct {
	SessionID string          `json:"sessionId"`
	Update    json.RawMessage `json:"update"`
}

type sessionUpdate struct {
	SessionUpdate string          `json:"sessionUpdate"`
	Content       *updateContent  `json:"content,omitempty"`
	ToolCall      *toolCallUpdate `json:"toolCall,omitempty"`
}

type updateContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type toolCallUpdate struct {
	Title string `json:"title"`
	// Kind is ACP's public tool-call taxonomy (agentclientprotocol.com):
	// read/edit/delete/move/search/execute/think/fetch/other. Trust gating
	// classifies on this field — see streamSession.toolKindAllowed.
	Kind  string          `json:"kind,omitempty"`
	Input json.RawMessage `json:"rawInput,omitempty"`
}

// requestPermissionParams — "session/request_permission" request params: the
// tool call the agent wants to make, offered alongside a list of options the
// caller could pick. This adapter doesn't inspect Options; it derives its
// answer entirely from the session's trust level and the tool call's Kind.
type requestPermissionParams struct {
	ToolCall toolCallUpdate `json:"toolCall"`
}

// permissionResult / permissionOutcome — the adapter's reply to
// "session/request_permission". See streamSession.autoApprove/denyPermission.
type permissionResult struct {
	Outcome permissionOutcome `json:"outcome"`
}

type permissionOutcome struct {
	Outcome  string `json:"outcome"`
	OptionID string `json:"optionId,omitempty"`
}

// wireModel is one entry of `kiro-cli chat --list-models --format json`.
type wireModel struct {
	ModelID   string `json:"model_id"`
	ModelName string `json:"model_name"`
	IsDefault bool   `json:"is_default"`
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

// toolUse normalizes a tool_call session/update into the provider-neutral
// shape shared by live events and transcript replay.
func (u *toolCallUpdate) toolUse() *provider.ToolUse {
	if u == nil {
		return nil
	}
	// Title is free display text, so mutation is derived from ACP's Kind
	// taxonomy instead: edit/delete/move touch the file, everything else
	// (read/search/execute/think/fetch/other) is a read for §5 purposes.
	mutates := u.Kind == "edit" || u.Kind == "delete" || u.Kind == "move"
	return &provider.ToolUse{Name: u.Title, Input: u.Input, Path: toolPath(u.Input), Mutates: mutates}
}
