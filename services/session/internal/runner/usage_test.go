package runner

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/providers/fake"
	"github.com/drewkidwell/mads/providers/provider"
	"github.com/drewkidwell/mads/services/data/datatest"
)

// Totals across two processes of one session (a sleep and a wake): each
// runner adds its turns onto the record, so nothing restarts from zero. A
// delegate's call repeated on several lines counts once; a zero-token turn
// (a /compact with no report) adds its cost and nothing else; a delegate's
// call after the last turn end is sent when the process ends.
func TestUsageTotalsAcrossTurnsAndProcesses(t *testing.T) {
	ctx := context.Background()
	base := datatest.Start(t)
	sess := newSession(t, base)
	data := dataclient.New(base, sess.ID)

	first := New(sess.ID, data, provider.NewRegistry())
	call := &provider.Usage{InputTokens: 10, OutputTokens: 4, CacheCreationInputTokens: 10_792}
	for _, ev := range []provider.Event{
		{Kind: provider.EventSubagentUsage, ParentToolUseID: "toolu_a", Usage: call},
		{Kind: provider.EventSubagentUsage, ParentToolUseID: "toolu_a", Usage: call}, // same call, next content block
		{Kind: provider.EventTurnEnd, Usage: &provider.Usage{InputTokens: 100, OutputTokens: 50, CacheReadInputTokens: 2000, CostUSD: 0.25}},
		{Kind: provider.EventTurnEnd, Usage: &provider.Usage{ContextWindow: 200_000, CostUSD: 0.05}}, // /compact
	} {
		first.observeUsage(ev)
	}
	// A backgrounded delegate keeps going after the turn; the process ends.
	first.observeUsage(provider.Event{Kind: provider.EventSubagentUsage, ParentToolUseID: "toolu_a",
		Usage: &provider.Usage{InputTokens: 8, OutputTokens: 2, CacheReadInputTokens: 10_792}})
	first.flushUsage()

	second := New(sess.ID, data, provider.NewRegistry())
	second.observeUsage(provider.Event{Kind: provider.EventTurnEnd, Usage: &provider.Usage{InputTokens: 1, OutputTokens: 1, CostUSD: 0.1}})

	cur, err := data.Session(ctx, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	want := contracts.UsageTotals{
		TokenCounts: contracts.TokenCounts{Input: 119, Output: 57, CacheRead: 12_792, CacheWrite: 10_792},
		Turns:       2,
		CostUSD:     0.4,
		Delegates:   &contracts.TokenCounts{Input: 18, Output: 6, CacheRead: 10_792, CacheWrite: 10_792},
	}
	got := cur.UsageTotals
	if got == nil || got.TokenCounts != want.TokenCounts || got.Turns != want.Turns ||
		got.CostUSD < 0.4-1e-9 || got.CostUSD > 0.4+1e-9 || got.Delegates == nil || *got.Delegates != *want.Delegates {
		t.Errorf("totals = %+v (delegates %+v), want %+v (delegates %+v)", got, got.Delegates, want, *want.Delegates)
	}
}

// A provider that reports no usage at all still has its turns counted.
func TestUsageTotalsCountUsagelessTurns(t *testing.T) {
	ctx := context.Background()
	base := datatest.Start(t)
	sess := newSession(t, base)
	data := dataclient.New(base, sess.ID)
	r := New(sess.ID, data, provider.NewRegistry())
	r.observeUsage(provider.Event{Kind: provider.EventTurnEnd})
	r.observeUsage(provider.Event{Kind: provider.EventTurnEnd, Interrupted: true})

	cur, err := data.Session(ctx, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if cur.UsageTotals == nil || cur.UsageTotals.Turns != 2 || !cur.UsageTotals.IsZero() {
		t.Errorf("totals = %+v, want 2 turns and no tokens", cur.UsageTotals)
	}
}

// The billing mode and windows land on the record; a repeated report isn't
// written again, and Status reports the provider's last word.
func TestAccountEventsPersistBilling(t *testing.T) {
	ctx := context.Background()
	base := datatest.Start(t)
	sess := newSession(t, base)
	data := dataclient.New(base, sess.ID)
	r := New(sess.ID, data, provider.NewRegistry())

	if st := r.Status(); st.StartedAt.IsZero() || !st.LastEventAt.IsZero() {
		t.Fatalf("status before any event = %+v, want a start and no last event", st)
	}
	resets := time.Date(2026, 10, 12, 14, 10, 0, 0, time.UTC)
	window := []provider.RateLimitWindow{{ID: "five_hour", UsedPct: 62, ResetsAt: resets}, {ID: "seven_day", UsedPct: 31}}
	r.observeUsage(provider.Event{Kind: provider.EventAccount, Account: &provider.AccountStatus{BillingMode: provider.BillingPlan}})
	r.observeUsage(provider.Event{Kind: provider.EventAccount, Account: &provider.AccountStatus{Limits: window}})

	cur, err := data.Session(ctx, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	b := cur.Billing
	if b == nil || b.Mode != contracts.BillingPlan || len(b.Limits) != 2 || b.LimitsAt == nil {
		t.Fatalf("billing = %+v, want plan with two windows", b)
	}
	if l := b.Limits[0]; l.ID != "five_hour" || l.Label != "5-hour" || l.UsedPct != 62 || !l.ResetsAt.Equal(resets) || l.Scope != "account" {
		t.Errorf("first window = %+v", l)
	}
	if b.Limits[1].Label != "Weekly" {
		t.Errorf("second window label = %q, want Weekly", b.Limits[1].Label)
	}

	stamped := *b.LimitsAt
	time.Sleep(5 * time.Millisecond)
	r.observeUsage(provider.Event{Kind: provider.EventAccount, Account: &provider.AccountStatus{Limits: window}})
	if again, _ := data.Session(ctx, sess.ID); again.Billing == nil || !again.Billing.LimitsAt.Equal(stamped) {
		t.Errorf("an unchanged report was written again: %+v", again.Billing)
	}
	if st := r.Status(); st.LastEventAt.IsZero() {
		t.Error("status has no last event after several")
	}
}

// A compaction pass the Runner started (goose's /compact over ACP reports
// real usage) adds its tokens and cost — they were spent — but isn't a turn
// of conversation. The flag is the pass's alone: the next turn counts.
func TestUsageTotalsCompactionTurnIsNotATurn(t *testing.T) {
	ctx := context.Background()
	base := datatest.Start(t)
	sess := newSession(t, base)
	data := dataclient.New(base, sess.ID)
	r := New(sess.ID, data, provider.NewRegistry())

	r.tally.setCompactTurn(true)
	r.observeUsage(provider.Event{Kind: provider.EventTurnEnd, Usage: &provider.Usage{InputTokens: 9_000, OutputTokens: 400, CostUSD: 0.02}})
	r.observeUsage(provider.Event{Kind: provider.EventTurnEnd, Usage: &provider.Usage{InputTokens: 100, OutputTokens: 10}})

	cur, err := data.Session(ctx, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	got := cur.UsageTotals
	if got == nil || got.Turns != 1 || got.Input != 9_100 || got.Output != 410 || got.CostUSD < 0.02-1e-9 || got.CostUSD > 0.02+1e-9 {
		t.Errorf("totals = %+v, want 1 turn, 9100 in / 410 out, $0.02", got)
	}
}

// session.usage_updated lands after the turn's usage writes — the totals
// are already on the record when it fires — and after session.agent_state's
// idle, which the /usage card used to refetch on too early.
func TestUsageUpdatedFollowsTheWrites(t *testing.T) {
	ctx := context.Background()
	base := datatest.Start(t)
	sess := newSession(t, base)
	data := dataclient.New(base, sess.ID)

	reg := provider.NewRegistry()
	if err := reg.Register(fake.New()); err != nil {
		t.Fatalf("register fake: %v", err)
	}
	r := New(sess.ID, data, reg)
	if err := r.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = r.Shutdown(ctx) })
	ch, unsub := r.Subscribe()
	defer unsub()
	if err := r.Send(ctx, "echo hi"); err != nil {
		t.Fatalf("send: %v", err)
	}
	drainText(t, ch)

	var updated, idle int64
	waitFor(t, "session.usage_updated", func() bool {
		evs, err := data.Events(ctx, 0, sess.ID, []string{string(contracts.SessionUsageUpdated), string(contracts.SessionAgentState)})
		if err != nil {
			t.Fatal(err)
		}
		for _, ev := range evs {
			switch ev.Type {
			case contracts.SessionUsageUpdated:
				updated = ev.Seq
			case contracts.SessionAgentState:
				var st contracts.AgentState
				if json.Unmarshal(ev.Payload, &st) == nil && st.State == contracts.AgentIdle {
					idle = ev.Seq
				}
			}
		}
		return updated != 0
	})
	if idle == 0 || updated < idle {
		t.Errorf("usage_updated seq %d, idle agent_state seq %d: want usage_updated after", updated, idle)
	}
	cur, err := data.Session(ctx, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if cur.UsageTotals == nil || cur.UsageTotals.Turns != 1 || cur.LastUsage == nil {
		t.Errorf("record when usage_updated fired: totals %+v, last usage %+v; want both written", cur.UsageTotals, cur.LastUsage)
	}
}
