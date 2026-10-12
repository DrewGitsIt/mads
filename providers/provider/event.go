package provider

import (
	"encoding/json"
	"time"
)

// EventKind classifies one normalized session event. Every adapter maps its
// harness's wire format onto these — the platform above the provider layer
// never sees a harness-specific shape (spec §11).
type EventKind string

const (
	// EventText — a streamed chunk of the agent's response.
	EventText EventKind = "text"
	// EventToolUse — the agent invoked a tool; Tool carries the details.
	// Tool uses with a Path are the raw material for §5 file tracking.
	EventToolUse EventKind = "tool_use"
	// EventTurnEnd — the response finished (or was interrupted). Usage is
	// attached when the provider reports cost.
	EventTurnEnd EventKind = "turn_end"
	// EventError — a provider-level failure surfaced mid-session (rate
	// limit, protocol error). The session may or may not survive; a
	// subsequent channel close without EventTurnEnd means it did not.
	EventError EventKind = "error"
	// EventSessionRef — the provider announced its native session ref
	// (Ref) mid-invocation, before the turn finished. OneShot callers that
	// need exact attribution before completion (e.g. the pipeline engine,
	// to record which transcript belongs to a running stage attempt) rely
	// on this rather than waiting for OneShotResult.Ref. Fired at most
	// once per invocation; providers with no such wire-level announcement
	// never emit it.
	EventSessionRef EventKind = "session_ref"
	// EventPTYOutput — a raw chunk of terminal (PTY) output, distinct from
	// EventText so it can be buffered and replayed on reattach (tmux-attach
	// semantics) without leaking into chat-mode consumers, which only ever
	// listen for EventText.
	EventPTYOutput EventKind = "pty_output"
	// EventToolResult — a tool call's outcome arrived on the wire out of
	// band from the model's own text/tool_use stream (claude-code reports
	// these, including permission denials, as a "user" role message that
	// feeds the result back into the model's context). Tool.ID correlates
	// back to the EventToolUse that started it; Text is the result body,
	// IsError marks a failure. Surfacing this live (rather than only on the
	// next transcript refetch) is what lets a headless session show a
	// permission-blocked tool call instead of silently stalling — see
	// providers/claudecode's wireLine.events() and its README.
	EventToolResult EventKind = "tool_result"
	// EventPermissionRequest — the harness asked permission for a tool call
	// and is waiting on an allow/deny decision (chat transport only — see
	// StartOptions.Permissions and providers/claudecode's README "Trust
	// enforcement"). Permission carries the request; the Runner (not the
	// provider adapter) constructs and broadcasts this event from its
	// PermissionAsker, since the decision loop lives in the session service,
	// not the provider process.
	EventPermissionRequest EventKind = "permission_request"
	// EventCompact — the provider is running (or just finished) a native
	// context-compaction pass. Today the only producer is the Runner's
	// resume-above-threshold trigger (services/session/internal/runner/
	// compact.go) asking a Compactor session to compact; Compact.Phase
	// distinguishes the "start" status line from the "done" outcome, so the
	// UI can render a completed/failed status instead of a "Compacting…"
	// banner stuck forever. See providers/claudecode's wire.go systemEvents
	// for the wire shapes this normalizes (verified empirically — claude-code
	// 2.1.220 emits a "system"/"status" line with status "compacting", then
	// another with compact_result "success"/"failed", not a single event).
	EventCompact EventKind = "compact"
	// EventTask — a lifecycle update for a Task/Agent subagent the harness
	// spawned; Task carries the details. Task.Phase distinguishes the four
	// shapes claude-code streams as "system" subtypes (task_started,
	// task_progress, task_updated, task_notification — verified against the
	// real capture in providers/claudecode/testdata/
	// stream-task-subagent.jsonl). Distinct from the parent-tagged
	// text/tool_use events the subagent itself produces (ParentToolUseID
	// below): this kind is the harness's own bookkeeping about the task —
	// spawn, progress counters, final status + summary.
	EventTask EventKind = "task"
	// EventTaskRoster — the harness's own complete list of its currently
	// running background subagents (TaskRoster: their task ids; empty means
	// none). claude-code streams it as "system"/"background_tasks_changed"
	// on every change. It is reconciliation evidence, not a lifecycle event:
	// the roster drops a finished task just BEFORE that task's
	// task_updated/task_notification arrive (both real captures in
	// providers/claudecode/testdata), so absence alone means "ending", and
	// only the lifecycle line says how.
	EventTaskRoster EventKind = "task_roster"
	// EventBackground — the harness's complete list of its running
	// background processes that aren't subagents (a run_in_background Bash
	// call, a Monitor watch), sent whenever it changes; empty means none.
	// Live-only: never persisted. The runner keeps the latest for a viewer
	// that connects mid-run and sends an empty one when the process exits,
	// which takes its background processes with it.
	EventBackground EventKind = "background"
	// EventSubagentUsage — one of a subagent's API calls, tagged with its
	// ParentToolUseID: Usage is that call's accounting, Model included (a
	// subagent can run on a different model from its parent). Live-only:
	// the delegate's record keeps the latest (contracts.Delegate.Usage).
	EventSubagentUsage EventKind = "subagent_usage"
	// EventThinking — the model's extended-thinking output, distinct from
	// EventText so chat-mode consumers that render only the reply never
	// interleave reasoning into it. Two facets share the kind, mirroring
	// EventCompact's phases: Text non-empty carries a whole thinking block;
	// Text empty with ThinkingTokens set is a live estimated-token progress
	// tick (claude-code's "system"/"thinking_tokens" lines) for a "Thinking…"
	// indicator title.
	EventThinking EventKind = "thinking"
)

// Event is one entry in a session's normalized stream.
type Event struct {
	Kind EventKind `json:"kind"`
	// Text for EventText: the chunk. For EventError: the message.
	Text string `json:"text,omitempty"`
	// Tool for EventToolUse.
	Tool *ToolUse `json:"tool,omitempty"`
	// Usage for EventTurnEnd, when cost reporting works, and for
	// EventSubagentUsage.
	Usage *Usage `json:"usage,omitempty"`
	// Interrupted marks a turn ended by Interrupt rather than completion.
	Interrupted bool `json:"interrupted,omitempty"`
	// Ref for EventSessionRef: the provider-native session ref.
	Ref string `json:"ref,omitempty"`
	// IsError marks EventToolResult as the tool call having failed
	// (including a permission denial) — mirrors Message.IsError below.
	IsError bool `json:"is_error,omitempty"`
	// Permission for EventPermissionRequest.
	Permission *PermissionRequest `json:"permission,omitempty"`
	// Compact for EventCompact.
	Compact *CompactStatus `json:"compact,omitempty"`
	// Task for EventTask.
	Task *TaskStatus `json:"task,omitempty"`
	// TaskRoster for EventTaskRoster: the running background subagents'
	// task ids, in the harness's order.
	TaskRoster []string `json:"task_roster,omitempty"`
	// Background for EventBackground: the running background processes, in
	// the harness's order. Empty (omitted) means none.
	Background []BackgroundTask `json:"background,omitempty"`
	// ThinkingTokens for EventThinking with no Text: the estimated token
	// count so far while the model thinks (see EventThinking).
	ThinkingTokens int `json:"thinking_tokens,omitempty"`
	// ParentToolUseID tags an event as a subagent's, not the main agent's:
	// the ID of the Agent tool_use that spawned the subagent whose sidechain
	// produced it. Zero value means main agent — consumers route tagged
	// events out of the main transcript stream instead of interleaving them.
	// SubagentType and TaskDescription ride along so a tagged event can
	// render a labeled subagent card without a lookup.
	ParentToolUseID string `json:"parent_tool_use_id,omitempty"`
	// SubagentType is the spawned agent's type (e.g. "general-purpose") on a
	// parent-tagged event.
	SubagentType string `json:"subagent_type,omitempty"`
	// TaskDescription is the spawning tool call's short description on a
	// parent-tagged event.
	TaskDescription string `json:"task_description,omitempty"`
}

// BackgroundTask is one running background process in EventBackground.
type BackgroundTask struct {
	TaskID string `json:"task_id"`
	// ToolUseID is the call that started it; empty until the harness says.
	ToolUseID   string `json:"tool_use_id,omitempty"`
	Description string `json:"description,omitempty"`
	// StartedAt is when the provider first saw it running.
	StartedAt time.Time `json:"started_at"`
}

// TaskStatus carries EventTask's detail: one lifecycle update for a spawned
// subagent task. Which fields are populated depends on Phase — see each
// field's note; zero values mean the wire line didn't carry the field.
type TaskStatus struct {
	// Phase is "started", "progress", "updated", or "notification" (the
	// harness's completion notice — subagents run backgrounded by default,
	// so the spawning tool_result returns immediately and this is where the
	// real outcome arrives).
	Phase string `json:"phase"`
	// TaskID is the harness's task identifier, stable across phases.
	TaskID string `json:"task_id"`
	// ToolUseID is the spawning Agent tool_use's ID — the correlation key to
	// the parent-tagged event stream (Event.ParentToolUseID) and the
	// spawning EventToolUse. Absent on "updated" lines.
	ToolUseID string `json:"tool_use_id,omitempty"`
	// Description is the task's short label ("started"); on "progress" it is
	// instead a live activity line (e.g. "Running <tool description>").
	Description string `json:"description,omitempty"`
	// SubagentType is the spawned agent's type (e.g. "general-purpose").
	SubagentType string `json:"subagent_type,omitempty"`
	// Prompt is the full task prompt — "started" only.
	Prompt string `json:"prompt,omitempty"`
	// IsBackgrounded reports whether the task runs in the background —
	// "started" only.
	IsBackgrounded bool `json:"is_backgrounded,omitempty"`
	// SpawnDepth is the task's nesting depth (1 = spawned by the main
	// agent) — "started" only.
	SpawnDepth int `json:"spawn_depth,omitempty"`
	// TotalTokens, ToolUses, and DurationMS are the task's running counters
	// ("progress") or final totals ("notification").
	TotalTokens int `json:"total_tokens,omitempty"`
	ToolUses    int `json:"tool_uses,omitempty"`
	DurationMS  int `json:"duration_ms,omitempty"`
	// LastToolName is the tool the subagent most recently ran — "progress"
	// only.
	LastToolName string `json:"last_tool_name,omitempty"`
	// Status is the task's state ("completed", ...) — "updated" (from the
	// patch) and "notification".
	Status string `json:"status,omitempty"`
	// Summary is the subagent's final report text — "notification" only.
	Summary string `json:"summary,omitempty"`
	// OutputFile is the harness's on-disk path to the subagent's full
	// transcript — "notification" only.
	OutputFile string `json:"output_file,omitempty"`
}

// CompactStatus carries EventCompact's detail: which phase of a native
// compaction pass this event reports.
type CompactStatus struct {
	// Phase is "start" (compaction began) or "done" (finished, successfully
	// or not).
	Phase string `json:"phase"`
	// Success is meaningful only when Phase is "done".
	Success bool `json:"success,omitempty"`
	// Error is the provider's failure reason when Phase is "done" and
	// Success is false (e.g. claude-code's "Not enough messages to
	// compact.").
	Error string `json:"error,omitempty"`
}

// PermissionRequest is one harness permission prompt awaiting (or past) a
// decision — the mads-side record of a PermissionAsk. ID correlates the
// eventual resolve call (POST /permission-requests/{id}/resolve) back to the
// waiting asker.
type PermissionRequest struct {
	ID    string          `json:"id"`
	Tool  string          `json:"tool"`
	Input json.RawMessage `json:"input,omitempty"`
	// Status starts "pending" and moves to "allowed", "denied", "expired"
	// (nobody answered in time — a deny), or "cancelled" (the harness
	// stopped waiting, e.g. the turn was interrupted — also a deny) —
	// carried on the event so a late subscriber (transcript refetch) can
	// reconstruct a resolved card's final state, not just the open one.
	Status string `json:"status"`
	// ToolUseID is the asking call's wire id (PermissionAsk.ToolUseID) —
	// the key a client uses to attach the card to that call's row.
	ToolUseID string `json:"tool_use_id,omitempty"`
	// ParentToolUseID is the subagent call the asking call runs under;
	// empty for the main agent (Event.ParentToolUseID's meaning).
	ParentToolUseID string `json:"parent_tool_use_id,omitempty"`
	// AgentID is the harness's id for the asking subagent; empty for the
	// main agent.
	AgentID string `json:"agent_id,omitempty"`
	// Reason is the harness's own reason for asking, when it gave one.
	Reason string `json:"reason,omitempty"`
}

// ToolKind classifies a tool invocation for rendering — icon choice and
// activity grouping happen by kind, never by harness-specific Name. The
// vocabulary is ACP's tool-kind taxonomy (read, edit, delete, move, search,
// execute, think, fetch, agent, other) plus mads extensions (mcp, skill,
// plan). Adapters map their harness's tool names onto these; anything they
// can't classify is ToolKindOther, never a guess.
type ToolKind string

const (
	ToolKindRead    ToolKind = "read"
	ToolKindEdit    ToolKind = "edit"
	ToolKindDelete  ToolKind = "delete"
	ToolKindMove    ToolKind = "move"
	ToolKindSearch  ToolKind = "search"
	ToolKindExecute ToolKind = "execute"
	ToolKindThink   ToolKind = "think"
	ToolKindFetch   ToolKind = "fetch"
	ToolKindAgent   ToolKind = "agent"
	// The mads extensions beyond ACP's set: an MCP server's tool (Server
	// names which one), a skill injection, and the plan/todo-list tool the
	// UI renders as a checklist widget instead of activity rows.
	ToolKindMCP   ToolKind = "mcp"
	ToolKindSkill ToolKind = "skill"
	ToolKindPlan  ToolKind = "plan"
	ToolKindOther ToolKind = "other"
)

// ToolStatus is a tool call's lifecycle state. Adapters set it only when
// their wire actually reports one (ACP conformance); synthesizing the
// missing states everywhere else — in_progress on arrival, completed/failed
// on result, interrupted on a resultless turn end, pending while
// permission-parked, blocked when permission was refused — is the layer
// above's job (pkg/toolstatus), in one place, not each adapter's.
type ToolStatus string

const (
	ToolStatusPending     ToolStatus = "pending"
	ToolStatusInProgress  ToolStatus = "in_progress"
	ToolStatusCompleted   ToolStatus = "completed"
	ToolStatusFailed      ToolStatus = "failed"
	ToolStatusInterrupted ToolStatus = "interrupted"
	// ToolStatusBlocked: the call never ran because permission was refused
	// — the user denied it, nobody answered in time, the harness stopped
	// waiting, or the harness itself refused for lack of a grant. Terminal,
	// and distinct from failed (the call ran and errored).
	ToolStatusBlocked ToolStatus = "blocked"
)

// ToolLocation is one file position a tool call touched — absolute path
// when the harness reports one (claude-code inputs do). Line is 1-based;
// zero means the whole file.
type ToolLocation struct {
	Path string `json:"path"`
	Line int    `json:"line,omitempty"`
}

// ToolUse is a normalized tool invocation.
type ToolUse struct {
	// ID is the provider-native tool_use identifier, when the wire format
	// has one — the correlation key back to a matching tool_result.
	ID    string          `json:"id,omitempty"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input,omitempty"`
	// Path is set when the tool referenced a file — read or write. Digests
	// and transcripts render it for both; §5 file-change tracking must not
	// use it alone, because a Read/Grep/Glob carries a Path too.
	Path string `json:"path,omitempty"`
	// Mutates is true only when the tool writes or deletes the file at Path,
	// per the adapter's own knowledge of its tool vocabulary (claude-code
	// tool names, ACP's kind taxonomy). This, not Path, is the §5
	// file-change signal.
	Mutates bool `json:"mutates,omitempty"`
	// Kind classifies the call for rendering — see ToolKind. Empty when the
	// adapter predates enrichment.
	Kind ToolKind `json:"kind,omitempty"`
	// Title is the adapter-built one-line human summary of the call (the
	// command's description, the search pattern, the file path) — built
	// server-side so every consumer renders the same line without
	// re-deriving it from Input.
	Title string `json:"title,omitempty"`
	// Status is the call's lifecycle state — see ToolStatus. Empty when the
	// wire didn't state one; the defensive synthesis layer above fills it.
	Status ToolStatus `json:"status,omitempty"`
	// Locations lists the file positions the call touched, when known.
	Locations []ToolLocation `json:"locations,omitempty"`
	// Server is the MCP server name, set only when Kind is ToolKindMCP.
	Server string `json:"server,omitempty"`
}

// Usage is token accounting for one turn or one-shot (event log → metrics
// store, spec §8).
type Usage struct {
	InputTokens  int    `json:"input_tokens"`
	OutputTokens int    `json:"output_tokens"`
	Model        string `json:"model,omitempty"`
	// CacheCreationInputTokens and CacheReadInputTokens are the prompt-cache
	// halves of context Claude Code bills separately from InputTokens —
	// without them InputTokens wildly understates real context on any turn
	// that hit the cache.
	CacheCreationInputTokens int `json:"cache_creation_input_tokens,omitempty"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens,omitempty"`
	// CostUSD is the caller-computed dollar cost, when the provider reports
	// it (BehaviorCostReporting). Zero when unknown — the platform does no
	// pricing math of its own (spec §11's hookpoint for usage.recorded).
	CostUSD float64 `json:"cost_usd,omitempty"`
	// ContextTokens is the prompt-side occupancy of the turn's FINAL API
	// call (input + cache read + cache creation) — what the next turn will
	// actually carry as context. Distinct from the fields above, which
	// claude-code reports as a per-turn billing aggregate summed across
	// every API iteration: on a multi-tool turn that aggregate overstates
	// occupancy by roughly the iteration count, so it must never be used as
	// a context measure when this field is set. Zero when the provider
	// doesn't report per-call occupancy.
	ContextTokens int `json:"context_tokens,omitempty"`
	// ContextWindow is the model's context window size as reported by the
	// provider at runtime (claude-code's modelUsage[model].contextWindow;
	// goose's usage_update size) — authoritative over any static table.
	// Zero when unreported.
	ContextWindow int `json:"context_window,omitempty"`
}

// Message is one normalized transcript entry (TranscriptReader). Synthesis
// and retro (spec §4, §7) read this shape regardless of harness.
type Message struct {
	Role string   `json:"role"` // "user", "agent", "tool", "tool_result", "compact", or "notification"
	Text string   `json:"text,omitempty"`
	Tool *ToolUse `json:"tool,omitempty"`
	// Timestamp is when the provider recorded this entry, when the wire
	// format carries one. Zero value when unknown.
	Timestamp time.Time `json:"timestamp,omitempty"`
	// IsError marks a Role "tool_result" entry as the tool call having
	// failed, or a Role "agent" entry as the harness reporting a failed
	// turn in the agent's place (auth rejected, API unreachable, out of
	// credits) rather than a reply. Meaningless on other roles.
	IsError bool `json:"is_error,omitempty"`
	// Compact describes a Role "compact" entry: a compaction pass, placed
	// where it happened. Its Text is the summary the agent carried forward
	// (empty if the provider recorded none) — never a user message.
	Compact *CompactMarker `json:"compact,omitempty"`
}

// CompactMarker is one completed compaction pass as a transcript records
// it. Zero fields mean the provider didn't say.
type CompactMarker struct {
	Trigger    string `json:"trigger,omitempty"` // "manual" (the user asked) or "auto"
	PreTokens  int    `json:"pre_tokens,omitempty"`
	PostTokens int    `json:"post_tokens,omitempty"`
	DurationMS int64  `json:"duration_ms,omitempty"`
}
