package job

import (
	"context"
	"errors"
	"fmt"
	"log"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/providers/fastmodel"
)

const RunDigestKind = "run_digest"

const runDigestSystemPrompt = "You compress one pipeline task's outcome into " +
	"a short digest for the agent whose own pipeline run this is. Plain " +
	"prose, a sentence or two at most: what task, what happened."

// GenerateRunDigest is the job body for a pipeline task reaching a terminal
// status (done or blocked) on a session that holds the implicit hand-raiser
// link over its own pipeline run (docs/LINKED-SESSIONS-DESIGN.md decision
// 6): compress the outcome via the Fast Model and deliver it back into the
// same session's own conversation. Best-effort throughout, mirroring
// GenerateDigest: a session/task that no longer exists, a feature flag
// that's off, or delivery failing never fails the job.
//
// Scoped to Implicit links only, not every read-holder — see run_digest.go's
// package doc / docs/LINKED-SESSIONS-DESIGN.md decision 6 implementation
// note. Generalizing "any monitor over a live-pipeline session also gets
// task digests" is a reasonable fast-follow, not built here.
func GenerateRunDigest(sessionID, taskID, toStatus string) Func {
	return func(ctx context.Context, deps Deps) error {
		cfg, err := deps.Data.Config(ctx)
		if err != nil {
			return fmt.Errorf("run digest: load config: %w", err)
		}
		if !cfg.FeatureFlags.LinkedSessions {
			return nil
		}

		links, err := deps.Data.LinksForSession(ctx, sessionID)
		if err != nil {
			return fmt.Errorf("run digest: load links: %w", err)
		}

		var targets []contracts.Link
		for _, l := range links {
			if l.SubjectSessionID != sessionID || !l.HasGrant(contracts.GrantRead) {
				continue
			}
			if !l.Implicit {
				continue
			}
			if l.Cadence == contracts.CadenceManualOnly {
				continue
			}
			targets = append(targets, l)
		}
		if len(targets) == 0 || deps.Resolver == nil {
			return nil // no live implicit hand-raiser; skip the fast-model call entirely
		}

		task, err := deps.Data.Task(ctx, taskID)
		if errors.Is(err, dataclient.ErrNotFound) {
			return nil // task was deleted mid-flight; nothing to do
		}
		if err != nil {
			return fmt.Errorf("run digest: load task: %w", err)
		}

		prompt := fmt.Sprintf("task %q is now %s", task.Title, toStatus)
		if toStatus == string(contracts.StatusBlocked) {
			for i := len(task.Notes) - 1; i >= 0; i-- {
				if task.Notes[i].Type == contracts.NoteBlocker {
					prompt += fmt.Sprintf("\nblocker: %s", task.Notes[i].Content)
					break
				}
			}
		}

		text, _, err := deps.FastModel.DoText(ctx, fastmodel.Job{
			System: runDigestSystemPrompt, Prompt: prompt,
		})
		if err != nil {
			if fastmodel.IsSkippable(err) {
				return nil // no Fast Model backend configured: skip, not a failure
			}
			return fmt.Errorf("run digest: compress: %w", err)
		}

		for _, l := range targets {
			addr, ok := deps.Resolver.SessionAddr(l.GrantingSessionID)
			if !ok {
				continue // hand-raiser's own session not running: best-effort skip
			}
			// "your pipeline run", not sess.Name: granting == subject for an
			// implicit hand-raiser, so the session's own Name would produce a
			// self-referential "⇄ from Leader · <its own session's name>",
			// which reads strangely — naming the source what it actually is
			// reads better. Judgment call for L4's rendering, not load-bearing.
			if err := deliverDigest(ctx, addr, text, contracts.Attribution{
				LinkID: l.ID, SourceName: "your pipeline run",
			}); err != nil {
				log.Printf("run digest: deliver to %s via link %s: %v", l.GrantingSessionID, l.ID, err)
				continue // one target's failure must not affect the others
			}
		}
		return nil
	}
}
