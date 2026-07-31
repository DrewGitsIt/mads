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
	var workspace contracts.Workspace
	if sess.Workspace != "" {
		if ws, err := s.data.Workspace(ctx, sess.Workspace); err == nil {
			workspace = ws
		} else if !errors.Is(err, dataclient.ErrNotFound) {
			return fmt.Errorf("resolve workspace %q: %w", sess.Workspace, err)
		}
	}

	suggested, err := capinfer.New(s.data, s.jobs.FastModel()).Infer(ctx, openingPrompt, workspace.Repos)
	if err != nil {
		return fmt.Errorf("infer capabilities: %w", err)
	}
	registry, err := s.data.Capabilities(ctx)
	if err != nil {
		return fmt.Errorf("list capabilities: %w", err)
	}

	// Union workspace baseline with inference's suggestions through the same
	// EffectiveCapabilities pipeline agent:auto uses (services/pipeline/engine.
	// effectiveCapabilities) — a workspace's ambient facts (e.g. "gitlab") must
	// reach BuildRequest even when inference alone doesn't also suggest them.
	effective := agentselect.EffectiveCapabilities(contracts.Task{RequiredCapabilities: suggested}, workspace)
	req := agentselect.BuildRequest("", "", "", "", openingPrompt, nil, effective, registry)
	name, _, err := autostaff.SelectOrGenerate(ctx, s.data, s.jobs.FastModel(), req)
	if err != nil {
		return err
	}
	sess.Agent = name
	return nil
}
