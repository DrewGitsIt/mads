package api_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
)

func getReport(t *testing.T, srvURL, id string) contracts.SessionReport {
	t.Helper()
	resp, err := http.Get(srvURL + "/sessions/" + id + "/report")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("report status = %d, want 200", resp.StatusCode)
	}
	return decodeJSON[contracts.SessionReport](t, resp)
}

// A session with no process: asleep, its persisted totals and billing
// assembled per the billing rules, and crashed once its last word was a
// crash — until it wakes again.
func TestSessionReportDormant(t *testing.T) {
	srv, base := newTestServerWithData(t)
	ctx := context.Background()
	data := dataclient.New(base, "session")
	sess := contracts.Session{Name: "report", Provider: "claude-code", Activity: contracts.SessionInactive}
	if err := data.CreateSession(ctx, &sess); err != nil {
		t.Fatal(err)
	}
	if err := data.AddSessionUsage(ctx, sess.ID, contracts.UsageIncrement{
		Turns: 3, CostUSD: 1.25,
		Agent:     contracts.TokenCounts{Input: 300, Output: 90, CacheRead: 9000},
		Delegates: contracts.TokenCounts{Input: 40, Output: 10},
	}); err != nil {
		t.Fatal(err)
	}
	if err := data.SetSessionBilling(ctx, sess.ID, contracts.SessionBillingPatch{Mode: contracts.BillingUsage}); err != nil {
		t.Fatal(err)
	}

	rep := getReport(t, srv.URL, sess.ID)
	if rep.Health.State != contracts.HealthSleeping || rep.Health.ProcessStartedAt != nil || rep.Health.LastEventAt != nil {
		t.Errorf("health = %+v, want sleeping with no process fields", rep.Health)
	}
	if rep.Health.Harness != "claude-code" || rep.Health.PreflightOK == nil || !*rep.Health.PreflightOK {
		t.Errorf("health harness = %+v, want claude-code's preflight row", rep.Health)
	}
	if rep.Tokens == nil || rep.Tokens.Input != 340 || rep.Tokens.Turns != 3 || rep.Tokens.Delegates == nil || rep.Tokens.Delegates.Input != 40 {
		t.Errorf("tokens = %+v, want 340 in over 3 turns, 40 from delegates", rep.Tokens)
	}
	if rep.Billing.Mode != contracts.BillingUsage || rep.Cost == nil || rep.Cost.USD != 1.25 || rep.Limits != nil {
		t.Errorf("billing %q, cost %+v, limits %+v; want usage, $1.25, none", rep.Billing.Mode, rep.Cost, rep.Limits)
	}
	if rep.Context != nil {
		t.Errorf("context = %+v with no turn usage recorded, want none", rep.Context)
	}

	mustAppend(t, data, sess.ID, contracts.SessionCrashed)
	if rep := getReport(t, srv.URL, sess.ID); rep.Health.State != contracts.HealthCrashed || rep.Health.Since == nil {
		t.Errorf("after a crash: health = %+v, want crashed with its time", rep.Health)
	}
	if err := data.SetSessionActivity(ctx, sess.ID, contracts.SessionActive); err != nil {
		t.Fatal(err)
	}
	if err := data.SetSessionActivity(ctx, sess.ID, contracts.SessionInactive); err != nil {
		t.Fatal(err)
	}
	if rep := getReport(t, srv.URL, sess.ID); rep.Health.State != contracts.HealthSleeping {
		t.Errorf("after waking and sleeping again: state %q, want sleeping", rep.Health.State)
	}
}

// Plan limits belong to the login: a sleeping session's card shows what
// another session on the same provider and config dir heard since, never
// a different login's.
func TestSessionReportSharesAccountLimits(t *testing.T) {
	srv, base := newTestServerWithData(t)
	ctx := context.Background()
	data := dataclient.New(base, "session")
	weekly := func(pct float64) []contracts.RateLimit {
		return []contracts.RateLimit{{ID: "seven_day", Label: "Weekly", UsedPct: pct, ResetsAt: time.Now().Add(96 * time.Hour), Scope: "account"}}
	}
	mk := func(name, account string, pct float64, at time.Time) contracts.Session {
		t.Helper()
		sess := contracts.Session{Name: name, Provider: "claude-code", Activity: contracts.SessionInactive}
		if err := data.CreateSession(ctx, &sess); err != nil {
			t.Fatal(err)
		}
		if err := data.SetSessionBilling(ctx, sess.ID, contracts.SessionBillingPatch{
			Mode: contracts.BillingPlan, Limits: weekly(pct), At: at, Account: account,
		}); err != nil {
			t.Fatal(err)
		}
		return sess
	}
	now := time.Now()
	asleep := mk("asleep", "claude-code:/home/me/.claude", 12, now.Add(-21*time.Hour))
	mk("active", "claude-code:/home/me/.claude", 21, now.Add(-time.Minute))
	work := mk("work login", "claude-code:/home/me/.claude-work", 90, now)

	if rep := getReport(t, srv.URL, asleep.ID); len(rep.Limits) != 1 || rep.Limits[0].UsedPct != 21 || rep.LimitsAt == nil {
		t.Errorf("sleeping session: limits %+v at %v, want the active session's 21%%", rep.Limits, rep.LimitsAt)
	}
	if rep := getReport(t, srv.URL, work.ID); len(rep.Limits) != 1 || rep.Limits[0].UsedPct != 90 {
		t.Errorf("other login: limits %+v, want its own 90%%", rep.Limits)
	}
}

// A running session reports its process: the child's own start and last
// event, through GET /status.
func TestSessionReportRunning(t *testing.T) {
	srv, _ := newTestServerWithRealSession(t)
	resp := postJSON(t, srv.URL+"/sessions", map[string]string{"name": "report-live", "provider": "fake"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d, want 201", resp.StatusCode)
	}
	created := decodeJSON[struct {
		Session struct {
			ID string `json:"id"`
		} `json:"session"`
	}](t, resp)

	rep := getReport(t, srv.URL, created.Session.ID)
	if rep.Health.State != contracts.HealthIdle && rep.Health.State != contracts.HealthWorking {
		t.Errorf("state = %q, want an agent state", rep.Health.State)
	}
	if rep.Health.ProcessStartedAt == nil {
		t.Errorf("health = %+v, want the process start", rep.Health)
	}
	if rep.Billing.Mode != contracts.BillingUnknown || rep.Cost != nil || rep.Limits != nil {
		t.Errorf("fake provider: billing %+v, cost %+v, limits %+v; want unknown and neither", rep.Billing, rep.Cost, rep.Limits)
	}
}

func TestSessionReportNotFound(t *testing.T) {
	srv := newTestServer(t)
	resp, err := http.Get(srv.URL + "/sessions/ses_missing/report")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}
