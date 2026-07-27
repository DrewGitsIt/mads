// Package orchestratemcp implements the leader capability's MCP-over-stdio
// server (docs/LINKED-SESSIONS-DESIGN.md decisions 1-3, decomposition item
// 5): list_links, get_summary, get_turns, send_message — each a thin
// wrapper over one of the Router's /sessions/{id}/orchestrate/* and
// /links/{id}/orchestrate/message routes. JSON-RPC 2.0, newline-delimited
// framing per the MCP stdio transport; only initialize, tools/list, and
// tools/call are implemented — the whole surface these tools need, no
// resources/prompts/sampling.
package orchestratemcp

import (
	"bufio"
	"context"
	"encoding/json"
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
// dispatches to the three orchestrate tools via client, and writes responses
// to out. One Server per spawned process (pkg/capability.Registry starts
// exactly one long-lived process per session, matching setupMCP's existing
// lifecycle).
type Server struct {
	client *client
	in     *bufio.Reader
	out    io.Writer
}

// New builds a Server. routerURL and sessionID are the leader's own Router
// base URL and session ID (env MADS_ROUTER_URL / MADS_SESSION_ID, set by the
// Runner at spawn time) — every tool call is scoped to this one leader
// session.
func New(routerURL, sessionID string, in io.Reader, out io.Writer) *Server {
	return &Server{
		client: newClient(routerURL, sessionID),
		in:     bufio.NewReader(in),
		out:    out,
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
		return &rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{
			"protocolVersion": protocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "mads-orchestrate", "version": "0.1.0"},
		}}
	case "tools/list":
		return &rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{"tools": toolDefs}}
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

	var status int
	var body []byte
	var err error
	switch params.Name {
	case "list_links":
		status, body, err = s.client.listLinks(ctx)
	case "get_summary":
		var args struct {
			SubjectSessionID string `json:"subject_session_id"`
		}
		if unmarshalErr := json.Unmarshal(params.Arguments, &args); unmarshalErr != nil {
			return &rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: -32602, Message: "invalid arguments: " + unmarshalErr.Error()}}
		}
		status, body, err = s.client.getSummary(ctx, args.SubjectSessionID)
	case "get_turns":
		var args struct {
			SubjectSessionID string `json:"subject_session_id"`
			FromTurn         int    `json:"from_turn"`
			ToTurn           int    `json:"to_turn"`
		}
		if unmarshalErr := json.Unmarshal(params.Arguments, &args); unmarshalErr != nil {
			return &rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: -32602, Message: "invalid arguments: " + unmarshalErr.Error()}}
		}
		status, body, err = s.client.getTurns(ctx, args.SubjectSessionID, args.FromTurn, args.ToTurn)
	case "send_message":
		var args struct {
			LinkID string `json:"link_id"`
			Text   string `json:"text"`
			WaitMS int64  `json:"wait_ms,omitempty"`
		}
		if unmarshalErr := json.Unmarshal(params.Arguments, &args); unmarshalErr != nil {
			return &rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: -32602, Message: "invalid arguments: " + unmarshalErr.Error()}}
		}
		status, body, err = s.client.sendMessage(ctx, args.LinkID, args.Text, args.WaitMS)
	default:
		return &rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: -32602, Message: "unknown tool: " + params.Name}}
	}

	if err != nil {
		return &rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: -32000, Message: err.Error()}}
	}
	return &rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{
		"content": []map[string]string{{"type": "text", "text": string(body)}},
		"isError": status >= 400,
	}}
}
