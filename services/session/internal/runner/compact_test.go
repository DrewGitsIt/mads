package runner

import (
	"context"
	"errors"
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

	if err := r.CompactNow(ctx); !errors.Is(err, ErrNotReady) {
		t.Fatalf("CompactNow before Start = %v, want ErrNotReady", err)
	}

	if err := r.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = r.Shutdown(ctx) })

	if err := r.CompactNow(ctx); err != nil {
		t.Fatalf("CompactNow: %v", err)
	}
	if calls := shared.CompactCalls(); len(calls) != 1 {
		t.Fatalf("CompactCalls() = %v, want exactly 1 call", calls)
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
