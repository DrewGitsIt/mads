package contracts

// Wire shapes of the session's Commit and Land actions
// (docs/SESSION-WORKTREES-DESIGN.md D5, D6), served by the Router at
// /sessions/{id}/worktree, /commit, /commit/undo, /commit/draft and /land.
// The same shapes come back whether a live session child or the Router
// itself ran the operation (D3).

// WorktreeFile is one session file git still reports changed: a Commit
// dialog row. Path is relative to the session's folder.
type WorktreeFile struct {
	Path string `json:"path"`
	// Kind is "added" (new, untracked or staged as new), "deleted", or
	// "modified" — FileChange's vocabulary.
	Kind string `json:"kind"`
}

// WorktreeCommit is the commit at the tip of the session's folder.
type WorktreeCommit struct {
	Hash    string `json:"hash"`
	Subject string `json:"subject"`
	// Undoable: a Commit-made commit (it carries this session's Session:
	// trailer, has one parent) that hasn't landed — POST /commit/undo
	// would take it back.
	Undoable bool `json:"undoable"`
}

// WorktreeStatus is GET /sessions/{id}/worktree: everything the header
// control, the Commit dialog and the delete confirmation need, computed on
// request (never per render).
type WorktreeStatus struct {
	// Isolated is true when the session has its own worktree and branch,
	// so Land applies. False for a session working in place, which still
	// commits.
	Isolated bool `json:"isolated"`
	// Folder is the top of the working tree the session works in: its
	// worktree, or the checkout it runs in. Empty when it has none (no
	// directory was asked for, or it isn't a git repo).
	Folder string `json:"folder,omitempty"`
	Branch string `json:"branch,omitempty"`
	Base   string `json:"base,omitempty"`
	// Files are the session's uncommitted files (D4: its change list
	// intersected with git status, untracked new files included). Never
	// null.
	Files            []WorktreeFile  `json:"files"`
	UncommittedCount int             `json:"uncommitted_count"`
	UnlandedCount    int             `json:"unlanded_count"`
	LastCommit       *WorktreeCommit `json:"last_commit,omitempty"`
	// LandBlocked says why Land would be refused right now; nil when it
	// would run. Same reason codes as LandResult.
	LandBlocked *LandRefusal `json:"land_blocked,omitempty"`
}

// CommitRequest is POST /sessions/{id}/commit.
type CommitRequest struct {
	// Paths are relative to the session's folder (WorktreeFile.Path).
	// Empty means every uncommitted session file.
	Paths []string `json:"paths,omitempty"`
	// Message is the commit message as the user wrote it; the Session:
	// trailer is appended server-side. Empty is refused unless
	// Config.AutoCommitMessages is on, in which case the Fast Model drafts
	// one first.
	Message string `json:"message,omitempty"`
	// Drafted is the Router's to set, never a client's: the Fast Model
	// wrote Message (the header's one-click Commit). It rides along to
	// whoever runs the commit so its commit.created says so — the
	// conversation offers Undo only on such a commit.
	Drafted bool `json:"drafted,omitempty"`
}

// CommitSource* name who made a commit mads recorded itself (commit.created
// "source"): the Commit button, or Land's merge commit. Commits the agent
// makes on its own (hook-reported) carry none.
const (
	CommitSourceCommit = "commit"
	CommitSourceLand   = "land"
)

// Commit per-path outcomes.
const (
	CommitPathCommitted        = "committed"
	CommitPathAlreadyCommitted = "already_committed"
	CommitPathRejected         = "rejected"
)

// CommitPath is one requested path's outcome. Reason is set for rejected
// paths: "outside_folder", "not_session_file", "pipeline_task_file".
type CommitPath struct {
	Path   string `json:"path"`
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
}

// CommitResult is a successful POST /sessions/{id}/commit.
type CommitResult struct {
	Hash string `json:"hash"`
	// Message is the full message as committed, trailer included.
	Message string `json:"message"`
	// Drafted is true when the Fast Model wrote Message.
	Drafted bool `json:"drafted,omitempty"`
	// Undoable is true while the commit is the branch tip (it is, now).
	Undoable bool         `json:"undoable"`
	Paths    []CommitPath `json:"paths"`
}

// Commit refusal codes (CommitError.Code).
const (
	CommitInvalidPaths    = "invalid_paths"
	CommitNothingToCommit = "nothing_to_commit"
	CommitMessageRequired = "message_required"
	CommitGitFailed       = "git_failed"
	CommitNoFolder        = "no_folder"
	CommitNoFastModel     = "no_fast_model"
	CommitDraftFailed     = "draft_failed"
)

// CommitError is a refused POST /sessions/{id}/commit (or /commit/draft):
// a structured outcome the dialog renders in place, keeping the user's
// selection so they can fix the cause and retry.
type CommitError struct {
	Code string `json:"code"`
	// Error is one human sentence.
	Error string `json:"error"`
	// Detail is git's own output, verbatim (code git_failed: a pre-commit
	// hook's output, "Please tell me who you are", a signing failure).
	Detail    string       `json:"detail,omitempty"`
	Paths     []CommitPath `json:"paths,omitempty"`
	Selection []string     `json:"selection,omitempty"`
}

// CommitDraftRequest is POST /sessions/{id}/commit/draft. Empty Paths
// means every uncommitted session file.
type CommitDraftRequest struct {
	Paths []string `json:"paths,omitempty"`
}

// CommitDraft is a successful POST /sessions/{id}/commit/draft.
type CommitDraft struct {
	Message string `json:"message"`
	// Truncated is true when the diff was cut to fit the Fast Model's
	// prompt budget, so the draft saw only part of the change.
	Truncated bool `json:"truncated,omitempty"`
}

// UndoCommitRequest is POST /sessions/{id}/commit/undo.
type UndoCommitRequest struct {
	Hash string `json:"hash"`
}

// UndoCommitResult is POST /sessions/{id}/commit/undo's answer. A refusal
// (Undone false) names its Reason: "not_tip", "not_session_commit",
// "merge_commit", "already_landed", "mid_merge", "no_folder".
type UndoCommitResult struct {
	Undone bool   `json:"undone"`
	Hash   string `json:"hash"`
	Reason string `json:"reason,omitempty"`
	Detail string `json:"detail,omitempty"`
}

// Land outcomes (LandResult.Outcome).
const (
	LandLanded     = "landed"
	LandConflicted = "conflicted"
	LandRefused    = "refused"
)

// Land refusal reasons (LandRefusal.Reason, LandResult.Reason).
const (
	// Gates (D6 "When Land is allowed"), checked before anything runs.
	LandNotIsolated       = "not_isolated"
	LandBusy              = "busy"
	LandPipeline          = "pipeline"
	LandMidMerge          = "mid_merge"
	LandOffBranch         = "off_branch"
	LandDirty             = "dirty"
	LandBaseNotCheckedOut = "base_not_checked_out"
	LandPrimaryMidMerge   = "primary_mid_merge"
	LandNothingToLand     = "nothing_to_land"
	// Step 1 failed for a reason other than a conflict.
	LandMergeFailed = "merge_failed"
	// The base kept moving between steps 1 and 2.
	LandBaseMoving = "base_moving"
	// Step 2: the user's checkout refused the merge, which was aborted.
	LandPrimaryDirty     = "primary_dirty"
	LandPrimaryUntracked = "primary_untracked"
	LandPrimaryRefused   = "primary_refused"
)

// LandRefusal is why Land won't (or didn't) run.
type LandRefusal struct {
	Reason string `json:"reason"`
	// Detail is human text, or git's output verbatim where that's clearest.
	Detail string `json:"detail,omitempty"`
	// Files the refusal names: the folder's uncommitted files (dirty), or
	// the files in the user's checkout that block the merge.
	Files []string `json:"files,omitempty"`
}

// LandResult is POST /sessions/{id}/land's answer — always HTTP 200 for
// these three outcomes; only an unexpected failure is an error status.
type LandResult struct {
	Outcome string `json:"outcome"`
	Branch  string `json:"branch,omitempty"`
	Base    string `json:"base,omitempty"`
	// MergeCommit is the --no-ff merge commit on the base (landed).
	MergeCommit string `json:"merge_commit,omitempty"`
	// FastForwarded: the session branch caught up with the base afterwards
	// (best-effort; false is not an error).
	FastForwarded bool `json:"fast_forwarded,omitempty"`
	// Files: the conflicted files (conflicted), or LandRefusal.Files.
	Files  []string `json:"files,omitempty"`
	Reason string   `json:"reason,omitempty"`
	Detail string   `json:"detail,omitempty"`
}
