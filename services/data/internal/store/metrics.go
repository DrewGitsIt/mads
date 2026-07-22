package store

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
)

// Metrics answers GET /metrics/* — the dashboard's data (spec §8), computed
// as pure queries over the event log plus the task and capability tables.
// There is no separate metrics store (spec §15): every view here is derived,
// read-only, and needs no outbox transaction.
type Metrics struct {
	log   *Log
	tasks *Tasks
	caps  *Registry[contracts.Capability]
	// now is the clock, swappable in tests.
	now func() time.Time
}

// NewMetrics wires the metrics views to their underlying stores.
func NewMetrics(log *Log, tasks *Tasks, caps *Registry[contracts.Capability]) *Metrics {
	return &Metrics{log: log, tasks: tasks, caps: caps, now: func() time.Time { return time.Now().UTC() }}
}

// runStaleAfter is how long an open pipeline run may go without any event
// (of any type) before it's reclassified "interrupted" instead of left
// "running" forever. Set well above the engine's default LeaseTTL (10min,
// services/pipeline/engine/engine.go) — a live engine heartbeats faster than
// that, so silence past this margin means the process is gone, not just slow.
const runStaleAfter = 30 * time.Minute

type usagePayload struct {
	Model        string  `json:"model"`
	InputTokens  int64   `json:"input_tokens"`
	OutputTokens int64   `json:"output_tokens"`
	CostUSD      float64 `json:"cost_usd"`
}

// Cost answers GET /metrics/cost: usage.recorded events sliced by day/model,
// by session, and by pipeline run (spec §8).
func (m *Metrics) Cost(ctx context.Context, sessionID string) (contracts.CostMetrics, error) {
	envs, err := m.log.Query(ctx, Filter{SessionID: sessionID, Types: []string{string(contracts.UsageRecorded)}})
	if err != nil {
		return contracts.CostMetrics{}, fmt.Errorf("metrics: cost: %w", err)
	}

	type dayModel struct {
		day   time.Time
		model string
	}
	byDay := map[dayModel]*contracts.CostPoint{}
	bySession := map[string]float64{}

	for _, env := range envs {
		var p usagePayload
		if err := json.Unmarshal(env.Payload, &p); err != nil {
			return contracts.CostMetrics{}, fmt.Errorf("metrics: cost: decode seq %d: %w", env.Seq, err)
		}
		day := env.Timestamp.UTC().Truncate(24 * time.Hour)
		key := dayModel{day: day, model: p.Model}
		cp, ok := byDay[key]
		if !ok {
			cp = &contracts.CostPoint{Day: day, Model: p.Model}
			byDay[key] = cp
		}
		cp.InputTokens += p.InputTokens
		cp.OutputTokens += p.OutputTokens
		cp.CostUSD += p.CostUSD

		bySession[env.SessionID] += p.CostUSD
	}

	// Empty slices, not nil: these marshal as [] over the wire — the UI
	// consumes them directly and a JSON null is not an array (found by
	// driving the Dashboard against a store with no usage events).
	out := contracts.CostMetrics{
		ByDay:         []contracts.CostPoint{},
		BySession:     []contracts.CostBySession{},
		ByPipelineRun: []contracts.CostByPipelineRun{},
	}
	for _, cp := range byDay {
		out.ByDay = append(out.ByDay, *cp)
	}
	sort.Slice(out.ByDay, func(i, j int) bool {
		if !out.ByDay[i].Day.Equal(out.ByDay[j].Day) {
			return out.ByDay[i].Day.Before(out.ByDay[j].Day)
		}
		return out.ByDay[i].Model < out.ByDay[j].Model
	})

	for sid, cost := range bySession {
		out.BySession = append(out.BySession, contracts.CostBySession{SessionID: sid, CostUSD: cost})
	}
	sort.Slice(out.BySession, func(i, j int) bool { return out.BySession[i].SessionID < out.BySession[j].SessionID })

	sessions, err := m.sessionsFor(ctx, sessionID, contracts.UsageRecorded)
	if err != nil {
		return contracts.CostMetrics{}, err
	}
	for _, sid := range sessions {
		runs, err := m.PipelineRuns(ctx, sid)
		if err != nil {
			return contracts.CostMetrics{}, err
		}
		for _, run := range runs {
			if run.CostUSD > 0 {
				out.ByPipelineRun = append(out.ByPipelineRun, contracts.CostByPipelineRun{
					SessionID: run.SessionID, RunSeq: run.RunSeq, CostUSD: run.CostUSD,
				})
			}
		}
	}

	return out, nil
}

// sessionsFor returns the sessions to consider for an event type: just
// sessionID when it's set, otherwise every distinct session that ever
// emitted that event type.
func (m *Metrics) sessionsFor(ctx context.Context, sessionID string, t contracts.EventType) ([]string, error) {
	if sessionID != "" {
		return []string{sessionID}, nil
	}
	return m.distinctSessions(ctx, t)
}

type agentSelectedPayload struct {
	Agent string `json:"agent"`
}

type agentOutcomePayload struct {
	Agent   string `json:"agent"`
	Outcome string `json:"outcome"`
}

// AgentLeaderboard answers GET /metrics/agents: each agent's usage count,
// success/failure tally, and derived success rate (spec §8).
func (m *Metrics) AgentLeaderboard(ctx context.Context) ([]contracts.AgentLeaderboardEntry, error) {
	selected, err := m.log.Query(ctx, Filter{Types: []string{string(contracts.AgentSelected)}})
	if err != nil {
		return nil, fmt.Errorf("metrics: agent leaderboard: %w", err)
	}
	outcomes, err := m.log.Query(ctx, Filter{Types: []string{string(contracts.AgentOutcomeRecorded)}})
	if err != nil {
		return nil, fmt.Errorf("metrics: agent leaderboard: %w", err)
	}

	entries := map[string]*contracts.AgentLeaderboardEntry{}
	get := func(agent string) *contracts.AgentLeaderboardEntry {
		e, ok := entries[agent]
		if !ok {
			e = &contracts.AgentLeaderboardEntry{Agent: agent}
			entries[agent] = e
		}
		return e
	}

	for _, env := range selected {
		var p agentSelectedPayload
		if err := json.Unmarshal(env.Payload, &p); err != nil {
			return nil, fmt.Errorf("metrics: agent leaderboard: decode seq %d: %w", env.Seq, err)
		}
		if p.Agent == "" {
			continue
		}
		get(p.Agent).UsageCount++
	}
	for _, env := range outcomes {
		var p agentOutcomePayload
		if err := json.Unmarshal(env.Payload, &p); err != nil {
			return nil, fmt.Errorf("metrics: agent leaderboard: decode seq %d: %w", env.Seq, err)
		}
		if p.Agent == "" {
			continue
		}
		e := get(p.Agent)
		switch p.Outcome {
		case "success":
			e.Successes++
		case "failure":
			e.Failures++
		}
	}

	out := make([]contracts.AgentLeaderboardEntry, 0, len(entries))
	for _, e := range entries {
		if total := e.Successes + e.Failures; total > 0 {
			e.SuccessRate = float64(e.Successes) / float64(total)
		}
		e.Retries = e.Failures
		out = append(out, *e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Agent < out[j].Agent })
	return out, nil
}

// TaskOutcomes answers GET /metrics/tasks: the current status distribution
// for confirmed (non-draft) tasks (spec §8).
func (m *Metrics) TaskOutcomes(ctx context.Context, sessionID string) (contracts.TaskOutcomes, error) {
	excludeDrafts := false
	tasks, err := m.tasks.List(ctx, TaskQuery{SessionID: sessionID, Draft: &excludeDrafts})
	if err != nil {
		return contracts.TaskOutcomes{}, fmt.Errorf("metrics: task outcomes: %w", err)
	}
	out := contracts.TaskOutcomes{Counts: map[contracts.TaskStatus]int{}, Total: len(tasks)}
	for _, t := range tasks {
		out.Counts[t.Status]++
	}
	if out.Total > 0 {
		out.CompletionRate = float64(out.Counts[contracts.StatusDone]) / float64(out.Total)
	}
	return out, nil
}

type capabilityInvokedPayload struct {
	Capability string `json:"capability"`
}

// CapabilityUsage answers GET /metrics/capabilities: every registered
// capability's invocation count, zero-filled for never-used ones so they're
// visible as removal candidates (spec §8).
func (m *Metrics) CapabilityUsage(ctx context.Context) ([]contracts.CapabilityUsage, error) {
	caps, err := m.caps.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("metrics: capability usage: %w", err)
	}
	envs, err := m.log.Query(ctx, Filter{Types: []string{string(contracts.CapabilityInvoked)}})
	if err != nil {
		return nil, fmt.Errorf("metrics: capability usage: %w", err)
	}
	counts := map[string]int{}
	for _, env := range envs {
		var p capabilityInvokedPayload
		if err := json.Unmarshal(env.Payload, &p); err != nil {
			return nil, fmt.Errorf("metrics: capability usage: decode seq %d: %w", env.Seq, err)
		}
		counts[p.Capability]++
	}

	out := make([]contracts.CapabilityUsage, 0, len(caps))
	for _, c := range caps {
		out = append(out, contracts.CapabilityUsage{
			Capability:  c.Name,
			Type:        c.Type,
			Invocations: counts[c.Name],
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Capability < out[j].Capability })
	return out, nil
}

type pipelineCompletedPayload struct {
	Outcome string `json:"outcome"`
	Done    int    `json:"done"`
	Blocked int    `json:"blocked"`
	Skipped int    `json:"skipped"`
}

// PipelineRuns answers GET /metrics/pipelines: reconstructed pipeline runs
// (spec §8) — the span between a pipeline.started event and its next
// terminal event within one session.
func (m *Metrics) PipelineRuns(ctx context.Context, sessionID string) ([]contracts.PipelineRun, error) {
	sessions, err := m.sessionsFor(ctx, sessionID, contracts.PipelineStarted)
	if err != nil {
		return nil, err
	}

	var out []contracts.PipelineRun
	for _, sid := range sessions {
		runs, err := m.pipelineRunsForSession(ctx, sid)
		if err != nil {
			return nil, err
		}
		out = append(out, runs...)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartedAt.Before(out[j].StartedAt) })
	return out, nil
}

func (m *Metrics) pipelineRunsForSession(ctx context.Context, sessionID string) ([]contracts.PipelineRun, error) {
	envs, err := m.log.Query(ctx, Filter{
		SessionID: sessionID,
		Types: []string{
			string(contracts.PipelineStarted),
			string(contracts.PipelineCompleted),
			string(contracts.PipelineCancelled),
			string(contracts.PipelineFailed),
		},
	})
	if err != nil {
		return nil, fmt.Errorf("metrics: pipeline runs: %w", err)
	}

	type openRun struct {
		run    contracts.PipelineRun
		endSeq int64 // 0 while still open
	}
	var (
		runs []openRun
		open *openRun
	)
	closeOpen := func() {
		if open != nil {
			runs = append(runs, *open)
			open = nil
		}
	}
	for _, env := range envs {
		switch env.Type {
		case contracts.PipelineStarted:
			closeOpen()
			open = &openRun{run: contracts.PipelineRun{
				SessionID: sessionID,
				RunSeq:    env.Seq,
				StartedAt: env.Timestamp,
				Outcome:   "running",
			}}
		case contracts.PipelineCompleted:
			if open == nil {
				continue
			}
			var p pipelineCompletedPayload
			if err := json.Unmarshal(env.Payload, &p); err != nil {
				return nil, fmt.Errorf("metrics: pipeline runs: decode seq %d: %w", env.Seq, err)
			}
			open.run.EndedAt = env.Timestamp
			open.run.Outcome = p.Outcome
			open.run.TasksDone = p.Done
			open.run.TasksBlocked = p.Blocked
			open.run.TasksSkipped = p.Skipped
			open.endSeq = env.Seq
			closeOpen()
		case contracts.PipelineCancelled:
			if open == nil {
				continue
			}
			open.run.EndedAt = env.Timestamp
			open.run.Outcome = "cancelled"
			open.endSeq = env.Seq
			closeOpen()
		case contracts.PipelineFailed:
			if open == nil {
				continue
			}
			open.run.EndedAt = env.Timestamp
			open.run.Outcome = "failed"
			open.endSeq = env.Seq
			closeOpen()
		}
	}
	if open != nil {
		lastSeq, lastAt, err := m.lastActivity(ctx, sessionID, open.run.RunSeq-1)
		if err != nil {
			return nil, err
		}
		if lastSeq != 0 && m.now().Sub(lastAt) > runStaleAfter {
			open.run.Outcome = "interrupted"
			open.run.EndedAt = lastAt
			open.endSeq = lastSeq
		}
	}
	closeOpen()

	out := make([]contracts.PipelineRun, len(runs))
	for i, r := range runs {
		run := r.run
		if err := m.fillRunUsageAndAgents(ctx, &run, r.endSeq); err != nil {
			return nil, err
		}
		out[i] = run
	}
	return out, nil
}

func (m *Metrics) fillRunUsageAndAgents(ctx context.Context, run *contracts.PipelineRun, endSeq int64) error {
	filter := Filter{
		SessionID: run.SessionID,
		Since:     run.RunSeq - 1,
		Until:     endSeq,
		Types:     []string{string(contracts.UsageRecorded), string(contracts.AgentSelected)},
	}
	envs, err := m.log.Query(ctx, filter)
	if err != nil {
		return fmt.Errorf("metrics: pipeline runs: usage range: %w", err)
	}
	var cost float64
	agentSet := map[string]struct{}{}
	for _, env := range envs {
		switch env.Type {
		case contracts.UsageRecorded:
			var p usagePayload
			if err := json.Unmarshal(env.Payload, &p); err != nil {
				return fmt.Errorf("metrics: pipeline runs: decode seq %d: %w", env.Seq, err)
			}
			cost += p.CostUSD
		case contracts.AgentSelected:
			var p agentSelectedPayload
			if err := json.Unmarshal(env.Payload, &p); err != nil {
				return fmt.Errorf("metrics: pipeline runs: decode seq %d: %w", env.Seq, err)
			}
			if p.Agent != "" {
				agentSet[p.Agent] = struct{}{}
			}
		}
	}
	run.CostUSD = cost
	if len(agentSet) > 0 {
		agents := make([]string, 0, len(agentSet))
		for a := range agentSet {
			agents = append(agents, a)
		}
		sort.Strings(agents)
		run.Agents = agents
	}
	return nil
}

// lastActivity returns the seq/timestamp of the last event of any type for
// sessionID after sinceSeq (exclusive) — the liveness signal used to decide
// whether a still-open pipeline run has gone silent (runStaleAfter). Returns
// seq 0 when there's no such event.
func (m *Metrics) lastActivity(ctx context.Context, sessionID string, sinceSeq int64) (int64, time.Time, error) {
	envs, err := m.log.Query(ctx, Filter{SessionID: sessionID, Since: sinceSeq})
	if err != nil {
		return 0, time.Time{}, fmt.Errorf("metrics: pipeline runs: last activity: %w", err)
	}
	if len(envs) == 0 {
		return 0, time.Time{}, nil
	}
	last := envs[len(envs)-1]
	return last.Seq, last.Timestamp, nil
}

// distinctSessions returns every distinct, non-empty session ID that ever
// emitted an event of type t.
func (m *Metrics) distinctSessions(ctx context.Context, t contracts.EventType) ([]string, error) {
	rows, err := m.log.db.QueryContext(ctx,
		`SELECT DISTINCT session_id FROM events WHERE type = ? AND session_id IS NOT NULL ORDER BY session_id`,
		string(t))
	if err != nil {
		return nil, fmt.Errorf("metrics: distinct sessions: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var sid string
		if err := rows.Scan(&sid); err != nil {
			return nil, fmt.Errorf("metrics: distinct sessions: %w", err)
		}
		out = append(out, sid)
	}
	return out, rows.Err()
}
