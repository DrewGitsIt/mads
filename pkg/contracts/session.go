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

// SessionTrust is the session's trust level (MADS-UX §3.2 Trust pill): a flat
// per-session field with no workspace-level default propagation yet.
type SessionTrust string

const (
	TrustFull     SessionTrust = "full"
	TrustLimited  SessionTrust = "limited"
	TrustReadOnly SessionTrust = "read_only"
)

// SynthesisMode controls the synthesis engine's trigger sensitivity (spec §4,
// §10): off does nothing, light surfaces only high-confidence drafts, full
// surfaces everything the fast model proposes.
type SynthesisMode string

const (
	SynthesisOff   SynthesisMode = "off"
	SynthesisLight SynthesisMode = "light"
	SynthesisFull  SynthesisMode = "full"
)

// SynthesisModes is the full registry, for validation and enumeration.
var SynthesisModes = []SynthesisMode{SynthesisOff, SynthesisLight, SynthesisFull}

// Valid reports whether m is a registered synthesis mode.
func (m SynthesisMode) Valid() bool {
	for _, v := range SynthesisModes {
		if m == v {
			return true
		}
	}
	return false
}

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
	// Agent binds this session to a registry Agent (spec §9), the source of
	// its declared capabilities (spec §12) for capability-runtime wiring.
	// Empty means no agent binding — no capability wiring occurs.
	Agent     string `json:"agent,omitempty"`
	Isolation string `json:"isolation,omitempty"`
	// Trust is the session's trust level (MADS-UX §3.2 Trust pill).
	Trust SessionTrust `json:"trust,omitempty"`
	// WorktreeDir is the absolute path of this session's isolated worktree
	// (spec §17), set when Isolation is "worktree" and Cwd is a git repo.
	// Empty means the session works directly in its configured directory.
	WorktreeDir string `json:"worktree_dir,omitempty"`
	// WorktreeBranch is the branch backing WorktreeDir. Non-empty means the
	// session has isolated work not yet merged back — the UI's signal to flag
	// the session, active or inactive, as needing merge-back.
	WorktreeBranch string `json:"worktree_branch,omitempty"`
	Summary        string `json:"summary,omitempty"`
	// ActiveTaskID is the session's current task context (spec §1) — the
	// second rung of the commit-linkage chain.
	ActiveTaskID string `json:"active_task_id,omitempty"`
	// LinkedLeaderID is the schema hook for linked sessions (spec §1.12,
	// behind the linked-sessions flag); empty until that feature lands.
	LinkedLeaderID string `json:"linked_leader_id,omitempty"`
	// Tags marks a session for cross-cutting classification — currently just
	// "retro" for a retro-generated follow-up session (spec §7's retro-of-retros:
	// selecting tagged sessions, not a separate mode).
	Tags []string `json:"tags,omitempty"`
	// RetroID is set when this session was created as a retro's follow-up
	// (spec §7 DPA → task flow) — the traceability link back to the analysis.
	RetroID string `json:"retro_id,omitempty"`
	// SynthesisMode controls the synthesis engine (spec §4, §10). Empty is
	// treated as SynthesisOff by the engine — no DB default needed, since
	// sessions are a JSON blob column.
	SynthesisMode SynthesisMode `json:"synthesis_mode,omitempty"`
	// ProviderRef is the provider-native session handle (provider.Session.Ref) —
	// set once, right after the provider session starts, and passed back as
	// StartOptions.Resume to reattach after a bounce. Also the transcript
	// pointer: TranscriptReader.Transcript(ctx, ref) reads by this same value.
	ProviderRef string    `json:"provider_ref,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// MergeResult is the response to a session's merge-back request. A
// conflicted merge is a normal, structured outcome — not a Go error — so
// the API can hand it straight to the UI's resolution flow: Branch names
// the (still-existing) worktree branch, Files names the conflicted paths.
type MergeResult struct {
	Conflicted bool     `json:"conflicted"`
	Branch     string   `json:"branch,omitempty"`
	Files      []string `json:"files,omitempty"`
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
