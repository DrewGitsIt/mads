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

// SessionTrusts is the full registry, for validation and enumeration.
var SessionTrusts = []SessionTrust{TrustFull, TrustLimited, TrustReadOnly}

// Valid reports whether t is a registered trust level.
func (t SessionTrust) Valid() bool {
	for _, v := range SessionTrusts {
		if t == v {
			return true
		}
	}
	return false
}

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

// FastModelMode selects where Fast Model completions run (spec §11/§13):
// provider uses the user's default provider's hermetic Completer capability
// (zero-config, dormant if that provider doesn't implement one); endpoint
// uses the explicit OpenAI-compatible FastModelBaseURL; off disables Fast
// Model features entirely.
type FastModelMode string

const (
	FastModelModeProvider FastModelMode = "provider"
	FastModelModeEndpoint FastModelMode = "endpoint"
	FastModelModeOff      FastModelMode = "off"
)

// FastModelModes is the full registry, for validation and enumeration.
var FastModelModes = []FastModelMode{FastModelModeProvider, FastModelModeEndpoint, FastModelModeOff}

// Valid reports whether m is a registered Fast Model mode.
func (m FastModelMode) Valid() bool {
	for _, v := range FastModelModes {
		if m == v {
			return true
		}
	}
	return false
}

// SessionNameSource says whether Name still reflects its placeholder origin
// or has been claimed by a human: Derived is the first-line-of-the-launch-
// message placeholder (or any other auto-derived starting point) that the
// session-title job is still free to replace; User marks a name the human
// set via the header rename affordance; Generated marks the session-title
// job's own output. Only Derived is ever overwritten by that job — User and
// Generated both mean "leave this alone."
type SessionNameSource string

const (
	SessionNameSourceDerived   SessionNameSource = "derived"
	SessionNameSourceUser      SessionNameSource = "user"
	SessionNameSourceGenerated SessionNameSource = "generated"
)

// Session is the workspace-of-record for one conversation-with-work (spec §1).
// Provider and Model bind at first message (spec §11.5); Summary is the
// compressed narrative regenerated on each deactivation (spec §16).
type Session struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// NameSource governs whether the session-title job may replace Name;
	// empty defaults to Derived on Create (see store.Sessions.Create).
	NameSource SessionNameSource `json:"name_source,omitempty"`
	Activity   SessionActivity   `json:"activity"`
	// Workspace references a registry Workspace by id *or* name: Create
	// canonicalises it to the id (resolveSessionDefaultsSourced), while the
	// registry lookup accepts either (dataclient.Workspace(ctx, idOrName)).
	// It is a reference, not a label — resolve it before showing it.
	Workspace string `json:"workspace,omitempty"`
	Provider  string `json:"provider,omitempty"`
	Model     string `json:"model,omitempty"`
	// Agent binds this session to a registry Agent (spec §9), the source of
	// its declared capabilities (spec §12) for capability-runtime wiring.
	// Empty means no agent binding — no capability wiring occurs.
	Agent     string `json:"agent,omitempty"`
	Isolation string `json:"isolation,omitempty"`
	// Trust is the session's trust level (MADS-UX §3.2 Trust pill).
	Trust SessionTrust `json:"trust,omitempty"`
	// WorktreeDir is the absolute path of this session's isolated worktree
	// (spec §17; docs/SESSION-WORKTREES-DESIGN.md D1), set at its first start
	// when Isolation is "worktree" and Dir is a git repo. Empty means the
	// session works directly in its configured directory. Like
	// WorktreeBranch and WorktreeBase, written only through the scoped
	// PATCH /sessions/{id}/worktree; a full-record PUT leaves it as it was.
	WorktreeDir string `json:"worktree_dir,omitempty"`
	// Dir is the directory the session was asked to start in — absolute,
	// "~" already expanded: the Launchpad's folder pick, else the
	// workspace's primary repo, else Config.DefaultDir (resolved by the
	// Router at create). The Session runner starts from here, isolating
	// into a worktree on top when Isolation asks for one and Dir is a git
	// repo. Empty means no directory was asked for: the runner keeps its
	// own -cwd, which is wherever the Router itself was started.
	Dir string `json:"dir,omitempty"`
	// Cwd is the absolute directory the agent actually runs in — the worktree
	// for worktree isolation, otherwise the session's configured directory.
	// Set by the Session runner at start so clients (e.g. the docked shell) can
	// open in the same context as the agent instead of guessing.
	Cwd string `json:"cwd,omitempty"`
	// WorktreeBranch is the branch backing WorktreeDir, always
	// mads/session/<id>. It lives as long as the session does; whether it
	// holds work not yet landed is UnlandedCount's to say.
	WorktreeBranch string `json:"worktree_branch,omitempty"`
	// WorktreeBase is the branch the primary had checked out when the
	// session's worktree was created: where its branch started, what its
	// unlanded commits are counted against, and the Land target
	// (docs/SESSION-WORKTREES-DESIGN.md D1). Empty for sessions that work in
	// place.
	WorktreeBase string `json:"worktree_base,omitempty"`
	// UncommittedCount and UnlandedCount are the header state (D4): how many
	// of the session's files git still reports changed, and how many commits
	// its branch has that WorktreeBase doesn't (always 0 in place). Computed
	// by whoever just changed them — the live runner at start and turn end —
	// never per render, and written only through the scoped
	// PATCH /sessions/{id}/worktree (SessionWorktreePatch), so a full-record
	// PUT carrying an older read can't roll them back.
	UncommittedCount int `json:"uncommitted_count,omitempty"`
	UnlandedCount    int `json:"unlanded_count,omitempty"`
	// ShadowDir is the absolute base path of this session's shadow git store
	// (docs/SHADOW-GIT-DIFFS-ADR.md; services/session/shadow) — one shadow
	// git dir per touched root underneath it, plus the capture index. Set by
	// the Session runner at provisioning so the Router reads diffs from the
	// recorded location instead of guessing; empty means no shadow store
	// (shadowing disabled, or a session predating it).
	ShadowDir string `json:"shadow_dir,omitempty"`
	Summary   string `json:"summary,omitempty"`
	// ActiveTaskID is the session's current task context (spec §1) — the
	// second rung of the commit-linkage chain.
	ActiveTaskID string `json:"active_task_id,omitempty"`
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
	ProviderRef string `json:"provider_ref,omitempty"`
	// ForkOnResume marks ProviderRef as someone else's native conversation
	// (an imported transcript) that must never be appended to: the next
	// start passes it as StartOptions.Resume with Fork set, and the Runner
	// clears this in the same write that replaces ProviderRef with the fork's
	// own ref. While ProviderRef is still the original it stays set, so a
	// start that dies before the fork exists re-forks from the untouched
	// original.
	ForkOnResume bool `json:"fork_on_resume,omitempty"`
	// LastUsage is the most recent turn's token accounting (provider.Usage,
	// mirrored as Usage above), persisted by the Runner on every turn end —
	// the context meter's data source at wake time, before this process has
	// seen any turn_end of its own, and the resume-compaction trigger's input
	// (services/session/internal/runner/compact.go). Nil until the session's
	// first turn ends.
	LastUsage *Usage `json:"last_usage,omitempty"`
	// AgentState mirrors the latest SessionAgentState transition the Runner
	// appended — the same payload, written by the same scoped store call
	// that appends the event (store.Sessions.SetAgentState), so the record
	// and the stream can never disagree. It exists because an event stream
	// alone leaves a cold reader blind: a page load or an SSE reconnect
	// mid-turn would show every session idle until its next transition.
	// Nil before the session's first transition. A consumer should read it
	// as idle on an inactive session: only the live Runner clears it, so a
	// hard-killed process (the supervisor's boot sweep reconciles those to
	// inactive, services/router/internal/supervisor) leaves its last
	// working state behind.
	AgentState *AgentState `json:"agent_state,omitempty"`
	// LastTurnAt is when this session's most recent turn ended, stamped
	// beside LastUsage by the Runner on every turn end. Deliberately not
	// UpdatedAt, which renames, summary writes and activity flips all bump:
	// this is the only field that means "last response", which makes it the
	// rail's last-reply stamp and its recency sort key — one a rename
	// cannot perturb. Nil until the session's first turn ends.
	LastTurnAt *time.Time `json:"last_turn_at,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
}

// SessionWorktreePatch is a scoped write of a session's isolation state:
// nil leaves a field alone, a non-nil zero value clears it. The Data Service
// applies it inside one transaction (store.Sessions.SetWorktree), so writers
// of these fields — the session runner, and later the Router and the
// pipeline child — can't drop each other's or anyone else's fields the way
// a Session-then-UpdateSession round trip can.
type SessionWorktreePatch struct {
	WorktreeDir      *string `json:"worktree_dir,omitempty"`
	WorktreeBranch   *string `json:"worktree_branch,omitempty"`
	WorktreeBase     *string `json:"worktree_base,omitempty"`
	UncommittedCount *int    `json:"uncommitted_count,omitempty"`
	UnlandedCount    *int    `json:"unlanded_count,omitempty"`
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

// SetSessionModelResponse is POST /sessions/{id}/model's wire shape. Session
// is the updated record — the model is persisted whatever happens to the
// live process, so a later cold start always reads the user's choice.
//
// Queued reports what happened to the process running right now: false means
// it was cycled onto the new model immediately, true means a turn was in
// flight and the switch is parked until that turn ends (a model change never
// interrupts an agent mid-turn — see runner.SetModel). It is the UI's cue to
// say "applies when this turn finishes" rather than claiming the model
// already changed.
type SetSessionModelResponse struct {
	Session Session `json:"session"`
	Queued  bool    `json:"queued"`
}
