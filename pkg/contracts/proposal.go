package contracts

import "time"

// ProposalType names what kind of change a synthesis proposal suggests
// (spec §4): a new task, a note on the active task, or an update to an
// existing task — a capability no prior draft mechanism (Task.Draft,
// NoteStatusDraft) supports.
type ProposalType string

const (
	ProposalCreateTask ProposalType = "create_task"
	ProposalAddNote    ProposalType = "add_note"
	ProposalUpdateTask ProposalType = "update_task"
)

// ProposalTypes is the full registry, for validation and enumeration.
var ProposalTypes = []ProposalType{ProposalCreateTask, ProposalAddNote, ProposalUpdateTask}

// Valid reports whether t is a registered proposal type.
func (t ProposalType) Valid() bool {
	for _, v := range ProposalTypes {
		if t == v {
			return true
		}
	}
	return false
}

// ProposalStatus tracks a proposal's review state (spec §4): proposed until
// a user decision (or auto_approve, spec: "power-user preference") applies
// or dismisses it.
type ProposalStatus string

const (
	ProposalProposed  ProposalStatus = "proposed"
	ProposalApproved  ProposalStatus = "approved"
	ProposalDismissed ProposalStatus = "dismissed"
)

// ProposalStatuses is the full registry, for validation and enumeration.
var ProposalStatuses = []ProposalStatus{ProposalProposed, ProposalApproved, ProposalDismissed}

// Valid reports whether s is a registered proposal status.
func (s ProposalStatus) Valid() bool {
	for _, v := range ProposalStatuses {
		if s == v {
			return true
		}
	}
	return false
}

// ProposedTask is a create_task proposal's payload (spec §4): the same
// authorable fields a real Task has, minus everything orchestration owns.
type ProposedTask struct {
	Title              string `json:"title"`
	Description        string `json:"description,omitempty"`
	AcceptanceCriteria string `json:"acceptance_criteria,omitempty"`
	Scope              string `json:"scope,omitempty"`
}

// ProposedNote is an add_note proposal's payload (spec §4).
type ProposedNote struct {
	Type    NoteType `json:"type"`
	Content string   `json:"content"`
}

// ProposedTaskUpdate is an update_task proposal's payload: non-empty fields
// replace the target task's on approval; empty fields leave it unchanged.
type ProposedTaskUpdate struct {
	Title              string `json:"title,omitempty"`
	Description        string `json:"description,omitempty"`
	AcceptanceCriteria string `json:"acceptance_criteria,omitempty"`
	Scope              string `json:"scope,omitempty"`
}

// Proposal is a synthesizer-authored suggestion awaiting review (spec §4's
// "Dual Mandate": a full historical record of what synthesis noticed,
// independent of whether it's ever approved). Approving one mutates through
// the existing task APIs (actor "synthesizer") — the proposal record itself
// never bypasses that path to write a task or note directly.
type Proposal struct {
	ID        string         `json:"id"`
	SessionID string         `json:"session_id"`
	Type      ProposalType   `json:"type"`
	Status    ProposalStatus `json:"status"`

	// TaskID is the active task (spec §4: most recently touched in_progress)
	// this proposal targets — set for add_note and update_task, empty for
	// create_task, which has no target until approved.
	TaskID string `json:"task_id,omitempty"`

	Task   *ProposedTask       `json:"task,omitempty"`
	Note   *ProposedNote       `json:"note,omitempty"`
	Update *ProposedTaskUpdate `json:"update,omitempty"`

	// AppliedTaskID is the task a create_task proposal produced, set once
	// approved, so a later viewer can trace the proposal to its result.
	AppliedTaskID string `json:"applied_task_id,omitempty"`

	// Source is who authored the proposal: "synthesizer" for every proposal
	// this task produces; kept as a field (not hardcoded at the call site)
	// for the same reason Task.Source and Note.Source are fields.
	Source    string    `json:"source"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}
