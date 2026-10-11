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
	// TranscriptPath is the delegate's own durable transcript, found in the
	// harness's store at run time (provider.DelegateInspector). Not the
	// notification's output_file, which is a task output, not a conversation.
	TranscriptPath string `json:"transcript_path,omitempty"`
	// WorktreePath is the separate worktree the harness ran it in, if any.
	WorktreePath string `json:"worktree_path,omitempty"`
	// Model is the model its latest API call ran on — not necessarily the
	// parent's: a harness can run a subagent on a smaller one.
	Model string `json:"model,omitempty"`
	// Branch is the git branch it worked on, as its harness recorded it.
	Branch string `json:"branch,omitempty"`
	// Usage is its latest API call's accounting: ContextTokens is its
	// context occupancy, ContextWindow the window when the harness reported
	// one. Kept current while it runs, at most every few seconds.
	Usage *Usage `json:"usage,omitempty"`
	// Capabilities is what can be done with it — the provider matrix's
	// defaults, corrected by what was observed about this one delegate.
	// Nil until the runner has resolved them.
	Capabilities *DelegateCapabilities `json:"capabilities,omitempty"`
	// Final counters as the harness last reported them.
	TotalTokens int       `json:"total_tokens,omitempty"`
	ToolUses    int       `json:"tool_uses,omitempty"`
	DurationMS  int       `json:"duration_ms,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// DelegateSteer is how a user's message can reach a delegate.
type DelegateSteer string

const (
	// DelegateSteerNone: nothing can reach it — watch only.
	DelegateSteerNone DelegateSteer = "none"
	// DelegateSteerRelay: through its parent agent, which decides how
	// faithfully to pass it on (Claude Code's SendMessage).
	DelegateSteerRelay DelegateSteer = "relay"
	// DelegateSteerDirect: sent to the delegate itself.
	DelegateSteerDirect DelegateSteer = "direct"
)

// DelegateCapabilities is the resolved capability set for one delegate
// (docs/DELEGATES-DESIGN.md C1–C7): the harness's defaults from its provider
// matrix, overridden by run-time evidence.
type DelegateCapabilities struct {
	// Transcript (C1): its own durable transcript is on disk
	// (Delegate.TranscriptPath). Only ever true on evidence.
	Transcript bool `json:"transcript"`
	// Continuable (C2a): it could be continued as a new session forked from
	// that transcript. Requires Transcript.
	Continuable bool `json:"continuable"`
	// Steer (C2b).
	Steer DelegateSteer `json:"steer"`
	// Stop (C3a): mads can stop it alone.
	Stop bool `json:"stop"`
	// StopsWithParent (C3b): stopping its parent's turn stops it too.
	StopsWithParent bool `json:"stops_with_parent"`
}

// DelegateTranscript is a delegate's own transcript as its harness keeps it
// (GET /sessions/{id}/delegates/{harnessId}/transcript). Available is false
// when the harness keeps none or it isn't there (yet, or any more); the view
// then has only what mads recorded from the live stream.
type DelegateTranscript struct {
	Available bool                `json:"available"`
	Messages  []TranscriptMessage `json:"messages"`
}
