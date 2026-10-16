// Package turndigest owns "n turns of agent activity in, minimum tokens for
// a reading LLM out" — the one compression path shared by every reader of a
// session's activity (docs/LINKED-SESSIONS-DESIGN.md decision 4). It
// consolidates what used to be sibling implementations: the per-turn digest
// push's system prompt (services/router/internal/job), the session child's
// digest rendering (services/session/internal/runner), and — reserved via
// the Audience constants — get_summary's transcript tail and pipeline-run
// digests.
//
// The pipeline is headroom-inspired: a deterministic normalize pass first
// (keep user messages and the final assistant reply, tally tool calls into
// one line per turn, cap per-entry and total length); if the normalized rendering fits the caller's token
// budget it is returned verbatim with no model call; otherwise exactly one
// Fast Model call compresses it, with hard truncation as the backstop. A
// skippable backend (fastmodel.IsSkippable) degrades to truncated verbatim
// rather than erroring — the same best-effort stance as every digest path.
package turndigest

import (
	"context"
	"strings"

	"github.com/drewkidwell/mads/providers/fastmodel"
)

// Entry is the shared shape promoted from the session child's digestEntry
// (services/session/internal/runner) / synth's deltaEntry: text chunks
// verbatim, tool uses reduced to {name, path} — raw tool input/output never
// leaves the session process.
type Entry struct {
	Kind string // KindUser | KindAssistant | KindTool
	Text string
	Tool string
	Path string
}

const (
	KindUser      = "user"
	KindAssistant = "assistant"
	KindTool      = "tool"
)

// Turn is one turn's entries plus the caller's anchor number (0 = unknown;
// unknown turns render without a header even in multi-turn requests).
type Turn struct {
	Number  int
	Entries []Entry
}

// Audience selects the system prompt: who is going to read the digest.
type Audience string

const (
	// MonitorPush is the per-turn push to a read-link monitor.
	MonitorPush Audience = "monitor_push"
	// LeaderCatchup is reserved for get_summary's transcript tail: a leader
	// agent catching up on a session it delegates to.
	LeaderCatchup Audience = "leader_catchup"
	// RunDigest is reserved for pipeline-run digests.
	RunDigest Audience = "run_digest"
)

// Request describes one digest job.
type Request struct {
	Turns    []Turn
	Audience Audience
	// BudgetTokens is the reader's verbatim tolerance and the truncation
	// cap: a normalized rendering that fits is returned as-is with no model
	// call. <= 0 means "always compress" — nothing fits a zero budget — and
	// makes the skippable-backend fallback empty.
	BudgetTokens int
	// Subject is optional prompt context ("session build-fix",
	// "task tsk_x"); empty = omitted from the prompt.
	Subject string
	// Classify asks the model to lead its reply with an actionability
	// verdict line, parsed off into Result.Actionable/Reason. Opt-in so
	// non-triaging callers keep their prompt (and replies) byte-identical.
	// Only meaningful on the compressed path — a verbatim or degraded
	// result is never actionable, because no model ever looked at it.
	Classify bool
}

// Source reports which path produced Result.Text.
type Source string

const (
	SourceVerbatim   Source = "verbatim"
	SourceCompressed Source = "compressed"
)

// Result always carries FromTurn/ToTurn anchors so readers can drill down
// into the range the digest covers.
type Result struct {
	Text     string
	Source   Source
	FromTurn int // Turns[0].Number (0 when no turns)
	ToTurn   int // Turns[len-1].Number (0 when no turns)
	// Actionable/Reason carry Request.Classify's verdict: whether the
	// digested activity needs a monitor's attention right now. Always false
	// when Classify was off, on verbatim/degraded paths, or when the model
	// omitted the verdict line — unparseable defaults to quiet, since
	// deterministic signals (job.DeliverSignal) still guarantee terminal
	// events wake the monitor regardless.
	Actionable bool
	Reason     string
}

// Digester compresses turns via a Fast Model runner.
type Digester struct{ fm *fastmodel.Runner }

// New builds a Digester over fm.
func New(fm *fastmodel.Runner) *Digester {
	return &Digester{fm: fm}
}

// estimateTokens is the crude bytes/4 heuristic — it only gates the
// verbatim-vs-compress threshold and the truncation backstop, so precision
// doesn't matter.
func estimateTokens(s string) int {
	return len(s) / 4
}

// Digest runs the pipeline: normalize, render, return verbatim if within
// budget, otherwise one Fast Model call. A skippable backend error degrades
// to truncated verbatim with a nil error; any other error propagates for
// the caller to decide (the job path fails the job).
func (d *Digester) Digest(ctx context.Context, req Request) (Result, error) {
	res := Result{Source: SourceVerbatim}
	if len(req.Turns) > 0 {
		res.FromTurn = req.Turns[0].Number
		res.ToTurn = req.Turns[len(req.Turns)-1].Number
	}
	entries := 0
	for _, t := range req.Turns {
		entries += len(t.Entries)
	}
	if entries == 0 {
		return res, nil // nothing to digest: empty verbatim, no model call
	}

	rendering := renderTurns(req.Turns)
	if req.BudgetTokens > 0 && estimateTokens(rendering) <= req.BudgetTokens {
		res.Text = rendering
		return res, nil
	}

	reply, _, err := d.fm.DoText(ctx, fastmodel.Job{
		System: systemPrompt(req), Prompt: rendering,
	})
	if err != nil {
		if fastmodel.IsSkippable(err) {
			res.Text = truncate(rendering, req.BudgetTokens)
			return res, nil
		}
		return Result{}, err
	}
	if req.Classify {
		// Parse before the length backstop, so the verdict line never
		// counts against the caller's budget.
		res.Actionable, res.Reason, reply = parseVerdict(reply)
	}
	// Backstop for a model that ignored the prompt-side length target; a
	// zero budget passes the reply through untouched.
	if req.BudgetTokens > 0 && estimateTokens(reply) > 2*req.BudgetTokens {
		reply = truncate(reply, req.BudgetTokens)
	}
	res.Text = reply
	res.Source = SourceCompressed
	return res, nil
}

// parseVerdict splits a Classify reply into (actionable, reason, digest).
// The expected first line is "ACTIONABLE: yes — <why>" or "ACTIONABLE: no";
// a reply without it parses as not actionable with the text untouched.
func parseVerdict(reply string) (bool, string, string) {
	trimmed := strings.TrimLeft(reply, " \t\n")
	line, rest, _ := strings.Cut(trimmed, "\n")
	const marker = "actionable:"
	if !strings.HasPrefix(strings.ToLower(line), marker) {
		return false, "", reply
	}
	verdict := strings.TrimSpace(line[len(marker):])
	rest = strings.TrimSpace(rest)
	lower := strings.ToLower(verdict)
	if !strings.HasPrefix(lower, "yes") {
		return false, "", rest
	}
	reason := strings.TrimSpace(verdict[len("yes"):])
	reason = strings.TrimSpace(strings.TrimLeft(reason, "—–-:,"))
	return true, reason, rest
}

// truncate cuts s rune-safe at ~budgetTokens*4 bytes, appending "…" when it
// cuts. A budget <= 0 yields "" — the degraded fallback for callers that
// always compress.
func truncate(s string, budgetTokens int) string {
	if budgetTokens <= 0 {
		return ""
	}
	max := budgetTokens * 4
	if len(s) <= max {
		return s
	}
	for max > 0 && s[max]&0xC0 == 0x80 { // back up off a UTF-8 continuation byte
		max--
	}
	return s[:max] + "…"
}
