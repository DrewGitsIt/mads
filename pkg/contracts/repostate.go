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
