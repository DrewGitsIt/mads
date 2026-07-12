package contracts

import "time"

// TaskStatus is a task's lifecycle state (spec §3). Dependency-waiting tasks
// keep status "blocked" in the schema; the UI renders dependency-blocked tasks
// as "Queued" and reserves alarm treatment for needs-human states (MADS-UX §3.3).
type TaskStatus string

const (
	StatusReady       TaskStatus = "ready"
	StatusInProgress  TaskStatus = "in_progress"
	StatusBlocked     TaskStatus = "blocked"
	StatusNeedsReview TaskStatus = "needs_review"
	StatusDone        TaskStatus = "done"
	StatusWontDo      TaskStatus = "wont_do"
)

// TaskStatuses is the full registry, for validation and enumeration.
var TaskStatuses = []TaskStatus{
	StatusReady, StatusInProgress, StatusBlocked, StatusNeedsReview, StatusDone, StatusWontDo,
}

// Valid reports whether s is a registered task status.
func (s TaskStatus) Valid() bool {
	for _, v := range TaskStatuses {
		if s == v {
			return true
		}
	}
	return false
}

// Task is the unit of work (spec §3): orchestrator fields drive scheduling,
// core fields carry the work itself. Every field is mutable after creation;
// synthesized tasks start as drafts and become real on confirmation (spec §4).
type Task struct {
	ID        string     `json:"id"`
	SessionID string     `json:"session_id"`
	Status    TaskStatus `json:"status"`
	// Dependencies lists task IDs that must be done before this task is
	// eligible to run. Cycles may exist at rest; they block pipeline launch
	// (spec §3.6).
	Dependencies []string  `json:"dependencies,omitempty"`
	Labels       []string  `json:"labels,omitempty"`
	RetryCount   int       `json:"retry_count"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`

	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
	// AcceptanceCriteria is markdown — numbered, checkable conditions the
	// review stage judges against (spec §3).
	AcceptanceCriteria string `json:"acceptance_criteria,omitempty"`
	Scope              string `json:"scope,omitempty"`

	// Draft marks a synthesized task awaiting user confirmation (spec §4).
	// Draft tasks render with draft provenance everywhere (MADS-UX rule:
	// unconfirmed LLM output always looks like a draft).
	Draft bool `json:"draft,omitempty"`
	// Source is who authored the task: "user", "synthesizer", "pipeline", or
	// an agent name.
	Source string `json:"source"`

	Handoffs []Handoff `json:"handoffs,omitempty"`
	Notes    []Note    `json:"notes,omitempty"`
}

// Handoff is context passed from one pipeline stage's agent to the next
// (spec §3, §6): what was done, what remains, what the next agent should know.
type Handoff struct {
	From      string    `json:"from"`
	Content   string    `json:"content"`
	Timestamp time.Time `json:"timestamp"`
}

// NoteType classifies a typed note attached to a task (spec §4).
type NoteType string

const (
	NoteBlocker     NoteType = "blocker"
	NoteDecision    NoteType = "decision"
	NoteContext     NoteType = "context"
	NoteRisk        NoteType = "risk"
	NoteFollowUp    NoteType = "follow_up"
	NoteObservation NoteType = "observation"
)

// NoteStatus tracks a note's confirmation state (spec §4): synthesized notes
// start as drafts; user-authored notes are confirmed at creation.
type NoteStatus string

const (
	NoteStatusDraft     NoteStatus = "draft"
	NoteStatusConfirmed NoteStatus = "confirmed"
)

// Note is a typed annotation on a task (spec §4).
type Note struct {
	Type      NoteType   `json:"type"`
	Content   string     `json:"content"`
	Source    string     `json:"source"`
	Status    NoteStatus `json:"status"`
	Timestamp time.Time  `json:"timestamp"`
}
