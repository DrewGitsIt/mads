package goosecli

import (
	"context"
	"encoding/json"
	"time"
)

// sessionListEntry is one `goose session list --format json` row — only the
// fields the locator filters on (the full row also carries name,
// user_set_name, session_type, message_count, updated_at). CreatedAt stays
// a string so one row with a mangled timestamp skips (decline-biased)
// instead of failing the whole list unmarshal.
type sessionListEntry struct {
	ID         string `json:"id"`
	WorkingDir string `json:"working_dir"`
	CreatedAt  string `json:"created_at"`
}

// locateInterval is how often LocateSession retries while its caller waits —
// goose writes its session store asynchronously with the process that
// produces it. Coarser than claudecode's 500ms because each attempt execs
// the goose binary (session list, plus an export to verify) rather than
// reading a local file.
const locateInterval = time.Second

// locateSkew is the clock-skew margin subtracted from spawnedAfter before
// comparing against created_at (claudecode's candidateTranscripts
// tolerance): a session row stamped microseconds behind this process's own
// spawn timestamp — created_at has one-second granularity — must not lose
// the comparison.
const locateSkew = 2 * time.Second

// LocateSession implements provider.SessionLocator (design doc
// "SessionLocator"): filter `goose session list --format json` by
// working_dir == cwd and created_at >= spawnedAfter, then verify the single
// survivor's first visible user message via `session export`. 0 or 2+
// candidates, a wrong first message, timeout, or cancellation ⇒ ok=false —
// a wrong ref would silently resume someone else's conversation, so it
// never guesses. Like claudecode's, it retries on locateInterval until ctx
// is done: the Runner calls it once and relies on it being best-effort for
// as long as the ctx allows (PTY: the session's whole life).
func (p *Provider) LocateSession(ctx context.Context, cwd string, spawnedAfter time.Time, firstMessage string) (string, bool) {
	if ref, ok := p.locateOnce(ctx, cwd, spawnedAfter, firstMessage); ok {
		return ref, true
	}
	ticker := time.NewTicker(locateInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return "", false
		case <-ticker.C:
			if ref, ok := p.locateOnce(ctx, cwd, spawnedAfter, firstMessage); ok {
				return ref, true
			}
		}
	}
}

// locateOnce runs one list-filter-verify pass. Ambiguity (2+ survivors of
// the cwd/time filter) declines before any export is attempted — the
// first-message check verifies a unique candidate, it never arbitrates
// between several.
func (p *Provider) locateOnce(ctx context.Context, cwd string, spawnedAfter time.Time, firstMessage string) (string, bool) {
	out, err := p.gooseQuery(ctx, "session", "list", "--format", "json")
	if err != nil {
		return "", false
	}
	var entries []sessionListEntry
	if err := json.Unmarshal(out, &entries); err != nil {
		return "", false
	}
	margin := spawnedAfter.Add(-locateSkew)
	var candidates []string
	for _, e := range entries {
		created, err := time.Parse(time.RFC3339, e.CreatedAt)
		if err != nil || e.WorkingDir != cwd || created.Before(margin) {
			continue
		}
		candidates = append(candidates, e.ID)
	}
	if len(candidates) != 1 {
		return "", false
	}
	if !p.verifyFirstMessage(ctx, candidates[0], firstMessage) {
		return "", false
	}
	return candidates[0], true
}

// verifyFirstMessage confirms the candidate's first visible user message is
// exactly firstMessage, via the same export normalization Transcript uses —
// injected userVisible:false turn-context entries are already skipped and
// tool results already carry role "tool_result", so the first Role "user"
// Message is the first thing a human actually sent.
func (p *Provider) verifyFirstMessage(ctx context.Context, ref, firstMessage string) bool {
	msgs, err := p.Transcript(ctx, ref)
	if err != nil {
		return false
	}
	for _, m := range msgs {
		if m.Role == "user" {
			return m.Text == firstMessage
		}
	}
	return false
}
