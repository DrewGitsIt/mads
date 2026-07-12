package contracts

import "time"

// SessionActivity is derived state, not a user-managed one (spec §1): active
// while the agent process runs or activity is recent, inactive otherwise.
type SessionActivity string

const (
	SessionActive   SessionActivity = "active"
	SessionInactive SessionActivity = "inactive"
)

// Isolation modes (spec §17): worktree is the default in git repos.
const (
	IsolationWorktree   = "worktree"
	IsolationCurrentDir = "current_dir"
)

// Session is the workspace-of-record for one conversation-with-work (spec §1).
// Provider and Model bind at first message (spec §11.5); Summary is the
// compressed narrative regenerated on each deactivation (spec §16).
type Session struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Activity  SessionActivity `json:"activity"`
	Workspace string          `json:"workspace,omitempty"`
	Provider  string          `json:"provider,omitempty"`
	Model     string          `json:"model,omitempty"`
	Isolation string          `json:"isolation,omitempty"`
	Summary   string          `json:"summary,omitempty"`
	// ActiveTaskID is the session's current task context (spec §1) — the
	// second rung of the commit-linkage chain.
	ActiveTaskID string `json:"active_task_id,omitempty"`
	// LinkedLeaderID is the schema hook for linked sessions (spec §1.12,
	// behind the linked-sessions flag); empty until that feature lands.
	LinkedLeaderID string    `json:"linked_leader_id,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// ChangeOrigin is how a file change was noticed (spec §5).
type ChangeOrigin string

const (
	// OriginToolUse: the session's agent edited the file through its tools.
	OriginToolUse ChangeOrigin = "tool_use"
	// OriginCommit: the change arrived in a commit made in session context.
	OriginCommit ChangeOrigin = "commit"
	// OriginObserved: the watcher saw it change — user edits, scripts, other
	// processes.
	OriginObserved ChangeOrigin = "observed"
)

// AttributedChange is the §5 attribution rule (decided): in an isolated
// worktree everything is session work — the worktree exists only for this
// session. Outside isolation, only what the agent verifiably did (tool use,
// session-context commits) is attributed; other observed changes are recorded
// as unattributed observations, visible but never counted as session work.
// This lives in contracts so the Data Service and the Session Service apply
// the identical rule.
func AttributedChange(isolation string, origin ChangeOrigin) bool {
	if isolation == IsolationWorktree {
		return true
	}
	return origin == OriginToolUse || origin == OriginCommit
}

// FileChange is one tracked change (spec §5).
type FileChange struct {
	ID         int64        `json:"id"`
	SessionID  string       `json:"session_id"`
	TaskID     string       `json:"task_id,omitempty"`
	Repo       string       `json:"repo,omitempty"` // multi-repo tracking hook (spec §17)
	Path       string       `json:"path"`
	Change     string       `json:"change"` // added | modified | deleted
	Origin     ChangeOrigin `json:"origin"`
	Attributed bool         `json:"attributed"`
	Actor      string       `json:"actor,omitempty"`
	Timestamp  time.Time    `json:"timestamp"`
}

// How a commit got linked to a task — the §3/§5 resolution chain, in order.
const (
	LinkedViaTrailer        = "trailer"         // explicit Task: <id> trailer
	LinkedViaSessionContext = "session_context" // the session's active task
	LinkedViaHeuristic      = "heuristic"       // file-overlap candidates
)

// Commit is a recorded commit and its task linkage (spec §5). Orphan commits
// are flagged, never silently dropped — visible in file tracking and retro.
type Commit struct {
	Hash      string    `json:"hash"`
	SessionID string    `json:"session_id"`
	TaskID    string    `json:"task_id,omitempty"`
	Repo      string    `json:"repo,omitempty"`
	Message   string    `json:"message"`
	Author    string    `json:"author,omitempty"`
	LinkedVia string    `json:"linked_via,omitempty"`
	Orphan    bool      `json:"orphan,omitempty"`
	Timestamp time.Time `json:"timestamp"`
}
