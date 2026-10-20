package runner

import (
	"context"
	"log"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/providers/provider"
)

// processStartedAt is when this Session Service process started — the
// session's "up since" (GET /status). The harness starts with it.
var processStartedAt = time.Now().UTC()

// usageTally is what the session's usage section of the /usage card is
// built from, as the runner sees it go by: the turn totals not yet written
// (contracts.UsageIncrement — a failed write is kept and folded into the
// next one rather than lost), the delegates' calls since the last turn end,
// and the billing snapshot last persisted, so an unchanged report isn't
// written again.
type usageTally struct {
	mu      sync.Mutex
	pending contracts.UsageIncrement
	// lastCall is each delegate's latest API call by spawning tool-use id.
	// The CLI repeats one call's usage on a line per content block, so a
	// report equal to the last one is the same call, not a new one (the
	// rule delegates.Tracker.Usage dedupes its record by).
	lastCall map[string]contracts.TokenCounts
	billing  contracts.SessionBilling
	// compactTurn is set while the in-flight turn is a compaction pass the
	// Runner requested (beginCompactLocked), and consumed by that turn's
	// end (countTurn).
	compactTurn bool

	// lastEvent is when the provider last sent anything, in Unix nanos.
	lastEvent atomic.Int64
}

// observeUsage folds one provider event into the tally: every event stamps
// lastEvent, a delegate's API call adds to the delegates' share, a turn end
// writes the turn through, and an account report updates the billing
// snapshot.
func (r *Runner) observeUsage(ev provider.Event) {
	r.tally.lastEvent.Store(time.Now().UnixNano())
	switch {
	case ev.Kind == provider.EventSubagentUsage && ev.Usage != nil:
		r.noteDelegateCall(ev.ParentToolUseID, ev.Usage)
	case ev.Kind == provider.EventTurnEnd:
		r.countTurn(ev.Usage)
	case ev.Kind == provider.EventAccount && ev.Account != nil:
		r.persistAccount(ev.Account)
	}
}

func tokenCounts(u *provider.Usage) contracts.TokenCounts {
	return contracts.TokenCounts{
		Input:      u.InputTokens,
		Output:     u.OutputTokens,
		CacheRead:  u.CacheReadInputTokens,
		CacheWrite: u.CacheCreationInputTokens,
	}
}

// noteDelegateCall adds one delegate API call to the pending delegates'
// share, unless it is a repeat of the call already counted.
func (r *Runner) noteDelegateCall(spawnID string, u *provider.Usage) {
	c := tokenCounts(u)
	if c.IsZero() {
		return
	}
	t := &r.tally
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.lastCall == nil {
		t.lastCall = map[string]contracts.TokenCounts{}
	}
	if prev, ok := t.lastCall[spawnID]; ok && prev == c {
		return
	}
	t.lastCall[spawnID] = c
	t.pending.Delegates.Add(c)
}

// setCompactTurn marks (or unmarks) the in-flight turn as a compaction pass.
func (t *usageTally) setCompactTurn(on bool) {
	t.mu.Lock()
	t.compactTurn = on
	t.mu.Unlock()
}

// countTurn adds one main-agent turn to the totals and writes them, along
// with the delegates' calls since the last one, then tells any open /usage
// card the report moved (publishUsageUpdated). A compaction pass isn't a
// turn of conversation: one whose usage counts no tokens (claude-code's
// /compact) adds only its cost, and one the Runner started (compactTurn —
// acprpc's goose /compact turn reports real token usage) adds its tokens
// and cost but no turn. Its tokens are counted because they were spent: the
// summarizer read the whole context, and totals that dropped them would
// understate what the session used. A provider that reports no usage at all
// still has its turns counted.
func (r *Runner) countTurn(u *provider.Usage) {
	t := &r.tally
	t.mu.Lock()
	compact := t.compactTurn
	t.compactTurn = false
	switch u {
	case nil:
		if !compact {
			t.pending.Turns++
		}
	default:
		if c := tokenCounts(u); !c.IsZero() {
			if !compact {
				t.pending.Turns++
			}
			t.pending.Agent.Add(c)
		}
		t.pending.CostUSD += u.CostUSD
	}
	t.mu.Unlock()
	r.flushUsage()
	r.publishUsageUpdated()
}

// publishUsageUpdated appends session.usage_updated once a turn's usage
// writes have landed: LastUsage (persistTurnEnd, which the pump runs ahead
// of this) and the running totals (flushUsage). session.turn_ended and the
// idle session.agent_state go out before either write, so a reader that
// refetched on those alone saw the previous turn's numbers. Fired even when
// a write failed — the report is as fresh as it will get until the next
// turn, and a stale-but-current refetch is harmless.
func (r *Runner) publishUsageUpdated() {
	r.appendConversation(contracts.SessionUsageUpdated, struct{}{})
}

// flushUsage writes the pending increment, reporting whether it wrote one.
// Best-effort like persistTurnEnd, but a failed increment is kept for the
// next attempt — a total that drops a turn would stay wrong forever, unlike
// a stale LastUsage.
//
// Known gap: the write isn't idempotent. A request that fails client-side
// after the Data Service committed it (a timeout on the response, a dropped
// connection) is kept and re-sent, counting that increment twice. Fixing it
// needs an increment id the store remembers per session, and a retried
// increment kept apart rather than merged into the next turn's (Merge would
// change what the id names) — the Data Service has no request-id pattern to
// reuse, and a rare over-count of a display total is the lesser error than
// the dropped turn the retry exists to prevent.
func (r *Runner) flushUsage() bool {
	if r.data == nil {
		return false
	}
	t := &r.tally
	t.mu.Lock()
	inc := t.pending
	t.pending = contracts.UsageIncrement{}
	t.mu.Unlock()
	if inc.IsZero() {
		return false
	}
	if err := r.data.AddSessionUsage(context.Background(), r.sessionID, inc); err != nil {
		log.Printf("runner: add usage totals (kept for the next turn): %v", err)
		t.mu.Lock()
		t.pending.Merge(inc)
		t.mu.Unlock()
		return false
	}
	return true
}

// persistAccount writes the harness's billing report onto the record when
// it changes what was last written. Held under the tally's lock across the
// write so two reports can't land out of order.
func (r *Runner) persistAccount(a *provider.AccountStatus) {
	if r.data == nil {
		return
	}
	t := &r.tally
	t.mu.Lock()
	defer t.mu.Unlock()
	patch := contracts.SessionBillingPatch{At: time.Now().UTC()}
	if a.BillingMode != "" && a.BillingMode != t.billing.Mode {
		patch.Mode = a.BillingMode
	}
	if a.Limits != nil {
		limits := make([]contracts.RateLimit, 0, len(a.Limits))
		for _, w := range a.Limits {
			limits = append(limits, contracts.RateLimit{
				ID: w.ID, Label: contracts.RateLimitLabel(w.ID), UsedPct: w.UsedPct, ResetsAt: w.ResetsAt, Scope: "account",
			})
		}
		if !slices.Equal(limits, t.billing.Limits) {
			patch.Limits = limits
		}
	}
	if patch.Mode == "" && patch.Limits == nil {
		return
	}
	if err := r.data.SetSessionBilling(context.Background(), r.sessionID, patch); err != nil {
		log.Printf("runner: persist billing: %v", err)
		return
	}
	if patch.Mode != "" {
		t.billing.Mode = patch.Mode
	}
	if patch.Limits != nil {
		t.billing.Limits = patch.Limits
	}
	r.publishUsageUpdated()
}

// Status is what GET /status reports: the process's start and the
// provider's last word.
func (r *Runner) Status() contracts.SessionStatus {
	st := contracts.SessionStatus{StartedAt: processStartedAt}
	if n := r.tally.lastEvent.Load(); n != 0 {
		st.LastEventAt = time.Unix(0, n).UTC()
	}
	return st
}
