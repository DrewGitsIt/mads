package job

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/providers/fastmodel"
)

const DigestKind = "digest"

const digestSystemPrompt = "You compress one turn of an AI coding agent's " +
	"activity into a short digest for someone monitoring the session. Plain " +
	"prose, a few sentences at most: what was asked, what the agent did."

// GenerateDigest is the job body for a watched session's turn completing
// (docs/LINKED-SESSIONS-DESIGN.md decision 4, the `read` grant's push half):
// compress delta via the Fast Model and fan it out to every `read`+`per_turn`
// link's granting (monitor) session. Best-effort throughout — a session that
// no longer exists, a feature flag that's off, or one monitor's delivery
// failing never fails the job; the watched session must be completely
// unaffected by its observers.
func GenerateDigest(sessionID, delta string) Func {
	return func(ctx context.Context, deps Deps) error {
		sess, err := deps.Data.Session(ctx, sessionID)
		if errors.Is(err, dataclient.ErrNotFound) {
			return nil // watched session was deleted mid-flight; nothing to do
		}
		if err != nil {
			return fmt.Errorf("digest: load session: %w", err)
		}

		cfg, err := deps.Data.Config(ctx)
		if err != nil {
			return fmt.Errorf("digest: load config: %w", err)
		}
		if !cfg.FeatureFlags.LinkedSessions {
			return nil
		}

		links, err := deps.Data.LinksForSession(ctx, sessionID)
		if err != nil {
			return fmt.Errorf("digest: load links: %w", err)
		}

		var targets []contracts.Link
		for _, l := range links {
			if l.SubjectSessionID != sessionID || !l.HasGrant(contracts.GrantRead) {
				continue
			}
			if l.Cadence == contracts.CadenceManualOnly {
				continue
			}
			targets = append(targets, l)
		}
		if len(targets) == 0 || deps.Resolver == nil {
			return nil // no live per_turn readers; skip the fast-model call entirely
		}

		text, _, err := deps.FastModel.DoText(ctx, fastmodel.Job{
			System: digestSystemPrompt, Prompt: delta,
		})
		if err != nil {
			if fastmodel.IsSkippable(err) {
				return nil // no Fast Model backend configured: skip, not a failure
			}
			return fmt.Errorf("digest: compress: %w", err)
		}

		for _, l := range targets {
			addr, ok := deps.Resolver.SessionAddr(l.GrantingSessionID)
			if !ok {
				continue // monitor not running: best-effort skip, not a failure
			}
			if err := deliverDigest(ctx, addr, text, contracts.Attribution{
				LinkID: l.ID, SourceName: sess.Name,
			}); err != nil {
				log.Printf("digest: deliver to %s via link %s: %v", l.GrantingSessionID, l.ID, err)
				continue // one monitor's failure must not affect the others
			}
		}
		return nil
	}
}

// deliverDigest POSTs an attributed digest to a monitor child's /inject
// endpoint. A standalone helper (not api.postJSONToChild) because this runs
// detached inside a job goroutine, with no inbound request to proxy a
// response back to.
func deliverDigest(ctx context.Context, addr, text string, attribution contracts.Attribution) error {
	raw, err := json.Marshal(contracts.InjectRequest{Text: text, Attribution: attribution})
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+addr+"/inject", bytes.NewReader(raw))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("post: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusAccepted {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	return nil
}
