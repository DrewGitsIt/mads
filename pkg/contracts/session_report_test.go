package contracts_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/golden"
)

var reportNow = time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)

func reportTime(t time.Time) *time.Time { return &t }

func runningHealth(state, harness, version string) contracts.SessionHealth {
	ok := true
	return contracts.SessionHealth{
		State:            state,
		Since:            reportTime(reportNow.Add(-134 * time.Second)),
		ProcessStartedAt: reportTime(reportNow.Add(-92 * time.Minute)),
		LastEventAt:      reportTime(reportNow.Add(-3 * time.Second)),
		Harness:          harness,
		Version:          version,
		PreflightOK:      &ok,
	}
}

// The three sessions the mockup's Ledger shows (docs/mockups/
// session-commands.html, section 1): a claude-code subscription login, a
// claude-code API key, and goose on a local model.
func reportCases() map[string]struct {
	sess contracts.Session
	in   contracts.ReportInputs
} {
	plan := contracts.Session{
		ID:       "ses_plan",
		Provider: "claude-code",
		LastUsage: &contracts.Usage{
			InputTokens: 12, OutputTokens: 900, CacheReadInputTokens: 140_000,
			ContextTokens: 142_300, ContextWindow: 200_000, Model: "claude-opus-5", CostUSD: 0.42,
		},
		UsageTotals: &contracts.UsageTotals{
			TokenCounts: contracts.TokenCounts{Input: 228_000, Output: 48_600, CacheRead: 982_000, CacheWrite: 31_000},
			Turns:       37,
			// A plan login's list-price estimate: never shown.
			CostUSD:   18.5,
			Delegates: &contracts.TokenCounts{Input: 41_000, Output: 6_100, CacheRead: 120_000},
		},
		Billing: &contracts.SessionBilling{
			Mode: contracts.BillingPlan,
			Limits: []contracts.RateLimit{
				{ID: "five_hour", Label: "5-hour", UsedPct: 62, ResetsAt: reportNow.Add(2 * time.Hour), Scope: "account"},
				{ID: "seven_day", Label: "Weekly", UsedPct: 31, ResetsAt: reportNow.Add(70 * time.Hour), Scope: "account"},
			},
			LimitsAt: reportTime(reportNow.Add(-time.Minute)),
		},
	}
	api := contracts.Session{
		ID:       "ses_api",
		Provider: "claude-code",
		LastUsage: &contracts.Usage{
			InputTokens: 9, OutputTokens: 400, CacheReadInputTokens: 43_000,
			ContextTokens: 44_000, ContextWindow: 200_000, Model: "claude-sonnet-5",
		},
		UsageTotals: &contracts.UsageTotals{
			TokenCounts: contracts.TokenCounts{Input: 69_000, Output: 12_200, CacheRead: 241_000},
			Turns:       9,
			CostUSD:     0.91,
		},
		Billing: &contracts.SessionBilling{Mode: contracts.BillingUsage},
	}
	local := contracts.Session{
		ID:       "ses_local",
		Provider: "goose",
		// goose reports occupancy as a token count, with no per-call split.
		LastUsage: &contracts.Usage{InputTokens: 18_100, OutputTokens: 300, ContextWindow: 131_072, Model: "qwen3.8"},
		UsageTotals: &contracts.UsageTotals{
			TokenCounts: contracts.TokenCounts{Input: 212_000, Output: 9_400},
			Turns:       8,
			// An ACP-reported cost with no billing mode: dropped.
			CostUSD: 0.07,
		},
	}
	sleeping := contracts.SessionHealth{
		State: contracts.HealthSleeping, Since: reportTime(reportNow.Add(-6 * time.Minute)), Harness: "goose", Version: "1.31.0",
	}
	return map[string]struct {
		sess contracts.Session
		in   contracts.ReportInputs
	}{
		"session_report_plan.json":  {plan, contracts.ReportInputs{Health: runningHealth(contracts.HealthWorking, "claude-code", "2.1.290"), CanCompact: true, Now: reportNow}},
		"session_report_api.json":   {api, contracts.ReportInputs{Health: runningHealth(contracts.HealthIdle, "claude-code", "2.1.290"), CanCompact: true, Now: reportNow}},
		"session_report_local.json": {local, contracts.ReportInputs{Health: sleeping, Now: reportNow}},
	}
}

// Producer side: the report each kind of session assembles into, pinned.
func TestSessionReportProducerMatchesGolden(t *testing.T) {
	for name, c := range reportCases() {
		t.Run(name, func(t *testing.T) {
			got, err := json.MarshalIndent(contracts.BuildSessionReport(c.sess, c.in), "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			golden.Assert(t, name, append(got, '\n'))
		})
	}
}

// Consumer side: each golden parses back into the same report.
func TestSessionReportConsumerParsesGolden(t *testing.T) {
	for name, c := range reportCases() {
		t.Run(name, func(t *testing.T) {
			var got contracts.SessionReport
			if err := json.Unmarshal(golden.Read(t, name), &got); err != nil {
				t.Fatalf("consumer failed to parse golden: %v", err)
			}
			want := contracts.BuildSessionReport(c.sess, c.in)
			if compact(t, mustMarshal(t, got)) != compact(t, mustMarshal(t, want)) {
				t.Errorf("parsed report differs:\n got %+v\nwant %+v", got, want)
			}
		})
	}
}

// The billing switch: limits only for a plan, cost only when usage-billed.
func TestSessionReportBillingRules(t *testing.T) {
	cases := reportCases()
	plan := contracts.BuildSessionReport(cases["session_report_plan.json"].sess, cases["session_report_plan.json"].in)
	if plan.Billing.Mode != contracts.BillingPlan || len(plan.Limits) != 2 || plan.Cost != nil {
		t.Errorf("plan: billing %q, %d limits, cost %+v; want plan, 2 limits, no cost", plan.Billing.Mode, len(plan.Limits), plan.Cost)
	}
	api := contracts.BuildSessionReport(cases["session_report_api.json"].sess, cases["session_report_api.json"].in)
	if api.Billing.Mode != contracts.BillingUsage || api.Limits != nil || api.Cost == nil || api.Cost.USD != 0.91 {
		t.Errorf("api key: billing %q, limits %+v, cost %+v; want usage, no limits, $0.91", api.Billing.Mode, api.Limits, api.Cost)
	}
	local := contracts.BuildSessionReport(cases["session_report_local.json"].sess, cases["session_report_local.json"].in)
	if local.Billing.Mode != contracts.BillingUnknown || local.Limits != nil || local.Cost != nil {
		t.Errorf("local: billing %q, limits %+v, cost %+v; want unknown and neither", local.Billing.Mode, local.Limits, local.Cost)
	}
	if local.Context == nil || local.Context.Used != 18_100 || local.Context.CanCompact {
		t.Errorf("local context = %+v, want the input count as occupancy and no compaction", local.Context)
	}

	// Limits left on the record after the mode flipped to usage say nothing.
	flipped := cases["session_report_plan.json"].sess
	flipped.Billing = &contracts.SessionBilling{Mode: contracts.BillingUsage, Limits: flipped.Billing.Limits}
	if rep := contracts.BuildSessionReport(flipped, contracts.ReportInputs{Now: reportNow}); rep.Limits != nil || rep.Cost == nil {
		t.Errorf("usage-billed with stale limits: limits %+v, cost %+v; want no limits and the cost", rep.Limits, rep.Cost)
	}

	// A window whose reset has passed is stale and left out.
	later := contracts.BuildSessionReport(cases["session_report_plan.json"].sess, contracts.ReportInputs{Now: reportNow.Add(3 * time.Hour)})
	if len(later.Limits) != 1 || later.Limits[0].ID != "seven_day" {
		t.Errorf("after the 5-hour reset: limits %+v, want the weekly one alone", later.Limits)
	}
}

// Plan limits are the account's: a session asleep since its last reading
// shows a fresher one heard by another session on the same login.
func TestSessionReportAccountLimits(t *testing.T) {
	const acct = "claude-code:/Users/me/.claude"
	old, fresh := reportNow.Add(-21*time.Hour), reportNow.Add(-10*time.Second)
	weekly := func(pct float64) []contracts.RateLimit {
		return []contracts.RateLimit{{ID: "seven_day", Label: "Weekly", UsedPct: pct, ResetsAt: reportNow.Add(96 * time.Hour), Scope: "account"}}
	}
	sleeping := contracts.Session{ID: "ses_asleep", Billing: &contracts.SessionBilling{
		Mode: contracts.BillingPlan, Limits: weekly(12), LimitsAt: &old, Account: acct,
	}}
	active := contracts.Session{ID: "ses_active", Billing: &contracts.SessionBilling{
		Mode: contracts.BillingPlan, Limits: weekly(21), LimitsAt: &fresh, Account: acct,
	}}
	otherLogin := contracts.Session{ID: "ses_other", Billing: &contracts.SessionBilling{
		Mode: contracts.BillingPlan, Limits: weekly(90), LimitsAt: reportTime(reportNow), Account: "claude-code:/Users/me/.claude-work",
	}}
	apiKey := contracts.Session{ID: "ses_api", Billing: &contracts.SessionBilling{
		Mode: contracts.BillingUsage, Limits: weekly(50), LimitsAt: reportTime(reportNow), Account: acct,
	}}
	all := []contracts.Session{sleeping, active, otherLogin, apiKey}

	shared := contracts.NewestAccountBilling(all, acct)
	if shared == nil || shared.Limits[0].UsedPct != 21 {
		t.Fatalf("newest for the account = %+v, want the active session's 21%%", shared)
	}
	if contracts.NewestAccountBilling(all, "") != nil {
		t.Error("an empty account matched sessions")
	}

	rep := contracts.BuildSessionReport(sleeping, contracts.ReportInputs{Now: reportNow, AccountBilling: shared})
	if len(rep.Limits) != 1 || rep.Limits[0].UsedPct != 21 || rep.LimitsAt == nil || !rep.LimitsAt.Equal(fresh) {
		t.Errorf("sleeping session's report: limits %+v at %v; want 21%% heard %v", rep.Limits, rep.LimitsAt, fresh)
	}
	// The session's own reading wins when it is the newer one.
	rep = contracts.BuildSessionReport(active, contracts.ReportInputs{Now: reportNow, AccountBilling: sleeping.Billing})
	if rep.Limits[0].UsedPct != 21 || !rep.LimitsAt.Equal(fresh) {
		t.Errorf("active session's report took an older reading: %+v at %v", rep.Limits, rep.LimitsAt)
	}
}

// A session that hasn't finished a turn has only its health.
func TestSessionReportOmitsUnknownSections(t *testing.T) {
	rep := contracts.BuildSessionReport(contracts.Session{ID: "ses_new", Provider: "kiro-cli"}, contracts.ReportInputs{
		Health: contracts.SessionHealth{State: contracts.HealthIdle, Harness: "kiro-cli"},
	})
	got := compact(t, mustMarshal(t, rep))
	want := `{"session_id":"ses_new","health":{"state":"idle","harness":"kiro-cli"},"billing":{"mode":"unknown"}}`
	if got != want {
		t.Errorf("report = %s\nwant %s", got, want)
	}
}

// Totals: delegates land in the totals and in their own subtotal.
func TestUsageTotalsAdd(t *testing.T) {
	var tot contracts.UsageTotals
	tot.Add(contracts.UsageIncrement{Turns: 1, CostUSD: 0.5, Agent: contracts.TokenCounts{Input: 10, Output: 5, CacheRead: 100}})
	if tot.Delegates != nil {
		t.Fatalf("no delegate usage yet, got subtotal %+v", tot.Delegates)
	}
	tot.Add(contracts.UsageIncrement{Turns: 1, Agent: contracts.TokenCounts{Input: 1}, Delegates: contracts.TokenCounts{Input: 20, Output: 2, CacheWrite: 7}})
	want := contracts.UsageTotals{
		TokenCounts: contracts.TokenCounts{Input: 31, Output: 7, CacheRead: 100, CacheWrite: 7},
		Turns:       2,
		CostUSD:     0.5,
		Delegates:   &contracts.TokenCounts{Input: 20, Output: 2, CacheWrite: 7},
	}
	if compact(t, mustMarshal(t, tot)) != compact(t, mustMarshal(t, want)) {
		t.Errorf("totals = %+v, want %+v", tot, want)
	}
}
