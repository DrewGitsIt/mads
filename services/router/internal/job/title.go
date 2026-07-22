package job

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/providers/fastmodel"
)

const SessionTitleKind = "session_title"

// maxGeneratedTitleLength caps the auto-title job's own output — independent
// of the (now unbounded) first-line name a session starts with.
const maxGeneratedTitleLength = 50

const sessionTitleSystem = "You write short, specific titles for an AI coding agent's session, " +
	"based on its first exchange. Reply with ONLY a JSON object of this exact shape, no prose, " +
	"no code fences:\n\n{\"title\": \"a concise, specific title\"}\n\n" +
	"The title must be a single line, under 50 characters, with no trailing punctuation."

type sessionTitleDraft struct {
	Title string `json:"title"`
}

func (d *sessionTitleDraft) Validate() error {
	if strings.TrimSpace(d.Title) == "" {
		return errors.New("title is required")
	}
	return nil
}

// GenerateSessionTitle is the job body for a session's first turn completing
// (digestwatch's Watcher spawns this alongside GenerateDigest on every
// session.turn_ended): ask the Fast Model for a concise title from the
// turn's delta and rename the session, but only while its name is still
// Derived — a session already renamed by a human (NameSource User) or by an
// earlier run of this same job (NameSource Generated) is left alone. An
// unconfigured Fast Model backend degrades to a no-op, same as
// GenerateSessionSummary: the first-line name simply stays.
func GenerateSessionTitle(sessionID, delta string) Func {
	return func(ctx context.Context, deps Deps) error {
		sess, err := deps.Data.Session(ctx, sessionID)
		if errors.Is(err, dataclient.ErrNotFound) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("session title: load session: %w", err)
		}
		if sess.NameSource != contracts.SessionNameSourceDerived {
			return nil
		}

		var d sessionTitleDraft
		if _, err := deps.FastModel.Do(ctx, fastmodel.Job{System: sessionTitleSystem, Prompt: delta}, &d); err != nil {
			if fastmodel.IsSkippable(err) {
				return nil
			}
			return fmt.Errorf("session title: generate: %w", err)
		}

		title := cleanGeneratedTitle(d.Title)
		if title == "" {
			return nil
		}

		sess.Name = title
		sess.NameSource = contracts.SessionNameSourceGenerated
		return deps.Data.UpdateSession(ctx, &sess)
	}
}

// cleanGeneratedTitle enforces single-line and length on the model's output
// regardless of how well it followed the prompt — defense in depth, not
// trust.
func cleanGeneratedTitle(s string) string {
	line := strings.TrimSpace(strings.SplitN(s, "\n", 2)[0])
	line = strings.TrimRight(line, ".!?,;: \t")
	if runes := []rune(line); len(runes) > maxGeneratedTitleLength {
		line = strings.TrimSpace(string(runes[:maxGeneratedTitleLength]))
	}
	return line
}
