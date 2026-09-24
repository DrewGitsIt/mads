package api

import (
	"context"
	"errors"
	"fmt"
	"log"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/pkg/personapick"
)

// resolveAutoAgent is interactive session-start's agent="auto" path: pick
// the best persona for the user's opening prompt.
//
// Primary path (agent-concept redesign §4): run personapick.AutoPick
// against the persona registry + the workspace's default persona. A
// *thinking-disabled Fast Model one-shot* does the selection (a
// single, prompt-in, JSON-out call) — no capability inference, no
// generation, no confirmation. The persona id is written back onto
// sess.Agent in place (the field name stays; its semantics are now a
// *persona* id rather than an old "agent" name).
//
// Fallback path: a Fast Model call fails (transport error, timeout,
// rate-limit, or unusable output) — personapick returns a *fallback
// result* (workspace default persona, or "none") and no error. The
// only real error is a *transport* failure that the backend never
// returns as skippable; that propagates up to the caller who reports
// it to the session, per §11.9's "inform and wait, never silent retry".
//
// The legacy agentselect.SelectOrGenerate machinery is still exported
// (autostaff) but no longer called here — the redesign's persona
// split makes capability-inference + on-demand-generation redundant
// for the "auto" case (a persona is *chosen* from the registry; it is
// not *generated at session start*).
func (s *server) resolveAutoAgent(ctx context.Context, sess *contracts.Session, openingPrompt string) error {
	// Load the full persona registry (the persona is a registry object —
	// spec §9's Agent record, now narrower).
	personas, err := s.data.Agents(ctx)
	if err != nil {
		return fmt.Errorf("list personas: %w", err)
	}
	// Workspace default persona is the *fallback* (per the redesign §4
	// failure-and-fallback rule) and is also always a *candidate* — the
	// Fast Model is asked to pick the best, and the workspace default is
	// the one it falls back to when no candidate wins.
	var fallback, defaultPersonaID string
	if sess.Workspace != "" {
		if w, werr := s.data.Workspace(ctx, sess.Workspace); werr == nil {
			defaultPersonaID = w.Defaults.PersonaID()
			fallback = defaultPersonaID
		} else if !errors.Is(werr, dataclient.ErrNotFound) {
			return fmt.Errorf("resolve workspace %q: %w", sess.Workspace, werr)
		}
	}

	// Build the candidate set: every persona in the registry, plus the
	// workspace default *if it isn't already in the set*. (The workspace
	// default is a persona id — it may not yet be registered, in which
	// case it's a *name* the user knows but the registry doesn't; the
	// fallback path below handles that case.)
	cands := make([]personapick.Candidate, 0, len(personas)+1)
	byID := make(map[string]bool, len(personas))
	for _, a := range personas {
		cands = append(cands, personapick.CandidateFromAgent(a))
		byID[a.ID] = true
	}
	if defaultPersonaID != "" && !byID[defaultPersonaID] {
		cands = append(cands, personapick.Candidate{ID: defaultPersonaID})
	}

	res, perr := personapick.AutoPick(ctx, s.jobs.FastModel(), personapick.Request{
		OpeningPrompt:   openingPrompt,
		Candidates:      cands,
		FallbackPersona: fallback,
	})
	if perr != nil {
		// A non-skippable transport failure (rare). The session *can*
		// still start with no persona — §11.9's "inform and wait" rule
		// wants a *notice*, which the caller's caller (the router's
		// sessions.go create path) logs. Degrade to the fallback so the
		// user isn't blocked by a flaky Fast Model.
		log.Printf("personapick: %v; falling back to %q", perr, fallback)
		res = personapick.Result{PersonaID: fallback, Source: "fallback-on-transport-error"}
	}
	// res.PersonaID may be "" (no persona — a legal outcome per the
	// redesign §4 "the session starts; nothing is broken"). sess.Agent
	// is a persona id (the field name from the old "agent" shape is
	// retained on the wire).
	sess.Agent = res.PersonaID
	return nil
}
