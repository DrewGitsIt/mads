package contracts

import "time"

// Repo state (docs/COMMIT-LAND-PUSH-DESIGN.md D10): facts about branches,
// which several sessions can share, kept once per repo by the Router rather
// than copied onto every session record.

// RepoChangedPayload is repo.changed's payload.
type RepoChangedPayload struct {
	// Repo is the main checkout's top; GitDir the repo's shared git dir.
	Repo   string `json:"repo"`
	GitDir string `json:"git_dir"`
}

// Git state parts (docs/GIT-STATE-DESIGN.md): what the Router keeps per
// session and names in GitStateUpdatedPayload.
const (
	GitPartRepos     = "repos"
	GitPartChangeSet = "changeset"
	GitPartCounts    = "counts"
)

// GitStateUpdatedPayload is session.git_state_updated's payload: the parts
// a finished recompute covered, the session's git state generation it
// finished at (monotonic per session, across Router restarts too: it is
// clock-based, never below the previous one plus one), and the subset of
// Parts whose result differs from the one before. One announcement may
// cover several parts (every part a batch of recomputes finished).
//
// Baseline is set only for a change set against another baseline than
// session start ("base" or "default"), recomputed on demand: such an
// announcement covers that baseline's change set alone (Parts is
// ["changeset"]), and says nothing about session start's. Absent, a
// "changeset" in Parts is the change set against session start.
type GitStateUpdatedPayload struct {
	SessionID  string   `json:"session_id"`
	Parts      []string `json:"parts"`
	Generation int64    `json:"generation"`
	Changed    []string `json:"changed"`
	Baseline   string   `json:"baseline,omitempty"`
}

// RepoWorktree is one checkout of a repo and the branch it's on.
type RepoWorktree struct {
	// Path is the top of this checkout; Main marks the repo's main checkout
	// (the others are linked worktrees).
	Path   string `json:"path"`
	Main   bool   `json:"main,omitempty"`
	GitDir string `json:"git_dir"`
	Branch string `json:"branch,omitempty"`
	Head   string `json:"head,omitempty"`
	// Detached: no branch checked out; nothing to push.
	Detached bool `json:"detached,omitempty"`
	// Remote and RemoteBranch are where the branch pushes (empty: no
	// remote); Upstream whether it tracks one; NewBranch when the remote
	// has no such branch yet.
	Remote       string `json:"remote,omitempty"`
	RemoteBranch string `json:"remote_branch,omitempty"`
	Upstream     bool   `json:"upstream,omitempty"`
	NewBranch    bool   `json:"new_branch,omitempty"`
	// Ahead is what a push would publish, copies of commits the remote
	// already has left out; Behind the remote branch's commits this one
	// lacks. Both as of the last fetch.
	Ahead  int `json:"ahead"`
	Behind int `json:"behind"`
	// For a linked worktree (not Main) on a branch: where Land puts its
	// work (Base) and how that was worked out (BaseSource: recorded,
	// confirmed, reflog or default — docs/COMMIT-LAND-PUSH-DESIGN.md D12),
	// the commit it started from (Fork, when known), and how many of its
	// commits Base doesn't have yet, copies left out (ToLand).
	Base       string `json:"base,omitempty"`
	BaseSource string `json:"base_source,omitempty"`
	Fork       string `json:"fork,omitempty"`
	ToLand     int    `json:"to_land,omitempty"`
	// ComputedAt is when these were read.
	ComputedAt time.Time `json:"computed_at"`
}

// SessionRepoState is one repo of a session's set with its branch facts:
// GET /sessions/{id}/repos.
type SessionRepoState struct {
	// Root is the top of the checkout the session works in (CommitRequest.Repo).
	Root     string `json:"root"`
	Primary  bool   `json:"primary,omitempty"`
	Isolated bool   `json:"isolated,omitempty"`
	// Base is an isolated session's base branch.
	Base     string       `json:"base,omitempty"`
	Worktree RepoWorktree `json:"worktree"`
	// BaseWorktree is, for an isolated session, the user's checkout with
	// the base: what a push after Land publishes. Nil otherwise, or when
	// it can't be read.
	BaseWorktree *RepoWorktree `json:"base_worktree,omitempty"`
}
