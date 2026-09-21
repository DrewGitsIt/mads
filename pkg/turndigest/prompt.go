package turndigest

import (
	"fmt"
	"strings"
)

// monitorPushPrompt moved verbatim from services/router/internal/job's
// digestSystemPrompt — with a zero budget and no subject it must stay
// byte-identical, so the refactored GenerateDigest sends exactly the prompt
// it always did.
const monitorPushPrompt = "You compress one turn of an AI coding agent's " +
	"activity into a short digest for someone monitoring the session. Plain " +
	"prose, a few sentences at most: what was asked, what the agent did."

const leaderCatchupPrompt = "You compress several turns of an AI coding " +
	"agent's activity into a short digest for a leader agent catching up on " +
	"a session it delegates to. Plain prose, a few sentences at most: what " +
	"was asked, what the agent did, where things stand."

const runDigestPrompt = "You compress a pipeline run's activity into a " +
	"short digest for the agent whose own pipeline run this is. Plain " +
	"prose, a sentence or two at most: what was done, what happened."

// systemPrompt picks the audience's prompt — unknown values fall back to
// MonitorPush's, never an error — and appends the optional subject and the
// prompt-side length target.
func systemPrompt(req Request) string {
	var base string
	switch req.Audience {
	case LeaderCatchup:
		base = leaderCatchupPrompt
	case RunDigest:
		base = runDigestPrompt
	default:
		base = monitorPushPrompt
	}
	var sb strings.Builder
	sb.WriteString(base)
	if req.Subject != "" {
		fmt.Fprintf(&sb, " The activity concerns %s.", req.Subject)
	}
	if req.BudgetTokens > 0 {
		fmt.Fprintf(&sb, " Reply in at most ~%d tokens.", req.BudgetTokens)
	}
	if req.Classify {
		sb.WriteString(" Start your reply with its own first line, exactly" +
			" \"ACTIONABLE: yes — <why>\" if this activity needs the" +
			" monitor's attention right now (blocked, crashed, waiting on" +
			" input or permission, or clearly drifting from its goal), or" +
			" \"ACTIONABLE: no\" otherwise; then the digest.")
	}
	return sb.String()
}
