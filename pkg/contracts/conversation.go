package contracts

import "time"

// SubagentEvent is the payload of SessionSubagentEvent: one conversation
// event a spawned subagent's sidechain produced, persisted so the stream
// replays after the fact (the harness transcript omits sidechains). The
// tagging fields mirror providers/provider/event.go Event's
// parent_tool_use_id/subagent_type/task_description exactly; duplicated here
// (not imported) so pkg/contracts stays free of a dependency on the provider
// layer, same as every other cross-boundary type in this package.
type SubagentEvent struct {
	// ParentToolUseID is the Agent tool_use that spawned the subagent whose
	// sidechain produced this event — the correlation key to the spawning
	// tool call and its SubagentTask lifecycle events. Always set (an
	// untagged event belongs to the main agent and is never persisted as
	// this type). Depth is unbounded by construction: a nested subagent's
	// events simply carry their own parent's tool_use id.
	ParentToolUseID string `json:"parent_tool_use_id"`
	// SubagentType is the spawned agent's type (e.g. "general-purpose").
	SubagentType string `json:"subagent_type,omitempty"`
	// TaskDescription is the spawning tool call's short description.
	TaskDescription string `json:"task_description,omitempty"`
	// Kind is the event's shape: "text", "tool_use", "tool_result", or
	// "thinking" — the subset of the provider event vocabulary a sidechain
	// produces.
	Kind string `json:"kind"`
	// Text carries the chunk for "text"/"thinking", and the result body for
	// "tool_result".
	Text string `json:"text,omitempty"`
	// Tool carries the enriched call for "tool_use" (kind/title/status/
	// locations/server included) and the correlating ID for "tool_result".
	Tool *ToolUse `json:"tool,omitempty"`
	// IsError marks a "tool_result" as the call having failed.
	IsError bool `json:"is_error,omitempty"`
}

// SubagentTask is the payload of SessionSubagentTask — one lifecycle update
// for a spawned subagent task, mirroring providers/provider/event.go
// TaskStatus field for field. Which fields are populated depends on Phase;
// zero values mean the harness's wire line didn't carry the field.
type SubagentTask struct {
	// Phase is "started", "progress", "updated", or "notification" (the
	// completion notice — subagents run backgrounded by default, so the
	// spawning tool_result returns immediately and this is where the real
	// outcome arrives).
	Phase string `json:"phase"`
	// TaskID is the harness's task identifier, stable across phases.
	TaskID string `json:"task_id"`
	// ToolUseID is the spawning Agent tool_use's ID — the correlation key to
	// the task's SessionSubagentEvent stream (parent_tool_use_id there).
	// Absent on "updated" lines.
	ToolUseID string `json:"tool_use_id,omitempty"`
	// Description is the task's short label ("started"); on "progress" it is
	// instead a live activity line.
	Description string `json:"description,omitempty"`
	// SubagentType is the spawned agent's type (e.g. "general-purpose").
	SubagentType string `json:"subagent_type,omitempty"`
	// Prompt is the full task prompt — "started" only.
	Prompt string `json:"prompt,omitempty"`
	// IsBackgrounded reports whether the task runs in the background —
	// "started" only.
	IsBackgrounded bool `json:"is_backgrounded,omitempty"`
	// SpawnDepth is the task's nesting depth (1 = spawned by the main
	// agent) — "started" only. Whether depth ≥ 2 sidechain lines actually
	// stream on claude-code's wire is unverified; this schema tolerates any
	// depth regardless (per-event parent tagging plus this counter).
	SpawnDepth int `json:"spawn_depth,omitempty"`
	// TotalTokens, ToolUses, and DurationMS are the task's running counters
	// ("progress") or final totals ("notification").
	TotalTokens int `json:"total_tokens,omitempty"`
	ToolUses    int `json:"tool_uses,omitempty"`
	DurationMS  int `json:"duration_ms,omitempty"`
	// LastToolName is the tool the subagent most recently ran — "progress"
	// only.
	LastToolName string `json:"last_tool_name,omitempty"`
	// Status is the task's state ("completed", ...) — "updated" and
	// "notification".
	Status string `json:"status,omitempty"`
	// Summary is the subagent's final report text — "notification" only.
	Summary string `json:"summary,omitempty"`
	// OutputFile is the harness's on-disk path to the subagent's full
	// transcript — "notification" only.
	OutputFile string `json:"output_file,omitempty"`
	// ParentToolUseID is set only when the lifecycle line itself arrived on
	// a subagent's sidechain (a nested spawn, depth ≥ 2) — empty for tasks
	// the main agent spawned, matching the per-event tagging convention.
	ParentToolUseID string `json:"parent_tool_use_id,omitempty"`
}

// ThinkingBlock is the payload of SessionThinking (no longer written; old
// rows still decode): one whole extended-thinking block from the main
// agent. ThinkingTokens existed for completeness of the wire shape and is
// zero on every stored row.
type ThinkingBlock struct {
	Text           string `json:"text"`
	ThinkingTokens int    `json:"thinking_tokens,omitempty"`
}

// ToolStatusChange is the payload of SessionToolStatusChanged: the runner's
// status synthesis (pkg/toolstatus) moved one tool call to a state no wire
// event carries. Status uses ToolUse.Status's vocabulary.
type ToolStatusChange struct {
	ToolUseID string `json:"tool_use_id"`
	Status    string `json:"status"`
	// ParentToolUseID routes the change to a subagent's stream when the
	// call was a sidechain's; empty for the main agent's calls.
	ParentToolUseID string `json:"parent_tool_use_id,omitempty"`
}

// CompactStatus is the payload of SessionCompact: one phase of a native
// context-compaction pass, mirroring providers/provider's CompactStatus.
type CompactStatus struct {
	// Phase is "start" (compaction began) or "done" (finished, successfully
	// or not).
	Phase string `json:"phase"`
	// Success is meaningful only when Phase is "done".
	Success bool `json:"success,omitempty"`
	// Error is the provider's failure reason when Phase is "done" and
	// Success is false.
	Error string `json:"error,omitempty"`
}

// Agent states, the vocabulary of AgentState.State: whether a turn is in
// flight at all. What the agent is doing inside a working turn lives in
// Label, not in a wider state enum — a rail line is one clause, and every
// finer distinction the UI draws (needs-approval, crashed, done · your turn)
// already comes from its own event type.
const (
	AgentWorking = "working"
	AgentIdle    = "idle"
)

// AgentState is the payload of SessionAgentState: the session main agent's
// in-flight state, appended by the session runner on transitions only.
type AgentState struct {
	// State is AgentWorking or AgentIdle.
	State string `json:"state"`
	// Label is the server-built one-line clause naming what the agent is
	// doing right now — a verb for the tool call's enriched Kind plus the
	// adapter-built ToolUse.Title verbatim ("Running bash · make check"),
	// the generic "Working…" before the first activity of a turn, or
	// "Thinking…" while an extended-thinking block streams. Built from the
	// same enrichment ToolUse.Title carries so a rail line and the open
	// conversation's working indicator can never disagree. Empty on idle.
	Label string `json:"label,omitempty"`
	// Since is when the CURRENT State began — the turn's start for every
	// working event in it, unchanged as Label transitions, so an elapsed
	// counter driven off it spans the whole turn rather than resetting per
	// tool (ui/src/conversation/ChatRenderer/WorkingIndicator.tsx's own
	// rule). Turn-end time on idle.
	Since time.Time `json:"since"`
	// Background is how many background processes the agent started are
	// still running (a run_in_background Bash call, a Monitor watch; not
	// subagents, which have delegate records). A rail reading "Idle" while
	// one runs would be wrong: the session is waiting on it. Updated on
	// change, independent of State; live-only in spirit — a session service
	// restart loses it, which is why a reader trusts it on an active session
	// only.
	Background int `json:"background,omitempty"`
}

// QueuedMessage is one user message waiting in the Runner's queue behind an
// in-flight turn.
type QueuedMessage struct {
	ID       string    `json:"id"`
	Text     string    `json:"text"`
	QueuedAt time.Time `json:"queued_at"`
}

// QueueChanged is the payload of SessionQueueChanged. Queued is the whole
// remaining queue in delivery order (never null), so a reader can adopt it
// as-is. Delivered names the message that just went out as the next turn;
// Failed names one whose delivery failed, with Error its reason. Both are
// already gone from Queued.
type QueueChanged struct {
	Queued    []QueuedMessage `json:"queued"`
	Delivered *QueuedMessage  `json:"delivered,omitempty"`
	Failed    *QueuedMessage  `json:"failed,omitempty"`
	Error     string          `json:"error,omitempty"`
}
