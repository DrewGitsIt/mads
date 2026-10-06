package claudecode

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/drewkidwell/mads/providers/provider"
)

// claude's host permission channel. With --permission-prompt-tool stdio
// (trustArgs, chat transport with a StartOptions.Permissions asker) every
// permission prompt claude would otherwise show in its own UI arrives on
// stdout as an inbound control_request instead, and claude blocks that tool
// call until a control_response with the same request_id comes back on
// stdin. Wire shapes verified against claude-code 2.1.285 (capture:
// testdata/stream-can-use-tool.jsonl):
//
//	← {"type":"control_request","request_id":"<uuid>","request":{"subtype":"can_use_tool",
//	     "tool_name":"Write","input":{...},"tool_use_id":"toolu_…","agent_id":"a…",
//	     "decision_reason":"Path is outside allowed working directories",...}}
//	→ {"type":"control_response","response":{"subtype":"success","request_id":"<uuid>",
//	     "response":{"behavior":"allow","updatedInput":{...}}}}
//	→ … "response":{"behavior":"deny","message":"<relayed to the model verbatim>"}
//	← {"type":"control_cancel_request","request_id":"<uuid>"}
//
// agent_id is present only for a subagent's calls. The cancel arrives when
// an interrupt lands while a prompt is open; claude then refuses the call
// itself ("The user doesn't want to proceed with this tool use. …") and
// wants no response. An error response ({"subtype":"error","error":"…"})
// makes claude refuse the call with "Tool permission request failed: …" —
// that is what any other inbound request subtype gets, so claude never
// hangs waiting on a request mads doesn't understand.

// inboundControl is a control_request (or control_cancel_request) claude
// sends to its host.
type inboundControl struct {
	Type      string          `json:"type"`
	RequestID string          `json:"request_id"`
	Request   json.RawMessage `json:"request"`
}

// canUseTool is a can_use_tool request body.
type canUseTool struct {
	Subtype        string          `json:"subtype"`
	ToolName       string          `json:"tool_name"`
	Input          json.RawMessage `json:"input"`
	ToolUseID      string          `json:"tool_use_id"`
	AgentID        string          `json:"agent_id"`
	DecisionReason string          `json:"decision_reason"`
}

// handleControlRequest answers one inbound control_request. A can_use_tool
// request with an asker wired goes to the asker on its own goroutine —
// asking the user can take minutes, and the read loop must keep draining
// stdout meanwhile (other calls, other subagents, the cancel for this very
// request). Everything else gets an immediate error response.
func (s *streamSession) handleControlRequest(raw []byte) {
	var in inboundControl
	if json.Unmarshal(raw, &in) != nil || in.RequestID == "" {
		return
	}
	var req canUseTool
	_ = json.Unmarshal(in.Request, &req)
	if req.Subtype != "can_use_tool" || s.asker == nil {
		s.respondControlError(in.RequestID, fmt.Sprintf("mads does not handle control request %q", req.Subtype))
		return
	}
	s.mu.Lock()
	ask := provider.PermissionAsk{
		ToolUseID:       req.ToolUseID,
		ParentToolUseID: s.agentParents[req.AgentID],
		AgentID:         req.AgentID,
		Tool:            req.ToolName,
		Input:           req.Input,
		Reason:          req.DecisionReason,
	}
	ctx, cancel := context.WithCancel(s.askCtx)
	s.asks[in.RequestID] = cancel
	s.mu.Unlock()
	go s.ask(ctx, in.RequestID, ask)
}

// ask runs the asker for one request and writes claude its answer — unless
// claude stopped waiting first (cancelled, exited, closed), in which case
// nobody is left to receive it.
func (s *streamSession) ask(ctx context.Context, requestID string, ask provider.PermissionAsk) {
	dec := s.asker(ctx, ask)
	s.mu.Lock()
	cancel, open := s.asks[requestID]
	delete(s.asks, requestID)
	s.mu.Unlock()
	stale := !open || ctx.Err() != nil
	if open {
		cancel() // releases ctx; cancelAsk already did when !open
	}
	if stale {
		return
	}
	_ = s.writeControl(requestID, permissionResult(ask, dec))
}

// permissionResult is the can_use_tool response body for a decision. An
// allow echoes the input back unchanged as updatedInput (claude runs the
// call with whatever comes back here); a deny carries the message claude
// relays to the model as the refused call's result.
func permissionResult(ask provider.PermissionAsk, dec provider.PermissionDecision) map[string]any {
	if dec.Allow {
		input := ask.Input
		if len(input) == 0 || string(input) == "null" {
			input = json.RawMessage(`{}`)
		}
		return map[string]any{"behavior": "allow", "updatedInput": input}
	}
	msg := dec.Message
	if msg == "" {
		msg = "Permission denied."
	}
	return map[string]any{"behavior": "deny", "message": msg}
}

// cancelAsk handles control_cancel_request: claude withdrew the prompt (an
// interrupt landed while it was open), so the asker's ctx is cancelled and
// its eventual decision dropped.
func (s *streamSession) cancelAsk(raw []byte) {
	var in inboundControl
	if json.Unmarshal(raw, &in) != nil {
		return
	}
	s.mu.Lock()
	cancel := s.asks[in.RequestID]
	delete(s.asks, in.RequestID)
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// writeControl sends a success control_response for an inbound request.
func (s *streamSession) writeControl(requestID string, response any) error {
	line, err := json.Marshal(map[string]any{
		"type": "control_response",
		"response": map[string]any{
			"subtype":    "success",
			"request_id": requestID,
			"response":   response,
		},
	})
	if err != nil {
		return err
	}
	return s.write(append(line, '\n'))
}

// respondControlError refuses an inbound request claude is blocked on.
func (s *streamSession) respondControlError(requestID, msg string) {
	line, err := json.Marshal(map[string]any{
		"type": "control_response",
		"response": map[string]any{
			"subtype":    "error",
			"request_id": requestID,
			"error":      msg,
		},
	})
	if err == nil {
		_ = s.write(append(line, '\n'))
	}
}

// noteSubagent learns which Agent tool call a subagent runs under from its
// task_started line (task_id is the subagent's agent_id — the same id its
// can_use_tool requests carry), so an ask can name its parent call. Caller
// holds s.mu.
func (s *streamSession) noteSubagent(l *wireLine) {
	if l.Subtype != "task_started" || l.TaskID == "" || l.ToolUseID == "" {
		return
	}
	if s.agentParents == nil {
		s.agentParents = map[string]string{}
	}
	s.agentParents[l.TaskID] = l.ToolUseID
}
