package api

import (
	"context"
	"errors"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/providers/provider"
)

// resolveSessionDefaults fills provider/model/agent/isolation/trust on sess
// from, in order: the session itself (already set), the named workspace's
// Defaults (spec §10: workspace overrides global), then global Config. Best
// effort on workspace lookup — a stale/missing Config.DefaultWorkspace or an
// explicit sess.Workspace that no longer exists degrades to "no workspace
// defaults available", not a failed create (Workspace is optional on Session
// today; this must not become a new hard dependency).
func (s *server) resolveSessionDefaults(ctx context.Context, sess *contracts.Session) error {
	cfg, err := s.data.Config(ctx)
	if err != nil {
		return err
	}

	var wsDefaults contracts.WorkspaceDefaults
	if wsKey := sess.Workspace; wsKey != "" || cfg.DefaultWorkspace != "" {
		if wsKey == "" {
			wsKey = cfg.DefaultWorkspace
		}
		ws, err := s.data.Workspace(ctx, wsKey)
		switch {
		case err == nil:
			wsDefaults = ws.Defaults
		case errors.Is(err, dataclient.ErrNotFound):
			// Stale DefaultWorkspace or a Workspace field that no longer
			// resolves — degrade to "no workspace defaults", not a failure.
		default:
			return err
		}
	}

	if sess.Provider == "" {
		sess.Provider = cfg.DefaultProvider
	}
	if sess.Agent == "" {
		sess.Agent = wsDefaults.Agent
	}
	if sess.Isolation == "" {
		sess.Isolation = wsDefaults.Isolation
	}
	if sess.Isolation == "" {
		sess.Isolation = contracts.IsolationWorktree
	}
	if sess.Trust == "" {
		sess.Trust = cfg.DefaultTrust
	}
	if sess.Trust == "" {
		sess.Trust = contracts.TrustLimited
	}

	// Model: session -> workspace -> the resolved provider's discovered
	// default model. cfg.DefaultModelTier (cheapest/balanced/best) is NOT
	// consulted yet: every tier currently maps to "the default provider's
	// default model". Documented simplification for this task; a real
	// tier->model mapping is future work.
	if sess.Model == "" {
		sess.Model = wsDefaults.Model
	}
	if sess.Model == "" && sess.Provider != "" {
		if p, err := s.providers.Get(sess.Provider); err == nil {
			if md, ok := p.(provider.ModelDiscoverer); ok {
				if models, err := md.Models(ctx); err == nil {
					for _, m := range models {
						if m.Default {
							sess.Model = m.ID
							break
						}
					}
				}
			}
		}
	}

	return nil
}
