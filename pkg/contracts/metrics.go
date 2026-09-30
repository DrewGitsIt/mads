package contracts

import "time"

// CostPoint is one day×model cost bucket (spec §8, cost-over-time-by-model).
type CostPoint struct {
	Day          time.Time `json:"day"` // UTC, truncated to midnight
	Model        string    `json:"model"`
	InputTokens  int64     `json:"input_tokens"`
	OutputTokens int64     `json:"output_tokens"`
	CostUSD      float64   `json:"cost_usd"`
}

// CostBySession is total cost attributed to one session's usage.recorded events.
type CostBySession struct {
	SessionID string  `json:"session_id"`
	CostUSD   float64 `json:"cost_usd"`
}

// CostByPipelineRun is total cost attributed to one reconstructed pipeline run.
// RunSeq is the seq of that run's pipeline.started event — a stable identity within
// the session (see PipelineRun below).
type CostByPipelineRun struct {
	SessionID string  `json:"session_id"`
	RunSeq    int64   `json:"run_seq"`
	CostUSD   float64 `json:"cost_usd"`
}

// CostMetrics is GET /metrics/cost's response: the same usage.recorded events sliced
// three ways (spec §8: "cost over time by model/session/pipeline").
type CostMetrics struct {
	ByDay         []CostPoint         `json:"by_day"`
	BySession     []CostBySession     `json:"by_session"`
	ByPipelineRun []CostByPipelineRun `json:"by_pipeline_run"`
}

// AgentLeaderboardEntry is one agent's aggregate track record (spec §8).
// UsageCount counts agent.selected events that actually picked this agent.
// Successes/Failures count agent.outcome_recorded events. Retries equals Failures:
// every recorded failure is a stage that looped back or blocked under this agent
// (spec §6 on_fail), i.e. a retry signal — there's no separate "retry" event to count.
type AgentLeaderboardEntry struct {
	Agent       string  `json:"agent"`
	UsageCount  int     `json:"usage_count"`
	Successes   int     `json:"successes"`
	Failures    int     `json:"failures"`
	SuccessRate float64 `json:"success_rate"` // successes / (successes+failures); 0 if none recorded
	Retries     int     `json:"retries"`
}

// TaskOutcomes is GET /metrics/tasks's response: current status distribution.
// Draft tasks are excluded — they aren't "outcomes" yet, they're pending confirmation.
type TaskOutcomes struct {
	Counts         map[TaskStatus]int `json:"counts"`
	Total          int                `json:"total"`
	CompletionRate float64            `json:"completion_rate"` // counts[done] / total; 0 if total is 0
}

// CapabilityUsage is one registered capability's invocation count (spec §8, including
// never-used candidates for removal — Invocations is 0 for those).
type CapabilityUsage struct {
	Capability  string         `json:"capability"`
	Type        CapabilityType `json:"type"`
	Invocations int            `json:"invocations"`
}

// PipelineRun is one reconstructed pipeline run: the span between a pipeline.started
// event and its matching terminal event (pipeline.completed / .cancelled / .failed)
// within one session (spec §8 Pipeline Run History). There is no dedicated run-ID
// field on the envelope; RunSeq (the started event's seq) is the stable identity.
// A run with no terminal event and no activity of any kind for a long time (its
// engine died without emitting one) is reported as "interrupted" rather than left
// "running" forever — see runStaleAfter in services/data/internal/store/metrics.go.
type PipelineRun struct {
	SessionID    string    `json:"session_id"`
	RunSeq       int64     `json:"run_seq"`
	RunID        string    `json:"run_id,omitempty"` // from pipeline.started; empty for runs started before it carried one
	StartedAt    time.Time `json:"started_at"`
	EndedAt      time.Time `json:"ended_at,omitempty"` // zero if still running (no terminal event yet)
	Outcome      string    `json:"outcome"`            // "completed" | "incomplete" | "cancelled" | "failed" | "running" | "interrupted"
	TasksDone    int       `json:"tasks_done"`
	TasksBlocked int       `json:"tasks_blocked"`
	TasksSkipped int       `json:"tasks_skipped"`
	CostUSD      float64   `json:"cost_usd"`
	Agents       []string  `json:"agents,omitempty"` // distinct agents selected during the run, sorted
}
