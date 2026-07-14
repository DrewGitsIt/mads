// Package retro generates Durable Preventative Actions (DPAs) from a set of
// sessions (spec §7): a one-shot Fast Model analysis of tasks, file changes,
// commits, and user-steering observations, mirroring pkg/sessionsummary's
// Generator shape. Generation never writes to the Data Service — the caller
// (the Router's retro job body) persists the result in a single write only
// after generation fully succeeds, so cancellation mid-generation can never
// leave partial state.
package retro

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/providers/fastmodel"
)

// generationSystem is the fixed retro-analysis prompt (spec §7): the DPA
// schema, the seven exact remediation_layer values, and the one-shot framing
// (this is an analysis, not a conversation).
const generationSystem = `You are the mads retro assistant. Given data for one or more finished ` +
	`sessions — their tasks, file changes, commits, and any prior retro findings — identify Durable ` +
	`Preventative Actions (DPAs): concrete, structural fixes for what went wrong, not just what happened. ` +
	`Each DPA has exactly these fields:

- problem: what went wrong, stated plainly
- evidence: the specific data (task, note, file change, or commit) that shows it happened
- root_cause: why it happened, not just what happened
- remediation_layer: exactly one of agent_config, tool_config, prompt_engineering, task_decomposition, dependency_structure, pipeline_template, human_process
- suggested_action: the concrete fix

Reply with ONLY a JSON object of this exact shape, no prose, no code fences:

{"dpas": [{"problem": "...", "evidence": "...", "root_cause": "...", "remediation_layer": "...", "suggested_action": "..."}]}

Produce at least one DPA. If user observations are given, let them steer which problems matter most.`

// Generator produces DPAs against a live Data Service and Fast Model —
// mirrors pkg/sessionsummary.Generator's shape.
type Generator struct {
	Data      *dataclient.Client
	FastModel *fastmodel.Runner
}

// New builds a Generator.
func New(data *dataclient.Client, fm *fastmodel.Runner) *Generator {
	return &Generator{Data: data, FastModel: fm}
}

// dpaDraft is one Fast Model DPA reply, before ID minting and status default.
type dpaDraft struct {
	Problem          string                     `json:"problem"`
	Evidence         string                     `json:"evidence"`
	RootCause        string                     `json:"root_cause"`
	RemediationLayer contracts.RemediationLayer `json:"remediation_layer"`
	SuggestedAction  string                     `json:"suggested_action"`
}

// dpaListReply is the Fast Model's structured reply shape.
type dpaListReply struct {
	DPAs []dpaDraft `json:"dpas"`
}

// Validate requires every field on every DPA, and a valid remediation_layer —
// the fastmodel.Runner retry loop this feeds is what makes "all five DPA
// fields populated" a reliable acceptance criterion.
func (d *dpaListReply) Validate() error {
	if len(d.DPAs) == 0 {
		return errors.New("at least one DPA is required")
	}
	for i, dpa := range d.DPAs {
		switch {
		case dpa.Problem == "":
			return fmt.Errorf("dpa %d: problem is required", i)
		case dpa.Evidence == "":
			return fmt.Errorf("dpa %d: evidence is required", i)
		case dpa.RootCause == "":
			return fmt.Errorf("dpa %d: root_cause is required", i)
		case dpa.SuggestedAction == "":
			return fmt.Errorf("dpa %d: suggested_action is required", i)
		case !dpa.RemediationLayer.Valid():
			return fmt.Errorf("dpa %d: unknown remediation_layer %q", i, dpa.RemediationLayer)
		}
	}
	return nil
}

// Generate produces DPAs for the given sessions. progress is called at each
// checkpoint ("gathering", then "generating") so the caller can turn these
// into retro.progress events. Every Data Service and Fast Model call below
// takes ctx, so a cancelled ctx aborts generation promptly and returns
// ctx.Err() — Generate itself never writes to the Data Service.
func (g *Generator) Generate(ctx context.Context, sessionIDs []string, userObservations string, progress func(step string)) ([]contracts.DPA, error) {
	progress("gathering")

	var b strings.Builder
	for _, sessionID := range sessionIDs {
		if err := g.renderSession(ctx, &b, sessionID); err != nil {
			return nil, err
		}
	}
	if userObservations != "" {
		fmt.Fprintf(&b, "User observations:\n%s\n", userObservations)
	}

	progress("generating")
	var reply dpaListReply
	if _, err := g.FastModel.Do(ctx, fastmodel.Job{System: generationSystem, Prompt: b.String()}, &reply); err != nil {
		return nil, fmt.Errorf("retro: generate: %w", err)
	}

	dpas := make([]contracts.DPA, len(reply.DPAs))
	for i, d := range reply.DPAs {
		dpas[i] = contracts.DPA{
			ID:               fmt.Sprintf("dpa_%d", i+1),
			Problem:          d.Problem,
			Evidence:         d.Evidence,
			RootCause:        d.RootCause,
			RemediationLayer: d.RemediationLayer,
			SuggestedAction:  d.SuggestedAction,
			Status:           contracts.DPAAccepted,
		}
	}
	return dpas, nil
}

// renderSession appends one session's prompt block: name/summary, tasks with
// their notes, file changes, and commits. If the session is tagged "retro"
// and points back at a prior retro, that retro's DPAs are folded in as prior
// findings this session was meant to address — this branch is the entire
// retro-of-retros / tag-aware-analysis requirement; there is no separate
// mode or function.
func (g *Generator) renderSession(ctx context.Context, b *strings.Builder, sessionID string) error {
	sess, err := g.Data.Session(ctx, sessionID)
	if err != nil {
		return fmt.Errorf("retro: session %s: %w", sessionID, err)
	}
	tasks, err := g.Data.Tasks(ctx, sessionID, "")
	if err != nil {
		return fmt.Errorf("retro: tasks %s: %w", sessionID, err)
	}
	changes, err := g.Data.FileChanges(ctx, sessionID)
	if err != nil {
		return fmt.Errorf("retro: file changes %s: %w", sessionID, err)
	}
	commits, err := g.Data.Commits(ctx, sessionID)
	if err != nil {
		return fmt.Errorf("retro: commits %s: %w", sessionID, err)
	}

	fmt.Fprintf(b, "Session: %s\n", sess.Name)
	if sess.Summary != "" {
		fmt.Fprintf(b, "Summary: %s\n", sess.Summary)
	}

	if isRetroTagged(sess) {
		prior, err := g.Data.Retro(ctx, sess.RetroID)
		if err != nil {
			return fmt.Errorf("retro: prior retro %s: %w", sess.RetroID, err)
		}
		b.WriteString("Prior retro findings this session was meant to address:\n")
		for _, dpa := range prior.DPAs {
			fmt.Fprintf(b, "- Problem: %s\n  Root cause: %s\n  Suggested action: %s\n",
				dpa.Problem, dpa.RootCause, dpa.SuggestedAction)
		}
	}

	for _, t := range tasks {
		fmt.Fprintf(b, "Task: %s\nStatus: %s\nRetries: %d\n", t.Title, t.Status, t.RetryCount)
		if t.Description != "" {
			fmt.Fprintf(b, "Description: %s\n", t.Description)
		}
		// NoteType spec/code divergence: REBUILD-SPEC.md §7 names dead_end/
		// test_result as the richest signal, but those types were never
		// implemented (contracts.NoteType only has the six below). Surface
		// every existing type instead — blocker/risk are the closest analogue.
		for _, n := range t.Notes {
			fmt.Fprintf(b, "Note (%s): %s\n", n.Type, n.Content)
		}
	}
	for _, fc := range changes {
		fmt.Fprintf(b, "File change: %s (%s, attributed=%v)\n", fc.Path, fc.Change, fc.Attributed)
	}
	for _, c := range commits {
		fmt.Fprintf(b, "Commit %s: %s (orphan=%v)\n", c.Hash, c.Message, c.Orphan)
	}
	b.WriteString("\n")
	return nil
}

func isRetroTagged(sess contracts.Session) bool {
	if sess.RetroID == "" {
		return false
	}
	for _, tag := range sess.Tags {
		if tag == "retro" {
			return true
		}
	}
	return false
}
