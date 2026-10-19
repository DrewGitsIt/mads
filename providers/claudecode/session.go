package claudecode

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"strings"
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
	// With an asker wired, claude's permission prompts come to mads over
	// the host channel (hostprompt.go); without one claude refuses whatever
	// would have asked.
	args = append(args, trustArgs(opts.Trust, opts.Permissions != nil)...)
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
	permArgVals, permSettingsPath, err := permissionArgs(opts.PersonaAllowedPaths)
	if err != nil {
		if mcpConfigPath != "" {
			_ = os.Remove(mcpConfigPath)
		}
		return nil, fmt.Errorf("path-scope hook settings: %w", err)
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
		launchMCP:        map[string]provider.MCPServerRef{},
		controlWaiters:   map[string]chan controlReply{},
		asker:            opts.Permissions,
		asks:             map[string]context.CancelFunc{},
		hasAgentFile:     p.hasDelegateFile,
	}
	s.askCtx, s.askStop = context.WithCancel(context.Background())
	for _, srv := range opts.MCPServers {
		s.launchMCP[srv.Name] = srv
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
	// A resumed (or forked) process's total_cost_usd starts from what the
	// conversation already cost (restoredCostTotal), not from 0: without
	// this baseline the first turn end after every wake re-billed the
	// whole prior total. The new process appends its own cost-state only as
	// it ends, so reading after its start still sees the last one. On a fork, if the CLI doesn't carry the
	// original's total over, the first result reads below the baseline and
	// turnCostLocked takes it as a count that started over: all of it new.
	if opts.Resume != "" {
		s.costSeen = p.restoredCostTotal(opts.Resume)
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
	// compactedTo is the post_tokens of a compaction this turn ran, held
	// for the turn's result line: /compact's result counts no tokens, so
	// without it the turn end would report the conversation as empty.
	compactedTo int
	// costSeen is the last total_cost_usd a turn end accounted for. The CLI
	// reports that figure as a running total (a /compact turn that counts
	// no tokens still raises it), so each turn end carries the difference:
	// provider.Usage.CostUSD is one turn's cost. The total is the
	// conversation's, not the process's — a --resume restores it from the
	// transcript — so a resumed session starts from that restored figure
	// (restoredCostTotal), not 0. A suppressed in-turn notification result
	// leaves it alone, so what that continuation spent lands on the next
	// turn end instead of nowhere.
	costSeen float64
	reqSeq   int
	closed   bool
	// agentTasks records, per background task_id, whether it is a subagent
	// (wire.go isAgentTask). Only a task_started line says so, so the later
	// phases are kept or dropped by id. Never pruned: one entry per
	// background task, gone with the process.
	agentTasks map[string]bool
	// hasAgentFile answers for a task this process never saw start — one
	// an earlier process started, reported after a restart. A subagent
	// leaves its own transcript file; a background shell never does. nil
	// keeps such tasks (tests that build a bare streamSession).
	hasAgentFile func(ref, taskID string) bool
	// background is the running background processes that aren't
	// subagents, as EventBackground reports them: membership from the
	// latest roster (bgRoster), who started it from task_started (bgCalls,
	// which can arrive on either side of the roster that names the task;
	// never pruned, like agentTasks). A task is held back until its
	// task_started says whose it is — the roster alone can't tell a
	// subagent's process from the main agent's, and the line follows the
	// roster immediately. A subagent's calls (sideCalls: tool_use id →
	// spawning call, dropped once a task_started claims one) say which
	// subagent a process belongs to.
	background []provider.BackgroundTask
	bgRoster   []wireRosterTask
	bgCalls    map[string]bgCall
	sideCalls  map[string]string
	// windows is each model's context window as the latest result line's
	// modelUsage reports it, keyed by model id with any "[1m]"-style suffix
	// stripped: a subagent's own lines name its model but never its window.
	windows  map[string]int
	refFired bool // guards announced against a double-send

	// launchMCP holds the servers this process was started with
	// (--mcp-config), by name. mcp_set_servers manages a separate, "dynamic"
	// set on top of them, so SetMCPServers can add servers but can't take
	// these away or change them.
	launchMCP map[string]provider.MCPServerRef
	// controlWaiters routes a control_response to the call waiting on its
	// request_id.
	controlWaiters map[string]chan controlReply

	// asker answers claude's permission prompts (StartOptions.Permissions;
	// hostprompt.go). asks holds the cancel for each prompt still open,
	// by claude's request_id; askCtx parents them all, and askStop — run
	// when stdout ends or on Close — withdraws every one at once.
	asker   provider.PermissionAsker
	asks    map[string]context.CancelFunc
	askCtx  context.Context
	askStop context.CancelFunc
	// agentParents maps a subagent's agent_id to the Agent tool call it
	// runs under, learned from task_started lines (noteSubagent).
	agentParents map[string]string
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
	defer s.askStop() // the process is gone: no open prompt can be answered
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
	switch l.Type {
	case "control_response":
		s.routeControlResponse(raw)
		return
	case "control_request":
		s.handleControlRequest(raw)
		return
	case "control_cancel_request":
		s.cancelAsk(raw)
		return
	}
	s.mu.Lock()
	s.noteSubagent(&l)
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
	compactedTo := 0
	turnCost := 0.0
	switch {
	case l.Type == "system" && l.Subtype == "compact_boundary" && l.CompactMetadata != nil:
		s.compactedTo = l.CompactMetadata.PostTokens
	case l.isTaskNotificationResult():
		// Only an end for output this stream actually saw after the turn
		// closed. A continuation that produced nothing past the turn's end
		// (the capture in testdata/stream-task-subagent.jsonl: its result
		// lands right behind the canonical one) has nothing to close, and a
		// second end there would double-fire every per-turn consumer.
		closesUnprompted = !s.turnOpen && s.unprompted
		s.unprompted = false
		if closesUnprompted {
			turnCost = s.turnCostLocked(l.TotalCostUSD)
		}
	case l.Type == "result":
		turnCost = s.turnCostLocked(l.TotalCostUSD)
		for key, mu := range l.ModelUsage {
			if mu.ContextWindow > 0 {
				if s.windows == nil {
					s.windows = map[string]int{}
				}
				s.windows[baseModel(key)] = mu.ContextWindow
			}
		}
		s.interrupted = false
		s.turnOpen = false
		s.unprompted = false
		compactedTo, s.compactedTo = s.compactedTo, 0
	case l.Type == "assistant" && l.ParentToolUseID == "" && !s.turnOpen:
		s.unprompted = true
	}
	dropTask := s.isOtherTask(&l)
	background, bgChanged := s.noteBackground(&l, time.Now())
	subagentWindow := 0
	if u := l.Message.callUsage(); l.ParentToolUseID != "" && u != nil {
		subagentWindow = s.windows[baseModel(u.Model)]
	}
	s.mu.Unlock()

	evs := l.events(interrupted, true)
	if closesUnprompted {
		evs = l.notificationTurnEnd()
	}
	if dropTask {
		evs = nil
	}
	for _, ev := range evs {
		if ev.Kind == provider.EventSubagentUsage {
			ev.Usage.ContextWindow = subagentWindow
		} else if ev.Usage != nil {
			// A compaction's turn end: what it left behind is the occupancy.
			// A compaction inside a working turn is followed by more model
			// calls, whose own figure stands.
			if ev.Kind == provider.EventTurnEnd && compactedTo > 0 && l.Usage.countsNoTokens() {
				ev.Usage.ContextTokens = compactedTo
			}
			if ev.Kind == provider.EventTurnEnd {
				ev.Usage.CostUSD = turnCost
			}
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
	if bgChanged {
		s.events <- provider.Event{Kind: provider.EventBackground, Background: background}
	}
}

// turnCostLocked is the cost of the turn a result line reporting the
// running total closes (see costSeen), and advances costSeen to it. A line
// with no figure costs nothing and moves nothing; a total below the last
// one can only be a count that started over, all of it new. Caller holds
// s.mu.
func (s *streamSession) turnCostLocked(total float64) float64 {
	if total <= 0 {
		return 0
	}
	cost := total - s.costSeen
	if cost < 0 {
		cost = total
	}
	s.costSeen = total
	return cost
}

// bgCall is what a background task's task_started line says: the call that
// started it and, for a subagent's process, that subagent's spawning call.
type bgCall struct {
	toolUseID string
	parent    string
	owned     bool
}

// noteBackground folds a roster, a task_started or a subagent's assistant
// line into the running background processes, returning a copy of the list
// when it changed. A process's clock starts when it is first listed. Caller
// holds s.mu.
func (s *streamSession) noteBackground(l *wireLine, now time.Time) ([]provider.BackgroundTask, bool) {
	switch {
	case l.Type == "system" && l.Subtype == "background_tasks_changed":
		s.bgRoster = s.bgRoster[:0]
		for _, t := range l.Tasks {
			if t.TaskID != "" && !t.isAgent() {
				s.bgRoster = append(s.bgRoster, t)
			}
		}
	case l.Subtype == "task_started" && !l.isAgentTask() && l.ToolUseID != "":
		if s.bgCalls == nil {
			s.bgCalls = map[string]bgCall{}
		}
		c := bgCall{toolUseID: l.ToolUseID, owned: l.OwnedBySubagent}
		if c.owned {
			c.parent = s.sideCalls[l.ToolUseID]
		}
		delete(s.sideCalls, l.ToolUseID)
		s.bgCalls[l.TaskID] = c
	case l.Type == "assistant" && l.ParentToolUseID != "":
		for _, b := range l.Message.blocks() {
			if b.Type == "tool_use" && b.ID != "" {
				if s.sideCalls == nil {
					s.sideCalls = map[string]string{}
				}
				s.sideCalls[b.ID] = l.ParentToolUseID
			}
		}
		return nil, false
	default:
		return nil, false
	}

	var next []provider.BackgroundTask
	for _, t := range s.bgRoster {
		c, ok := s.bgCalls[t.TaskID]
		if !ok || (c.owned && c.parent == "") {
			// Not yet known whose it is; or a subagent's we can't place,
			// which is at least not the main agent's.
			continue
		}
		b := provider.BackgroundTask{
			TaskID: t.TaskID, ToolUseID: c.toolUseID, ParentToolUseID: c.parent, Description: t.Description, StartedAt: now,
		}
		if i := slices.IndexFunc(s.background, func(o provider.BackgroundTask) bool { return o.TaskID == t.TaskID }); i >= 0 {
			b.StartedAt = s.background[i].StartedAt
		}
		next = append(next, b)
	}
	if slices.Equal(next, s.background) {
		return nil, false
	}
	s.background = next
	return slices.Clone(next), true
}

// isOtherTask reports whether l is a task-lifecycle line for a background
// task that isn't a subagent — a run_in_background Bash call, a Monitor —
// learning the task_id's kind from its task_started line. Those lines never
// become EventTask: downstream every task is a subagent. A task with no
// task_started in this process was started by an earlier one: after a
// restart claude-code reports the background tasks it left behind
// ("Orphaned by a previous Claude Code process exit"), by id alone, so its
// subagent file decides. Caller holds s.mu.
func (s *streamSession) isOtherTask(l *wireLine) bool {
	if !l.isTaskLine() {
		return false
	}
	agent, seen := s.agentTasks[l.TaskID]
	switch {
	case l.Subtype == "task_started":
		agent = l.isAgentTask()
	case seen:
		return !agent
	case s.hasAgentFile == nil:
		return false
	default:
		agent = s.hasAgentFile(s.ref, l.TaskID)
	}
	if s.agentTasks == nil {
		s.agentTasks = map[string]bool{}
	}
	s.agentTasks[l.TaskID] = agent
	return !agent
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
// systemEvents), not via this call's return. Instructions ride after the
// command exactly as a user would type them ("/compact <instructions>"),
// claude's own custom-summarization argument.
func (s *streamSession) Compact(ctx context.Context, instructions string) error {
	if instructions = strings.TrimSpace(instructions); instructions != "" {
		return s.Send(ctx, "/compact "+instructions)
	}
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
// So is one that changes a launch server's config under the same name — a
// read-only orchestrate server gaining send_message differs only in env —
// and a non-stdio server, which this adapter never passes live.
func (s *streamSession) SetMCPServers(ctx context.Context, servers []provider.MCPServerRef) error {
	s.mu.Lock()
	launch := make(map[string]provider.MCPServerRef, len(s.launchMCP))
	for name, srv := range s.launchMCP {
		launch[name] = srv
	}
	s.mu.Unlock()

	dynamic := map[string]mcpServerJSON{}
	for _, srv := range servers {
		if was, ok := launch[srv.Name]; ok {
			if !sameMCPServer(was, srv) {
				return fmt.Errorf("%w: would change launch-time server %q", provider.ErrMCPHotSetUnsupported, srv.Name)
			}
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

// sameMCPServer reports whether a and b launch the same server: every
// field, env in order (callers build it deterministically).
func sameMCPServer(a, b provider.MCPServerRef) bool {
	return a.Name == b.Name && a.Command == b.Command && a.Type == b.Type && a.URL == b.URL &&
		slices.Equal(a.Env, b.Env)
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
	s.askStop()
	closeWait(s.cmd, 3*time.Second)
	if s.mcpConfigPath != "" {
		_ = os.Remove(s.mcpConfigPath)
	}
	if s.permSettingsPath != "" {
		_ = os.Remove(s.permSettingsPath)
	}
	return nil
}
