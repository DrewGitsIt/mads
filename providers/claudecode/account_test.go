package claudecode

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/drewkidwell/mads/providers/provider"
)

// captureAccountEvents collects the EventAccount events a capture's lines
// yield. Lines that don't decode into a wireLine are skipped, the way
// handleLine skips them (stream-auto-mode.jsonl's permission_denied line
// carries a string "message").
func captureAccountEvents(t *testing.T, raw []byte) []provider.Event {
	t.Helper()
	var out []provider.Event
	for _, ln := range strings.Split(string(raw), "\n") {
		var l wireLine
		if json.Unmarshal([]byte(ln), &l) != nil {
			continue
		}
		for _, ev := range l.events(false, true) {
			if ev.Kind == provider.EventAccount {
				out = append(out, ev)
			}
		}
	}
	return out
}

// Every real capture is a subscription login: its init line says
// apiKeySource "none", which is billing mode "plan", and its
// rate_limit_event lines carry both unified windows.
func TestAccountEventsFromCaptures(t *testing.T) {
	names, err := filepath.Glob(filepath.Join("testdata", "stream-*.jsonl"))
	if err != nil || len(names) == 0 {
		t.Fatalf("no stream captures: %v", err)
	}
	for _, path := range names {
		name := filepath.Base(path)
		t.Run(name, func(t *testing.T) {
			raw, _ := os.ReadFile(path)
			var plan, limits int
			for _, ev := range captureAccountEvents(t, raw) {
				a := ev.Account
				switch {
				case a.BillingMode != "":
					if a.BillingMode != provider.BillingPlan || a.Limits != nil {
						t.Errorf("init account = %+v, want plan mode alone", a)
					}
					plan++
				case a.Limits != nil:
					if len(a.Limits) != 2 || a.Limits[0].ID != "five_hour" || a.Limits[1].ID != "seven_day" {
						t.Fatalf("limits = %+v, want five_hour then seven_day", a.Limits)
					}
					for _, l := range a.Limits {
						if l.UsedPct < 0 || l.UsedPct > 100 || l.ResetsAt.IsZero() {
							t.Errorf("window %+v: want a 0-100 pct and a reset time", l)
						}
					}
					limits++
				default:
					t.Errorf("account event says nothing: %+v", a)
				}
			}
			if want := strings.Count(string(raw), `"apiKeySource":"none"`); plan != want || want == 0 {
				t.Errorf("plan events = %d, want one per init line (%d)", plan, want)
			}
			if want := strings.Count(string(raw), `"type":"rate_limit_event"`); limits != want {
				t.Errorf("limit events = %d, want one per rate_limit_event (%d)", limits, want)
			}
		})
	}
}

// The exact mapping, against the first capture's line: utilization 0-1
// becomes a percentage, resetsAt epoch seconds a time.
func TestRateLimitEventMapping(t *testing.T) {
	l := parseWireLine(t, `{"type":"rate_limit_event","rate_limit_info":{"status":"allowed","resetsAt":1791324600,"rateLimitType":"five_hour","unifiedWindows":{"seven_day":{"utilization":0.22,"resetsAt":1791799200},"five_hour":{"utilization":0.06,"resetsAt":1791324600}}},"session_id":"e5d9"}`)
	evs := l.events(false, true)
	if len(evs) != 1 || evs[0].Kind != provider.EventAccount {
		t.Fatalf("want one EventAccount, got %+v", evs)
	}
	got := evs[0].Account.Limits
	want := []provider.RateLimitWindow{
		{ID: "five_hour", UsedPct: 6, ResetsAt: time.Unix(1791324600, 0).UTC()},
		{ID: "seven_day", UsedPct: 22, ResetsAt: time.Unix(1791799200, 0).UTC()},
	}
	if len(got) != len(want) {
		t.Fatalf("limits = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i].ID != want[i].ID || math.Abs(got[i].UsedPct-want[i].UsedPct) > 1e-9 || !got[i].ResetsAt.Equal(want[i].ResetsAt) {
			t.Errorf("limit %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// An API-key login names where its key came from; anything but "none" is
// usage-billed. An init line from a CLI too old to say stays silent.
func TestInitBillingMode(t *testing.T) {
	for _, tc := range []struct{ line, want string }{
		{`{"type":"system","subtype":"init","session_id":"s","model":"m","apiKeySource":"ANTHROPIC_API_KEY"}`, provider.BillingUsage},
		{`{"type":"system","subtype":"init","session_id":"s","model":"m","apiKeySource":"none"}`, provider.BillingPlan},
		{`{"type":"system","subtype":"init","session_id":"s","model":"m"}`, ""},
	} {
		l := parseWireLine(t, tc.line)
		evs := l.events(false, true)
		got := ""
		if len(evs) == 1 && evs[0].Account != nil {
			got = evs[0].Account.BillingMode
		} else if len(evs) > 1 {
			t.Fatalf("%s: want at most one event, got %+v", tc.line, evs)
		}
		if got != tc.want {
			t.Errorf("%s: billing mode %q, want %q", tc.line, got, tc.want)
		}
	}
}

// total_cost_usd is a running total for the CLI process: each turn end must
// carry only its own share, so the turns sum to the final figure — the
// /compact turn included, which counts no tokens but still costs.
func TestStreamSessionTurnCostIsPerTurn(t *testing.T) {
	s := &streamSession{events: make(chan provider.Event, 4096), announced: make(chan string, 1), stdin: discardWriteCloser{}}
	raw, err := os.ReadFile(filepath.Join("testdata", "stream-compact.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	for _, ln := range strings.Split(string(raw), "\n") {
		if strings.TrimSpace(ln) != "" {
			s.handleLine([]byte(ln))
		}
	}
	ends := drainTurnEnds(s)
	if len(ends) != 3 {
		t.Fatalf("want 3 turn ends, got %d", len(ends))
	}
	want := []float64{0.0177513, 0.0230199 - 0.0177513, 0.035079 - 0.0230199}
	sum := 0.0
	for i, ev := range ends {
		if ev.Usage == nil || math.Abs(ev.Usage.CostUSD-want[i]) > 1e-9 {
			t.Errorf("turn %d cost = %+v, want %v", i, ev.Usage, want[i])
			continue
		}
		sum += ev.Usage.CostUSD
	}
	if math.Abs(sum-0.035079) > 1e-9 {
		t.Errorf("turn costs sum to %v, want the final running total 0.035079", sum)
	}
}

// total_cost_usd is the conversation's running total, not the process's: a
// --resume restores it from the transcript's last cost-state line. Probed
// on claude 2.1.285 — one turn reported 0.0183063; the resumed process's
// one turn reported 0.0208454, the restored total plus its own ~0.0025. The
// resumed session starts from that line, so its first turn end carries its
// own share, not the whole conversation's again.
func TestResumedStreamSessionStartsFromTheRestoredCost(t *testing.T) {
	p := testProvider(t)
	const ref = "native-resumed"
	proj := filepath.Join(p.cfg.TranscriptDir, "-tmp-project")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	transcript := strings.Join([]string{
		`{"type":"user","sessionId":"` + ref + `","message":{"role":"user","content":"say ok"}}`,
		`{"type":"cost-state","sessionId":"` + ref + `","totalCostUSD":0.0101}`,
		`{"type":"cost-state","sessionId":"` + ref + `","totalCostUSD":0.0183063}`,
		`{"type":"cost-state","sessionId":"some-other-session","totalCostUSD":9.5}`,
		``,
	}, "\n")
	if err := os.WriteFile(filepath.Join(proj, ref+".jsonl"), []byte(transcript), 0o644); err != nil {
		t.Fatal(err)
	}

	sess, err := p.startStream(context.Background(), provider.StartOptions{Mode: provider.ModeACP, Resume: ref})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sess.Close() }()
	ss := sess.(*streamSession)
	ss.mu.Lock()
	cost := ss.turnCostLocked(0.0208454)
	ss.mu.Unlock()
	if math.Abs(cost-(0.0208454-0.0183063)) > 1e-9 {
		t.Errorf("first resumed turn cost = %v, want its own %v", cost, 0.0208454-0.0183063)
	}

	// A fresh session has nothing restored: its first total is all its own.
	fresh, err := p.startStream(context.Background(), provider.StartOptions{Mode: provider.ModeACP})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = fresh.Close() }()
	fs := fresh.(*streamSession)
	fs.mu.Lock()
	cost = fs.turnCostLocked(0.0183063)
	fs.mu.Unlock()
	if cost != 0.0183063 {
		t.Errorf("fresh session's first turn cost = %v, want 0.0183063", cost)
	}
}

func TestAccountKey(t *testing.T) {
	cases := []struct {
		env  []string
		want string
	}{
		{[]string{"HOME=/Users/me"}, "claude-code:/Users/me/.claude"},
		{[]string{"HOME=/Users/me", "CLAUDE_CONFIG_DIR=/Users/me/.claude-work/"}, "claude-code:/Users/me/.claude-work"},
		// The last assignment wins, as it does for the process.
		{[]string{"CLAUDE_CONFIG_DIR=/a", "HOME=/Users/me", "CLAUDE_CONFIG_DIR=/b"}, "claude-code:/b"},
	}
	for _, c := range cases {
		if got := accountKey(c.env); got != c.want {
			t.Errorf("accountKey(%q) = %q, want %q", c.env, got, c.want)
		}
	}
}
