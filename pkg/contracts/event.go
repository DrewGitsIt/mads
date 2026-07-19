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
	// and no rate-limit text reaches the next agent prompt. Payload:
	// {"stage": string, "attempt": int, "message": string, "backoff": string}.
	StageRateLimited EventType = "stage.rate_limited"
	// StageRateLimitCleared fires when a rate-limited stage's pause ends and
	// the engine is about to retry. Payload: {"stage": string, "attempt": int}.
	StageRateLimitCleared EventType = "stage.rate_limit_cleared"

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
	// "target_branch": string}.
	MergeAttempted EventType = "merge.attempted"
	// MergeConflicted fires when a merge attempt aborts due to conflicts; the
	// worktree and branch are kept for manual resolution. Payload: {"branch":
	// string, "target_branch": string, "files": []string}.
	MergeConflicted EventType = "merge.conflicted"
	// MergeCompleted fires when a merge lands cleanly and the worktree/branch
	// are cleaned up. Payload: {"branch": string, "target_branch": string}.
	MergeCompleted EventType = "merge.completed"

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
)

// EventTypes is the full registry, for validation and enumeration.
var EventTypes = []EventType{
	SessionCreated, SessionProvisioning, SessionRenamed, SessionActivityChanged, SessionUpdated, SessionDeleted, SessionCrashed,
	TaskCreated, TaskUpdated, TaskStatusChanged, TaskConfirmed, TaskDiscarded,
	NoteCreated, NoteConfirmed, NoteDiscarded,
	FileChanged,
	CommitCreated, CommitLinked,
	PipelineStarted, PipelinePaused, PipelineResumed, PipelineCancelled, PipelineCompleted, PipelineFailed,
	StageStarted, StageCompleted, StageFailed, StageSkipped, StageRateLimited, StageRateLimitCleared,
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
	MergeAttempted, MergeConflicted, MergeCompleted,
	UsageRecorded, CapabilityInvoked,
	StageAttemptRef,
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
