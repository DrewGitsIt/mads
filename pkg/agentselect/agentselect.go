// Package agentselect implements auto-select (spec §9): capability filter →
// quadrant (persona) filter → deterministic ranking (failure exclusion →
// label affinity → extraneous-capability count → track record → recency →
// alphabetical), with every selection logged to the event log so retros
// (§7) can evaluate ranking quality after the fact. The extraneous-capability
// tier ranks the fewest-extraneous-capabilities qualified candidate highest
// (owner decision: additive-only capability composition — see
// EffectiveCapabilities — is only acceptable if lean agents actually beat
// kitchen-sink generalists, keeping context small).
package agentselect

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
)

// Request describes the task/stage being staffed.
type Request struct {
	TaskID      string
	SessionID   string
	Stage       string // stage name; "" for non-pipeline callers (e.g. agent picker)
	Title       string
	Description string
	Labels      []string

	// Required capabilities the candidate agent must declare a superset of
	// (spec §9: fast set-intersection). Populated by the caller — this
	// package takes no position on where "required capabilities" live on
	// the task/template schema; that's the caller's job.
	RequiredTools      []string
	RequiredMCPServers []string
	RequiredSkills     []string
}

// ScoreBreakdown is one registry agent's ranking detail, preserved verbatim
// in the logged agent.selected event. Every agent in the registry gets one
// entry — Eliminated explains an early filter drop; entries that reached
// ranking have real scores.
type ScoreBreakdown struct {
	Agent string `json:"agent"`
	// Eliminated is "" (survived), "quadrant", "failure", or
	// "capability (missing <names>)" naming every required tool/MCP
	// server/skill the agent doesn't declare, so a "no candidate" outcome
	// tells the user what to add to which agent instead of a bare failure.
	Eliminated    string `json:"eliminated,omitempty"`
	LabelAffinity int    `json:"label_affinity,omitempty"`
	// Extraneous is how many capabilities the agent declares beyond the
	// request's effective required set (spec §9 ranking tier: fewer wins).
	Extraneous   int        `json:"extraneous,omitempty"`
	TrackRecord  float64    `json:"track_record,omitempty"` // [0,1]; 0.5 = no history (neutral, spec §9)
	LastSelected *time.Time `json:"last_selected,omitempty"`
	Selected     bool       `json:"selected,omitempty"`
}

// Selection is the full record of one auto-select decision — logged as the
// agent.selected event payload.
type Selection struct {
	TaskID     string             `json:"task_id"`
	Stage      string             `json:"stage,omitempty"`
	Quadrant   contracts.Quadrant `json:"quadrant"`
	Agent      string             `json:"agent,omitempty"` // "" when no candidate survived
	Candidates []ScoreBreakdown   `json:"candidates"`
}

// ErrNoCandidates is returned when every registry agent was eliminated by the
// capability filter, the quadrant filter, or failure exclusion.
var ErrNoCandidates = errors.New("agentselect: no candidate agents available")

// outcomePayload mirrors the agent.outcome_recorded event payload.
type outcomePayload struct {
	Agent   string   `json:"agent"`
	Stage   string   `json:"stage"`
	Labels  []string `json:"labels"`
	Outcome string   `json:"outcome"`
}

// Selector runs auto-select against a live Data Service.
type Selector struct {
	Data   *dataclient.Client
	Scorer Scorer // nil → LexicalScorer{}
}

// New builds a Selector with the default LexicalScorer.
func New(data *dataclient.Client) *Selector {
	return &Selector{Data: data}
}

func (s *Selector) scorer() Scorer {
	if s.Scorer != nil {
		return s.Scorer
	}
	return LexicalScorer{}
}

// Select runs capability filter → quadrant filter → failure exclusion →
// ranking, logs the decision as agent.selected (always — even when no
// candidate survives, so misses are visible to retros), and returns it.
// Returns ErrNoCandidates (with the logged Selection still populated) when
// every agent was eliminated.
func (s *Selector) Select(ctx context.Context, req Request) (Selection, error) {
	agents, err := s.Data.Agents(ctx)
	if err != nil {
		return Selection{}, err
	}

	sel := Selection{
		TaskID:   req.TaskID,
		Stage:    req.Stage,
		Quadrant: Classify(req),
	}

	survivors := make([]contracts.Agent, 0, len(agents))
	for _, a := range agents {
		if reason := eliminationReason(req, sel.Quadrant, a); reason != "" {
			sel.Candidates = append(sel.Candidates, ScoreBreakdown{Agent: a.Name, Eliminated: reason})
			continue
		}
		survivors = append(survivors, a)
	}

	failed, err := s.failedAgents(ctx, req)
	if err != nil {
		return Selection{}, err
	}
	remaining := make([]contracts.Agent, 0, len(survivors))
	for _, a := range survivors {
		if failed[a.Name] {
			sel.Candidates = append(sel.Candidates, ScoreBreakdown{Agent: a.Name, Eliminated: "failure"})
			continue
		}
		remaining = append(remaining, a)
	}

	if len(remaining) == 0 {
		sortByName(sel.Candidates)
		if err := s.logSelection(ctx, req, sel); err != nil {
			return sel, err
		}
		return sel, ErrNoCandidates
	}

	trackRecord, err := s.trackRecord(ctx, req)
	if err != nil {
		return Selection{}, err
	}
	recency, err := s.recency(ctx)
	if err != nil {
		return Selection{}, err
	}

	scorer := s.scorer()
	scored := make([]ScoreBreakdown, 0, len(remaining))
	for _, a := range remaining {
		sb := ScoreBreakdown{
			Agent:         a.Name,
			LabelAffinity: scorer.LabelAffinity(req, a),
			Extraneous:    extraneousCount(req, a),
			TrackRecord:   0.5,
		}
		if tr, ok := trackRecord[a.Name]; ok {
			sb.TrackRecord = tr
		}
		if t, ok := recency[a.Name]; ok {
			t := t
			sb.LastSelected = &t
		}
		scored = append(scored, sb)
	}

	rankCandidates(scored)
	scored[0].Selected = true
	sel.Agent = scored[0].Agent
	sel.Candidates = append(sel.Candidates, scored...)

	if err := s.logSelection(ctx, req, sel); err != nil {
		return sel, err
	}
	return sel, nil
}

func (s *Selector) logSelection(ctx context.Context, req Request, sel Selection) error {
	payload, err := json.Marshal(sel)
	if err != nil {
		return err
	}
	return s.Data.Append(ctx, &contracts.Envelope{
		Type:      contracts.AgentSelected,
		SessionID: req.SessionID,
		TaskID:    req.TaskID,
		Payload:   payload,
	})
}

// RecordOutcome logs a stage result under the agent that ran it. Callers
// (a future pipeline-engine wiring, or tests) invoke this once a stage
// completes or fails. taskID/stage should match a prior Select.
func (s *Selector) RecordOutcome(ctx context.Context, taskID, sessionID, stage, agent string, labels []string, success bool) error {
	outcome := "failure"
	if success {
		outcome = "success"
	}
	payload, err := json.Marshal(outcomePayload{
		Agent:   agent,
		Stage:   stage,
		Labels:  labels,
		Outcome: outcome,
	})
	if err != nil {
		return err
	}
	return s.Data.Append(ctx, &contracts.Envelope{
		Type:      contracts.AgentOutcomeRecorded,
		SessionID: sessionID,
		TaskID:    taskID,
		Payload:   payload,
	})
}
