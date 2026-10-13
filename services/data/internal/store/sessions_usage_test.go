package store

import (
	"context"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
)

// TestAddUsageFoldsTotals: increments sum on the record, delegates in both
// the totals and their own subtotal, an empty increment is a no-op, and a
// full-record PUT carrying an older read can't roll the sum back.
func TestAddUsageFoldsTotals(t *testing.T) {
	s, _, l := newSessionStore(t)
	ctx := context.Background()
	sess := mkSession(t, s, contracts.Session{Name: "Totals"})
	stale, err := s.Get(ctx, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	before := len(eventTypes(t, l, Filter{SessionID: sess.ID}))

	for _, inc := range []contracts.UsageIncrement{
		{Turns: 1, CostUSD: 0.25, Agent: contracts.TokenCounts{Input: 100, Output: 20, CacheRead: 1000}},
		{},
		{Turns: 1, CostUSD: 0.5, Agent: contracts.TokenCounts{Input: 50, Output: 10}, Delegates: contracts.TokenCounts{Input: 7, Output: 3}},
	} {
		if err := s.AddUsage(ctx, sess.ID, inc); err != nil {
			t.Fatal(err)
		}
	}
	stale.Name = "Renamed from a stale read"
	if err := s.Update(ctx, stale, "user"); err != nil {
		t.Fatal(err)
	}

	cur, err := s.Get(ctx, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	got := cur.UsageTotals
	if got == nil || got.Turns != 2 || got.Input != 157 || got.Output != 33 || got.CacheRead != 1000 || got.CostUSD != 0.75 {
		t.Fatalf("totals = %+v, want 2 turns, 157 in, 33 out, 1000 cache read, $0.75", got)
	}
	if got.Delegates == nil || got.Delegates.Input != 7 || got.Delegates.Output != 3 {
		t.Errorf("delegate subtotal = %+v, want 7 in / 3 out", got.Delegates)
	}
	// Only the rename announced anything.
	if after := len(eventTypes(t, l, Filter{SessionID: sess.ID})); after != before+1 {
		t.Errorf("events %d → %d, want only the rename's", before, after)
	}

	if err := s.AddUsage(ctx, "ses_missing", contracts.UsageIncrement{Turns: 1}); err == nil {
		t.Error("want an error adding usage to a missing session")
	}
}

// TestSetBillingMergesHalves: mode and limits arrive on different harness
// lines, so each write leaves the other half alone; a bogus mode is
// refused.
func TestSetBillingMergesHalves(t *testing.T) {
	s, _, _ := newSessionStore(t)
	ctx := context.Background()
	sess := mkSession(t, s, contracts.Session{Name: "Billing"})

	at := time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)
	limits := []contracts.RateLimit{{ID: "five_hour", Label: "5-hour", UsedPct: 6, ResetsAt: at.Add(time.Hour), Scope: "account"}}
	if err := s.SetBilling(ctx, sess.ID, contracts.SessionBillingPatch{Limits: limits, At: at}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetBilling(ctx, sess.ID, contracts.SessionBillingPatch{Mode: contracts.BillingPlan}); err != nil {
		t.Fatal(err)
	}
	cur, err := s.Get(ctx, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	b := cur.Billing
	if b == nil || b.Mode != contracts.BillingPlan || len(b.Limits) != 1 || b.Limits[0].UsedPct != 6 || b.LimitsAt == nil || !b.LimitsAt.Equal(at) {
		t.Fatalf("billing = %+v, want plan with the earlier window kept", b)
	}

	// Full-record PUTs keep it, like the totals.
	cur.Billing = nil
	if err := s.Update(ctx, cur, "user"); err != nil {
		t.Fatal(err)
	}
	if again, _ := s.Get(ctx, sess.ID); again.Billing == nil || again.Billing.Mode != contracts.BillingPlan {
		t.Errorf("billing after a PUT without it = %+v, want it kept", again.Billing)
	}

	if err := s.SetBilling(ctx, sess.ID, contracts.SessionBillingPatch{Mode: "prepaid"}); err == nil {
		t.Error("want a validation error for an unknown billing mode")
	}
}
