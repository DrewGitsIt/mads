package synth

import (
	"fmt"
	"strings"

	"github.com/drewkidwell/mads/pkg/contracts"
)

// synthesisSystemPrompt instructs the fast model to draft tasks and notes
// from a transcript delta (spec §4). Note Type must be one of the existing
// contracts.NoteType vocabulary — the model's prompt-level vocabulary maps
// onto that enum directly, rather than growing a parallel one.
const synthesisSystemPrompt = `You are a note-taker watching a coding session's transcript delta.
Read the delta below and propose draft tasks and notes worth surfacing to the user.

Reply with ONLY JSON of this shape:
{"tasks":[{"title":"...","description":"...","acceptance_criteria":"...","scope":"...","confidence":0.0}],
 "notes":[{"type":"blocker|decision|context|risk|follow_up|observation","content":"...","confidence":0.0}]}

Only propose a task when real, actionable work is implied. Only propose a note for a genuine
decision, blocker, risk, follow-up, or observation worth recording. Confidence is your own
estimate, 0 to 1, of how clearly the transcript supports this item. Omit "tasks" or "notes"
entirely when you have nothing to propose for that kind.`

// Draft is the fast model's structured synthesis output (spec §4).
type Draft struct {
	Tasks []DraftTask `json:"tasks,omitempty"`
	Notes []DraftNote `json:"notes,omitempty"`
}

// DraftTask is one candidate task, not yet written (spec §4).
type DraftTask struct {
	Title              string  `json:"title"`
	Description        string  `json:"description,omitempty"`
	AcceptanceCriteria string  `json:"acceptance_criteria,omitempty"`
	Scope              string  `json:"scope,omitempty"`
	Confidence         float64 `json:"confidence"`
}

// DraftNote is one candidate note, not yet written (spec §4).
type DraftNote struct {
	Type       contracts.NoteType `json:"type"`
	Content    string             `json:"content"`
	Confidence float64            `json:"confidence"`
}

// Validate satisfies fastmodel.Validator: every problem reported at once
// (spec §10.3 ethos).
func (d *Draft) Validate() error {
	var probs []string
	for i, t := range d.Tasks {
		if t.Title == "" {
			probs = append(probs, fmt.Sprintf("tasks[%d].title is required", i))
		}
		if t.Confidence < 0 || t.Confidence > 1 {
			probs = append(probs, fmt.Sprintf("tasks[%d].confidence must be within [0,1]", i))
		}
	}
	for i, n := range d.Notes {
		if n.Content == "" {
			probs = append(probs, fmt.Sprintf("notes[%d].content is required", i))
		}
		if !validNoteType(n.Type) {
			probs = append(probs, fmt.Sprintf("notes[%d].type %q is not a known note type", i, n.Type))
		}
		if n.Confidence < 0 || n.Confidence > 1 {
			probs = append(probs, fmt.Sprintf("notes[%d].confidence must be within [0,1]", i))
		}
	}
	if len(probs) == 0 {
		return nil
	}
	return fmt.Errorf("%s", strings.Join(probs, "; "))
}

func validNoteType(t contracts.NoteType) bool {
	switch t {
	case contracts.NoteBlocker, contracts.NoteDecision, contracts.NoteContext,
		contracts.NoteRisk, contracts.NoteFollowUp, contracts.NoteObservation:
		return true
	default:
		return false
	}
}

// lightConfidenceThreshold is the minimum confidence Light mode keeps
// (spec §10); Full mode keeps everything the fast model proposes.
const lightConfidenceThreshold = 0.7

// filterHighConfidence drops entries below lightConfidenceThreshold.
func filterHighConfidence(d Draft) Draft {
	var out Draft
	for _, t := range d.Tasks {
		if t.Confidence >= lightConfidenceThreshold {
			out.Tasks = append(out.Tasks, t)
		}
	}
	for _, n := range d.Notes {
		if n.Confidence >= lightConfidenceThreshold {
			out.Notes = append(out.Notes, n)
		}
	}
	return out
}
