package claudecode

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/drewkidwell/mads/providers/provider"
)

// startStream launches the structured transport: a persistent
// `claude -p --input-format stream-json --output-format stream-json`
// process. User messages go in as JSON lines; each turn's response streams
// back and closes with a result line.
func (p *Provider) startStream(_ context.Context, opts provider.StartOptions) (provider.Session, error) {
	// --include-partial-messages surfaces token-level content_block_delta lines
	// so the UI streams text as it arrives instead of one whole message block
	// per turn (see wire.go's events/streamDeltaEvents).
	args := []string{"-p", "--input-format", "stream-json", "--output-format", "stream-json", "--verbose", "--include-partial-messages"}
	if opts.Model != "" {
		args = append(args, "--model", opts.Model)
	}
	if opts.SystemPrompt != "" {
		args = append(args, "--append-system-prompt", opts.SystemPrompt)
	}
	resume, forkID := resumeArgs(opts)
	args = append(args, resume...)
	args = append(args, trustArgs(opts.Trust)...)
	// Persona-level allow/deny (agent-concept redesign §2): merged on
	// top of trustArgs's native set. personaDisallowedTools returns ""
	// when the persona's policy doesn't add anything trustArgs didn't
	// already block — so a zero-value TrustFull+no-persona session is
	// byte-for-byte identical to today's behavior (no --disallowedTools
	// at all).
	if extra := personaDisallowedTools(opts); extra != "" {
		args = append(args, "--disallowedTools", extra)
	}
	mcpArgVals, mcpConfigPath, err := mcpArgs(opts.MCPServers)
	if err != nil {
		return nil, fmt.Errorf("mcp config: %w", err)
	}
	args = append(args, mcpArgVals...)
	permArgVals, permSettingsPath, err := permissionArgs(opts.Trust, opts.PermissionPromptURL, opts.PersonaAllowedPaths)
	if err != nil {
		if mcpConfigPath != "" {
			_ = os.Remove(mcpConfigPath)
		}
		return nil, fmt.Errorf("permission hook settings: %w", err)
	}
	args = append(args, permArgVals...)
	args = append(args, p.cfg.ExtraArgs...)

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
		if mcpConfigPath != "" {
			_ = os.Remove(mcpConfigPath)
		}
		if permSettingsPath != "" {
			_ = os.Remove(permSettingsPath)
		}
		return nil, fmt.Errorf("start %s: %w", p.cfg.Bin, err)
	}

	s := &streamSession{
		cmd:              cmd,
		stdin:            stdin,
		events:           make(chan provider.Event, 256),
		ref:              opts.Resume,
		mcpConfigPath:    mcpConfigPath,
		permSettingsPath: permSettingsPath,
		announced:        make(chan string, 1),
		launchMCP:        map[string]bool{},
		controlWaiters:   map[string]chan controlReply{},
	}
	for _, srv := range opts.MCPServers {
		s.launchMCP[srv.Name] = true
	}
	switch {
	case forkID != "":
		// A fork's ref is the new id, which exists only once claude
		// confirms it on the wire (handleLine) — announcing Resume here
		// would hand the Runner the original to persist and then append to.
		s.ref = ""
		s.forkFrom = opts.Resume
	case opts.Resume != "":
		s.refFired = true
		s.announced <- opts.Resume
	}
	go s.read(stdout)
	return s, nil
}

// streamSession owns one claude process on the structured transport.
type streamSession struct {
	cmd              *exec.Cmd
	events           chan provider.Event
	mcpConfigPath    string
	permSettingsPath string
	// announced delivers ref exactly once — on Start for a resumed session,
	// or from handleLine the moment the wire's system/init line carries a
	// session_id (claude-code announces this at connect, before any turn:
	// see fakeclaude_test.go's fcStream) — and closes with nothing sent if
	// the process dies before that ever happens.
	announced chan string

	mu          sync.Mutex
	stdin       io.WriteCloser
	ref         string
	forkFrom    string // original's id on a fork: never adopted as ref
	model       string
	interrupted bool // an interrupt was requested this turn
	// turnOpen is set from Send until the turn's own (origin-less) result
	// line; unprompted records main-agent output seen while no turn was
	// open. Together they tell handleLine what a task-notification result
	// closes. Inside a turn it closes an internal continuation and must stay
	// silent (see wire.go). After the turn it may close an unprompted turn
	// the CLI ran by itself, when a background task finished late. That
	// turn's tool and thinking events had already set the session
	// "working", and before this nothing ever set it back: the session sat
	// on "Working…" with a climbing counter, 2026-09-30.
	turnOpen   bool
	unprompted bool
	reqSeq     int
	closed     bool
	refFired   bool // guards announced against a double-send

	// launchMCP names the servers this process was started with
	// (--mcp-config). mcp_set_servers manages a separate, "dynamic" set on
	// top of them, so SetMCPServers can add servers but can't take these
	// away.
	launchMCP map[string]bool
	// controlWaiters routes a control_response to the call waiting on its
	// request_id.
	controlWaiters map[string]chan controlReply
}

// controlReply is the part of a control_response line a waiter needs.
type controlReply struct {
	Subtype   string          `json:"subtype"`
	RequestID string          `json:"request_id"`
	Error     string          `json:"error"`
	Response  json.RawMessage `json:"response"`
}

// read is the sole owner of the events channel: it normalizes stdout lines
// into events and closes the channel when the process's stdout ends. A close
// without a preceding turn end is the process-died signal (contract in
// providers/provider).
func (s *streamSession) read(stdout io.Reader) {
	defer close(s.events)
	defer func() {
		s.mu.Lock()
		if !s.refFired {
			s.refFired = true
			close(s.announced)
		}
		s.mu.Unlock()
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
	var l wireLine
	if err := json.Unmarshal(raw, &l); err != nil {
		return // not a protocol line; claude prints nothing else on stdout, but stay tolerant
	}
	if l.Type == "control_response" {
		s.routeControlResponse(raw)
		return
	}
	s.mu.Lock()
	if l.SessionID != "" && l.SessionID != s.forkFrom {
		s.ref = l.SessionID
	}
	if !s.refFired && s.ref != "" {
		s.refFired = true
		s.announced <- s.ref
	}
	if m := l.model(); m != "" {
		s.model = m
	}
	model := s.model
	interrupted := s.interrupted
	closesUnprompted := false
	switch {
	case l.isTaskNotificationResult():
		// Only an end for output this stream actually saw after the turn
		// closed. A continuation that produced nothing past the turn's end
		// (the capture in testdata/stream-task-subagent.jsonl: its result
		// lands right behind the canonical one) has nothing to close, and a
		// second end there would double-fire every per-turn consumer.
		closesUnprompted = !s.turnOpen && s.unprompted
		s.unprompted = false
	case l.Type == "result":
		s.interrupted = false
		s.turnOpen = false
		s.unprompted = false
	case l.Type == "assistant" && l.ParentToolUseID == "" && !s.turnOpen:
		s.unprompted = true
	}
	s.mu.Unlock()

	evs := l.events(interrupted, true)
	if closesUnprompted {
		evs = l.notificationTurnEnd()
	}
	for _, ev := range evs {
		if ev.Usage != nil {
			if ev.Usage.Model == "" {
				ev.Usage.Model = model // learned from the init line
			}
			// events() resolves the window from the line alone; a result line
			// whose modelUsage lists several models needs the init-line model
			// to pick the right (possibly "[1m]"-suffixed) entry.
			if ev.Usage.ContextWindow == 0 {
				ev.Usage.ContextWindow = l.contextWindowFor(ev.Usage.Model)
			}
		}
		s.events <- ev
	}
}

func (s *streamSession) Ref() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ref
}

// SetRef implements provider.RefSetter: the Runner's SessionLocator backstop
// (locate.go) calls this for the rare case the wire's system/init line never
// carries a session_id at all, so handleLine never learns s.ref on its own.
// Guarded the same way handleLine's own ref learning is — never overwrite a
// ref this session already knows, whether from opts.Resume or a session_id
// that did arrive on the wire after all (a race between the wire and the
// backstop, wire wins since it's authoritative).
func (s *streamSession) SetRef(ref string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ref == "" {
		s.ref = ref
	}
}

func (s *streamSession) Mode() provider.Mode { return provider.ModeACP }

// RefAnnounced implements provider.RefAnnouncer — see announced's doc comment.
func (s *streamSession) RefAnnounced() <-chan string { return s.announced }

func (s *streamSession) Events() <-chan provider.Event { return s.events }

func (s *streamSession) Send(_ context.Context, text string) error {
	line, err := json.Marshal(map[string]any{
		"type": "user",
		"message": map[string]any{
			"role":    "user",
			"content": []map[string]any{{"type": "text", "text": text}},
		},
	})
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.turnOpen = true
	s.mu.Unlock()
	return s.write(append(line, '\n'))
}

// Compact implements provider.Compactor: sends the native "/compact" slash
// command as an ordinary user turn over stdin. Verified empirically against
// claude-code 2.1.220 (a throwaway -p --input-format stream-json probe) that
// this actually triggers a real compaction pass — the CLI has no separate
// "command" wire shape for slash commands, so the same Send path a normal
// message uses is all this needs. Progress and outcome arrive as ordinary
// "system"/"status" lines on the same Events() stream (see wire.go's
// systemEvents), not via this call's return.
func (s *streamSession) Compact(ctx context.Context) error {
	return s.Send(ctx, "/compact")
}

// Interrupt asks the harness to stop the current response via a stream-json
// control request; the turn still closes with its result line.
func (s *streamSession) Interrupt(_ context.Context) error {
	s.mu.Lock()
	s.reqSeq++
	seq := s.reqSeq
	s.interrupted = true
	s.mu.Unlock()
	line, err := json.Marshal(map[string]any{
		"type":       "control_request",
		"request_id": fmt.Sprintf("mads-%d", seq),
		"request":    map[string]any{"subtype": "interrupt"},
	})
	if err != nil {
		return err
	}
	return s.write(append(line, '\n'))
}

// SetMCPServers implements provider.MCPHotSetter over the stream-json
// control channel's mcp_set_servers request, which replaces the process's
// dynamic MCP server set. claude connects the new servers right away and the
// model sees their tools from the next turn, on the same conversation.
// Verified against claude-code 2.1.283, mid-session: tools absent on turn 1,
// set, present on turn 2 with the same session id (docs/research/
// claude-mcp-hot-mount.md). Servers the process was launched with aren't in
// the dynamic set, so a change that drops one is ErrMCPHotSetUnsupported.
// So is a non-stdio server, which this adapter never passes live.
func (s *streamSession) SetMCPServers(ctx context.Context, servers []provider.MCPServerRef) error {
	s.mu.Lock()
	launch := make(map[string]bool, len(s.launchMCP))
	for name := range s.launchMCP {
		launch[name] = true
	}
	s.mu.Unlock()

	dynamic := map[string]mcpServerJSON{}
	for _, srv := range servers {
		if launch[srv.Name] {
			delete(launch, srv.Name)
			continue
		}
		if srv.Type != "" && srv.Type != "stdio" {
			return fmt.Errorf("%w: server %q is %s", provider.ErrMCPHotSetUnsupported, srv.Name, srv.Type)
		}
		entry, err := mcpServerEntry(srv)
		if err != nil {
			return err
		}
		entry.Type = "stdio"
		dynamic[srv.Name] = entry
	}
	if len(launch) > 0 {
		return fmt.Errorf("%w: would drop a launch-time server", provider.ErrMCPHotSetUnsupported)
	}

	reply, err := s.control(ctx, map[string]any{"subtype": "mcp_set_servers", "servers": dynamic})
	if err != nil {
		return err
	}
	var result struct {
		Errors map[string]string `json:"errors"`
	}
	_ = json.Unmarshal(reply.Response, &result)
	if len(result.Errors) > 0 {
		return fmt.Errorf("claude mcp_set_servers: %v", result.Errors)
	}
	return nil
}

// control sends one control_request and waits for its control_response.
func (s *streamSession) control(ctx context.Context, request map[string]any) (controlReply, error) {
	s.mu.Lock()
	s.reqSeq++
	id := fmt.Sprintf("mads-%d", s.reqSeq)
	ch := make(chan controlReply, 1)
	s.controlWaiters[id] = ch
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.controlWaiters, id)
		s.mu.Unlock()
	}()

	line, err := json.Marshal(map[string]any{"type": "control_request", "request_id": id, "request": request})
	if err != nil {
		return controlReply{}, err
	}
	if err := s.write(append(line, '\n')); err != nil {
		return controlReply{}, err
	}
	select {
	case reply := <-ch:
		if reply.Subtype != "success" {
			detail := reply.Error
			if detail == "" {
				detail = reply.Subtype
			}
			return reply, fmt.Errorf("claude %v: %s", request["subtype"], detail)
		}
		return reply, nil
	case <-ctx.Done():
		return controlReply{}, fmt.Errorf("claude %v: %w", request["subtype"], ctx.Err())
	}
}

// routeControlResponse hands a control_response line to whoever is waiting
// on its request_id. Responses nobody waits for (an interrupt's) are
// dropped.
func (s *streamSession) routeControlResponse(raw []byte) {
	var line struct {
		Response controlReply `json:"response"`
	}
	if json.Unmarshal(raw, &line) != nil {
		return
	}
	s.mu.Lock()
	ch := s.controlWaiters[line.Response.RequestID]
	s.mu.Unlock()
	if ch != nil {
		ch <- line.Response
	}
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

// Close ends the session: stdin EOF asks the process to exit cleanly (which
// flushes its transcript); a laggard is killed.
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
	if s.mcpConfigPath != "" {
		_ = os.Remove(s.mcpConfigPath)
	}
	if s.permSettingsPath != "" {
		_ = os.Remove(s.permSettingsPath)
	}
	return nil
}
