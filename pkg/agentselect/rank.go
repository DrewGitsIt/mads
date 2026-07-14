package agentselect

import (
	"context"
	"encoding/json"
	"sort"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
)

// Scorer computes label affinity — keyword overlap between the request and
// a candidate agent. It is the only ranking tier behind a swappable
// interface; failure exclusion, track record, and recency are event-log
// mechanics, not heuristics, so they stay fixed in Selector.
type Scorer interface {
	LabelAffinity(req Request, agent contracts.Agent) int
}

// LexicalScorer is the default: token-set overlap between
// (req.Labels + Title + Description) and (agent.Tags + agent.Description).
// Deterministic, no external calls.
type LexicalScorer struct{}

func (LexicalScorer) LabelAffinity(req Request, agent contracts.Agent) int {
	reqParts := append(append([]string{}, req.Labels...), req.Title, req.Description)
	agentParts := append(append([]string{}, agent.Tags...), agent.Description)

	reqTokens := tokenSet(reqParts)
	agentTokens := tokenSet(agentParts)

	count := 0
	for t := range reqTokens {
		if _, ok := agentTokens[t]; ok {
			count++
		}
	}
	return count
}

func tokenSet(parts []string) map[string]struct{} {
	set := make(map[string]struct{})
	for _, p := range parts {
		for _, tok := range tokenize(p) {
			set[tok] = struct{}{}
		}
	}
	return set
}

// failedAgents returns the set of agent names with a recorded failure on
// req.TaskID (scope is the whole task, not just req.Stage — diversity on
// retry across stages, per spec §9).
func (s *Selector) failedAgents(ctx context.Context, req Request) (map[string]bool, error) {
	events, err := s.Data.Events(ctx, 0, req.SessionID, []string{string(contracts.AgentOutcomeRecorded)})
	if err != nil {
		return nil, err
	}
	failed := map[string]bool{}
	for _, ev := range events {
		if ev.TaskID != req.TaskID {
			continue
		}
		var p outcomePayload
		if err := json.Unmarshal(ev.Payload, &p); err != nil {
			continue
		}
		if p.Outcome == "failure" {
			failed[p.Agent] = true
		}
	}
	return failed, nil
}

// trackRecord aggregates success rate per agent over all history whose
// labels share at least one entry with req.Labels (unfiltered when
// req.Labels is empty). Agents absent from the result get the neutral 0.5
// default at the call site — cold start never disqualifies (spec §9).
func (s *Selector) trackRecord(ctx context.Context, req Request) (map[string]float64, error) {
	events, err := s.Data.Events(ctx, 0, "", []string{string(contracts.AgentOutcomeRecorded)})
	if err != nil {
		return nil, err
	}
	type tally struct{ successes, total int }
	tallies := map[string]*tally{}
	for _, ev := range events {
		var p outcomePayload
		if err := json.Unmarshal(ev.Payload, &p); err != nil {
			continue
		}
		if len(req.Labels) > 0 && !sharesLabel(req.Labels, p.Labels) {
			continue
		}
		t := tallies[p.Agent]
		if t == nil {
			t = &tally{}
			tallies[p.Agent] = t
		}
		t.total++
		if p.Outcome == "success" {
			t.successes++
		}
	}
	out := make(map[string]float64, len(tallies))
	for agent, t := range tallies {
		out[agent] = float64(t.successes) / float64(t.total)
	}
	return out, nil
}

func sharesLabel(want, have []string) bool {
	set := make(map[string]struct{}, len(want))
	for _, w := range want {
		set[w] = struct{}{}
	}
	for _, h := range have {
		if _, ok := set[h]; ok {
			return true
		}
	}
	return false
}

// recency tracks the most recent agent.selected timestamp per agent, across
// all history. Agents never selected before are absent from the result
// (treated as oldest by rankCandidates).
func (s *Selector) recency(ctx context.Context) (map[string]time.Time, error) {
	events, err := s.Data.Events(ctx, 0, "", []string{string(contracts.AgentSelected)})
	if err != nil {
		return nil, err
	}
	last := map[string]time.Time{}
	for _, ev := range events {
		var sel Selection
		if err := json.Unmarshal(ev.Payload, &sel); err != nil {
			continue
		}
		if sel.Agent == "" {
			continue
		}
		if t, ok := last[sel.Agent]; !ok || ev.Timestamp.After(t) {
			last[sel.Agent] = ev.Timestamp
		}
	}
	return last, nil
}

// rankCandidates sorts surviving agents by the deterministic tiers (spec
// §9): label affinity, then track record, then recency, all descending,
// with alphabetical agent name as the final tiebreak. Every field the
// comparator reads lives on ScoreBreakdown itself, never re-indexed from a
// parallel slice — sorting permutes positions, so indexing back into
// another slice inside the comparator would reintroduce a classic
// index-after-swap bug.
func rankCandidates(scored []ScoreBreakdown) {
	sort.SliceStable(scored, func(i, j int) bool {
		a, b := scored[i], scored[j]
		if a.LabelAffinity != b.LabelAffinity {
			return a.LabelAffinity > b.LabelAffinity
		}
		if a.TrackRecord != b.TrackRecord {
			return a.TrackRecord > b.TrackRecord
		}
		at, bt := timeOrZero(a.LastSelected), timeOrZero(b.LastSelected)
		if !at.Equal(bt) {
			return at.After(bt)
		}
		return a.Agent < b.Agent
	})
}

func timeOrZero(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return *t
}

// sortByName orders eliminated candidates alphabetically — used only on the
// no-survivors path, so the logged event is reproducible.
func sortByName(candidates []ScoreBreakdown) {
	sort.SliceStable(candidates, func(i, j int) bool {
		return candidates[i].Agent < candidates[j].Agent
	})
}
