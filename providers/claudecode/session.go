package claudecode

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

// startStream launches the structured transport: a persistent
// `claude -p --input-format stream-json --output-format stream-json`
// process. User messages go in as JSON lines; each turn's response streams
// back and closes with a result line.
func (p *Provider) startStream(_ context.Context, opts provider.StartOptions) (provider.Session, error) {
	args := []string{"-p", "--input-format", "stream-json", "--output-format", "stream-json", "--verbose"}
	if opts.Model != "" {
		args = append(args, "--model", opts.Model)
	}
	if opts.SystemPrompt != "" {
		args = append(args, "--append-system-prompt", opts.SystemPrompt)
	}
	if opts.Resume != "" {
		args = append(args, "--resume", opts.Resume)
	}
	args = append(args, disallowedToolsArgs(opts.Trust)...)
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
		return nil, fmt.Errorf("start %s: %w", p.cfg.Bin, err)
	}

	s := &streamSession{
		cmd:    cmd,
		stdin:  stdin,
		events: make(chan provider.Event, 256),
		ref:    opts.Resume,
	}
	go s.read(stdout)
	return s, nil
}

// streamSession owns one claude process on the structured transport.
type streamSession struct {
	cmd    *exec.Cmd
	events chan provider.Event

	mu          sync.Mutex
	stdin       io.WriteCloser
	ref         string
	model       string
	interrupted bool // an interrupt was requested this turn
	reqSeq      int
	closed      bool
}

// read is the sole owner of the events channel: it normalizes stdout lines
// into events and closes the channel when the process's stdout ends. A close
// without a preceding turn end is the process-died signal (contract in
// providers/provider).
func (s *streamSession) read(stdout io.Reader) {
	defer close(s.events)
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
	if m := l.model(); m != "" {
		s.model = m
	}
	model := s.model
	interrupted := s.interrupted
	if l.Type == "result" {
		s.interrupted = false
	}
	s.mu.Unlock()

	for _, ev := range l.events(interrupted) {
		if ev.Usage != nil && ev.Usage.Model == "" {
			ev.Usage.Model = model // learned from the init line
		}
		s.events <- ev
	}
}

func (s *streamSession) Ref() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ref
}

func (s *streamSession) Mode() provider.Mode { return provider.ModeACP }

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
	return nil
}
