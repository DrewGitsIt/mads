package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/preflight"
	"github.com/drewkidwell/mads/providers/provider"
)

// reportPreflightTTL is how long the session report reuses one preflight
// run for its harness version. A run spawns every harness's --version; a
// /usage card refreshing on every turn end mustn't do that each time, and
// a harness upgrade only takes effect at the next process start anyway.
const reportPreflightTTL = 5 * time.Minute

// reportStatusTimeout bounds the report's GET /status to a running child:
// the card should render without the process fields rather than wait on a
// wedged one.
const reportStatusTimeout = 2 * time.Second

// preflightMemo caches one preflight run for the session report.
type preflightMemo struct {
	mu     sync.Mutex
	at     time.Time
	checks []preflight.Check
}

func (m *preflightMemo) get(ctx context.Context, c preflight.Checker) []preflight.Check {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.checks != nil && time.Since(m.at) < reportPreflightTTL {
		return m.checks
	}
	checks, err := c.Run(ctx)
	if err != nil {
		return m.checks
	}
	m.checks, m.at = checks, time.Now()
	return checks
}

// handleSessionReport answers GET /sessions/{id}/report: the session's
// health and usage in one read, the /usage card's whole input. The
// section rules live in contracts.BuildSessionReport; this gathers what
// only the Router knows — whether a process runs, what it last said, the
// harness's preflight verdict.
func (s *server) handleSessionReport(w http.ResponseWriter, r *http.Request) {
	sess, err := s.data.Session(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	rep := contracts.BuildSessionReport(sess, contracts.ReportInputs{
		Health:     s.sessionHealth(r.Context(), sess),
		CanCompact: s.canCompact(sess),
		Now:        time.Now(),
	})
	writeJSON(w, http.StatusOK, rep)
}

// canCompact says whether the session's harness compacts natively, so the
// card can offer "Compact now": its provider matrix's BehaviorCompaction.
// Compaction is never emulated, so a provider this Router doesn't have
// registered (or no registry at all) can't be offered it.
func (s *server) canCompact(sess contracts.Session) bool {
	if s.providers == nil || sess.Provider == "" {
		return false
	}
	p, err := s.providers.Get(sess.Provider)
	if err != nil {
		return false
	}
	return p.Matrix()[provider.BehaviorCompaction].Works()
}

// sessionHealth builds the report's health section. A running process
// reports the agent's state off the record (store.Sessions.SetAgentState
// keeps it current) and its own start and last event (the child's
// GET /status). Without one the session is asleep — or crashed, when the
// last thing that happened to it was a crash.
func (s *server) sessionHealth(ctx context.Context, sess contracts.Session) contracts.SessionHealth {
	h := contracts.SessionHealth{Harness: sess.Provider}
	s.harnessPreflight(ctx, &h)
	addr, running := "", false
	if s.sup != nil {
		addr, running = s.sup.SessionAddr(sess.ID)
	}
	if !running {
		h.State, h.Since = s.dormantState(ctx, sess.ID)
		return h
	}
	h.State = contracts.HealthIdle
	if a := sess.AgentState; a != nil {
		if a.State == contracts.AgentWorking {
			h.State = contracts.HealthWorking
		}
		if !a.Since.IsZero() {
			since := a.Since
			h.Since = &since
		}
	}
	sctx, cancel := context.WithTimeout(ctx, reportStatusTimeout)
	defer cancel()
	var st contracts.SessionStatus
	if err := s.fetchChildJSON(sctx, addr, "/status", &st); err == nil {
		if !st.StartedAt.IsZero() {
			h.ProcessStartedAt = &st.StartedAt
		}
		if !st.LastEventAt.IsZero() {
			h.LastEventAt = &st.LastEventAt
		}
	}
	return h
}

// harnessPreflight fills the harness's version and verdict from its
// preflight row ("provider:<name>"), when preflight knows the provider.
func (s *server) harnessPreflight(ctx context.Context, h *contracts.SessionHealth) {
	if h.Harness == "" || s.preflight == nil {
		return
	}
	for _, c := range s.reportPreflight.get(ctx, s.preflight) {
		if name, ok := strings.CutPrefix(c.Name, "provider:"); !ok || name != h.Harness {
			continue
		}
		ok := c.Healthy
		h.Version, h.PreflightOK = c.Version, &ok
		return
	}
}

// dormantPages bounds dormantState's walk of the lifecycle log. Each page is
// a full server-side batch (the Data Service's GET /events cap, 500) of one
// session's crash and sleep/wake events only — filtered in SQL by session
// and type — which only a very long-lived session ever fills more than
// once.
//
// The walk is forward from seq 0 because that is the only read the log
// offers: GET /events has since/session/types but no descending order, no
// "latest of type", and no upper-bound-only query, and the session record
// keeps no crash stamp to read instead. The cost is one round trip per 500
// lifecycle events. Past the cap (25,000 such events — thousands of
// sleep/wake cycles) the answer is computed from the oldest events and goes
// stale; a "latest N, descending" read on the Data Service would make this
// one bounded query, and is the fix if that ever matters.
const dormantPages = 50

// dormantState reads why a session with no process has none: HealthCrashed
// when its latest crash (session.crashed) came after its latest wake, else
// HealthSleeping — with when that began, when the log says. A crash is
// followed by the supervisor marking the record inactive, so the sleep that
// comes right after one is the crash's, not a fresh sleep.
func (s *server) dormantState(ctx context.Context, id string) (string, *time.Time) {
	types := []string{string(contracts.SessionCrashed), string(contracts.SessionActivityChanged)}
	var crashed, woke, slept time.Time
	var since int64
	for range dormantPages {
		envs, err := s.data.Events(ctx, since, id, types)
		if err != nil || len(envs) == 0 {
			break
		}
		for _, env := range envs {
			since = env.Seq
			switch env.Type {
			case contracts.SessionCrashed:
				crashed = env.Timestamp
			case contracts.SessionActivityChanged:
				// The store's own flip says {"from","to"}; the runner's
				// shutdown announcement says {"activity"}.
				var p struct {
					To       contracts.SessionActivity `json:"to"`
					Activity contracts.SessionActivity `json:"activity"`
				}
				_ = json.Unmarshal(env.Payload, &p)
				if p.To == "" {
					p.To = p.Activity
				}
				switch p.To {
				case contracts.SessionActive:
					woke = env.Timestamp
				case contracts.SessionInactive:
					slept = env.Timestamp
				}
			}
		}
	}
	if !crashed.IsZero() && !crashed.Before(woke) {
		return contracts.HealthCrashed, &crashed
	}
	if slept.IsZero() {
		return contracts.HealthSleeping, nil
	}
	return contracts.HealthSleeping, &slept
}
