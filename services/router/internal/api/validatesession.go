package api

import (
	"net/http"

	"github.com/drewkidwell/mads/pkg/contracts"
)

// handleValidateSession is POST /sessions/validate, the dry run of POST
// /sessions: same body, same checks in the same order, but nothing is
// created — no session record, no link, no worktree (the worktree is made at
// spawn, which this never reaches). It answers what the create would
// resolve to (Dir/Isolation, including a joined counterpart worktree) and
// every blocking error, so the UI never re-implements that logic.
//
// Always 200 when the request could be evaluated, with problems in the
// body's errors: a refused create is the answer here, not a failure, and the
// UI calls this on every pill change. Malformed JSON stays 400, a staged
// link naming a missing session stays 404 (as on create), and
// resolveSessionDefaults' own failures stay 5xx. Not run: agent="auto"
// inference (a model call, not a rule) — the agent is left as "auto".
func (s *server) handleValidateSession(w http.ResponseWriter, r *http.Request) {
	var req createSessionRequest
	if !decode(w, r, &req) {
		return
	}
	ctx := r.Context()
	sess := &req.Session
	sess.ID = ""
	clearServerOwned(sess)
	var v contracts.ValidationError
	if len(req.Links) > 0 {
		cfg, err := s.data.Config(ctx)
		if err != nil {
			writeError(w, err)
			return
		}
		if !cfg.FeatureFlags.LinkedSessions {
			v.Add("links", `"links" requires the linked-sessions feature flag`)
			req.Links = nil
		}
	}
	if err := s.resolveSessionDefaults(ctx, sess); err != nil {
		writeError(w, err)
		return
	}
	// The Data Service's own record check, which create meets as a 422
	// (trust, isolation, name_source, synthesis_mode, activity) — except
	// the name. The UI dry-runs before the goal is typed and doesn't re-run
	// on goal edits (the goal "only names the session",
	// NewSessionCard.tsx), so a "name: required" here would stick and keep
	// Start disabled after the goal was typed; the UI already refuses an
	// empty goal itself.
	if verr := contracts.ValidateSession(sess); verr != nil {
		for _, fe := range verr.Errors {
			if fe.Field != "name" {
				v.Errors = append(v.Errors, fe)
			}
		}
	}
	if err := checkSessionDir(sess.Dir); err != nil {
		v.Add("dir", err.Error())
	}
	// create resolves agent "auto" before this check and may change the
	// provider; that inference isn't run here, so the check would be a guess.
	if sess.Agent != "auto" {
		if _, err := s.providers.Get(sess.Provider); err != nil {
			v.Add("provider", err.Error())
		}
	}
	out := contracts.SessionValidation{}
	join, verr, err := s.planStagedLinks(ctx, sess, req.Links)
	if err != nil {
		writeError(w, err)
		return
	}
	if verr != nil {
		v.Errors = append(v.Errors, verr.Errors...)
	} else if len(req.Links) > 0 {
		// The Data Service owns the per-link rules; its dry run applies them
		// without creating the links.
		dverr, err := s.data.ValidateLinks(ctx, req.Links)
		switch {
		case err != nil:
			writeError(w, err)
			return
		case dverr != nil:
			v.Errors = append(v.Errors, dverr.Errors...)
		}
	}
	join.apply(sess)
	if join.Dir != "" {
		out.JoinedWorktreeOf = &contracts.JoinedWorktree{SessionID: join.Counterpart.ID, Name: join.Counterpart.Name}
	}
	out.Dir, out.Isolation, out.Workspace = sess.Dir, sess.Isolation, sess.Workspace
	out.Errors = v.Errors
	if out.Errors == nil {
		out.Errors = []contracts.FieldError{}
	}
	writeJSON(w, http.StatusOK, out)
}
