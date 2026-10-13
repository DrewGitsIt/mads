// Package contracts holds the shared boundary contracts every mads service
// consumes: the event envelope and type registry (spec §15), the task schema
// (spec §3), and the API error shape (spec §10.3).
//
// Both sides of every service boundary import this package and pin its wire
// formats with golden fixtures under testdata/. Changing a contract must break
// producer and consumer tests together until the golden is regenerated.
package contracts

import (
	"encoding/json"
	"strings"
	"time"
)

// Envelope is the control-plane event record (spec §15). Events are appended
// to the Data Service's event log in the same transaction as the state change
// they describe, and delivered to subscribers as server-side filtered streams
// ordered by Seq.
type Envelope struct {
	// Seq is the global, gapless, monotonically increasing position in the
	// event log. Assigned by the Data Service at append; zero until then.
	Seq       int64     `json:"seq"`
	Timestamp time.Time `json:"timestamp"`
	Type      EventType `json:"type"`
	// SessionID scopes the event to a session; empty for system-level events
	// (e.g. agent registry changes).
	SessionID string `json:"session_id,omitempty"`
	// TaskID scopes the event to a task, when one applies.
	TaskID string `json:"task_id,omitempty"`
	// Actor is who caused the event: "user", "synthesizer", "pipeline", or an
	// agent name.
	Actor string `json:"actor"`
	// Payload is the event-type-specific body. Kept opaque here; each event
	// type documents its payload shape where it is introduced.
	Payload json.RawMessage `json:"payload,omitempty"`
}

// EventType names an event in dotted <domain>.<action> form, e.g. "task.created".
type EventType string

// The event vocabulary (MADS-UX §4). This is the starter set implied by the UI
// surfaces; it grows as services land. Every emitted event must use a type
// registered here so subscribers and the UI share one vocabulary.
const (
	SessionCreated         EventType = "session.created"
	SessionProvisioning    EventType = "session.provisioning"
	SessionRenamed         EventType = "session.renamed"
	SessionActivityChanged EventType = "session.activity_changed"
	SessionUpdated         EventType = "session.updated"
	SessionDeleted         EventType = "session.deleted"
	// SessionCrashed fires when the Router's supervision detects a Session
	// Service child exited unexpectedly. Payload: {"reason": string}.
	SessionCrashed EventType = "session.crashed"
	// SessionPermissionRequested fires when the harness asks permission for
	// a tool call and waits on an allow/deny decision (chat transport,
	// auto-review/limited/read-only trust — provider.StartOptions.Permissions). Payload:
	// {"request_id", "tool", "tool_use_id": string}. Its resolution fires
	// SessionPermissionResolved with {"request_id", "tool", "tool_use_id",
	// "status": "allowed"|"denied"|"expired"|"cancelled"} ("cancelled": the
	// harness stopped waiting, e.g. an interrupted turn). Persisted (unlike the live
	// EventPermissionRequest stream frame) so a stalled-awaiting-permission
	// incident is diagnosable from the event log after the fact.
	SessionPermissionRequested EventType = "session.permission_requested"
	SessionPermissionResolved  EventType = "session.permission_resolved"
	// SessionSubagentEvent fires for every conversation event a spawned
	// subagent's sidechain produces (parent-tagged text, tool_use,
	// tool_result, and thinking — providers/provider/event.go
	// Event.ParentToolUseID). Payload: contracts.SubagentEvent. Persisted by
	// the session runner because this event log is the ONLY historical
	// source for subagent streams: claude-code's own on-disk transcript
	// omits sidechains entirely, so a transcript refetch can never replay
	// them (docs/CONVERSATION-REDESIGN-TASKS.md T6).
	SessionSubagentEvent EventType = "session.subagent_event"
	// SessionSubagentTask fires for each lifecycle update of a spawned
	// subagent task (the harness's own bookkeeping — spawn, progress
	// counters, final status + summary; providers/provider/event.go
	// EventTask). Payload: contracts.SubagentTask; payload.phase
	// distinguishes started/progress/updated/notification. "session." not
	// "task." — the task.* domain is the mads work-item schema
	// (pkg/contracts/task.go), an unrelated thing.
	SessionSubagentTask EventType = "session.subagent_task"
	// SessionThinking fires for each whole extended-thinking block the
	// session's main agent emits (providers/provider/event.go
	// EventThinking with text). Payload: contracts.ThinkingBlock. Live
	// estimated-token progress ticks (EventThinking with only a count) are
	// NOT persisted — they are indicator fuel, high-frequency and worthless
	// after the fact; a subagent's thinking blocks arrive parent-tagged and
	// persist as SessionSubagentEvent instead.
	SessionThinking EventType = "session.thinking"
	// SessionToolStatusChanged fires when the runner's defensive status
	// synthesis (pkg/toolstatus) changes a tool call's lifecycle state
	// outside any wire event that could carry it: "interrupted" when a turn
	// ends (or the session stops) with no result for the call, "pending"
	// when the call parks behind a permission request, "in_progress" when
	// that park is allowed, "blocked" when it is denied, expires, or is
	// cancelled. Statuses that ride a wire event (in_progress on tool_use,
	// completed/failed/blocked on tool_result) are stamped onto the event
	// itself, not duplicated here. Payload: contracts.ToolStatusChange.
	SessionToolStatusChanged EventType = "session.tool_status_changed"
	// SessionCompact fires for each phase of a native context-compaction
	// pass on the session (providers/provider/event.go EventCompact — the
	// user's "Compact now"/"/compact", or the Runner's near-limit resume
	// trigger): "start", then "done" with its outcome. Payload:
	// contracts.CompactStatus. Persisted because the live /output stream
	// never replays chat frames, so a conversation opened (or reopened)
	// mid-compaction — or just after a failed one — can only learn the
	// status from this log.
	SessionCompact EventType = "session.compact"
	// SessionQueueChanged fires whenever the user's queue of messages waiting
	// behind an in-flight turn changes: one queued, one cancelled, or the head
	// delivered as the next turn (or failing to deliver). The queue lives in
	// the Runner, not the UI, so a message queued in one session is still
	// sent after the user navigates away. Payload: contracts.QueueChanged,
	// always carrying the full remaining queue.
	SessionQueueChanged EventType = "session.queue_changed"
	// SessionAgentState fires when a session's main agent crosses a state
	// boundary — a turn going in flight, the activity label inside that turn
	// changing, and the turn ending. Payload: contracts.AgentState. This is
	// the ONLY turn-start/turn-activity signal on the control plane: the
	// per-session data-plane /output stream carries the same information but
	// costs one connection per session, and the session rail watches every
	// session at once (ui/src/router/events.ts — the ~6-socket-per-origin
	// cap is why it must never open one per card). Transitions only, floored
	// to one append per ~500ms so a tool-heavy turn can't spam the log, and
	// never fired for a subagent's parent-tagged events — a sidechain rolls
	// up as a count, not as the main agent's label.
	SessionAgentState EventType = "session.agent_state"
	// SessionUsageUpdated fires once a turn's usage has been written to the
	// session record — LastUsage and the running totals — and when the
	// harness's billing report (mode, plan limits) changes. No payload: it
	// says GET /sessions/{id}/report has moved. Its own event because
	// session.turn_ended and the idle session.agent_state go out before
	// those writes land, so a /usage card that refetched on them read the
	// previous turn's numbers.
	SessionUsageUpdated EventType = "session.usage_updated"
	// SessionDelegateChanged fires when a session's delegate record
	// (contracts.Delegate — a harness-run subagent, docs/DELEGATES-DESIGN.md)
	// is created or changes state. Payload: contracts.Delegate, the full
	// record after the change. Written by the Data Service in the same
	// transaction as the record, so the record and its event can never
	// disagree; transitions and first sightings only, never progress ticks
	// (those stay session.subagent_task).
	SessionDelegateChanged EventType = "session.delegate_changed"
	// SessionTrustChanged fires when a session's trust level is set to a
	// different value (POST /sessions/{id}/trust; re-setting the level it
	// already has logs nothing). Payload: contracts.TrustChange — from, to,
	// and whether the live agent was restarted onto it or it waits for the
	// next spawn. The conversation draws it inline where it happened
	// (ui/src/conversation/Worktree/worktreeCards.ts), so a trust change is
	// durable in the history instead of a toast that's gone on reload.
	SessionTrustChanged EventType = "session.trust_changed"

	TaskCreated       EventType = "task.created"
	TaskUpdated       EventType = "task.updated"
	TaskStatusChanged EventType = "task.status_changed"
	TaskConfirmed     EventType = "task.confirmed"
	TaskDiscarded     EventType = "task.discarded"

	NoteCreated   EventType = "note.created"
	NoteConfirmed EventType = "note.confirmed"
	NoteDiscarded EventType = "note.discarded"

	FileChanged EventType = "file.changed"

	CommitCreated EventType = "commit.created"
	CommitLinked  EventType = "commit.linked"
	// CommitUndone fires when a session's Commit is taken back (POST
	// /sessions/{id}/commit/undo: `git reset --soft HEAD~1`) and its record
	// removed, so a commit card can drop it. Payload: {"hash": string}.
	CommitUndone EventType = "commit.undone"

	PipelineStarted   EventType = "pipeline.started"
	PipelinePaused    EventType = "pipeline.paused"
	PipelineResumed   EventType = "pipeline.resumed"
	PipelineCancelled EventType = "pipeline.cancelled"
	PipelineCompleted EventType = "pipeline.completed"
	// PipelineFailed fires when the Router's supervision detects a Pipeline
	// Engine child exited unexpectedly. Payload: {"run_id": string, "reason": string}.
	PipelineFailed EventType = "pipeline.failed"

	StageStarted   EventType = "stage.started"
	StageCompleted EventType = "stage.completed"
	StageFailed    EventType = "stage.failed"
	StageSkipped   EventType = "stage.skipped"
	// StageRateLimited fires when an agent stage hits a provider rate limit
	// (spec §11.9) and the engine pauses to retry the same attempt instead
	// of treating it as a stage failure — no on_fail loop-back is consumed
	// and no rate-limit text reaches the next agent prompt. attempt is the
	// LOCAL rate-limit-retry ordinal (1 on the first pause within this
	// stage entry, counting only rate-limit pauses); stage_attempt is the
	// stage-entry attempt number (e.attempts) held fixed across every pause
	// here since a rate-limit retry re-runs the SAME attempt — it's what
	// names the output log (<stage>-attempt-<n>.log) and matches
	// usage.recorded/stage.attempt_ref's own "attempt" field. Payload:
	// {"stage": string, "attempt": int, "stage_attempt": int, "message":
	// string, "backoff": string}.
	StageRateLimited EventType = "stage.rate_limited"
	// StageRateLimitCleared fires when a rate-limited stage's pause ends and
	// the engine is about to retry. attempt/stage_attempt carry the same
	// meaning as StageRateLimited's own fields (see its doc comment).
	// Payload: {"stage": string, "attempt": int, "stage_attempt": int}.
	StageRateLimitCleared EventType = "stage.rate_limit_cleared"
	// StageRetried fires when a stage's own retry: policy (pkg/template.Retry)
	// re-runs it after an ordinary failure — distinct from StageRateLimited,
	// which never consumes retry budget at all. attempt is this policy
	// retry's LOCAL 1-based count (1 on the first retry of this stage
	// entry), not the stage's overall attempt number. stage_attempt is the
	// stage-entry attempt number (e.attempts) that the FAILING try — the
	// one reason describes — actually ran under; it's what names that
	// attempt's output log (<stage>-attempt-<n>.log) and matches
	// usage.recorded/stage.attempt_ref's own "attempt" field. A loop_back
	// that re-enters this stage bumps stage_attempt again on the next
	// entry, while attempt resets to 1 for that entry's own first retry —
	// the two counters answer different questions and are not
	// interchangeable. Payload: {"stage": string, "attempt": int,
	// "stage_attempt": int, "max": int, "reason": string}.
	StageRetried EventType = "stage.retried"

	RetroStarted   EventType = "retro.started"
	RetroProgress  EventType = "retro.progress"
	RetroCompleted EventType = "retro.completed"
	RetroFailed    EventType = "retro.failed"
	// RetroCancelled fires when a retro job is cancelled mid-run; no state was
	// mutated (the single Data Service write happens only after generation
	// fully succeeds). Payload: {"retro_id": string}.
	RetroCancelled EventType = "retro.cancelled"
	// RetroDPAAccepted fires when a DPA's status is set to accepted. Payload:
	// {"retro_id": string, "dpa_id": string}.
	RetroDPAAccepted EventType = "retro.dpa_accepted"
	// RetroDPARejected fires when a DPA's status is set to rejected. Payload:
	// {"retro_id": string, "dpa_id": string}.
	RetroDPARejected EventType = "retro.dpa_rejected"
	// RetroDPAEdited fires when a DPA's fields are edited. Payload:
	// {"retro_id": string, "dpa_id": string} — the diff itself isn't in the
	// payload; callers re-fetch the DPA.
	RetroDPAEdited EventType = "retro.dpa_edited"
	// RetroFollowUpCreated fires when a follow-up session is created from a
	// retro's accepted DPAs. Payload: {"retro_id": string, "session_id": string}.
	RetroFollowUpCreated EventType = "retro.follow_up_created"

	// JobQueued fires when the Router accepts a transient Fast Model job (spec
	// §15) for execution. Payload: {"job_id": string, "kind": string}.
	JobQueued EventType = "job.queued"
	// JobStarted fires when a queued job begins running. Payload: {"job_id":
	// string, "kind": string}.
	JobStarted EventType = "job.started"
	// JobCompleted fires when a job finishes successfully. Payload: {"job_id":
	// string, "kind": string}.
	JobCompleted EventType = "job.completed"
	// JobFailed fires when a job's body returns an error or panics. Payload:
	// {"job_id": string, "kind": string, "error": string}.
	JobFailed EventType = "job.failed"
	// JobCancelled fires when a job is cancelled via Runner.Cancel before it
	// completes. Payload: {"job_id": string, "kind": string, "error": string}.
	JobCancelled EventType = "job.cancelled"

	CapabilityCreated       EventType = "capability.created"
	CapabilityUpdated       EventType = "capability.updated"
	CapabilityDeleted       EventType = "capability.deleted"
	CapabilityHealthChanged EventType = "capability.health_changed"

	AgentCreated EventType = "agent.created"
	AgentUpdated EventType = "agent.updated"
	AgentDeleted EventType = "agent.deleted"
	// AgentSelected fires when auto-select (spec §9) picks an agent for a task
	// stage. Payload is agentselect.Selection (json) — the quadrant classification
	// and every candidate's score breakdown, so retros (§7) can evaluate ranking
	// quality after the fact.
	AgentSelected EventType = "agent.selected"
	// AgentOutcomeRecorded fires once a task stage's result under a specific
	// agent is known, feeding future failure-exclusion and track-record ranking
	// (spec §9). Payload: {"agent": string, "stage": string, "labels": []string,
	// "outcome": "success"|"failure"}.
	AgentOutcomeRecorded EventType = "agent.outcome_recorded"
	// AgentDraftGenerated fires when agent generation (spec §9.5) produces a
	// draft agent definition. Payload: {"draft": contracts.Agent, "similar":
	// *agentselect.Similar (omitted if nil)}. TaskID/SessionID on the envelope
	// identify what triggered generation; the draft itself is never persisted
	// to the agent registry unless the caller later calls Data Service's
	// POST /agents explicitly.
	AgentDraftGenerated EventType = "agent.draft_generated"

	WorkspaceCreated EventType = "workspace.created"
	WorkspaceUpdated EventType = "workspace.updated"
	WorkspaceDeleted EventType = "workspace.deleted"

	// LinkCreated fires when a linked-sessions grant set is created
	// (docs/LINKED-SESSIONS-DESIGN.md). Payload: {"granting_session_id":
	// string, "subject_session_id": string, "grants": []string}. Envelope
	// SessionID is the subject session (the one being watched/led).
	LinkCreated EventType = "link.created"
	// LinkUpdated fires when a link's grant set changes. Same payload shape
	// as LinkCreated.
	LinkUpdated EventType = "link.updated"
	// LinkDeleted fires when a link is removed. Payload: {"granting_session_id":
	// string, "subject_session_id": string} — identity only, not the removed grants.
	LinkDeleted EventType = "link.deleted"
	// LinkMessageInjected fires when a granting session's message is
	// delivered into its subject's conversation (docs/LINKED-SESSIONS-DESIGN.md
	// decision 1/2). Payload: {"link_id": string, "source_name": string,
	// "text": string}. Envelope SessionID is the subject session (the one
	// the message was delivered into); Actor is stamped "link-<id>", not
	// the granting session's own actor identity.
	LinkMessageInjected EventType = "link.message_injected"
	// TurnEnded fires when a session's agent completes a turn with non-empty
	// activity (docs/LINKED-SESSIONS-DESIGN.md decision 4) — the trigger the
	// Router's digest watcher subscribes to in order to compress and push a
	// per-turn digest to every session holding a `read` grant on the subject.
	// Payload: {"delta": string} — the turn's rendered transcript delta, the
	// same shape synth's Fast Model prompt body uses. Envelope SessionID is
	// the session whose turn just ended (the subject, from the monitor's
	// point of view).
	TurnEnded EventType = "session.turn_ended"

	TemplateCreated EventType = "template.created"
	TemplateUpdated EventType = "template.updated"
	TemplateDeleted EventType = "template.deleted"

	ProfileCreated EventType = "profile.created"
	ProfileUpdated EventType = "profile.updated"
	ProfileDeleted EventType = "profile.deleted"

	// ConfigUpdated fires whenever the global Config row changes, whether via
	// a direct Settings save or a profile activation merge.
	ConfigUpdated EventType = "config.updated"

	AttachmentAdded EventType = "attachment.added"

	// MergeAttempted fires when a session's isolated worktree branch begins
	// merging back into a target branch. Payload: {"branch": string,
	// "target_branch": string}. The pipeline engine's land queue (spec: "Not
	// Rocket Science Rule") reuses this same type for each land attempt
	// (rebase -> gate -> merge), with an additive "fork_sha" field: the
	// task's trunk HEAD at worktree creation, TaskID-scoped.
	MergeAttempted EventType = "merge.attempted"
	// MergeConflicted fires when a merge attempt aborts due to conflicts; the
	// worktree and branch are kept for manual resolution. Payload: {"branch":
	// string, "target_branch": string, "files": []string}. The pipeline
	// engine's land queue additionally sets "fork_sha", "trunk_head", "hunks"
	// ([]ConflictHunk-shaped objects: file, start_line, end_line, commit,
	// task_id — best-effort, per hunk), and "landed_task_ids" ([]string): the
	// deterministic pairwise attribution naming which task's already-landed
	// commit each conflicting hunk traces to (spec: "this attribution
	// payload is an acceptance criterion, not a nice-to-have").
	MergeConflicted EventType = "merge.conflicted"
	// MergeCompleted fires when a merge lands cleanly and the worktree/branch
	// are cleaned up. Payload: {"branch": string, "target_branch": string}.
	// The pipeline engine's land queue additionally sets "fork_sha" and
	// "trunk_head": the trunk state this land attempt gated and landed
	// against.
	MergeCompleted EventType = "merge.completed"
	// MergeRefused fires when a session's Land is refused — a gate said no
	// before anything ran, or the user's checkout refused the merge and it
	// was aborted (docs/SESSION-WORKTREES-DESIGN.md D6). Nothing changed in
	// either tree.
	//
	// Land's payloads, for all four merge.* types: {"kind": "land",
	// "branch": string, "base": string, "target_branch": string (= base,
	// for older readers)}, plus merge.completed {"merge_commit": string,
	// "fast_forwarded": bool}, merge.conflicted {"files": []string,
	// "step": "base" (the base merging into the session folder conflicted,
	// and was aborted)}, merge.refused {"reason": string (a
	// contracts.Land* reason code), "detail": string, "files": []string}.
	// Empty keys are omitted.
	MergeRefused EventType = "merge.refused"
	// MergeAutoResolved fires when a session's Land stopped after step 1
	// because mergiraf (pkg/mergedriver) merged files that plain git would
	// have left conflicted: the base is merged and committed in the
	// session's folder, the user's checkout is untouched, and the next Land
	// carries on from there. Payload: Land's common fields plus {"files":
	// []string (the files mergiraf resolved), "merge_commit": string (the
	// step-1 merge commit in the session's folder)}.
	MergeAutoResolved EventType = "merge.auto_resolved"
	// WorktreeEnsured fires when the session runner had to do something to
	// give a session its isolated folder, or decided not to isolate it
	// (docs/SESSION-WORKTREES-DESIGN.md D1/D2: nothing heals silently). A
	// quiet reuse of a healthy folder emits nothing. Payload: {"action":
	// "created"|"reattached"|"reused"|"not_isolated", "dir": string,
	// "branch": string, "base": string, "reason": string (not_isolated:
	// "detached_head"), "stale_moved_to": string, "unlocked_stale": bool,
	// "base_missing": bool, "start_point": string, "head": string (reused
	// on another branch, or "HEAD" when detached)}. Every key but action is
	// omitted when empty.
	WorktreeEnsured EventType = "worktree.ensured"
	// LandRetried fires when a post-rebase gate failure is triaged as a flake
	// (an isolation rerun of the failing portion went green) and the land
	// attempt retries — bounded, never unbounded, per land (spec: flaky
	// tests are the canonical merge-queue killer; bounded retry with
	// recorded evidence is the standard countermeasure). Payload: {"branch":
	// string, "fork_sha": string, "trunk_head": string, "stage": string,
	// "attempt": int, "classification": "flake", "packages": []string
	// (Go packages the isolation rerun targeted, omitted when the failure
	// wasn't package-attributable), "isolation_runs": int}.
	LandRetried EventType = "land.retried"
	// LandAutofixed fires when a post-rebase gate failure is classified as a
	// mechanical, whitelisted-fixer-class lint failure (gofmt/gofumpt/
	// goimports — never errcheck/staticcheck/unused, which stay human-only)
	// and the land queue's mechanical autofix pass changes the tree: the
	// fixers ran, their result was committed as its own "land: mechanical
	// lint fixes" commit, and the full gate is about to rerun. Bounded to one
	// autofix attempt per land attempt (spec: "trunk went lint-red silently"
	// — the land gate must run make check, and a lint-only failure emergent
	// from combining two individually-clean branches gets one mechanical
	// pass before falling back to a human block). Payload: {"branch": string,
	// "fork_sha": string, "trunk_head": string, "stage": string, "fixers":
	// []string (whitelisted fixer binaries that ran), "diff_summary":
	// string (git diff --stat of the autofix commit)}.
	LandAutofixed EventType = "land.autofixed"

	// UsageRecorded fires when a provider turn or pipeline stage one-shot completes with
	// cost reporting available (spec §11's hookpoint: tokens in, tokens out, model, and the
	// caller-computed dollar cost — the Data Service does no pricing math itself). SessionID
	// is always set; TaskID is set for a pipeline stage's usage, empty for an interactive
	// session turn. Payload: {"model": string, "input_tokens": int, "output_tokens": int,
	// "cache_creation_input_tokens": int, "cache_read_input_tokens": int, "cost_usd":
	// float64, "stage": string, "attempt": int}. stage/attempt attribute a pipeline
	// one-shot to the template stage and loop iteration that produced it — empty/zero for
	// an interactive session turn. All fields are additive: events recorded before a given
	// field existed simply lack that key, which readers must treat as unknown, not zero.
	// Emission from services/pipeline's stage runs is live; services/session's runner on
	// EventTurnEnd is future wiring. W5.7's cost metrics view is the consumer this type
	// exists for.
	UsageRecorded EventType = "usage.recorded"
	// CapabilityInvoked fires when a session injects or calls a declared capability
	// mid-turn — skill injection or an MCP tool use (spec §12.4). Payload: {"capability":
	// string, "type": "skill"|"mcp"}. Emission is future wiring (the capability runtime's
	// injection paths, services/session/internal/runner); W5.7's capability-usage metric
	// (including never-used capabilities) is the consumer this type exists for.
	CapabilityInvoked EventType = "capability.invoked"

	// StageAttemptRef fires as soon as a pipeline stage's agent one-shot
	// announces its provider-native session ref (e.g. claude-code's
	// session_id), before the one-shot completes — exact attribution for
	// transcript lookup by task+stage+attempt, no path guessing. Payload:
	// {"stage": string, "attempt": int, "ref": string}. Providers with no
	// wire-level ref announcement never emit this.
	StageAttemptRef EventType = "stage.attempt_ref"
	// StageCapabilitiesMounted fires once per stage session right before an
	// agent stage's one-shot begins, recording which of the agent's declared
	// capabilities were mounted (started/installed) versus skipped by
	// effective-capability scoping (task+workspace required capabilities,
	// spec's context-assembly scoping) — so a "tool missing" report is
	// diagnosable as scoping vs agent declaration vs health. Payload:
	// {"stage": string, "scoped": bool, "mounted_skills": []string,
	// "skipped_skills": []string, "mounted_mcp_servers": []string,
	// "skipped_mcp_servers": []string}. scoped is false when the task's
	// effective capability set was empty — the full agent declaration is
	// mounted unchanged and nothing is skipped, matching pre-scoping
	// behavior.
	StageCapabilitiesMounted EventType = "stage.capabilities_mounted"

	// SynthesisProposalCreated fires when the background task synthesizer
	// (spec §4) persists a create_task/add_note/update_task proposal for
	// review. Payload: {"proposal_id": string, "type": string}.
	SynthesisProposalCreated EventType = "synthesis.proposal_created"
	// SynthesisProposalApproved fires once an approved proposal's change has
	// been applied through the existing task APIs (actor "synthesizer").
	// Payload: {"proposal_id": string}.
	SynthesisProposalApproved EventType = "synthesis.proposal_approved"
	// SynthesisProposalDismissed fires when a proposal is dismissed without
	// applying any change. Payload: {"proposal_id": string}.
	SynthesisProposalDismissed EventType = "synthesis.proposal_dismissed"
)

// EventTypes is the full registry, for validation and enumeration.
var EventTypes = []EventType{
	SessionCreated, SessionProvisioning, SessionRenamed, SessionActivityChanged, SessionUpdated, SessionDeleted, SessionCrashed,
	SessionPermissionRequested, SessionPermissionResolved,
	SessionSubagentEvent, SessionSubagentTask, SessionThinking, SessionToolStatusChanged, SessionCompact, SessionQueueChanged, SessionAgentState, SessionUsageUpdated, SessionDelegateChanged,
	SessionTrustChanged,
	TaskCreated, TaskUpdated, TaskStatusChanged, TaskConfirmed, TaskDiscarded,
	NoteCreated, NoteConfirmed, NoteDiscarded,
	FileChanged,
	CommitCreated, CommitLinked, CommitUndone,
	PipelineStarted, PipelinePaused, PipelineResumed, PipelineCancelled, PipelineCompleted, PipelineFailed,
	StageStarted, StageCompleted, StageFailed, StageSkipped, StageRateLimited, StageRateLimitCleared, StageRetried,
	RetroStarted, RetroProgress, RetroCompleted, RetroFailed,
	RetroCancelled, RetroDPAAccepted, RetroDPARejected, RetroDPAEdited, RetroFollowUpCreated,
	JobQueued, JobStarted, JobCompleted, JobFailed, JobCancelled,
	CapabilityCreated, CapabilityUpdated, CapabilityDeleted, CapabilityHealthChanged,
	AgentCreated, AgentUpdated, AgentDeleted, AgentSelected, AgentOutcomeRecorded, AgentDraftGenerated,
	WorkspaceCreated, WorkspaceUpdated, WorkspaceDeleted,
	LinkCreated, LinkUpdated, LinkDeleted, LinkMessageInjected, TurnEnded,
	TemplateCreated, TemplateUpdated, TemplateDeleted,
	ProfileCreated, ProfileUpdated, ProfileDeleted, ConfigUpdated,
	AttachmentAdded,
	MergeAttempted, MergeConflicted, MergeCompleted, MergeRefused, MergeAutoResolved, LandRetried, LandAutofixed,
	WorktreeEnsured,
	UsageRecorded, CapabilityInvoked,
	StageAttemptRef, StageCapabilitiesMounted,
	SynthesisProposalCreated, SynthesisProposalApproved, SynthesisProposalDismissed,
}

var eventTypeSet = func() map[EventType]struct{} {
	s := make(map[EventType]struct{}, len(EventTypes))
	for _, t := range EventTypes {
		s[t] = struct{}{}
	}
	return s
}()

// Valid reports whether t is a registered event type.
func (t EventType) Valid() bool {
	_, ok := eventTypeSet[t]
	return ok
}

// Domain returns the part before the first dot ("task" for "task.created").
func (t EventType) Domain() string {
	s := string(t)
	if i := strings.IndexByte(s, '.'); i >= 0 {
		return s[:i]
	}
	return s
}

// MatchesFilter reports whether t matches a subscription filter pattern.
// A pattern is either an exact type ("task.created") or a domain wildcard
// ("task.*"). Subscription endpoints accept lists of these patterns
// (spec §15: GET /events?types=...).
func (t EventType) MatchesFilter(pattern string) bool {
	if dom, ok := strings.CutSuffix(pattern, ".*"); ok {
		return t.Domain() == dom
	}
	return string(t) == pattern
}
