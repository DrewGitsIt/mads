package runner

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/providers/fake"
	"github.com/drewkidwell/mads/providers/provider"
	"github.com/drewkidwell/mads/services/data/datatest"
)

func TestShouldCompactOnResume(t *testing.T) {
	const model = "fake-fast" // unmapped model: provider.ContextWindowForModel falls back to DefaultContextWindow (200_000)
	tests := []struct {
		name  string
		usage *contracts.Usage
		want  bool
	}{
		{name: "nil usage never triggers", usage: nil, want: false},
		{name: "below threshold", usage: &contracts.Usage{ContextTokens: 100_000}, want: false},
		{name: "above threshold", usage: &contracts.Usage{ContextTokens: 180_000}, want: true},
		{
			name:  "output tokens alone never trigger",
			usage: &contracts.Usage{ContextTokens: 1_000, OutputTokens: 190_000},
			want:  false,
		},
		{
			// The summed fields are claude-code's per-turn billing aggregate
			// (~N× real occupancy on multi-tool turns); ContextTokens is the
			// final API call's true prompt side and must win when present —
			// this exact shape is why 10%-of-aggregate compacted nearly every
			// resumed session.
			name:  "accurate context_tokens beats the inflated aggregate",
			usage: &contracts.Usage{InputTokens: 500, CacheReadInputTokens: 400_000, ContextTokens: 45_000},
			want:  false,
		},
		{
			name:  "context_tokens alone can trigger",
			usage: &contracts.Usage{ContextTokens: 180_000},
			want:  true,
		},
		{
			// A pre-ContextTokens LastUsage carries only the billing aggregate
			// (267212 here — 1.34 "occupancy" against 200k). Trusting it made
			// waking a dormant session auto-compact; occupancy without
			// ContextTokens is unknown and unknown never compacts.
			name:  "aggregate-only usage never triggers, however huge",
			usage: &contracts.Usage{InputTokens: 18, CacheCreationInputTokens: 23_710, CacheReadInputTokens: 243_484},
			want:  false,
		},
		{
			// A provider-reported window (here a 128k local model) beats the
			// static 200k table: 120k is under 85% of 200k but over 85% of
			// the real window.
			name:  "provider-reported window beats the static table",
			usage: &contracts.Usage{ContextTokens: 120_000, ContextWindow: 128_000},
			want:  true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldCompactOnResume(tc.usage, model); got != tc.want {
				t.Errorf("shouldCompactOnResume(%+v, %q) = %v, want %v", tc.usage, model, got, tc.want)
			}
		})
	}
}

// TestStartTriggersCompactionAboveThreshold resumes a session whose stored
// LastUsage occupies more than compactOccupancyThreshold of the model's
// context window, and asserts the resuming Runner asks the shared fake's
// session to compact.
func TestStartTriggersCompactionAboveThreshold(t *testing.T) {
	ctx := context.Background()
	base := datatest.Start(t)
	sess := newSession(t, base)
	data := dataclient.New(base, sess.ID)

	shared := fake.New()
	reg1 := provider.NewRegistry()
	if err := reg1.Register(shared); err != nil {
		t.Fatalf("register fake: %v", err)
	}
	r1 := New(sess.ID, data, reg1)
	if err := r1.Start(ctx); err != nil {
		t.Fatalf("start r1: %v", err)
	}
	// A fresh fake session's ref is only announced once the first turn
	// begins (fakeSession's default RefAnnounce timing) — send one turn so
	// ProviderRef actually persists before r1 shuts down.
	ch1, unsub1 := r1.Subscribe()
	if err := r1.Send(ctx, "echo hi"); err != nil {
		t.Fatalf("send r1: %v", err)
	}
	drainText(t, ch1)
	unsub1()
	if err := r1.Shutdown(ctx); err != nil {
		t.Fatalf("shutdown r1: %v", err)
	}

	cur, err := data.Session(ctx, sess.ID)
	if err != nil {
		t.Fatalf("load session: %v", err)
	}
	if cur.ProviderRef == "" {
		t.Fatal("provider_ref not persisted after r1's first turn")
	}
	cur.LastUsage = &contracts.Usage{ContextTokens: 190_000, Model: "fake-fast"}
	if err := data.UpdateSession(ctx, &cur); err != nil {
		t.Fatalf("seed last_usage: %v", err)
	}

	reg2 := provider.NewRegistry()
	if err := reg2.Register(shared); err != nil {
		t.Fatalf("register fake: %v", err)
	}
	r2 := New(sess.ID, data, reg2)
	if err := r2.Start(ctx); err != nil {
		t.Fatalf("start r2: %v", err)
	}
	t.Cleanup(func() { _ = r2.Shutdown(ctx) })

	calls := shared.CompactCalls()
	if len(calls) != 1 {
		t.Fatalf("CompactCalls() = %v, want exactly 1 call", calls)
	}
	if calls[0] != cur.ProviderRef {
		t.Errorf("compact requested for ref %q, want %q", calls[0], cur.ProviderRef)
	}
}

// TestStartSkipsCompactionBelowThreshold mirrors the above-threshold case
// with LastUsage well under the near-limit cutoff: no compaction should be
// requested — an ordinary resume (or trust-change bounce) must not silently
// compact a conversation that still fits comfortably.
func TestStartSkipsCompactionBelowThreshold(t *testing.T) {
	ctx := context.Background()
	base := datatest.Start(t)
	sess := newSession(t, base)
	data := dataclient.New(base, sess.ID)

	shared := fake.New()
	reg1 := provider.NewRegistry()
	if err := reg1.Register(shared); err != nil {
		t.Fatalf("register fake: %v", err)
	}
	r1 := New(sess.ID, data, reg1)
	if err := r1.Start(ctx); err != nil {
		t.Fatalf("start r1: %v", err)
	}
	if err := r1.Shutdown(ctx); err != nil {
		t.Fatalf("shutdown r1: %v", err)
	}

	cur, err := data.Session(ctx, sess.ID)
	if err != nil {
		t.Fatalf("load session: %v", err)
	}
	cur.LastUsage = &contracts.Usage{ContextTokens: 5_000, Model: "fake-fast"}
	if err := data.UpdateSession(ctx, &cur); err != nil {
		t.Fatalf("seed last_usage: %v", err)
	}

	reg2 := provider.NewRegistry()
	if err := reg2.Register(shared); err != nil {
		t.Fatalf("register fake: %v", err)
	}
	r2 := New(sess.ID, data, reg2)
	if err := r2.Start(ctx); err != nil {
		t.Fatalf("start r2: %v", err)
	}
	t.Cleanup(func() { _ = r2.Shutdown(ctx) })

	if calls := shared.CompactCalls(); len(calls) != 0 {
		t.Fatalf("CompactCalls() = %v, want none", calls)
	}
}

// TestCompactNow covers the user-requested path (POST /compact): no
// threshold applies — the user asked — and calling before Start answers
// ErrNotReady rather than panicking on a nil session.
func TestCompactNow(t *testing.T) {
	ctx := context.Background()
	base := datatest.Start(t)
	sess := newSession(t, base)
	data := dataclient.New(base, sess.ID)

	shared := fake.New()
	reg := provider.NewRegistry()
	if err := reg.Register(shared); err != nil {
		t.Fatalf("register fake: %v", err)
	}
	r := New(sess.ID, data, reg)

	if err := r.CompactNow(ctx, ""); !errors.Is(err, ErrNotReady) {
		t.Fatalf("CompactNow before Start = %v, want ErrNotReady", err)
	}

	if err := r.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = r.Shutdown(ctx) })

	if err := r.CompactNow(ctx, ""); err != nil {
		t.Fatalf("CompactNow: %v", err)
	}
	if calls := shared.CompactCalls(); len(calls) != 1 {
		t.Fatalf("CompactCalls() = %v, want exactly 1 call", calls)
	}
	// The pass is the in-flight turn until its turn end.
	waitFor(t, "the compaction turn to end", func() bool { return !r.isBusy() })
	// The /compact focus text reaches the provider verbatim.
	if err := r.CompactNow(ctx, "keep the queue work"); err != nil {
		t.Fatalf("CompactNow with instructions: %v", err)
	}
	if got := shared.CompactInstructions(); len(got) != 2 || got[0] != "" || got[1] != "keep the queue work" {
		t.Fatalf("CompactInstructions() = %q, want [\"\" \"keep the queue work\"]", got)
	}
}

// refusingCompactor is a session whose harness compacts but takes no focus
// text — acprpc's goose shape.
type refusingCompactor struct{ provider.Session }

func (refusingCompactor) Compact(_ context.Context, instructions string) error {
	if instructions != "" {
		return provider.ErrUnsupported
	}
	return nil
}

// TestCompactNowRefusedInstructions: instructions a harness can't take come
// back as ErrCompactUnsupported (POST /compact's 400), never a compaction
// that silently dropped them.
func TestCompactNowRefusedInstructions(t *testing.T) {
	r := &Runner{sess: refusingCompactor{}}
	if err := r.CompactNow(context.Background(), "focus"); !errors.Is(err, ErrCompactUnsupported) {
		t.Fatalf("CompactNow(focus) = %v, want ErrCompactUnsupported", err)
	}
	if err := r.CompactNow(context.Background(), ""); err != nil {
		t.Fatalf("CompactNow() = %v, want nil", err)
	}
}

// TestStartSkipsCompactionWithoutUsage covers a session with no recorded
// LastUsage at all (the fresh-session case, and a resumed session this
// process never persisted usage for) — never triggers.
func TestStartSkipsCompactionWithoutUsage(t *testing.T) {
	ctx := context.Background()
	base := datatest.Start(t)
	sess := newSession(t, base)
	data := dataclient.New(base, sess.ID)

	shared := fake.New()
	reg := provider.NewRegistry()
	if err := reg.Register(shared); err != nil {
		t.Fatalf("register fake: %v", err)
	}
	r := New(sess.ID, data, reg)
	if err := r.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = r.Shutdown(ctx) })

	if calls := shared.CompactCalls(); len(calls) != 0 {
		t.Fatalf("CompactCalls() = %v, want none", calls)
	}
}

// A turn end whose usage counts no tokens (claude-code's /compact turn, when
// the CLI gave no post-compaction figure) must not overwrite LastUsage: saved,
// it reads as an empty context to the rim, the rail and the resume trigger.
func TestPersistTurnEndKeepsLastUsageOverAnEmptyCount(t *testing.T) {
	ctx := context.Background()
	base := datatest.Start(t)
	sess := newSession(t, base)
	data := dataclient.New(base, sess.ID)
	r := New(sess.ID, data, provider.NewRegistry())

	r.persistTurnEnd(ctx, &provider.Usage{InputTokens: 3, ContextTokens: 140_000, ContextWindow: 1_000_000})
	r.persistTurnEnd(ctx, &provider.Usage{ContextWindow: 1_000_000, CostUSD: 0.4})

	cur, err := data.Session(ctx, sess.ID)
	if err != nil {
		t.Fatalf("load session: %v", err)
	}
	if cur.LastUsage == nil || cur.LastUsage.ContextTokens != 140_000 {
		t.Errorf("LastUsage = %+v, want the last counted turn's (140000)", cur.LastUsage)
	}
	if cur.LastTurnAt == nil {
		t.Error("LastTurnAt not stamped")
	}
}

func (r *Runner) isBusy() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.busy
}

// holdingProvider's sessions compact the way acprpc does: Compact sends a
// real prompt that stays in flight until the test releases it, and a Send
// meanwhile is refused with provider.ErrTurnInFlight. Ordinary turns end at
// once.
type holdingProvider struct {
	mu   sync.Mutex
	sess *holdingSession
}

func (*holdingProvider) Name() string { return "holding" }
func (*holdingProvider) Matrix() provider.Matrix {
	return fake.New().Matrix()
}
func (*holdingProvider) OneShot(context.Context, provider.OneShotRequest) (*provider.OneShotResult, error) {
	return nil, provider.ErrUnsupported
}
func (p *holdingProvider) Start(context.Context, provider.StartOptions) (provider.Session, error) {
	s := &holdingSession{events: make(chan provider.Event, 64)}
	p.mu.Lock()
	p.sess = s
	p.mu.Unlock()
	return s, nil
}
func (p *holdingProvider) session() *holdingSession {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.sess
}

type holdingSession struct {
	mu         sync.Mutex
	events     chan provider.Event
	closed     bool
	compacting bool
	compacts   int
	sent       []string
}

func (*holdingSession) Ref() string                     { return "hold-1" }
func (*holdingSession) Mode() provider.Mode             { return provider.ModeACP }
func (s *holdingSession) Events() <-chan provider.Event { return s.events }
func (*holdingSession) Interrupt(context.Context) error { return nil }
func (s *holdingSession) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.closed {
		s.closed = true
		close(s.events)
	}
	return nil
}
func (s *holdingSession) Send(_ context.Context, text string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.compacting {
		return fmt.Errorf("session hold-1 already has a prompt in flight: %w", provider.ErrTurnInFlight)
	}
	s.sent = append(s.sent, text)
	s.events <- provider.Event{Kind: provider.EventTurnEnd}
	return nil
}
func (s *holdingSession) Compact(context.Context, string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.compacting = true
	s.compacts++
	s.events <- provider.Event{Kind: provider.EventCompact, Compact: &provider.CompactStatus{Phase: "start"}}
	return nil
}

// finishCompact answers the compaction prompt: done, then its turn end.
func (s *holdingSession) finishCompact() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.compacting = false
	s.events <- provider.Event{Kind: provider.EventCompact, Compact: &provider.CompactStatus{Phase: "done", Success: true}}
	s.events <- provider.Event{Kind: provider.EventTurnEnd, Usage: &provider.Usage{InputTokens: 5_000, OutputTokens: 300}}
}

func (s *holdingSession) sentTexts() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.sent...)
}

func startHolding(t *testing.T, lastUsage *contracts.Usage) (*Runner, *holdingSession, *dataclient.Client, string) {
	t.Helper()
	ctx := context.Background()
	base := datatest.Start(t)
	bootstrap := dataclient.New(base, "test-bootstrap")
	rec := contracts.Session{Name: "holding", Provider: "holding"}
	if err := bootstrap.CreateSession(ctx, &rec); err != nil {
		t.Fatalf("create session: %v", err)
	}
	data := dataclient.New(base, rec.ID)
	if lastUsage != nil {
		rec.LastUsage = lastUsage
		if err := data.UpdateSession(ctx, &rec); err != nil {
			t.Fatalf("seed last_usage: %v", err)
		}
	}
	p := &holdingProvider{}
	reg := provider.NewRegistry()
	if err := reg.Register(p); err != nil {
		t.Fatalf("register: %v", err)
	}
	r := New(rec.ID, data, reg)
	if err := r.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = r.Shutdown(ctx) })
	return r, p.session(), data, rec.ID
}

// assertQueuedThenDelivered sends a message while the session compacts and
// checks it waits for the pass rather than failing, then goes out once the
// pass ends — and that the pass itself wasn't counted as a turn.
func assertQueuedThenDelivered(t *testing.T, r *Runner, hs *holdingSession, data *dataclient.Client, id string) {
	t.Helper()
	ctx := context.Background()
	q, err := r.SendOrQueue(ctx, "after the compaction")
	if err != nil {
		t.Fatalf("SendOrQueue during compaction: %v", err)
	}
	if q == nil {
		t.Fatal("SendOrQueue during compaction sent straight to a compacting harness, want it queued")
	}
	if got := hs.sentTexts(); len(got) != 0 {
		t.Fatalf("harness got %q mid-compaction", got)
	}
	hs.finishCompact()
	waitFor(t, "the queued message to go out", func() bool {
		got := hs.sentTexts()
		return len(got) == 1 && got[0] == "after the compaction"
	})
	waitFor(t, "the delivered turn to end", func() bool { return !r.isBusy() })
	cur, err := data.Session(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if cur.UsageTotals == nil || cur.UsageTotals.Turns != 1 || cur.UsageTotals.Input != 5_000 {
		t.Errorf("totals = %+v, want the compaction's tokens and only the delivered turn counted", cur.UsageTotals)
	}
}

// A message sent while a "Compact now" pass runs on an ACP harness (one
// prompt at a time) queues behind it and is delivered once it ends, and the
// session reads as compacting meanwhile — before, the runner didn't know a
// pass was in flight, sent straight through, and the harness's refusal
// came back as a 500 with the message lost.
func TestCompactNowQueuesMessagesBehindThePass(t *testing.T) {
	r, hs, data, id := startHolding(t, nil)
	if err := r.CompactNow(context.Background(), ""); err != nil {
		t.Fatalf("CompactNow: %v", err)
	}
	if !r.isBusy() {
		t.Fatal("runner not busy during a compaction pass")
	}
	r.agent.mu.Lock()
	state, label := r.agent.state, r.agent.label
	r.agent.mu.Unlock()
	if state != contracts.AgentWorking || label != agentLabelCompacting {
		t.Errorf("agent state during compaction = %q %q, want working %q", state, label, agentLabelCompacting)
	}
	if err := r.CompactNow(context.Background(), ""); !errors.Is(err, ErrTurnInFlight) {
		t.Errorf("second CompactNow mid-pass = %v, want ErrTurnInFlight", err)
	}
	assertQueuedThenDelivered(t, r, hs, data, id)
}

// Waking a session above the near-limit threshold compacts it in Start; the
// wake's own message, arriving right behind, must queue behind that pass.
func TestResumeCompactionQueuesTheWakeMessage(t *testing.T) {
	r, hs, data, id := startHolding(t, &contracts.Usage{ContextTokens: 190_000, Model: "fake-fast"})
	hs.mu.Lock()
	compacts := hs.compacts
	hs.mu.Unlock()
	if compacts != 1 {
		t.Fatalf("compactions requested by Start = %d, want 1", compacts)
	}
	if !r.isBusy() {
		t.Fatal("runner not busy during the resume compaction")
	}
	assertQueuedThenDelivered(t, r, hs, data, id)
}

// The backstop: a harness turn the runner never marked (busy false) refuses
// the prompt with provider.ErrTurnInFlight, and SendOrQueue queues instead
// of failing; that turn's end delivers it.
func TestSendOrQueueQueuesOnAHarnessRefusal(t *testing.T) {
	r, hs, _, _ := startHolding(t, nil)
	if err := hs.Compact(context.Background(), ""); err != nil { // behind the runner's back
		t.Fatal(err)
	}
	q, err := r.SendOrQueue(context.Background(), "after the compaction")
	if err != nil || q == nil {
		t.Fatalf("SendOrQueue = (%v, %v), want queued", q, err)
	}
	hs.finishCompact()
	waitFor(t, "the queued message to go out", func() bool { return len(hs.sentTexts()) == 1 })
	// Plain Send still answers 409 rather than an opaque failure.
	if err := hs.Compact(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the delivered turn to end", func() bool { return !r.isBusy() })
	if err := r.Send(context.Background(), "x"); !errors.Is(err, ErrTurnInFlight) {
		t.Errorf("Send to a harness mid-turn = %v, want ErrTurnInFlight", err)
	}
	hs.finishCompact()
}
