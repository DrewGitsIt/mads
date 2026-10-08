package contracts

import "time"

// DelegateState is where a delegate — a unit of work an agent harness runs on
// a session's behalf, e.g. a Claude Code subagent (docs/DELEGATES-DESIGN.md) —
// is in its lifecycle. The parent's turn ending is deliberately NOT a
// transition: a backgrounded delegate outlives the turn that spawned it, and
// treating turn end as "done" is the assumption behind most of mads's
// subagent bugs. There is no "idle": a finished delegate isn't warm (mads has
// no notion of a KV-cache TTL), it is finished — a harness that resumes one
// under the same id simply re-enters running.
type DelegateState string

const (
	DelegateRunning   DelegateState = "running"
	DelegateCompleted DelegateState = "completed"
	DelegateFailed    DelegateState = "failed"
	// DelegateStopped: stopped before finishing — by the harness (a kill, a
	// cascading interrupt) or because the harness process itself ended.
	DelegateStopped DelegateState = "stopped"
	// DelegateOrphaned: mads lost track of it — the runner that was watching
	// went away without closing it out (a session service restart). Nothing
	// is known about how it ended.
	DelegateOrphaned DelegateState = "orphaned"
)

// DelegateStates is the full registry, for validation and enumeration.
var DelegateStates = []DelegateState{DelegateRunning, DelegateCompleted, DelegateFailed, DelegateStopped, DelegateOrphaned}

// Valid reports whether s is a registered state.
func (s DelegateState) Valid() bool {
	for _, v := range DelegateStates {
		if s == v {
			return true
		}
	}
	return false
}

// Delegate is one harness-run delegate, owned by the session whose agent
// spawned it. Identity is the harness's own id (HarnessID — Claude Code's
// task_id/agentId), unique within the session: it is the one key that stays
// stable when the harness resumes the delegate later (a Claude Code relay
// re-entry reports a different tool_use_id, the SendMessage call's).
type Delegate struct {
	ID        string `json:"id"`
	SessionID string `json:"session_id"`
	HarnessID string `json:"harness_id"`
	// ToolUseID is the spawning call — the delegate's anchor in the parent's
	// stream and the parent_tool_use_id its own events are tagged with.
	// First-seen wins: a re-entry's different tool_use_id never moves it.
	ToolUseID string `json:"tool_use_id,omitempty"`
	// Depth is the nesting depth (1 = spawned by the main agent).
	Depth       int    `json:"depth,omitempty"`
	Type        string `json:"type,omitempty"`
	Description string `json:"description,omitempty"`
	// Backgrounded: the parent did not wait on it. Sticky once seen — a
	// Claude Code re-entry always runs backgrounded.
	Backgrounded bool          `json:"backgrounded,omitempty"`
	State        DelegateState `json:"state"`
	// Reason is the evidence behind State: the harness's own status word
	// when it reported one ("completed", "killed", …), otherwise what mads
	// concluded from ("roster", "process_exit", "runner_restart").
	Reason string `json:"reason,omitempty"`
	// Inferred marks a State mads concluded rather than the harness
	// reported. A later harness report always overrides an inferred state.
	Inferred bool `json:"inferred,omitempty"`
	// Runs counts how many times it has entered running (re-entries > 1).
	Runs int `json:"runs"`
	// StartedAt is when the latest run started; EndedAt when it left
	// running (nil while running).
	StartedAt time.Time  `json:"started_at"`
	EndedAt   *time.Time `json:"ended_at,omitempty"`
	// TranscriptPath is the harness's on-disk transcript for the delegate,
	// when it reported one.
	TranscriptPath string `json:"transcript_path,omitempty"`
	// Final counters as the harness last reported them.
	TotalTokens int       `json:"total_tokens,omitempty"`
	ToolUses    int       `json:"tool_uses,omitempty"`
	DurationMS  int       `json:"duration_ms,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}
