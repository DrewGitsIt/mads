package job

import (
	"context"
	"errors"
	"fmt"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/providers/fastmodel"
)

const SynthesisKind = "synthesis"

// SynthesizeSession is the job body for a debounced burst of a session's
// turn_ended activity (spec §4): prompt the Fast Model for create_task/
// add_note/update_task changes, and persist each as a Proposal — never a
// direct task/note write. auto_approve (a config-level power-user
// preference) applies each proposal immediately after creation.
func SynthesizeSession(sessionID string, turns []string) Func {
	return func(ctx context.Context, deps Deps) error {
		if _, err := deps.Data.Session(ctx, sessionID); err != nil {
			if errors.Is(err, dataclient.ErrNotFound) {
				return nil // session was deleted mid-debounce; nothing to do
			}
			return fmt.Errorf("synthesis: load session: %w", err)
		}

		tasks, err := deps.Data.Tasks(ctx, sessionID, "")
		if err != nil {
			return fmt.Errorf("synthesis: load tasks: %w", err)
		}
		activeTaskID := mostRecentInProgress(tasks)

		prompt := BuildSynthesisPrompt(turns, tasks, activeTaskID)

		var draft SynthesisDraft
		if _, err := deps.FastModel.Do(ctx, fastmodel.Job{System: synthesisSystemPrompt, Prompt: prompt}, &draft); err != nil {
			if fastmodel.IsSkippable(err) {
				return nil // no Fast Model backend configured: skip, not a failure
			}
			return fmt.Errorf("synthesis: fast model: %w", err)
		}

		cfg, err := deps.Data.Config(ctx)
		if err != nil {
			return fmt.Errorf("synthesis: load config: %w", err)
		}

		for _, ct := range draft.CreateTasks {
			p := &contracts.Proposal{
				SessionID: sessionID,
				Type:      contracts.ProposalCreateTask,
				Task: &contracts.ProposedTask{
					Title: ct.Title, Description: ct.Description,
					AcceptanceCriteria: ct.AcceptanceCriteria, Scope: ct.Scope,
				},
			}
			if err := proposeAndMaybeApprove(ctx, deps, p, cfg.SynthesisAutoApprove); err != nil {
				return err
			}
		}

		// add_notes/update_tasks both target the active task; with none,
		// there is nothing to attach them to, so they're dropped rather than
		// guessed onto an unrelated task.
		if activeTaskID == "" {
			return nil
		}
		for _, an := range draft.AddNotes {
			p := &contracts.Proposal{
				SessionID: sessionID,
				Type:      contracts.ProposalAddNote,
				TaskID:    activeTaskID,
				Note:      &contracts.ProposedNote{Type: an.Type, Content: an.Content},
			}
			if err := proposeAndMaybeApprove(ctx, deps, p, cfg.SynthesisAutoApprove); err != nil {
				return err
			}
		}
		for _, ut := range draft.UpdateTasks {
			p := &contracts.Proposal{
				SessionID: sessionID,
				Type:      contracts.ProposalUpdateTask,
				TaskID:    activeTaskID,
				Update: &contracts.ProposedTaskUpdate{
					Title: ut.Title, Description: ut.Description,
					AcceptanceCriteria: ut.AcceptanceCriteria, Scope: ut.Scope,
				},
			}
			if err := proposeAndMaybeApprove(ctx, deps, p, cfg.SynthesisAutoApprove); err != nil {
				return err
			}
		}
		return nil
	}
}

// proposeAndMaybeApprove persists p and, when autoApprove is set, applies it
// immediately through the same Approve path a manual review would use.
func proposeAndMaybeApprove(ctx context.Context, deps Deps, p *contracts.Proposal, autoApprove bool) error {
	if err := deps.Data.CreateProposal(ctx, p); err != nil {
		return fmt.Errorf("synthesis: create proposal: %w", err)
	}
	if autoApprove {
		if _, err := deps.Data.ApproveProposal(ctx, p.ID); err != nil {
			return fmt.Errorf("synthesis: auto-approve proposal %s: %w", p.ID, err)
		}
	}
	return nil
}

// mostRecentInProgress returns the most recently touched in_progress task
// (spec §4's active-task definition), or "" when none is in progress.
func mostRecentInProgress(tasks []contracts.Task) string {
	var best contracts.Task
	found := false
	for _, t := range tasks {
		if t.Status != contracts.StatusInProgress {
			continue
		}
		if !found || t.UpdatedAt.After(best.UpdatedAt) {
			best, found = t, true
		}
	}
	if !found {
		return ""
	}
	return best.ID
}
