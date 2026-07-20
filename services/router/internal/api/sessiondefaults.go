package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/providers/provider"
)

// sessionDefaultSources records, per field, which layer actually produced a
// resolved value (only set on the branch that changes the field from empty
// to non-empty — see resolveSessionDefaultsSourced). Used only by the
// Launchpad preview endpoint.
type sessionDefaultSources struct {
	Provider  contracts.SessionDefaultSource
	Model     contracts.SessionDefaultSource
	Agent     contracts.SessionDefaultSource
	Isolation contracts.SessionDefaultSource
	Trust     contracts.SessionDefaultSource
	Workspace string // resolved workspace *name* whose Defaults were consulted; "" if none
}

// resolveSessionDefaults fills provider/model/agent/isolation/trust on sess
// from, in order: the session itself (already set), the named workspace's
// Defaults (spec §10: workspace overrides global), then global Config. Best
// effort on workspace lookup — a stale/missing Config.DefaultWorkspace or an
// explicit sess.Workspace that no longer exists degrades to "no workspace
// defaults available", not a failed create (Workspace is optional on Session
// today; this must not become a new hard dependency). A workspace resolved
// from Config.DefaultWorkspace is also written back onto sess.Workspace —
// otherwise a session created with no explicit pill selection inherits the
// default's other fields but persists workspace=null itself, with nothing
// on the record to say which workspace it was actually resolved against.
func (s *server) resolveSessionDefaults(ctx context.Context, sess *contracts.Session) error {
	_, err := s.resolveSessionDefaultsSourced(ctx, sess)
	return err
}

// resolveSessionDefaultsSourced is resolveSessionDefaults plus provenance
// for each field it resolves. Same body, same order, same mutations, same
// error returns — only used by GET /sessions/defaults today.
func (s *server) resolveSessionDefaultsSourced(ctx context.Context, sess *contracts.Session) (sessionDefaultSources, error) {
	var src sessionDefaultSources

	cfg, err := s.data.Config(ctx)
	if err != nil {
		return src, err
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
			src.Workspace = ws.Name
			sess.Workspace = ws.ID
		case errors.Is(err, dataclient.ErrNotFound):
			// Stale DefaultWorkspace or a Workspace field that no longer
			// resolves — degrade to "no workspace defaults", not a failure.
		default:
			return src, err
		}
	}

	if sess.Provider == "" {
		sess.Provider = cfg.DefaultProvider
		if sess.Provider != "" {
			src.Provider = contracts.SessionDefaultSourceConfig
		}
	}
	if sess.Agent == "" {
		sess.Agent = wsDefaults.Agent
		if sess.Agent != "" {
			src.Agent = contracts.SessionDefaultSourceWorkspace
		}
	}
	if sess.Isolation == "" {
		sess.Isolation = wsDefaults.Isolation
		if sess.Isolation != "" {
			src.Isolation = contracts.SessionDefaultSourceWorkspace
		}
	}
	if sess.Isolation == "" {
		sess.Isolation = contracts.IsolationWorktree
		src.Isolation = contracts.SessionDefaultSourceBuiltin
	}
	if sess.Trust == "" {
		sess.Trust = cfg.DefaultTrust
		if sess.Trust != "" {
			src.Trust = contracts.SessionDefaultSourceConfig
		}
	}
	if sess.Trust == "" {
		sess.Trust = contracts.TrustLimited
		src.Trust = contracts.SessionDefaultSourceBuiltin
	}

	// Model: session -> workspace -> the resolved provider's discovered
	// default model. cfg.DefaultModelTier (cheapest/balanced/best) is NOT
	// consulted yet: every tier currently maps to "the default provider's
	// default model". Documented simplification for this task; a real
	// tier->model mapping is future work.
	if sess.Model == "" {
		sess.Model = wsDefaults.Model
		if sess.Model != "" {
			src.Model = contracts.SessionDefaultSourceWorkspace
		}
	}
	if sess.Model == "" && sess.Provider != "" {
		if p, err := s.providers.Get(sess.Provider); err == nil {
			if md, ok := p.(provider.ModelDiscoverer); ok {
				if models, err := md.Models(ctx); err == nil {
					for _, m := range models {
						if m.Default {
							sess.Model = m.ID
							src.Model = contracts.SessionDefaultSourceProviderDefault
							break
						}
					}
				}
			}
		}
	}

	return src, nil
}

func (s *server) handleSessionDefaults(w http.ResponseWriter, r *http.Request) {
	sess := contracts.Session{Workspace: r.URL.Query().Get("workspace")}
	src, err := s.resolveSessionDefaultsSourced(r.Context(), &sess)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, contracts.SessionDefaults{
		Provider:  contracts.ResolvedSessionField{Value: sess.Provider, Source: src.Provider},
		Model:     contracts.ResolvedSessionField{Value: sess.Model, Source: src.Model},
		Agent:     contracts.ResolvedSessionField{Value: sess.Agent, Source: src.Agent},
		Isolation: contracts.ResolvedSessionField{Value: sess.Isolation, Source: src.Isolation},
		Trust:     contracts.ResolvedSessionField{Value: string(sess.Trust), Source: src.Trust},
		Workspace: src.Workspace,
	})
}
