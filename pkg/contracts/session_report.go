package contracts

import "time"

// TokenCounts is a token tally split the way providers bill it: fresh input,
// output, and the two prompt-cache halves (Usage's
// CacheReadInputTokens/CacheCreationInputTokens).
type TokenCounts struct {
	Input      int `json:"input"`
	Output     int `json:"output"`
	CacheRead  int `json:"cache_read,omitempty"`
	CacheWrite int `json:"cache_write,omitempty"`
}

// IsZero reports whether the tally counts nothing at all.
func (c TokenCounts) IsZero() bool {
	return c.Input == 0 && c.Output == 0 && c.CacheRead == 0 && c.CacheWrite == 0
}

// Add adds o to c.
func (c *TokenCounts) Add(o TokenCounts) {
	c.Input += o.Input
	c.Output += o.Output
	c.CacheRead += o.CacheRead
	c.CacheWrite += o.CacheWrite
}

// UsageTotals is a session's running usage across every turn it has had,
// persisted on Session.UsageTotals so it outlives the process that counted
// it (sleep/wake, a Router restart). The token fields include the
// delegates' share; Delegates repeats that share on its own for the /usage
// card's "↳ delegates" row. Written only through the scoped
// POST /sessions/{id}/usage (store.Sessions.AddUsage), never by a
// full-record PUT.
type UsageTotals struct {
	TokenCounts
	// Turns counts the main agent's turns that reported any usage, plus
	// those on a provider that reports none at all.
	Turns int `json:"turns"`
	// CostUSD sums what the provider said each turn cost, whatever that
	// means for this session's billing — SessionReport decides whether to
	// show it (only when Billing.Mode is BillingUsage).
	CostUSD   float64      `json:"cost_usd,omitempty"`
	Delegates *TokenCounts `json:"delegates,omitempty"`
}

// UsageIncrement is one POST /sessions/{id}/usage body: what to fold into
// UsageTotals. Agent is the main agent's share and Delegates the
// subagents'; both land in the totals, and Delegates in its subtotal too.
type UsageIncrement struct {
	Turns     int         `json:"turns,omitempty"`
	CostUSD   float64     `json:"cost_usd,omitempty"`
	Agent     TokenCounts `json:"agent"`
	Delegates TokenCounts `json:"delegates"`
}

// IsZero reports whether folding inc would change nothing.
func (inc UsageIncrement) IsZero() bool {
	return inc.Turns == 0 && inc.CostUSD == 0 && inc.Agent.IsZero() && inc.Delegates.IsZero()
}

// Merge adds o to inc — two increments as one.
func (inc *UsageIncrement) Merge(o UsageIncrement) {
	inc.Turns += o.Turns
	inc.CostUSD += o.CostUSD
	inc.Agent.Add(o.Agent)
	inc.Delegates.Add(o.Delegates)
}

// Add folds inc into t.
func (t *UsageTotals) Add(inc UsageIncrement) {
	t.Turns += inc.Turns
	t.CostUSD += inc.CostUSD
	t.TokenCounts.Add(inc.Agent)
	t.TokenCounts.Add(inc.Delegates)
	if !inc.Delegates.IsZero() {
		if t.Delegates == nil {
			t.Delegates = &TokenCounts{}
		}
		t.Delegates.Add(inc.Delegates)
	}
}

// Billing modes: how the account a session runs on pays for it. Only
// claude-code says (system/init apiKeySource); every other provider is
// BillingUnknown.
const (
	// BillingPlan is a subscription login: usage draws down rate-limit
	// windows (Limits), and a reported cost is a list-price estimate, not
	// money spent.
	BillingPlan = "plan"
	// BillingUsage is an API key: every token is billed, so the reported
	// cost is the spend.
	BillingUsage   = "usage"
	BillingUnknown = "unknown"
)

// RateLimit is one plan rate-limit window as the harness last reported it
// (claude-code's rate_limit_event unifiedWindows).
type RateLimit struct {
	// ID is the harness's window key ("five_hour", "seven_day").
	ID    string `json:"id"`
	Label string `json:"label"`
	// UsedPct is how much of the window is spent, 0-100.
	UsedPct  float64   `json:"used_pct"`
	ResetsAt time.Time `json:"resets_at,omitzero"`
	// Scope is what the window limits: always "account" today — the
	// windows are shared by every session on the login, not this one's.
	Scope string `json:"scope"`
}

// SessionBilling is the latest billing snapshot the session's harness
// reported, persisted on Session.Billing. Written only through the scoped
// PUT /sessions/{id}/billing (store.Sessions.SetBilling).
type SessionBilling struct {
	Mode   string      `json:"mode"`
	Limits []RateLimit `json:"limits,omitempty"`
	// LimitsAt is when Limits was reported.
	LimitsAt *time.Time `json:"limits_at,omitempty"`
}

// SessionBillingPatch is PUT /sessions/{id}/billing's body: an empty Mode
// or a nil Limits leaves that half as it was, since the harness reports
// them on different lines.
type SessionBillingPatch struct {
	Mode   string      `json:"mode,omitempty"`
	Limits []RateLimit `json:"limits,omitempty"`
	At     time.Time   `json:"at,omitzero"`
}

// Session health states (SessionHealth.State): the agent states while a
// process runs, and why there is none when it doesn't.
const (
	HealthWorking  = AgentWorking
	HealthIdle     = AgentIdle
	HealthSleeping = "sleeping"
	HealthCrashed  = "crashed"
)

// SessionStatus is a Session Service child's GET /status: what only the
// running process knows about itself.
type SessionStatus struct {
	StartedAt time.Time `json:"started_at"`
	// LastEventAt is when the harness last sent anything; zero before it
	// has.
	LastEventAt time.Time `json:"last_event_at,omitzero"`
}

// SessionHealth is SessionReport's always-present section.
type SessionHealth struct {
	// State is HealthWorking or HealthIdle while a process runs,
	// HealthSleeping or HealthCrashed when none does — and then the
	// process fields below are absent.
	State string `json:"state"`
	// Since is when State began, when known.
	Since            *time.Time `json:"since,omitempty"`
	ProcessStartedAt *time.Time `json:"process_started_at,omitempty"`
	LastEventAt      *time.Time `json:"last_event_at,omitempty"`
	// Harness is the provider name; Version and PreflightOK are its
	// preflight row's verdict, absent when preflight doesn't know it.
	Harness     string `json:"harness"`
	Version     string `json:"version,omitempty"`
	PreflightOK *bool  `json:"preflight_ok,omitempty"`
}

// ReportContext is the context window's occupancy as of the last turn.
type ReportContext struct {
	Used int `json:"used"`
	// Window is the provider-reported window size; absent when the
	// provider never said, leaving the client to its static table.
	Window int `json:"window,omitempty"`
	// CanCompact is whether "Compact now" can be offered: the harness
	// compacts natively. Compaction is never emulated.
	CanCompact bool `json:"can_compact"`
}

// ReportTokens is the session's cumulative token use (UsageTotals).
type ReportTokens struct {
	TokenCounts
	Turns     int          `json:"turns"`
	Delegates *TokenCounts `json:"delegates,omitempty"`
}

// ReportBilling says which spend section, if any, the report carries.
type ReportBilling struct {
	Mode string `json:"mode"`
}

// ReportCost is a usage-billed session's spend.
type ReportCost struct {
	USD float64 `json:"usd"`
	// Basis is how the figure was arrived at: "list_price", the
	// harness's own estimate at published API rates.
	Basis string `json:"basis"`
}

// SessionReport is GET /sessions/{id}/report: one session's health and
// usage, the /usage card's whole input. Health and Billing are always
// present; every other section is omitted when unknown. Limits appear only
// when Billing.Mode is BillingPlan and Cost only when it is BillingUsage —
// a cost on a session whose billing is unknown is dropped, since nobody can
// say whether it is money spent.
type SessionReport struct {
	SessionID string         `json:"session_id"`
	Health    SessionHealth  `json:"health"`
	Context   *ReportContext `json:"context,omitempty"`
	Tokens    *ReportTokens  `json:"tokens,omitempty"`
	Billing   ReportBilling  `json:"billing"`
	Limits    []RateLimit    `json:"limits,omitempty"`
	Cost      *ReportCost    `json:"cost,omitempty"`
}

// rateLimitLabels names the windows claude-code reports.
var rateLimitLabels = map[string]string{
	"five_hour": "5-hour",
	"seven_day": "Weekly",
}

// RateLimitLabel is the display label for a rate-limit window id, the id
// itself for one nobody has named.
func RateLimitLabel(id string) string {
	if l, ok := rateLimitLabels[id]; ok {
		return l
	}
	return id
}

// ReportInputs is everything BuildSessionReport reads besides the record:
// the facts only the Router has (the process, preflight, the provider's
// compaction support).
type ReportInputs struct {
	Health     SessionHealth
	CanCompact bool
	Now        time.Time
}

// BuildSessionReport assembles a SessionReport from a session record. The
// rules that decide which sections appear live here, not in the handler,
// so the contract tests pin them.
func BuildSessionReport(sess Session, in ReportInputs) SessionReport {
	rep := SessionReport{
		SessionID: sess.ID,
		Health:    in.Health,
		Billing:   ReportBilling{Mode: BillingUnknown},
	}
	if u := sess.LastUsage; u != nil {
		used := u.ContextTokens
		if used == 0 {
			used = u.InputTokens + u.CacheReadInputTokens + u.CacheCreationInputTokens
		}
		rep.Context = &ReportContext{Used: used, Window: u.ContextWindow, CanCompact: in.CanCompact}
	}
	if t := sess.UsageTotals; t != nil && (t.Turns > 0 || !t.TokenCounts.IsZero()) {
		rep.Tokens = &ReportTokens{TokenCounts: t.TokenCounts, Turns: t.Turns}
		if t.Delegates != nil && !t.Delegates.IsZero() {
			d := *t.Delegates
			rep.Tokens.Delegates = &d
		}
	}
	if b := sess.Billing; b != nil && (b.Mode == BillingPlan || b.Mode == BillingUsage) {
		rep.Billing.Mode = b.Mode
	}
	switch rep.Billing.Mode {
	case BillingPlan:
		// A window whose reset has passed says nothing true any more: its
		// usage started over, and the harness hasn't reported since.
		for _, l := range sess.Billing.Limits {
			if !l.ResetsAt.IsZero() && !l.ResetsAt.After(in.Now) {
				continue
			}
			rep.Limits = append(rep.Limits, l)
		}
	case BillingUsage:
		if t := sess.UsageTotals; t != nil && t.CostUSD > 0 {
			rep.Cost = &ReportCost{USD: t.CostUSD, Basis: "list_price"}
		}
	}
	return rep
}
