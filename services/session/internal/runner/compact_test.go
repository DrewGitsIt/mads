package runner

import (
	"context"
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
		{name: "below threshold", usage: &contracts.Usage{InputTokens: 10_000}, want: false},
		{name: "above threshold", usage: &contracts.Usage{InputTokens: 30_000}, want: true},
		{
			name:  "cache tokens count toward the prompt side",
			usage: &contracts.Usage{InputTokens: 5_000, CacheReadInputTokens: 20_000, CacheCreationInputTokens: 5_000},
			want:  true,
		},
		{
			name:  "output tokens alone never trigger",
			usage: &contracts.Usage{InputTokens: 1_000, OutputTokens: 190_000},
			want:  false,
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
	cur.LastUsage = &contracts.Usage{InputTokens: 190_000, Model: "fake-fast"}
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
// with LastUsage well under the 10% cutoff: no compaction should be
// requested.
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
	cur.LastUsage = &contracts.Usage{InputTokens: 5_000, Model: "fake-fast"}
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
