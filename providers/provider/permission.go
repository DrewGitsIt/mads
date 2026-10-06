package provider

import (
	"context"
	"encoding/json"
)

// PermissionAsker answers one permission prompt the harness raised for a
// tool call (StartOptions.Permissions). The adapter calls it on its own
// goroutine — never its wire read loop — and blocks the harness's request
// on the returned decision.
//
// ctx is cancelled when the harness stops waiting for the answer: it
// withdrew the request (claude-code's control_cancel_request, sent when the
// turn is interrupted; an ACP agent's $/cancel_request, or the client's own
// session/cancel), the process exited, or the session was closed. An
// asker seeing ctx done should stop asking the user and return promptly;
// the adapter discards a decision returned after that point, since nobody
// is left to receive it.
//
// The contract is harness-agnostic on purpose: the same func serves
// claude-code's stream-json can_use_tool requests (providers/claudecode
// hostprompt.go) and ACP agents' session/request_permission (providers/
// acprpc handlePermission — goose, kiro).
type PermissionAsker func(ctx context.Context, ask PermissionAsk) PermissionDecision

// PermissionAsk is one tool call the harness wants permission for.
type PermissionAsk struct {
	// ToolUseID is the wire id of the tool call awaiting the decision — the
	// same id its EventToolUse and EventToolResult carry. Empty only when
	// the harness didn't say.
	ToolUseID string
	// ParentToolUseID is the subagent (Task/Agent) call the asking tool call
	// runs under, when the adapter can tell; empty means the main agent or
	// unknown. The same correlation key as Event.ParentToolUseID.
	ParentToolUseID string
	// AgentID is the harness's own id for the subagent making the call
	// (claude-code's agent_id, which equals that subagent task's task_id);
	// empty for the main agent.
	AgentID string
	// Tool is the harness-native tool name ("Write", "Bash", ...).
	Tool string
	// Input is the tool call's arguments as the harness sent them.
	Input json.RawMessage
	// Reason is the harness's own explanation of why it is asking (e.g.
	// claude-code's "Path is outside allowed working directories"), when it
	// gives one.
	Reason string
}

// PermissionDecision is the answer to a PermissionAsk: allow the call to
// run as asked, or deny it. Message, on a deny, is what the harness relays
// to the model as the reason (claude-code returns it verbatim as the
// refused call's error tool_result); ignored on an allow.
type PermissionDecision struct {
	Allow   bool
	Message string
}
