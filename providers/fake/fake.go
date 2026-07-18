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
//	"echo slowly <x>" → same reply as "echo <x>", streamed as separate word
//	                    chunks with a 250ms delay between them — the
//	                    observable pacing lever ui-smoke's streaming
//	                    journey needs
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
	// liveRefs tracks which refs are currently attached to an un-closed
	// fakeSession, so Start can reject a second attachment to the same ref
	// the way a real harness (e.g. claude-code) refuses double-attaching to
	// a live session — the guarantee SwitchTransport's hard-sequencing
	// (old process fully dead before the new one starts) depends on.
	liveRefs map[string]bool
	// mcpServers records each Start call's opts.MCPServers by ref, mirroring
	// how trust is observable via fakeSession — lets tests assert on
	// materialization without a real subprocess round-trip.
	mcpServers map[string][]provider.MCPServerRef
	// starts records every Start call's StartOptions, in order — lets tests
	// assert what Resume value a switch's Start call actually received (e.g.
	// SwitchTransport resuming the provider-announced ref), not just infer
	// it from the resulting session's Ref().
	starts []provider.StartOptions
}

// StartCalls returns every Start call's StartOptions, in order.
func (f *Fake) StartCalls() []provider.StartOptions {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]provider.StartOptions{}, f.starts...)
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
	return &Fake{scenario: sc, histories: map[string][]provider.Message{}, rateLimitHits: map[*Rule]int{}, liveRefs: map[string]bool{}, mcpServers: map[string][]provider.MCPServerRef{}}, nil
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
		provider.BehaviorBranching:     provider.Unsupported,
		provider.BehaviorCwdReroot:     provider.Unsupported,
		provider.BehaviorTrustLimited:  provider.Native,
		provider.BehaviorTrustReadOnly: provider.Native,
		// The fake already fully records/exposes it via MCPServers(ref).
		provider.BehaviorMCPServers: provider.Native,
		// OneShot streams scripted chunks with their scripted delay.
		provider.BehaviorOneShotStream: provider.Native,
		// The fake implements RefAnnouncer and scripts timing via
		// Scenario.RefAnnounce for conformance/runner test coverage.
		provider.BehaviorRefAnnounce: provider.Native,
	}
}

func (f *Fake) Start(_ context.Context, opts provider.StartOptions) (provider.Session, error) {
	if opts.Mode != provider.ModeACP && opts.Mode != provider.ModePTY {
		return nil, fmt.Errorf("mode %q: %w", opts.Mode, provider.ErrUnsupported)
	}
	f.mu.Lock()
	f.starts = append(f.starts, opts)
	ref := opts.Resume
	if ref == "" {
		f.seq++
		ref = fmt.Sprintf("fake-%d", f.seq)
		// Register the ref as known immediately, even with no messages
		// sent yet — a session resumed (e.g. by a transport switch) right
		// after Start, before any turn, must find its ref valid.
		if _, exists := f.histories[ref]; !exists {
			f.histories[ref] = []provider.Message{}
		}
	} else if _, known := f.histories[ref]; !known {
		f.mu.Unlock()
		return nil, fmt.Errorf("resume: unknown session ref %q", ref)
	}
	if f.liveRefs[ref] {
		f.mu.Unlock()
		return nil, fmt.Errorf("start: session ref %q already attached", ref)
	}
	f.liveRefs[ref] = true
	f.mcpServers[ref] = opts.MCPServers
	f.mu.Unlock()

	trust := opts.Trust
	if trust == "" {
		trust = provider.TrustFull
	}
	// announced models providers/claudecode's real behavior (session.go): a
	// resumed session's ref is already known from opts.Resume regardless of
	// scenario timing. A fresh session's timing is scripted by
	// Scenario.RefAnnounce: on_connect fires immediately, the default fires
	// once the first turn begins (see Send), never leaves it unknown
	// forever (models PTY discovery giving up).
	timing := f.scenario.RefAnnounce
	announced := opts.Resume != "" || timing == RefAnnounceOnConnect
	refCh := make(chan string, 1)
	switch {
	case announced:
		refCh <- ref
	case timing == RefAnnounceNever:
		close(refCh)
	}
	s := &fakeSession{
		f: f, ref: ref, mode: opts.Mode, cwd: opts.Cwd, trust: trust,
		announced: announced, refAnnounce: timing, refCh: refCh,
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
		if req.OnEvent != nil {
			req.OnEvent(toolEvent(ts))
		}
	}
	// Delay on cue holds for one-shots too — downstream timing tests rely
	// on scripted turns taking as long as the scenario says. Chunks stream
	// through OnEvent (when set) with that same delay between them, so a
	// caller watching for incremental output sees genuine pacing rather
	// than one wait followed by the whole result at once.
	var b strings.Builder
	for _, chunk := range plan.chunks {
		if plan.delay > 0 {
			time.Sleep(plan.delay)
		}
		b.WriteString(chunk)
		if req.OnEvent != nil {
			req.OnEvent(provider.Event{Kind: provider.EventText, Text: chunk})
		}
	}
	text := b.String()
	u := usage(req.Prompt, text)
	if req.Model != "" {
		u.Model = req.Model
	}
	return &provider.OneShotResult{Text: text, Usage: applyUsageOverride(u, plan.usage)}, nil
}

// applyUsageOverride layers a scripted override onto the deterministic
// default: unset (zero) fields keep their default, CostUSD always takes the
// scripted value (0 when nil, same as an unscripted turn).
func applyUsageOverride(u provider.Usage, o *UsageOverride) provider.Usage {
	if o == nil {
		return u
	}
	if o.Model != "" {
		u.Model = o.Model
	}
	if o.InputTokens != 0 {
		u.InputTokens = o.InputTokens
	}
	if o.OutputTokens != 0 {
		u.OutputTokens = o.OutputTokens
	}
	u.CostUSD = o.CostUSD
	return u
}

func (f *Fake) Models(context.Context) ([]provider.Model, error) {
	if len(f.scenario.Models) > 0 {
		return append([]provider.Model{}, f.scenario.Models...), nil
	}
	return append([]provider.Model{}, DefaultModels...), nil
}

// MCPServers returns the MCPServers a Start call for ref was given —
// nil if ref is unknown or Start was called with none. Test-only accessor,
// mirroring how Trust is observed indirectly via tool-refusal behavior.
func (f *Fake) MCPServers(ref string) []provider.MCPServerRef {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.mcpServers[ref]
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
	usage     *UsageOverride
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
			tools: r.Tools, crash: r.Crash, rateLimit: rateLimit, usage: r.Usage,
		}
		if len(p.chunks) == 0 && r.Reply != "" {
			p.chunks = []string{r.Reply}
		}
		return p
	}
	switch {
	case strings.HasPrefix(prompt, "echo slowly "):
		full := "you said " + strings.TrimPrefix(prompt, "echo slowly ")
		words := strings.Split(full, " ")
		chunks := make([]string, len(words))
		for i, w := range words {
			if i < len(words)-1 {
				chunks[i] = w + " "
			} else {
				chunks[i] = w
			}
		}
		return turnPlan{chunks: chunks, delay: 250 * time.Millisecond}
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
	case prompt == "attempt-write":
		return turnPlan{
			tools:  []ToolStep{{Name: "write_file", Path: "trust-probe.txt", Content: "should not exist under read_only"}},
			chunks: []string{"wrote it"},
		}
	case prompt == "attempt-command":
		return turnPlan{
			tools:  []ToolStep{{Name: "run_command", Command: true}},
			chunks: []string{"ran it"},
		}
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

// release marks ref no longer attached to a live session, allowing a later
// Start to resume it. A different mutex than fakeSession.die() already
// holds (s.mu) — no nesting/deadlock risk.
func (f *Fake) release(ref string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.liveRefs, ref)
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
	f           *Fake
	ref         string
	mode        provider.Mode
	cwd         string
	trust       provider.Trust
	refAnnounce RefAnnounceTiming
	events      chan provider.Event
	interrupt   chan struct{}
	ptyR        *io.PipeReader
	ptyW        *io.PipeWriter
	// refCh delivers ref exactly once (Start, for on_connect/resume, or Send,
	// for the default first-turn timing) and closes with nothing sent if
	// refAnnounce is RefAnnounceNever — see RefAnnounced.
	refCh chan string

	mu        sync.Mutex
	closed    bool
	announced bool // see Start's construction comment
}

// toolAllowed reports whether ts is permitted at the session's trust level,
// and the refusal reason to emit when it is not. TrustFull (and the zero
// value, normalized to TrustFull in Start) always allows every tool.
func (s *fakeSession) toolAllowed(ts ToolStep) (bool, string) {
	switch s.trust {
	case provider.TrustLimited:
		if ts.Command {
			return false, fmt.Sprintf("trust %s refuses tool %q", s.trust, ts.Name)
		}
	case provider.TrustReadOnly:
		if ts.Command || (ts.Path != "" && ts.Content != "") {
			return false, fmt.Sprintf("trust %s refuses tool %q", s.trust, ts.Name)
		}
	}
	return true, ""
}

// Ref returns "" until the ref is known — immediately for a resumed session,
// only once the first turn begins for a fresh one (see Start's construction
// comment).
func (s *fakeSession) Ref() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.announced {
		return ""
	}
	return s.ref
}
func (s *fakeSession) Mode() provider.Mode           { return s.mode }
func (s *fakeSession) Events() <-chan provider.Event { return s.events }
func (s *fakeSession) Terminal() io.ReadWriter       { return terminal{s.ptyR, io.Discard} }
func (s *fakeSession) Resize(int, int) error         { return nil }

// RefAnnounced implements provider.RefAnnouncer — see refCh's doc comment.
func (s *fakeSession) RefAnnounced() <-chan string { return s.refCh }

// SetRef implements provider.RefSetter, the Runner backstop's write-back
// path (runner_test.go's TestTranscriptAfterLocateBackstop exercises it).
// Unlike a real provider, a fakeSession always knows its true ref from Start
// (deterministic "fake-N" generation, used as the record/history key from
// the very first Send) — announced is what actually models "unknown until
// discovered." So this only flips that visibility switch; it doesn't
// overwrite s.ref, which stays a fixed, lock-free-readable value for the
// whole session lifetime (Send and friends read it without s.mu).
func (s *fakeSession) SetRef(string) {
	s.mu.Lock()
	if s.announced {
		s.mu.Unlock()
		return
	}
	s.announced = true
	// RefAnnounceNever already closed refCh in Start (models "no
	// announcement mechanism at all") — sending on it now would panic. The
	// backstop's own goroutine (Runner's watchRefAnnounce) already observed
	// that close-with-nothing-sent and moved on; there's no reader left to
	// deliver to.
	never := s.refAnnounce == RefAnnounceNever
	s.mu.Unlock()
	if never {
		return
	}
	select {
	case s.refCh <- s.ref:
	default:
	}
}

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
		s.f.release(s.ref)
	}
}

func (s *fakeSession) Send(_ context.Context, text string) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return fmt.Errorf("session %s is closed", s.ref)
	}
	// Only the default (first-turn) timing learns the ref here; on_connect
	// already announced it in Start, and RefAnnounceNever must never learn
	// it at all (models a transport with no announcement mechanism, e.g.
	// PTY discovery that never finds a candidate).
	first := s.refAnnounce == RefAnnounceOnFirstTurn && !s.announced
	if first {
		s.announced = true
	}
	s.mu.Unlock()
	if first {
		select {
		case s.refCh <- s.ref:
		default:
		}
	}

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
		if allowed, reason := s.toolAllowed(ts); !allowed {
			s.emit(provider.Event{Kind: provider.EventError, Text: reason})
			s.emit(provider.Event{Kind: provider.EventTurnEnd})
			return
		}
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

// LocatingFake wraps Fake to add provider.SessionLocator support, scripted
// via Scenario.LocateRef/LocateDelay. Kept as a distinct type from Fake
// rather than a method on Fake itself: Go interface satisfaction is
// structural, so a LocateSession method on Fake would make every fake
// locate-capable with no way to model "this provider has no SessionLocator
// at all" — a case runner tests need to cover alongside the scripted-hit
// and scripted-never-finds cases.
type LocatingFake struct {
	*Fake
}

// NewLocatingWithScenario returns a fake scripted by sc (validated) that
// also implements provider.SessionLocator.
func NewLocatingWithScenario(sc Scenario) (*LocatingFake, error) {
	f, err := NewWithScenario(sc)
	if err != nil {
		return nil, err
	}
	return &LocatingFake{Fake: f}, nil
}

// LocateSession implements provider.SessionLocator: it resolves to
// Scenario.LocateRef after Scenario.LocateDelay, or declines (empty
// LocateRef, or ctx done first) — the scripted stand-in for a real
// provider's history/transcript scan.
func (lf *LocatingFake) LocateSession(ctx context.Context, cwd string, spawnedAfter time.Time, firstMessage string) (string, bool) {
	if lf.scenario.LocateRef == "" {
		<-ctx.Done()
		return "", false
	}
	select {
	case <-time.After(lf.scenario.LocateDelay):
		return lf.scenario.LocateRef, true
	case <-ctx.Done():
		return "", false
	}
}
