package contracts

// SessionDefaultSource says which layer produced a resolved session
// default value (spec §10 precedence). Used only by the Launchpad preview
// endpoint (GET /sessions/defaults) — resolveSessionDefaults itself
// doesn't expose provenance to its two create-session callers.
type SessionDefaultSource string

const (
	SessionDefaultSourceWorkspace       SessionDefaultSource = "workspace"
	SessionDefaultSourceConfig          SessionDefaultSource = "config"
	SessionDefaultSourceProviderDefault SessionDefaultSource = "provider_default"
	SessionDefaultSourceBuiltin         SessionDefaultSource = "builtin"
)

// ResolvedSessionField is one resolved session-default value plus where it
// came from. Empty Value with empty Source means nothing resolved it —
// e.g. Agent with no workspace default and no global fallback exists.
type ResolvedSessionField struct {
	Value  string               `json:"value,omitempty"`
	Source SessionDefaultSource `json:"source,omitempty"`
}

// SessionDefaults is what GET /sessions/defaults returns: the same
// resolution resolveSessionDefaults performs at session-creation time,
// computed without creating anything, so the Launchpad can show what
// Start would actually use.
type SessionDefaults struct {
	Provider  ResolvedSessionField `json:"provider"`
	Model     ResolvedSessionField `json:"model"`
	Agent     ResolvedSessionField `json:"agent"`
	Isolation ResolvedSessionField `json:"isolation"`
	Trust     ResolvedSessionField `json:"trust"`
	// Dir is the directory the session would start in. Source "builtin"
	// means nothing asked for one and Value is the Router's own working
	// directory — reported for the preview only, never written onto a
	// session (see Session.Dir).
	Dir ResolvedSessionField `json:"dir"`
	// DirStatus describes Dir on disk so the Launchpad can warn before
	// Start: DirStatusGit, DirStatusPlain (worktree isolation will run in
	// the folder itself) or DirStatusMissing. Empty when Dir is empty.
	DirStatus DirStatus `json:"dir_status,omitempty"`
	// DirBranch is the branch the checkout at Dir has open, when DirStatus
	// is DirStatusGit and HEAD is on a branch — empty when detached. The
	// conversation header shows it for a session working in place, whose
	// record carries no branch of its own (only isolated sessions have
	// worktree_branch).
	DirBranch string `json:"dir_branch,omitempty"`
	// DirOwnerSession is the session whose worktree Dir is — DirBranch is
	// that session's branch (mads/session/<id>) and the session still
	// exists. A session started there in place shares that branch, and one
	// isolated there branches from it and lands back into it; the Launchpad
	// says so. Nil for any other folder.
	DirOwnerSession *WorktreeTenant `json:"dir_owner_session,omitempty"`
	// Workspace is the workspace whose Defaults were consulted (explicit
	// ?workspace= query param, or Config.DefaultWorkspace) — empty if none
	// resolved (bad/missing key, or no workspace involved at all).
	Workspace string `json:"workspace,omitempty"`
}

// DirStatus is what GET /sessions/defaults found at the resolved Dir.
type DirStatus string

const (
	DirStatusGit     DirStatus = "git"
	DirStatusPlain   DirStatus = "plain"
	DirStatusMissing DirStatus = "missing"
)
