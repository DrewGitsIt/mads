// Package sessionsummary builds the compressed narrative for a session on
// its active → inactive transition (spec §16): task titles, statuses,
// handoffs, and notes in, a three-section markdown summary out, written to
// contracts.Session.Summary by the Router's session-summary job
// (services/router/internal/job). Generation is grounded in task data, never
// transcripts, so regeneration on every deactivation stays cheap.
package sessionsummary

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/providers/fastmodel"
)

// generationSystem is the fixed session-summary prompt (spec §16: "compress,
// not enumerate" — a session with 20 tasks might boil down to one or two
// sentences about the arc of the work).
const generationSystem = `You are the mads session-summary assistant. Given a session's tasks — ` +
	`their titles, statuses, descriptions, handoffs, and notes — write a compressed narrative of ` +
	`the session, not a rehash of the task list. Capture what was accomplished, not what was done: ` +
	`a session with 20 tasks might boil down to one or two sentences about the arc of the work. ` +
	`Reply with ONLY a JSON object of this exact shape, no prose, no code fences:

{
  "accomplished": "the primary thread(s) of work, described at the level of intent not mechanics",
  "decisions": "key decisions made, drawn only from the confirmed decisions listed below",
  "remaining": "open tasks or known next steps, if any"
}

If there is nothing to report for "decisions" or "remaining", reply with an empty string for that ` +
	`field — never invent filler.`

// Generator builds session summaries against a live Data Service and Fast
// Model — mirrors pkg/agentgen.Generator's shape.
type Generator struct {
	Data      *dataclient.Client
	FastModel *fastmodel.Runner
}

// New builds a Generator.
func New(data *dataclient.Client, fm *fastmodel.Runner) *Generator {
	return &Generator{Data: data, FastModel: fm}
}

// draft is the Fast Model's structured reply shape.
type draft struct {
	Accomplished string `json:"accomplished"`
	Decisions    string `json:"decisions,omitempty"`
	Remaining    string `json:"remaining,omitempty"`
}

// Validate requires the one field a usable summary can't do without —
// Decisions/Remaining may legitimately be empty strings, the normal
// "nothing to report" case.
func (d *draft) Validate() error {
	if d.Accomplished == "" {
		return errors.New("accomplished is required")
	}
	return nil
}

// Generate derives sessionID's compressed narrative from its task data. A
// session with no tasks has nothing to summarize and returns "", nil without
// spending a Fast Model call.
func (g *Generator) Generate(ctx context.Context, sessionID string) (string, error) {
	tasks, err := g.Data.Tasks(ctx, sessionID, "")
	if err != nil {
		return "", fmt.Errorf("sessionsummary: list tasks: %w", err)
	}
	if len(tasks) == 0 {
		return "", nil
	}

	var d draft
	if _, err := g.FastModel.Do(ctx, fastmodel.Job{System: generationSystem, Prompt: buildPrompt(tasks)}, &d); err != nil {
		return "", fmt.Errorf("sessionsummary: generate: %w", err)
	}
	return render(d), nil
}

// buildPrompt embeds the session's task data verbatim so tests (and, in
// production, the Fast Model) can key off stable, unmodified text. Confirmed
// decision notes are called out separately so the model's "decisions"
// section is grounded in settled, non-draft decisions only (spec §16: "drawn
// from decision type notes on tasks").
func buildPrompt(tasks []contracts.Task) string {
	var b strings.Builder
	b.WriteString("Summarize this session's work from its tasks.\n\n")

	var decisions []string
	for _, t := range tasks {
		fmt.Fprintf(&b, "Task: %s\nStatus: %s\n", t.Title, t.Status)
		if t.Description != "" {
			fmt.Fprintf(&b, "Description: %s\n", t.Description)
		}
		for _, h := range t.Handoffs {
			fmt.Fprintf(&b, "Handoff from %s: %s\n", h.From, h.Content)
		}
		for _, n := range t.Notes {
			fmt.Fprintf(&b, "Note (%s, %s): %s\n", n.Type, n.Status, n.Content)
			if n.Type == contracts.NoteDecision && n.Status == contracts.NoteStatusConfirmed {
				decisions = append(decisions, n.Content)
			}
		}
		b.WriteString("\n")
	}

	b.WriteString("Confirmed decisions:\n")
	if len(decisions) == 0 {
		b.WriteString("(none)\n")
	} else {
		for _, d := range decisions {
			fmt.Fprintf(&b, "- %s\n", d)
		}
	}
	return b.String()
}

// render assembles the three sections into one markdown string. A section
// whose trimmed text is empty is skipped entirely — no heading, no blank
// body (spec §16: empty sections are omitted, not rendered blank).
func render(d draft) string {
	sections := []struct{ heading, body string }{
		{"What was accomplished", d.Accomplished},
		{"Key decisions", d.Decisions},
		{"What remains", d.Remaining},
	}
	var parts []string
	for _, s := range sections {
		body := strings.TrimSpace(s.body)
		if body == "" {
			continue
		}
		parts = append(parts, "## "+s.heading+"\n\n"+body)
	}
	return strings.Join(parts, "\n\n")
}
