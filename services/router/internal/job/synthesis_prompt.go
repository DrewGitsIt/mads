package job

import (
	"fmt"
	"strings"

	"github.com/drewkidwell/mads/pkg/contracts"
)

// synthesisSystemPrompt instructs the Fast Model to propose task/note
// changes from a session's accumulated transcript delta (spec §4). Unlike
// services/session/internal/synth's Draft (new tasks/notes only), this
// vocabulary includes update_task, since the model also sees the session's
// current task list and can propose refining or splitting existing work.
const synthesisSystemPrompt = `You are a background task synthesizer watching a coding session.
Read the recent activity and the current task list below, and propose changes worth surfacing
to the user for review. Every change is a PROPOSAL: it will be reviewed before it takes effect.

Reply with ONLY JSON of this shape:
{"create_tasks":[{"title":"...","description":"...","acceptance_criteria":"...","scope":"..."}],
 "add_notes":[{"type":"blocker|decision|context|risk|follow_up|observation","content":"..."}],
 "update_tasks":[{"title":"...","description":"...","acceptance_criteria":"...","scope":"..."}]}

create_tasks proposes wholly new tasks. add_notes and update_tasks both target the session's
current active task (the task list below marks it) — omit them entirely if there is no active
task, or nothing worth noting/updating on it. Only propose a task when real, actionable work is
implied. Only propose a note for a genuine decision, blocker, risk, follow-up, or observation.
Only propose an update when the active task's title/description/acceptance criteria/scope should
change given what just happened — leave fields you're not changing empty. Omit any of the three
top-level keys entirely when you have nothing to propose for it.`

// SynthesisDraft is the Fast Model's structured synthesis output.
type SynthesisDraft struct {
	CreateTasks []DraftCreateTask `json:"create_tasks,omitempty"`
	AddNotes    []DraftAddNote    `json:"add_notes,omitempty"`
	UpdateTasks []DraftUpdateTask `json:"update_tasks,omitempty"`
}

// DraftCreateTask is one candidate new task, not yet proposed.
type DraftCreateTask struct {
	Title              string `json:"title"`
	Description        string `json:"description,omitempty"`
	AcceptanceCriteria string `json:"acceptance_criteria,omitempty"`
	Scope              string `json:"scope,omitempty"`
}

// DraftAddNote is one candidate note on the active task, not yet proposed.
type DraftAddNote struct {
	Type    contracts.NoteType `json:"type"`
	Content string             `json:"content"`
}

// DraftUpdateTask is a candidate change to the active task's fields, not yet
// proposed. Empty fields mean "leave unchanged".
type DraftUpdateTask struct {
	Title              string `json:"title,omitempty"`
	Description        string `json:"description,omitempty"`
	AcceptanceCriteria string `json:"acceptance_criteria,omitempty"`
	Scope              string `json:"scope,omitempty"`
}

// Validate satisfies fastmodel.Validator: every problem reported at once
// (spec §10.3 ethos).
func (d *SynthesisDraft) Validate() error {
	var probs []string
	for i, t := range d.CreateTasks {
		if t.Title == "" {
			probs = append(probs, fmt.Sprintf("create_tasks[%d].title is required", i))
		}
	}
	for i, n := range d.AddNotes {
		if n.Content == "" {
			probs = append(probs, fmt.Sprintf("add_notes[%d].content is required", i))
		}
		if !validSynthesisNoteType(n.Type) {
			probs = append(probs, fmt.Sprintf("add_notes[%d].type %q is not a known note type", i, n.Type))
		}
	}
	if len(probs) == 0 {
		return nil
	}
	return fmt.Errorf("%s", strings.Join(probs, "; "))
}

func validSynthesisNoteType(t contracts.NoteType) bool {
	switch t {
	case contracts.NoteBlocker, contracts.NoteDecision, contracts.NoteContext,
		contracts.NoteRisk, contracts.NoteFollowUp, contracts.NoteObservation:
		return true
	default:
		return false
	}
}

// Token budget constants (spec §4's Input Context table, ~3500-5800 tokens
// total). No tokenizer is vendored; truncateToTokens' chars/4 approximation
// is the same rough estimate used elsewhere in this codebase for budget
// enforcement, not for billing.
const (
	maxTotalTokens      = 5800
	maxRecentTokens     = 3000
	maxCompressedTokens = 1000
	maxTaskListTokens   = 1000
	// fullFidelityTurns is how many of the most recent turns render at full
	// fidelity; anything older is compressed to fit maxCompressedTokens.
	fullFidelityTurns = 6
)

func truncateToTokens(s string, maxTokens int) string {
	maxChars := maxTokens * 4
	if len(s) <= maxChars {
		return s
	}
	return s[:maxChars] + "\n[truncated]"
}

// BuildSynthesisPrompt assembles the Fast Model prompt for one synthesis
// pass: the most recent turns at full fidelity, older turns compressed to
// fit, and the session's current task list (spec §4: so the model can
// propose updates/splits, not just creations) — each independently capped,
// then the whole prompt capped again as a hard ceiling. turns is one
// rendered transcript delta (session/internal/synth/transcript.go's shape:
// "user: "/"assistant: "/"tool: name path" lines) per accumulated turn —
// tool entries never carry raw tool output, only name and path, so nothing
// here needs to separately strip it.
func BuildSynthesisPrompt(turns []string, tasks []contracts.Task, activeTaskID string) string {
	recent := turns
	var older []string
	if len(recent) > fullFidelityTurns {
		older = recent[:len(recent)-fullFidelityTurns]
		recent = recent[len(recent)-fullFidelityTurns:]
	}

	recentText := truncateToTokens(strings.Join(recent, ""), maxRecentTokens)
	taskListText := truncateToTokens(renderTaskList(tasks, activeTaskID), maxTaskListTokens)

	var sb strings.Builder
	if len(older) > 0 {
		olderText := truncateToTokens(strings.Join(older, ""), maxCompressedTokens)
		sb.WriteString("Earlier in this session (condensed):\n")
		sb.WriteString(olderText)
		sb.WriteString("\n\n")
	}
	sb.WriteString("Most recent activity:\n")
	sb.WriteString(recentText)
	sb.WriteString("\n\nCurrent task list:\n")
	sb.WriteString(taskListText)

	return truncateToTokens(sb.String(), maxTotalTokens)
}

func renderTaskList(tasks []contracts.Task, activeTaskID string) string {
	if len(tasks) == 0 {
		return "(no tasks yet)"
	}
	var sb strings.Builder
	for _, t := range tasks {
		marker := ""
		if t.ID == activeTaskID {
			marker = " (active)"
		}
		fmt.Fprintf(&sb, "- [%s] %s: %s%s\n", t.ID, t.Status, t.Title, marker)
	}
	return sb.String()
}
