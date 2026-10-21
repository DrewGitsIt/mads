package api

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/drewkidwell/mads/providers/fake"
	"github.com/drewkidwell/mads/providers/provider"
)

// scriptProvider plays a fixed event sequence per prompt, the way a real
// adapter would stream it: the tests below feed it claude-, codex-, fake-
// and kiro-shaped sequences and look at what /output makes of them. Its
// store (Transcript) holds whatever the test puts there.
type scriptProvider struct {
	name    string
	matrix  provider.Matrix
	scripts map[string][]provider.Event
	// strip, when set, drops events of these kinds before they stream.
	strip map[provider.EventKind]bool

	mu    sync.Mutex
	sess  *scriptSession
	opts  provider.StartOptions
	rows  []provider.Message
	sends []string
}

func newScriptProvider(name string, scripts map[string][]provider.Event) *scriptProvider {
	return &scriptProvider{name: name, matrix: fake.New().Matrix(), scripts: scripts}
}

func (p *scriptProvider) Name() string            { return p.name }
func (p *scriptProvider) Matrix() provider.Matrix { return p.matrix }
func (p *scriptProvider) OneShot(context.Context, provider.OneShotRequest) (*provider.OneShotResult, error) {
	return nil, provider.ErrUnsupported
}

func (p *scriptProvider) Start(_ context.Context, opts provider.StartOptions) (provider.Session, error) {
	s := &scriptSession{p: p, events: make(chan provider.Event, 1024)}
	p.mu.Lock()
	p.sess, p.opts = s, opts
	p.mu.Unlock()
	return s, nil
}

func (p *scriptProvider) Transcript(context.Context, string) ([]provider.Message, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]provider.Message(nil), p.rows...), nil
}

func (p *scriptProvider) store(rows ...provider.Message) {
	p.mu.Lock()
	p.rows = append(p.rows, rows...)
	p.mu.Unlock()
}

func (p *scriptProvider) session() *scriptSession {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.sess
}

func (p *scriptProvider) startOptions() provider.StartOptions {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.opts
}

type scriptSession struct {
	p      *scriptProvider
	mu     sync.Mutex
	events chan provider.Event
	closed bool
}

func (*scriptSession) Ref() string                     { return "script-1" }
func (*scriptSession) Mode() provider.Mode             { return provider.ModeACP }
func (s *scriptSession) Events() <-chan provider.Event { return s.events }
func (*scriptSession) Interrupt(context.Context) error { return nil }

func (s *scriptSession) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.closed {
		s.closed = true
		close(s.events)
	}
	return nil
}

// Send plays the prompt's script, if it has one; a prompt with none
// leaves the turn open for the test to drive with push.
func (s *scriptSession) Send(_ context.Context, text string) error {
	s.p.mu.Lock()
	s.p.sends = append(s.p.sends, text)
	evs := s.p.scripts[text]
	strip := s.p.strip
	s.p.mu.Unlock()
	for _, ev := range evs {
		if strip[ev.Kind] {
			continue
		}
		s.push(ev)
	}
	return nil
}

func (s *scriptSession) push(ev provider.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.closed {
		s.events <- ev
	}
}

// die closes the stream as a crashed harness process would.
func (s *scriptSession) die() { _ = s.Close() }

func rawJSON(s string) json.RawMessage { return json.RawMessage(s) }
