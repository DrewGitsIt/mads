package kirocli

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"time"

	"github.com/drewkidwell/mads/providers/provider"
)

// startACP launches kiro-cli's structured transport: `kiro-cli acp`, a
// standards-compliant JSON-RPC 2.0 agent over stdio. Unlike claude-code's
// fire-and-forget stream-json, this is genuine bidirectional RPC — requests
// need a correlated response, so streamSession carries a pending-request
// table alongside the single read loop.
func (p *Provider) startACP(ctx context.Context, opts provider.StartOptions) (provider.Session, error) {
	args := append([]string{"acp"}, p.cfg.ExtraArgs...)
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

	s := &streamSession{
		cmd:     cmd,
		stdin:   stdin,
		events:  make(chan provider.Event, 256),
		pending: make(map[int]chan rpcMessage),
	}
	go s.read(stdout)

	if _, err := s.request(ctx, "initialize", map[string]any{"protocolVersion": 1}); err != nil {
		_ = s.Close()
		return nil, fmt.Errorf("kiro-cli acp initialize: %w", err)
	}

	if opts.Resume != "" {
		s.mu.Lock()
		s.replaying = true
		s.mu.Unlock()
		_, err := s.request(ctx, "session/load", loadSessionParams{SessionID: opts.Resume, Cwd: opts.Cwd})
		s.mu.Lock()
		s.replaying = false
		s.ref = opts.Resume
		s.mu.Unlock()
		if err != nil {
			_ = s.Close()
			return nil, fmt.Errorf("kiro-cli acp session/load: %w", err)
		}
		return s, nil
	}

	resp, err := s.request(ctx, "session/new", newSessionParams{Cwd: opts.Cwd, Model: opts.Model})
	if err != nil {
		_ = s.Close()
		return nil, fmt.Errorf("kiro-cli acp session/new: %w", err)
	}
	var result newSessionResult
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		_ = s.Close()
		return nil, fmt.Errorf("kiro-cli acp session/new result: %w", err)
	}
	s.mu.Lock()
	s.ref = result.SessionID
	s.mu.Unlock()
	return s, nil
}

// streamSession owns one kiro-cli acp process. Requests it sends
// (initialize, session/new, session/load, session/prompt) are correlated to
// their responses via pending; session/update notifications keep flowing on
// the same read loop while a request is in flight.
type streamSession struct {
	cmd    *exec.Cmd
	events chan provider.Event

	mu          sync.Mutex
	stdin       io.WriteCloser
	nextID      int
	pending     map[int]chan rpcMessage
	ref         string
	interrupted bool // an interrupt was requested this turn
	replaying   bool // session/load is in flight: buffer updates, don't stream them
	replay      []provider.Message
	closed      bool
}

// read is the sole owner of the events channel: it decodes stdout lines and
// closes the channel when the process's stdout ends. A close without a
// preceding turn end is the process-died signal (contract in
// providers/provider). Any request still awaiting a response when that
// happens is unblocked (channel closed, not sent to) so its caller can tell
// the difference from a real answer.
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
	case m.Method == "" && m.ID != nil: // a response to one of our requests
		s.mu.Lock()
		ch, ok := s.pending[*m.ID]
		if ok {
			delete(s.pending, *m.ID)
		}
		s.mu.Unlock()
		if ok {
			ch <- m
		}
	case m.Method == "session/request_permission" && m.ID != nil:
		// Real ACP tool-permission prompt. Out of scope for this pass to
		// surface as a platform prompt (README) — auto-approve so a
		// scripted or real turn never blocks on it.
		s.autoApprove(*m.ID)
	case m.Method == "session/update":
		s.handleUpdate(m.Params)
	}
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
		if u.Content == nil || u.Content.Text == "" {
			return
		}
		if replaying {
			s.appendReplay(provider.Message{Role: "agent", Text: u.Content.Text})
			return
		}
		s.events <- provider.Event{Kind: provider.EventText, Text: u.Content.Text}
	case "user_message_chunk":
		if !replaying || u.Content == nil || u.Content.Text == "" {
			return // live echoes of our own prompt aren't surfaced as events
		}
		s.appendReplay(provider.Message{Role: "user", Text: u.Content.Text})
	case "tool_call":
		tool := u.ToolCall.toolUse()
		if tool == nil {
			return
		}
		if replaying {
			s.appendReplay(provider.Message{Role: "tool", Tool: tool})
			return
		}
		s.events <- provider.Event{Kind: provider.EventToolUse, Tool: tool}
	}
}

func (s *streamSession) appendReplay(msg provider.Message) {
	s.mu.Lock()
	s.replay = append(s.replay, msg)
	s.mu.Unlock()
}

// drainReplay returns the messages buffered by a session/load replay —
// Transcript's data source.
func (s *streamSession) drainReplay() []provider.Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.replay
}

func (s *streamSession) autoApprove(id int) {
	result, _ := json.Marshal(permissionResult{Outcome: permissionOutcome{Outcome: "selected", OptionID: "allow"}})
	line, err := json.Marshal(rpcMessage{JSONRPC: "2.0", ID: &id, Result: result})
	if err != nil {
		return
	}
	_ = s.write(append(line, '\n'))
}

// beginRequest allocates a request id, registers its response channel, and
// writes the request line. The caller decides whether to wait synchronously
// (request) or asynchronously (Send, so a slow turn's mid-stream events can
// be observed before the response arrives).
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

	line, err := json.Marshal(rpcMessage{JSONRPC: "2.0", ID: &id, Method: method, Params: raw})
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

// request sends a request and blocks for its response.
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
// response is what signals EventTurnEnd (unlike claude-code, where a
// "result" line plays that role). It must not block waiting for that
// response — a slow turn's session/update chunks need to reach Events()
// while the prompt is still in flight — so the wait happens in a goroutine.
func (s *streamSession) Send(_ context.Context, text string) error {
	ref := s.Ref()
	ch, err := s.beginRequest("session/prompt", promptParams{
		SessionID: ref,
		Prompt:    []promptBlock{{Type: "text", Text: text}},
	})
	if err != nil {
		return err
	}
	go s.awaitTurnEnd(ch)
	return nil
}

func (s *streamSession) awaitTurnEnd(ch chan rpcMessage) {
	resp, ok := <-ch
	if !ok {
		return // session died mid-turn; read()'s defer is closing events too
	}
	s.mu.Lock()
	interrupted := s.interrupted
	s.interrupted = false
	s.mu.Unlock()

	if resp.Error != nil {
		s.events <- provider.Event{Kind: provider.EventError, Text: resp.Error.Message}
		s.events <- provider.Event{Kind: provider.EventTurnEnd, Interrupted: interrupted}
		return
	}
	var pr promptResult
	_ = json.Unmarshal(resp.Result, &pr)
	if pr.StopReason == "cancelled" {
		interrupted = true
	}
	s.events <- provider.Event{Kind: provider.EventTurnEnd, Interrupted: interrupted}
}

// Interrupt asks the agent to stop the current response via session/cancel
// — the standard ACP cancel notification, no response expected. The turn
// still closes through its session/prompt response, same as Send always
// waits for.
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

// Close ends the session: stdin EOF asks the process to exit cleanly; a
// laggard is killed.
func (s *streamSession) Close() error {
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
