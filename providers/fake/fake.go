// Package fake is provider-fake, the keystone test double (spec §15): a full
// provider implementation with deterministic, scripted behavior. Every
// downstream suite — pipeline engine, session service, router — exercises
// real provider-layer code paths against it instead of a live harness.
//
// Behavior comes from two layers: an optional Scenario (ordered rules —
// reply/chunks, delays, tool events, real file edits, crash on cue, rate
// limit on cue) and a built-in dialect that always backstops it:
//
//	"echo <x>"  → reply containing <x>
//	"slow"      → streams until interrupted
//	"recall"    → replies with markers echoed earlier in the conversation
//	"crash"     → dies mid-turn (event stream closes with no turn end)
//	"ratelimit" → the turn fails with a rate-limit error
//	anything    → "ok"
package fake

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/drewkidwell/mads/providers/provider"
)

// DefaultModels is the discovery list when the scenario doesn't override it.
var DefaultModels = []provider.Model{
	{ID: "fake-fast", Name: "Fake Fast", Default: true},
	{ID: "fake-smart", Name: "Fake Smart"},
}

type Fake struct {
	scenario Scenario

	mu        sync.Mutex
	seq       int
	histories map[string][]provider.Message
	// rateLimitHits counts matches per rule for RateLimitTimes. Keyed by
	// rule pointer: Scenario.Rules is never mutated/reallocated after
	// NewWithScenario constructs the Fake, so &sc.Rules[i] stays valid for
	// the Fake's lifetime.
	rateLimitHits map[*Rule]int
}

// New returns a fake running only the built-in dialect.
func New() *Fake {
	f, _ := NewWithScenario(Scenario{Name: "builtin"})
	return f
}

// NewWithScenario returns a fake scripted by sc (validated).
func NewWithScenario(sc Scenario) (*Fake, error) {
	if err := sc.Validate(); err != nil {
		return nil, err
	}
	return &Fake{scenario: sc, histories: map[string][]provider.Message{}, rateLimitHits: map[*Rule]int{}}, nil
}

func (f *Fake) Name() string { return "fake" }

func (f *Fake) Matrix() provider.Matrix {
	return provider.Matrix{
		provider.BehaviorInteractive:    provider.Native,
		provider.BehaviorInterrupt:      provider.Native,
		provider.BehaviorOneShot:        provider.Native,
		provider.BehaviorResume:         provider.Native,
		provider.BehaviorTranscript:     provider.Native,
		provider.BehaviorModelDiscovery: provider.Native,
		provider.BehaviorCostReporting:  provider.Native,
		provider.BehaviorACP:            provider.Native,
		provider.BehaviorPTY:            provider.Native,
		// Honest until the platform defines their interfaces; the fake
		// grows them alongside the first probe.
		provider.BehaviorBranching: provider.Unsupported,
		provider.BehaviorCwdReroot: provider.Unsupported,
	}
}

func (f *Fake) Start(_ context.Context, opts provider.StartOptions) (provider.Session, error) {
	if opts.Mode != provider.ModeACP && opts.Mode != provider.ModePTY {
		return nil, fmt.Errorf("mode %q: %w", opts.Mode, provider.ErrUnsupported)
	}
	f.mu.Lock()
	ref := opts.Resume
	if ref == "" {
		f.seq++
		ref = fmt.Sprintf("fake-%d", f.seq)
	} else if _, known := f.histories[ref]; !known {
		f.mu.Unlock()
		return nil, fmt.Errorf("resume: unknown session ref %q", ref)
	}
	f.mu.Unlock()

	s := &fakeSession{
		f: f, ref: ref, mode: opts.Mode, cwd: opts.Cwd,
		events:    make(chan provider.Event, 256),
		interrupt: make(chan struct{}, 1),
	}
	if opts.Mode == provider.ModePTY {
		s.ptyR, s.ptyW = io.Pipe()
	}
	return s, nil
}

func (f *Fake) OneShot(_ context.Context, req provider.OneShotRequest) (*provider.OneShotResult, error) {
	plan := f.plan(req.Prompt, nil)
	switch {
	case plan.rateLimit:
		return nil, &provider.RateLimitError{RetryAfter: time.Second, Message: "scripted rate limit"}
	case plan.crash:
		return nil, fmt.Errorf("fake agent process exited unexpectedly (scripted crash)")
	}
	for _, ts := range plan.tools {
		if err := executeTool(ts, req.Cwd); err != nil {
			return nil, err
		}
	}
	// Delay on cue holds for one-shots too — downstream timing tests rely
	// on scripted turns taking as long as the scenario says.
	if plan.delay > 0 {
		time.Sleep(plan.delay * time.Duration(len(plan.chunks)))
	}
	text := strings.Join(plan.chunks, "")
	return &provider.OneShotResult{Text: text, Usage: usage(req.Prompt, text)}, nil
}

func (f *Fake) Models(context.Context) ([]provider.Model, error) {
	if len(f.scenario.Models) > 0 {
		return append([]provider.Model{}, f.scenario.Models...), nil
	}
	return append([]provider.Model{}, DefaultModels...), nil
}

func (f *Fake) Transcript(_ context.Context, ref string) ([]provider.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	msgs, known := f.histories[ref]
	if !known {
		return nil, fmt.Errorf("unknown session ref %q", ref)
	}
	return append([]provider.Message{}, msgs...), nil
}

// turnPlan is one prompt's resolved behavior.
type turnPlan struct {
	chunks    []string
	delay     time.Duration
	tools     []ToolStep
	slow      bool
	crash     bool
	rateLimit bool
}

func (f *Fake) plan(prompt string, history []provider.Message) turnPlan {
	if r := f.scenario.match(prompt); r != nil {
		rateLimit := r.RateLimit
		if r.RateLimitTimes > 0 {
			f.mu.Lock()
			f.rateLimitHits[r]++
			hit := f.rateLimitHits[r]
			f.mu.Unlock()
			rateLimit = hit <= r.RateLimitTimes
		}
		p := turnPlan{
			chunks: r.Chunks, delay: time.Duration(r.DelayMs) * time.Millisecond,
			tools: r.Tools, crash: r.Crash, rateLimit: rateLimit,
		}
		if len(p.chunks) == 0 && r.Reply != "" {
			p.chunks = []string{r.Reply}
		}
		return p
	}
	switch {
	case strings.HasPrefix(prompt, "echo "):
		return turnPlan{chunks: []string{"you said " + strings.TrimPrefix(prompt, "echo ")}}
	case prompt == "slow":
		return turnPlan{slow: true}
	case prompt == "recall":
		var parts []string
		for _, m := range history {
			if m.Role == "user" && strings.HasPrefix(m.Text, "echo ") {
				parts = append(parts, strings.TrimPrefix(m.Text, "echo "))
			}
		}
		return turnPlan{chunks: []string{"earlier you said: " + strings.Join(parts, ", ")}}
	case prompt == "crash":
		return turnPlan{crash: true}
	case prompt == "ratelimit":
		return turnPlan{rateLimit: true}
	default:
		return turnPlan{chunks: []string{"ok"}}
	}
}

func (f *Fake) record(ref string, msg provider.Message) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.histories[ref] = append(f.histories[ref], msg)
}

func (f *Fake) history(ref string) []provider.Message {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]provider.Message{}, f.histories[ref]...)
}

// executeTool really performs a scripted file edit so downstream
// file-tracking tests observe genuine changes.
func executeTool(ts ToolStep, cwd string) error {
	if ts.Content == "" || ts.Path == "" {
		return nil
	}
	if cwd == "" {
		return fmt.Errorf("scripted tool %q writes %s but the session has no cwd", ts.Name, ts.Path)
	}
	dst := filepath.Join(cwd, ts.Path)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	return os.WriteFile(dst, []byte(ts.Content), 0o644)
}

func toolEvent(ts ToolStep) provider.Event {
	input := json.RawMessage(`{}`)
	if ts.Input != "" {
		input = json.RawMessage(ts.Input)
	}
	return provider.Event{Kind: provider.EventToolUse, Tool: &provider.ToolUse{
		Name: ts.Name, Input: input, Path: ts.Path,
	}}
}

// usage is deterministic token accounting: ~4 chars per token, never zero.
func usage(prompt, reply string) provider.Usage {
	return provider.Usage{
		InputTokens:  len(prompt)/4 + 1,
		OutputTokens: len(reply)/4 + 1,
		Model:        "fake-fast",
	}
}

type fakeSession struct {
	f         *Fake
	ref       string
	mode      provider.Mode
	cwd       string
	events    chan provider.Event
	interrupt chan struct{}
	ptyR      *io.PipeReader
	ptyW      *io.PipeWriter

	mu     sync.Mutex
	closed bool
}

func (s *fakeSession) Ref() string                   { return s.ref }
func (s *fakeSession) Mode() provider.Mode           { return s.mode }
func (s *fakeSession) Events() <-chan provider.Event { return s.events }
func (s *fakeSession) Terminal() io.ReadWriter       { return terminal{s.ptyR, io.Discard} }
func (s *fakeSession) Resize(int, int) error         { return nil }

func (s *fakeSession) Interrupt(context.Context) error {
	select {
	case s.interrupt <- struct{}{}:
	default:
	}
	return nil
}

func (s *fakeSession) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.die()
	return nil
}

// die closes the event stream; callers hold s.mu.
func (s *fakeSession) die() {
	if !s.closed {
		s.closed = true
		close(s.events)
		if s.ptyW != nil {
			_ = s.ptyW.Close()
		}
	}
}

func (s *fakeSession) Send(_ context.Context, text string) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return fmt.Errorf("session %s is closed", s.ref)
	}
	s.mu.Unlock()

	history := s.f.history(s.ref)
	s.f.record(s.ref, provider.Message{Role: "user", Text: text})
	go s.respond(text, s.f.plan(text, history))
	return nil
}

func (s *fakeSession) respond(prompt string, plan turnPlan) {
	if plan.rateLimit {
		rle := provider.RateLimitError{RetryAfter: time.Second, Message: "scripted rate limit"}
		s.emit(provider.Event{Kind: provider.EventError, Text: rle.Error()})
		s.emit(provider.Event{Kind: provider.EventTurnEnd})
		return
	}
	for _, ts := range plan.tools {
		if err := executeTool(ts, s.cwd); err != nil {
			s.emit(provider.Event{Kind: provider.EventError, Text: err.Error()})
			s.emit(provider.Event{Kind: provider.EventTurnEnd})
			return
		}
		ev := toolEvent(ts)
		s.f.record(s.ref, provider.Message{Role: "tool", Tool: ev.Tool})
		if !s.emit(ev) {
			return
		}
	}
	if plan.slow {
		for i := 0; ; i++ {
			select {
			case <-s.interrupt:
				s.emit(provider.Event{Kind: provider.EventTurnEnd, Interrupted: true})
				return
			case <-time.After(5 * time.Millisecond):
				if !s.emit(provider.Event{Kind: provider.EventText, Text: fmt.Sprintf("tick %d ", i)}) {
					return
				}
			}
		}
	}

	var streamed strings.Builder
	for _, chunk := range plan.chunks {
		if plan.delay > 0 {
			select {
			case <-s.interrupt:
				s.finish(prompt, streamed.String(), true)
				return
			case <-time.After(plan.delay):
			}
		} else {
			select {
			case <-s.interrupt:
				s.finish(prompt, streamed.String(), true)
				return
			default:
			}
		}
		if !s.emit(provider.Event{Kind: provider.EventText, Text: chunk}) {
			return
		}
		streamed.WriteString(chunk)
		if s.ptyW != nil {
			_, _ = s.ptyW.Write([]byte(chunk))
		}
	}
	if plan.crash {
		s.mu.Lock()
		s.die() // no turn end: the process-died signal
		s.mu.Unlock()
		return
	}
	s.finish(prompt, streamed.String(), false)
}

// finish records the reply and closes the turn.
func (s *fakeSession) finish(prompt, reply string, interrupted bool) {
	s.f.record(s.ref, provider.Message{Role: "agent", Text: reply})
	u := usage(prompt, reply)
	s.emit(provider.Event{Kind: provider.EventTurnEnd, Usage: &u, Interrupted: interrupted})
	if s.ptyW != nil {
		_, _ = s.ptyW.Write([]byte("\r\n> "))
	}
}

// emit delivers an event unless the session is closed; false stops the
// responder so nothing outlives Close.
func (s *fakeSession) emit(ev provider.Event) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false
	}
	select {
	case s.events <- ev:
	default:
	}
	return true
}

type terminal struct {
	io.Reader
	io.Writer
}
