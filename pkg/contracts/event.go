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
	SessionDeleted         EventType = "session.deleted"

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

	StageStarted   EventType = "stage.started"
	StageCompleted EventType = "stage.completed"
	StageFailed    EventType = "stage.failed"

	RetroStarted   EventType = "retro.started"
	RetroProgress  EventType = "retro.progress"
	RetroCompleted EventType = "retro.completed"
	RetroFailed    EventType = "retro.failed"

	CapabilityCreated       EventType = "capability.created"
	CapabilityUpdated       EventType = "capability.updated"
	CapabilityDeleted       EventType = "capability.deleted"
	CapabilityHealthChanged EventType = "capability.health_changed"

	AgentCreated EventType = "agent.created"
	AgentUpdated EventType = "agent.updated"
	AgentDeleted EventType = "agent.deleted"

	WorkspaceCreated EventType = "workspace.created"
	WorkspaceUpdated EventType = "workspace.updated"
	WorkspaceDeleted EventType = "workspace.deleted"

	TemplateCreated EventType = "template.created"
	TemplateUpdated EventType = "template.updated"
	TemplateDeleted EventType = "template.deleted"

	AttachmentAdded EventType = "attachment.added"

	MergeAttempted  EventType = "merge.attempted"
	MergeConflicted EventType = "merge.conflicted"
	MergeCompleted  EventType = "merge.completed"
)

// EventTypes is the full registry, for validation and enumeration.
var EventTypes = []EventType{
	SessionCreated, SessionProvisioning, SessionRenamed, SessionActivityChanged, SessionDeleted,
	TaskCreated, TaskUpdated, TaskStatusChanged, TaskConfirmed, TaskDiscarded,
	NoteCreated, NoteConfirmed, NoteDiscarded,
	FileChanged,
	CommitCreated, CommitLinked,
	PipelineStarted, PipelinePaused, PipelineResumed, PipelineCancelled, PipelineCompleted,
	StageStarted, StageCompleted, StageFailed,
	RetroStarted, RetroProgress, RetroCompleted, RetroFailed,
	CapabilityCreated, CapabilityUpdated, CapabilityDeleted, CapabilityHealthChanged,
	AgentCreated, AgentUpdated, AgentDeleted,
	WorkspaceCreated, WorkspaceUpdated, WorkspaceDeleted,
	TemplateCreated, TemplateUpdated, TemplateDeleted,
	AttachmentAdded,
	MergeAttempted, MergeConflicted, MergeCompleted,
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
