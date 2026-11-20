package main

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"sort"
)

// Profile is a workload shape: how many records of each kind, and how many
// events of each type at what payload sizes. It never carries payload
// content — calibration writes only counts and sizes, so a profile made
// from the live database is safe to commit.
type Profile struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	// Seed makes a seeded database reproducible: same profile, same seed,
	// same rows, events and payload sizes (record-store timestamps aside).
	Seed uint64 `json:"seed"`

	Sessions int `json:"sessions"`
	// ActiveSessions are written with activity "active" (the Router's boot
	// sweep sets them inactive again: none has a live child).
	ActiveSessions int `json:"active_sessions"`
	// DelegateSessions is how many sessions own delegates; Delegates is the
	// total. Session 0 (the "parent") always owns the most.
	DelegateSessions int `json:"delegate_sessions"`
	Delegates        int `json:"delegates"`
	// RunningDelegates are left running on the parent session: the
	// Router's boot sweep concludes them (orphaned, ended now), so every
	// fresh stack shows them inside the rail's 10-minute fade window — the
	// row scenario 3 clicks.
	RunningDelegates int `json:"running_delegates"`
	Links            int `json:"links"`
	// WatchedRepos is the number of repos in the workspace (git work comes
	// later; today only the primary repo is used).
	WatchedRepos int `json:"watched_repos"`
	// SessionSkew is the Zipf exponent spreading session-scoped events over
	// sessions (session 0 heaviest). Calibrated from the top session's share.
	SessionSkew float64 `json:"session_skew"`

	// Events per type. Counts are totals in the seeded log, including the
	// events the record stores write themselves (session.created,
	// session.delegate_changed, link.created, ...).
	Events map[string]TypeStats `json:"events"`

	// Fixtures are the correctness-check states (plan §5, phase 5). The
	// seeder does not build them yet; nonzero values are reported and
	// ignored.
	Fixtures Fixtures `json:"fixtures"`
}

// TypeStats is one event type's volume and payload-size shape, in bytes of
// the stored payload JSON.
type TypeStats struct {
	Count int     `json:"count"`
	Mean  float64 `json:"mean_bytes"`
	P50   int     `json:"p50_bytes"`
	P95   int     `json:"p95_bytes"`
	Max   int     `json:"max_bytes"`
	// GlobalFrac is the share of this type's events with no session id.
	GlobalFrac float64 `json:"global_frac,omitempty"`
}

// Fixtures: see Profile.Fixtures.
type Fixtures struct {
	PendingPermissions   int `json:"pending_permissions"`
	QueuedMessages       int `json:"queued_messages"`
	ProvisioningSessions int `json:"provisioning_sessions"`
}

func loadProfile(path string) (*Profile, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var p Profile
	if err := json.Unmarshal(b, &p); err != nil {
		return nil, fmt.Errorf("profile %s: %w", path, err)
	}
	if err := p.validate(); err != nil {
		return nil, fmt.Errorf("profile %s: %w", path, err)
	}
	return &p, nil
}

func (p *Profile) validate() error {
	if p.Sessions < 2 {
		return fmt.Errorf("sessions: need at least 2 (scenario 2 switches between two)")
	}
	if p.DelegateSessions < 1 || p.DelegateSessions > p.Sessions {
		return fmt.Errorf("delegate_sessions: must be 1..sessions")
	}
	if p.Delegates < p.DelegateSessions {
		return fmt.Errorf("delegates: need at least one per delegate session")
	}
	if p.RunningDelegates > p.Delegates {
		return fmt.Errorf("running_delegates: more than delegates")
	}
	if p.Links > p.Sessions-1 {
		return fmt.Errorf("links: at most sessions-1 (one chain)")
	}
	return nil
}

func (p *Profile) write(path string) error {
	b, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}

// TotalEvents is the sum of every type's count.
func (p *Profile) TotalEvents() int {
	n := 0
	for _, s := range p.Events {
		n += s.Count
	}
	return n
}

// sortedTypes returns the profile's event types in a stable order, so a
// seed walks them identically every time (Go map order is random).
func (p *Profile) sortedTypes() []string {
	out := make([]string, 0, len(p.Events))
	for t := range p.Events {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

// Scaled returns a copy of p with every event count multiplied by f, and
// record counts by rf (record counts grow slower than the log).
func (p *Profile) Scaled(name string, f, rf float64) *Profile {
	q := *p
	q.Name = name
	q.Events = make(map[string]TypeStats, len(p.Events))
	for t, s := range p.Events {
		s.Count = int(math.Round(float64(s.Count) * f))
		q.Events[t] = s
	}
	scale := func(n int) int { return max(1, int(math.Round(float64(n)*rf))) }
	q.Sessions = scale(p.Sessions)
	q.ActiveSessions = int(math.Round(float64(p.ActiveSessions) * rf))
	q.DelegateSessions = min(scale(p.DelegateSessions), q.Sessions)
	q.Delegates = max(scale(p.Delegates), q.DelegateSessions)
	q.Links = min(scale(p.Links), q.Sessions-1)
	// The record stores write one of these per record: a scaled count can't
	// fall below what the records themselves produce.
	for typ, floor := range map[string]int{
		"session.created": q.Sessions, "session.agent_state": q.Sessions, "link.created": q.Links,
		"workspace.created": 1, "config.updated": 1, "session.delegate_changed": q.Delegates * 2,
	} {
		if s := q.Events[typ]; s.Count < floor {
			s.Count = floor
			q.Events[typ] = s
		}
	}
	return &q
}
