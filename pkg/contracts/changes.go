package contracts

import "time"

// ChangeSet is GET /sessions/{id}/changeset: everything a session changed,
// computed from disk against one baseline, shaped for the Changes tab and
// the Context Rail's Changes list (docs/CHANGES-DESIGN.md). The Router
// serves it from pkg/changes; the UI never re-derives tiers, turns or line
// counts.
//
// The file list is what differs on disk from the baseline, not the touch
// ledger: a file the agent touched and put back is absent, and a file
// changed by something mads didn't see (the user, a detached process) is
// present with Attributed false. The ledger contributes attribution and
// turn membership only.
type ChangeSet struct {
	// Baseline is the comparison actually used: one of the Baseline* consts.
	Baseline string `json:"baseline"`
	// Baselines lists every baseline with whether it applies to this
	// session, so the UI renders the switcher without guessing.
	Baselines []ChangeBaseline `json:"baselines"`
	// Git is whether any of the session's roots is a git repo. When false
	// there are no commits and the Commit grouping doesn't apply.
	Git     bool            `json:"git"`
	Files   []ChangedFile   `json:"files"`
	Turns   []ChangeTurn    `json:"turns"`
	Commits []ChangeCommit  `json:"commits"`
	Totals  ChangeSetTotals `json:"totals"`
	// Notes are honest caveats about completeness, shown as a quiet line:
	// "baseline snapshot still running", "line counts unavailable for 3
	// files outside a repo". Empty when the set is complete.
	Notes []string `json:"notes,omitempty"`
	// ComputedAt is when the set was computed.
	ComputedAt time.Time `json:"computed_at"`
}

// Baseline ids for ChangeSet.Baseline and GET .../changeset?vs=.
const (
	// BaselineSessionStart compares against the session's shadow baseline
	// (docs/SHADOW-GIT-DIFFS-ADR.md), or, for a file outside every repo,
	// its first captured pre-image. The default, and the only baseline
	// that covers non-git directories.
	BaselineSessionStart = "start"
	// BaselineBase compares against the merge base with the branch the
	// session's worktree was created from (Session.WorktreeBase) — what
	// Land would apply.
	BaselineBase = "base"
	// BaselineMain compares against the merge base with the repo's default
	// branch.
	BaselineMain = "main"
)

// ChangeBaseline is one entry in the baseline switcher.
type ChangeBaseline struct {
	ID string `json:"id"`
	// Label is the switcher text: "Session start", "Base branch ·
	// feat/x", "main".
	Label string `json:"label"`
	// Available is false when the baseline doesn't apply (not a git repo,
	// no worktree base) or resolves to the same commit as an earlier entry;
	// the UI hides unavailable entries.
	Available bool `json:"available"`
	// Reason says why an entry is unavailable, for a tooltip.
	Reason string `json:"reason,omitempty"`
}

// Change tiers, in display order. Deterministic path and attribute rules
// only (pkg/changes/classify.go) — never a model.
const (
	TierSource    = "source"
	TierTests     = "tests"
	TierConfig    = "config"
	TierGenerated = "generated"
	TierScratch   = "scratch"
)

// ChangeTiers is the tier display order.
var ChangeTiers = []string{TierSource, TierTests, TierConfig, TierGenerated, TierScratch}

// ChangedFile is one file that differs from the baseline.
type ChangedFile struct {
	// Path is absolute — the key every other endpoint (diff, delete,
	// open-in-editor) takes.
	Path string `json:"path"`
	// Display is Path relative to the session's folder (Session.WorktreeDir,
	// else Session.Dir), or Path itself when the file is outside it.
	Display string `json:"display"`
	// Outside marks a file outside the session's folder.
	Outside bool   `json:"outside,omitempty"`
	Change  string `json:"change"` // added | modified | deleted
	Added   int    `json:"added"`
	Deleted int    `json:"deleted"`
	// Binary marks a file whose line counts are meaningless (both zero).
	Binary bool `json:"binary,omitempty"`
	// CountsUnknown marks a file whose line counts couldn't be computed
	// (no pre-image for a file outside every repo); both are zero.
	CountsUnknown bool   `json:"counts_unknown,omitempty"`
	Tier          string `json:"tier"`
	// ScratchRule names the rule that made the file scratch ("name
	// pattern", "outside session folder", "gitignored", "scratch dir"),
	// shown in the delete confirmation. Empty unless Tier is scratch.
	ScratchRule string `json:"scratch_rule,omitempty"`
	// Attributed is whether the session's own tools (or commits) changed
	// it, per the ledger — false for a change only seen on disk.
	Attributed bool `json:"attributed"`
	// Turns are the 1-based turn numbers (ChangeTurn.N) whose ledger
	// entries touched the file, ascending. Empty when unattributed or the
	// transcript has no timestamps.
	Turns []int `json:"turns"`
	// Commits are the hashes of the session's commits that include the
	// file, newest first.
	Commits []string `json:"commits"`
	// Uncommitted is whether the file differs from HEAD in its repo (always
	// true outside a repo).
	Uncommitted bool `json:"uncommitted"`
	// LastChanged is the latest ledger timestamp for the file, else its
	// mtime — what the UI's "new since you looked" compares against.
	LastChanged time.Time `json:"last_changed"`
}

// ChangeTurn is one user turn: the window from its prompt to the next one.
// Only user prompts open a turn. Work the harness does on its own after a
// background task finishes is not a turn of its own; its changes fall in the
// window of the user turn that was running.
type ChangeTurn struct {
	N int `json:"n"`
	// Prompt is the user's message, trimmed and capped at 500 chars; the UI
	// clips further for display.
	Prompt string    `json:"prompt"`
	At     time.Time `json:"at"`
	// Files is how many ChangeSet.Files list this turn.
	Files int `json:"files"`
}

// ChangeCommit is one commit recorded for the session, newest first.
type ChangeCommit struct {
	Hash      string    `json:"hash"`
	Message   string    `json:"message"` // first line
	Timestamp time.Time `json:"timestamp"`
	// Paths are absolute paths of the commit's files, in git's order.
	Paths []string `json:"paths"`
}

// ChangeSetTotals sums Files.
type ChangeSetTotals struct {
	Files   int `json:"files"`
	Added   int `json:"added"`
	Deleted int `json:"deleted"`
}

// DeleteScratchRequest is POST /sessions/{id}/changeset/delete-scratch.
type DeleteScratchRequest struct {
	Paths []string `json:"paths"`
}

// DeleteScratchResult reports what was deleted. The server re-classifies
// every path at request time and deletes only files that are still in the
// session's change set and still classified scratch; anything else is
// Skipped with a reason, never deleted.
type DeleteScratchResult struct {
	Deleted []string            `json:"deleted"`
	Skipped []DeleteScratchSkip `json:"skipped"`
}

// DeleteScratchSkip is one path the server refused to delete.
type DeleteScratchSkip struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}
