package api

import (
	"context"
	"errors"
	"fmt"

	"github.com/drewkidwell/mads/pkg/agentselect"
	"github.com/drewkidwell/mads/pkg/autostaff"
	"github.com/drewkidwell/mads/pkg/capinfer"
	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
)

// resolveAutoAgent is interactive session-start's agent="auto" path: infer
// capability requirements from the composer's opening prompt (pkg/capinfer
// — the same inference mechanism the task editor's draft-chip suggestions
// use), then route through the identical shared population + staffing
// machinery pipeline agent:auto stages use (agentselect.BuildRequest,
// autostaff.SelectOrGenerate) — owner decision, 2026-07-30: one
// implementation, two call sites. Mutates sess.Agent in place from "auto"
// to a concrete, now-registered agent name; never leaves "auto" persisted.
func (s *server) resolveAutoAgent(ctx context.Context, sess *contracts.Session, openingPrompt string) error {
	var repos []contracts.WorkspaceRepo
	if sess.Workspace != "" {
		if ws, err := s.data.Workspace(ctx, sess.Workspace); err == nil {
			repos = ws.Repos
		} else if !errors.Is(err, dataclient.ErrNotFound) {
			return fmt.Errorf("resolve workspace %q: %w", sess.Workspace, err)
		}
	}

	suggested, err := capinfer.New(s.data, s.jobs.FastModel()).Infer(ctx, openingPrompt, repos)
	if err != nil {
		return fmt.Errorf("infer capabilities: %w", err)
	}
	registry, err := s.data.Capabilities(ctx)
	if err != nil {
		return fmt.Errorf("list capabilities: %w", err)
	}

	req := agentselect.BuildRequest("", "", "", "", openingPrompt, nil, suggested, registry)
	name, _, err := autostaff.SelectOrGenerate(ctx, s.data, s.jobs.FastModel(), req)
	if err != nil {
		return err
	}
	sess.Agent = name
	return nil
}
