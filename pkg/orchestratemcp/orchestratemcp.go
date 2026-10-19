// Package orchestratemcp implements the link tools' MCP-over-stdio server
// (docs/LINKED-SESSIONS-DESIGN.md decisions 1-3, decomposition item 5),
// mounted for the provider as "mads-links": list_linked_sessions,
// message_session, get_session_summary, get_session_turns, check_mailbox,
// set_attention — each a thin wrapper over one of the Router's
// /sessions/{id}/orchestrate/* and /links/{id}/orchestrate/message routes.
// Every per-session tool takes `session`, the linked session's name, and
// resolves it against the subjects route on each call (resolve.go), so the
// Router routes keep their link and session IDs unchanged. JSON-RPC 2.0,
// newline-delimited framing per the MCP stdio transport; only initialize,
// tools/list, and tools/call are implemented — the whole surface these
// tools need, no resources/prompts/sampling.
package orchestratemcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
)

const protocolVersion = "2024-11-05"

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Server is one MCP-over-stdio session: it reads JSON-RPC requests from in,
// dispatches to the link tools via client, and writes responses to out. One
// Server per spawned process (pkg/capability.Registry starts exactly one
// long-lived process per session, matching setupMCP's existing lifecycle).
type Server struct {
	client   *client
	readOnly bool
	in       *bufio.Reader
	out      io.Writer
}

// New builds a Server. routerURL and sessionID are the caller's own Router
// base URL and session ID (env MADS_ROUTER_URL / MADS_SESSION_ID, set by the
// Runner at spawn time) — every tool call is scoped to this one session.
// readOnly (env MADS_ORCHESTRATE_READONLY) excludes message_session from
// tools/list and rejects it if called anyway — set for a session that holds
// only the read grant, not the orchestrate grant (item 1).
func New(routerURL, sessionID string, readOnly bool, in io.Reader, out io.Writer) *Server {
	return &Server{
		client:   newClient(routerURL, sessionID),
		readOnly: readOnly,
		in:       bufio.NewReader(in),
		out:      out,
	}
}

// Run reads one JSON-RPC request per line until in reaches EOF or ctx is
// done, writing one response line per request that carries an id
// (notifications, e.g. "notifications/initialized", get no response, per
// JSON-RPC 2.0).
func (s *Server) Run(ctx context.Context) error {
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		line, err := s.in.ReadString('\n')
		if err != nil {
			if err == io.EOF && len(line) == 0 {
				return nil
			}
			if err != io.EOF {
				return fmt.Errorf("read request: %w", err)
			}
		}
		if len(line) == 0 {
			return nil
		}
		var req rpcRequest
		if unmarshalErr := json.Unmarshal([]byte(line), &req); unmarshalErr != nil {
			log.Printf("orchestratemcp: decode request: %v", unmarshalErr)
			continue
		}
		resp := s.handle(ctx, req)
		if resp == nil {
			continue // notification: no id, no response
		}
		raw, marshalErr := json.Marshal(resp)
		if marshalErr != nil {
			log.Printf("orchestratemcp: encode response: %v", marshalErr)
			continue
		}
		if _, writeErr := s.out.Write(append(raw, '\n')); writeErr != nil {
			return fmt.Errorf("write response: %w", writeErr)
		}
	}
}

func (s *Server) handle(ctx context.Context, req rpcRequest) *rpcResponse {
	if len(req.ID) == 0 {
		return nil // notification
	}
	switch req.Method {
	case "initialize":
		instr := instructions
		if s.readOnly {
			instr = readOnlyInstructions
		}
		return &rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{
			"protocolVersion": protocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "mads-links", "version": "0.2.0"},
			"instructions":    instr,
		}}
	case "tools/list":
		tools := toolDefs
		if s.readOnly {
			tools = readOnlyToolDefs()
		}
		return &rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{"tools": tools}}
	case "tools/call":
		return s.handleToolCall(ctx, req)
	default:
		return &rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: -32601, Message: "method not found: " + req.Method}}
	}
}

func (s *Server) handleToolCall(ctx context.Context, req rpcRequest) *rpcResponse {
	var params struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return &rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: -32602, Message: "invalid params: " + err.Error()}}
	}
	if s.readOnly && params.Name == "message_session" {
		return &rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: -32602, Message: "unknown tool: message_session"}}
	}

	var args struct {
		Session  string `json:"session"`
		Text     string `json:"text"`
		WaitMS   int64  `json:"wait_ms,omitempty"`
		FromTurn int    `json:"from_turn"`
		ToTurn   int    `json:"to_turn"`
		Cadence  string `json:"cadence"`
	}
	if len(params.Arguments) > 0 {
		if err := json.Unmarshal(params.Arguments, &args); err != nil {
			return &rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: -32602, Message: "invalid arguments: " + err.Error()}}
		}
	}

	var status int
	var body []byte
	var err error
	switch params.Name {
	case "list_linked_sessions":
		status, body, err = s.listLinkedSessions(ctx)
	case "message_session", "get_session_summary", "get_session_turns", "check_mailbox", "set_attention":
		var target linkedSession
		target, err = s.resolve(ctx, args.Session)
		if err != nil {
			break
		}
		switch params.Name {
		case "message_session":
			status, body, err = s.client.sendMessage(ctx, target.LinkID, args.Text, args.WaitMS)
		case "get_session_summary":
			status, body, err = s.client.getSummary(ctx, target.SubjectSessionID)
		case "get_session_turns":
			status, body, err = s.client.getTurns(ctx, target.SubjectSessionID, args.FromTurn, args.ToTurn)
		case "check_mailbox":
			status, body, err = s.client.checkMailbox(ctx, target.LinkID)
		case "set_attention":
			status, body, err = s.client.setAttention(ctx, target.LinkID, args.Cadence)
		}
	default:
		return &rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: -32602, Message: "unknown tool: " + params.Name}}
	}

	var te *toolError
	if errors.As(err, &te) {
		return toolResult(req.ID, te.msg, true)
	}
	if err != nil {
		return &rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: -32000, Message: err.Error()}}
	}
	return toolResult(req.ID, string(body), status >= 400)
}

func toolResult(id json.RawMessage, text string, isError bool) *rpcResponse {
	return &rpcResponse{JSONRPC: "2.0", ID: id, Result: map[string]any{
		"content": []map[string]string{{"type": "text", "text": text}},
		"isError": isError,
	}}
}

// resolve looks up the caller's linked sessions and picks the one arg names.
func (s *Server) resolve(ctx context.Context, arg string) (linkedSession, error) {
	sessions, err := s.client.linkedSessions(ctx)
	if err != nil {
		return linkedSession{}, err
	}
	return resolveSession(sessions, arg)
}

// listLinkedSessions reshapes the subjects route's rows into
// list_linked_sessions' agent-facing rows (listedSession).
func (s *Server) listLinkedSessions(ctx context.Context) (int, []byte, error) {
	sessions, err := s.client.linkedSessions(ctx)
	if err != nil {
		return 0, nil, err
	}
	out := make([]listedSession, len(sessions))
	for i, l := range sessions {
		out[i] = listedSession{
			Name:      l.SubjectName,
			SessionID: l.SubjectSessionID,
			Grants:    l.Grants,
			Activity:  l.Activity,
			Cadence:   l.Cadence,
		}
	}
	raw, err := json.Marshal(out)
	if err != nil {
		return 0, nil, err
	}
	return 200, raw, nil
}
