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
	if opts.Resume != "" {
		args = append(args, "--resume", opts.Resume)
	}
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
	permArgVals, permSettingsPath, err := permissionArgs(opts.Trust, opts.PermissionPromptURL)
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
	}
	if opts.Resume != "" {
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
	model       string
	interrupted bool // an interrupt was requested this turn
	reqSeq      int
	closed      bool
	refFired    bool // guards announced against a double-send
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
	s.mu.Lock()
	if l.SessionID != "" {
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
	if l.Type == "result" {
		s.interrupted = false
	}
	s.mu.Unlock()

	for _, ev := range l.events(interrupted, true) {
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
