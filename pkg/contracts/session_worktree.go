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
	// SharedWith are the other sessions working inside this session's
	// folder (a linked session that joined it at start), and
	// PipelineRunning is whether a pipeline run for this session is
	// active. Either one means delete keeps the folder and branch. The
	// Router's to set; the delete dialog says so.
	SharedWith      []WorktreeTenant `json:"shared_with,omitempty"`
	PipelineRunning bool             `json:"pipeline_running,omitempty"`
	// OwnerSession is, for a session working in place, the session whose
	// worktree that place is (SharedWith seen from the other side): this
	// session commits onto the owner's branch, and the owner lands it. The
	// Router's to set.
	OwnerSession *WorktreeTenant `json:"owner_session,omitempty"`
}

// WorktreeTenant names another session working in a session's folder, or
// the session whose folder it is.
type WorktreeTenant struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// CommitPlan is GET /sessions/{id}/commit-plan: everything the Commit
// dialog lists (docs/COMMIT-LAND-PUSH-DESIGN.md D2) — every uncommitted
// file in each of the session's repos, whoever changed it, and the
// session's changed files outside every repo, which can't be committed
// where they are. Computed on request.
type CommitPlan struct {
	// Repos are the session's repos with anything uncommitted, its
	// folder's repo first. Never null.
	Repos []CommitPlanRepo `json:"repos"`
	// Outside are files the session changed outside every repo, absolute.
	// Never null.
	Outside []CommitPlanOutside `json:"outside"`
	// MoveTargets are the repos an Outside file can be moved into (D4):
	// the repos above, the repos directly inside a session folder that
	// isn't one, and its workspace's repos. Never null.
	MoveTargets []string `json:"move_targets"`
}

// CommitPlanRepo is one repo's section of the Commit dialog.
type CommitPlanRepo struct {
	// Root is the top of the repo's working tree: CommitRequest.Repo.
	Root     string `json:"root"`
	Branch   string `json:"branch,omitempty"`
	Isolated bool   `json:"isolated,omitempty"`
	// Primary marks the session folder's own repo.
	Primary bool             `json:"primary,omitempty"`
	Files   []CommitPlanFile `json:"files"`
}

// When a file not changed by the session was changed (CommitPlanFile.Since).
const (
	// ChangedDuringSession: it differs from the session-start snapshot.
	ChangedDuringSession = "session"
	// ChangedBeforeSession: it was already uncommitted when the session
	// started, and hasn't changed since.
	ChangedBeforeSession = "before"
)

// CommitPlanFile is one uncommitted file in a repo.
type CommitPlanFile struct {
	// Path is relative to the repo's Root.
	Path string `json:"path"`
	Kind string `json:"kind"` // added | modified | deleted
	// Mine: the session's own tools changed it. The dialog checks these.
	Mine bool `json:"mine,omitempty"`
	// For a file that isn't Mine: the other session whose tools last
	// changed it, when the ledger says; and when it changed, when the
	// session-start snapshot can tell (ChangedDuringSession or
	// ChangedBeforeSession; empty when it can't).
	By    *WorktreeTenant `json:"by,omitempty"`
	Since string          `json:"since,omitempty"`
}

// CommitPlanOutside is a file the session changed outside every repo.
type CommitPlanOutside struct {
	Path string `json:"path"`
	Kind string `json:"kind"`
}

// CommitRequest is POST /sessions/{id}/commit.
type CommitRequest struct {
	// Repo is the top of the repo to commit in, one of the session's
	// (docs/COMMIT-LAND-PUSH-DESIGN.md D1). Empty is its folder's repo, or
	// its only repo when the folder isn't one. Paths are relative to it.
	Repo string `json:"repo,omitempty"`
	// Paths are relative to the repo's top (WorktreeFile.Path). Empty
	// means every uncommitted session file there. A named path may be any
	// file git reports changed in the repo, the session's or not (D2).
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
// "source"): the Commit button, Land's merge commit, a pipeline saving
// the agent's uncommitted edits before merging a task over them (D8), or
// Land's step-1 merge of the base into the session branch when mergiraf
// resolved files in it (recorded so the Files tab can show the result).
// Commits the agent makes on its own (hook-reported) carry none.
const (
	CommitSourceCommit       = "commit"
	CommitSourceLand         = "land"
	CommitSourcePipelineSave = "pipeline_save"
	CommitSourceLandBase     = "land_base"
)

// Commit per-path outcomes.
const (
	CommitPathCommitted        = "committed"
	CommitPathAlreadyCommitted = "already_committed"
	CommitPathRejected         = "rejected"
)

// CommitPath is one requested path's outcome. Reason is set for rejected
// paths: "outside_folder", "pipeline_task_file".
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
	CommitLocked          = "locked"
	CommitUnknownRepo     = "unknown_repo"
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
	Repo  string   `json:"repo,omitempty"` // as CommitRequest.Repo
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
	Repo string `json:"repo,omitempty"` // as CommitRequest.Repo
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
	// LandAutoResolved: bringing the base into the session branch (step 1)
	// merged cleanly only because mergiraf resolved files plain git would
	// have left conflicted. The merge is committed in the session's folder
	// (MergeCommit) and Land stopped there, before the user's checkout, so
	// the user can check the resolutions; the next Land carries on.
	LandAutoResolved = "auto_resolved"
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
	// Another operation held the session's folder or the user's checkout
	// past the Router's short wait; try again.
	LandLocked = "locked"
)

// LandRefusal is why Land won't (or didn't) run.
type LandRefusal struct {
	Reason string `json:"reason"`
	// Detail is human text, or git's output verbatim where that's clearest.
	Detail string `json:"detail,omitempty"`
	// Files the refusal names: the folder's uncommitted files (dirty), or
	// the files in the user's checkout that block the merge.
	Files []string `json:"files,omitempty"`
	// Outside (dirty only): those of Files that aren't on the session's
	// change list — changed through a shell or by something else — so
	// Commit can't take them; the agent or the user has to commit or revert
	// them.
	Outside []string `json:"outside,omitempty"`
}

// LandResult is POST /sessions/{id}/land's answer — always HTTP 200 for
// these four outcomes; only an unexpected failure is an error status.
type LandResult struct {
	Outcome string `json:"outcome"`
	Branch  string `json:"branch,omitempty"`
	Base    string `json:"base,omitempty"`
	// MergeCommit is the --no-ff merge commit on the base (landed), or the
	// step-1 merge commit in the session's folder (auto_resolved).
	MergeCommit string `json:"merge_commit,omitempty"`
	// FastForwarded: the session branch caught up with the base afterwards
	// (best-effort; false is not an error).
	FastForwarded bool `json:"fast_forwarded,omitempty"`
	// Files: the conflicted files (conflicted), the files mergiraf
	// resolved (auto_resolved), or LandRefusal.Files.
	Files []string `json:"files,omitempty"`
	// Outside is LandRefusal.Outside.
	Outside []string `json:"outside,omitempty"`
	Reason  string   `json:"reason,omitempty"`
	Detail  string   `json:"detail,omitempty"`
}

// MoveRequest is POST /sessions/{id}/move: move a file the session changed
// outside every repo into one (docs/COMMIT-LAND-PUSH-DESIGN.md D4).
type MoveRequest struct {
	// Path is the file, absolute (CommitPlanOutside.Path).
	Path string `json:"path"`
	// Repo is the top of the repo to move it into.
	Repo string `json:"repo"`
	// Dir is the folder inside Repo, relative; empty is the repo's top.
	Dir string `json:"dir,omitempty"`
}

// MoveResult is a successful move: where the file is now, absolute.
type MoveResult struct {
	Path string `json:"path"`
}

// Move refusal codes (MoveError.Code).
const (
	MoveMissing        = "missing"          // the file isn't there any more
	MoveNotSessionFile = "not_session_file" // not a file this session changed
	MoveInRepo         = "in_repo"          // already in a repo: Commit can take it
	MoveUnknownRepo    = "unknown_repo"     // Repo isn't the top of a git repo
	MoveOutsideRepo    = "outside_repo"     // Dir leads out of Repo
	MoveExists         = "exists"           // something is already at the destination
	MoveIgnored        = "ignored"          // the repo ignores the destination
	MoveFailed         = "failed"           // the filesystem said no
)

// MoveError is a refused move, rendered in the dialog's folder picker.
type MoveError struct {
	Code  string `json:"code"`
	Error string `json:"error"`
}

// MoveFolder is a folder a file could move into, relative to its repo's
// top, with how many files of the same kind it already holds.
type MoveFolder struct {
	Path    string `json:"path"`
	Matches int    `json:"matches,omitempty"`
}

// Push targets (PushRequest.Target, docs/COMMIT-LAND-PUSH-DESIGN.md D5).
const (
	// PushBranch is the branch the work is on: an isolated session's own
	// branch (from its worktree, ready for a PR), else the branch checked
	// out where it works in place.
	PushBranch = "branch"
	// PushBase is an isolated session's base, from the user's checkout:
	// what Land merged into.
	PushBase = "base"
)

// PushRequest is POST /sessions/{id}/push.
type PushRequest struct {
	Repo   string `json:"repo,omitempty"`   // as CommitRequest.Repo
	Target string `json:"target,omitempty"` // PushBranch (default) or PushBase
	// Rebase: the user chose "Rebase onto origin and push" — rebase onto
	// the remote branch first when it has commits this one doesn't. Never
	// done otherwise.
	Rebase bool `json:"rebase,omitempty"`
	// Expect is the commit the confirmation showed at the branch's tip;
	// a push refuses if the branch has moved since, so it never publishes
	// commits the user didn't see listed. Empty skips the check.
	Expect string `json:"expect,omitempty"`
}

// PushCommit is one commit a push would publish, or one coming in.
type PushCommit struct {
	Hash    string `json:"hash"`
	Subject string `json:"subject"`
}

// PushPreview is GET /sessions/{id}/push: what the confirmation shows,
// after fetching the remote branch.
type PushPreview struct {
	Repo   string `json:"repo"`   // the repo's top (the session's folder for an isolated branch push)
	Target string `json:"target"` // PushBranch or PushBase
	Dir    string `json:"dir"`    // where the push runs
	Branch string `json:"branch,omitempty"`
	Remote string `json:"remote,omitempty"`
	// RemoteBranch is the branch it goes to on Remote; NewBranch when the
	// remote has none yet (the push creates it and sets the upstream).
	RemoteBranch string `json:"remote_branch,omitempty"`
	NewBranch    bool   `json:"new_branch,omitempty"`
	Head         string `json:"head,omitempty"`
	// Commits would be published, newest first (at most 50; Ahead counts
	// all of them).
	Commits []PushCommit `json:"commits"`
	Ahead   int          `json:"ahead"`
	// Behind counts the remote's commits this branch lacks; Incoming lists
	// them, newest first (at most 20). A push then needs a rebase first.
	Behind   int          `json:"behind"`
	Incoming []PushCommit `json:"incoming"`
	// FetchError is set when the fetch failed: the counts are as of the
	// last fetch, and the push will likely fail the same way.
	FetchError string `json:"fetch_error,omitempty"`
	// Blocked says why Push can't run at all; RebaseBlocked why "Rebase
	// onto origin and push" can't, when Behind > 0.
	Blocked       *PushRefusal `json:"blocked,omitempty"`
	RebaseBlocked *PushRefusal `json:"rebase_blocked,omitempty"`
}

// Push refusal reasons (PushRefusal.Reason, PushResult.Reason).
const (
	PushNoRemote      = "no_remote"
	PushDetached      = "detached"
	PushNothingToPush = "nothing_to_push"
	PushNotIsolated   = "not_isolated"    // PushBase on a session working in place
	PushBehind        = "behind"          // the remote moved on and no rebase was asked for
	PushMoved         = "moved"           // the branch moved since the confirmation (Expect)
	PushBusy          = "busy"            // an agent is working in that folder: no rebase under it
	PushPipeline      = "pipeline"        // a pipeline run is active for the session
	PushNotCheckedOut = "not_checked_out" // a rebase needs the branch checked out there
	PushMidOperation  = "mid_operation"
	PushOverlap       = "overlap" // uncommitted files the incoming commits change
	PushRebaseFailed  = "rebase_failed"
	PushFailed        = "push_failed" // git push said no: rejected, auth, protected branch
	PushLocked        = "locked"
)

// PushRefusal is why a push, or its rebase, won't run.
type PushRefusal struct {
	Reason string   `json:"reason"`
	Detail string   `json:"detail,omitempty"`
	Files  []string `json:"files,omitempty"`
}

// Push outcomes (PushResult.Outcome).
const (
	PushPushed     = "pushed"
	PushRefused    = "refused"
	PushConflicted = "conflicted" // the rebase hit conflicts and was aborted; nothing changed
)

// PushResult is POST /sessions/{id}/push's answer: HTTP 200 for all three
// outcomes.
type PushResult struct {
	Outcome      string `json:"outcome"`
	Branch       string `json:"branch,omitempty"`
	Remote       string `json:"remote,omitempty"`
	RemoteBranch string `json:"remote_branch,omitempty"`
	// Rebased: the branch was rebased onto the remote before the push.
	Rebased bool   `json:"rebased,omitempty"`
	Head    string `json:"head,omitempty"`
	Pushed  int    `json:"pushed,omitempty"`
	Reason  string `json:"reason,omitempty"`
	// Detail is human text, or git's output verbatim.
	Detail string   `json:"detail,omitempty"`
	Files  []string `json:"files,omitempty"`
}
