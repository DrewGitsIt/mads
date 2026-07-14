package job

import (
	"context"
	"errors"
	"fmt"

	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/pkg/sessionsummary"
)

const SessionSummaryKind = "session_summary"

// GenerateSessionSummary is the job body for a session's active → inactive
// transition (spec §16): derive the compressed narrative from task data and
// write it to session.summary. Regenerating on a session that no longer
// exists (a race with the terminate-then-delete path) is a no-op, not a
// failure — there's nothing left to summarize.
func GenerateSessionSummary(sessionID string) Func {
	return func(ctx context.Context, deps Deps) error {
		sess, err := deps.Data.Session(ctx, sessionID)
		if errors.Is(err, dataclient.ErrNotFound) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("session summary: load session: %w", err)
		}

		gen := sessionsummary.New(deps.Data, deps.FastModel)
		summary, err := gen.Generate(ctx, sessionID)
		if err != nil {
			return fmt.Errorf("session summary: generate: %w", err)
		}

		sess.Summary = summary
		return deps.Data.UpdateSession(ctx, &sess)
	}
}
