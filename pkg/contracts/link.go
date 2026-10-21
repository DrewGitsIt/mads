package contracts

import "time"

// Grant is one capability a link conveys from its granting session onto its
// subject session (docs/LINKED-SESSIONS-DESIGN.md).
type Grant string

const (
	// GrantRead: receive digests of the subject's activity; read transcript
	// + task state.
	GrantRead Grant = "read"
	// GrantInject: deliver a message into the subject's conversation (queued,
	// attributed).
	GrantInject Grant = "inject"
	// GrantOrchestrate: agent-held tool to message/direct the subject's agent.
	GrantOrchestrate Grant = "orchestrate"
)

// Grants is the full registry, for validation and enumeration.
var Grants = []Grant{GrantRead, GrantInject, GrantOrchestrate}

// Valid reports whether g is a registered grant.
func (g Grant) Valid() bool {
	for _, v := range Grants {
		if g == v {
			return true
		}
	}
	return false
}

// LinkPreset names a grant-set shorthand (docs/LINKED-SESSIONS-DESIGN.md).
// Storage is always the grant set; presets are creation-time sugar.
type LinkPreset string

const (
	// PresetMonitor = {read} — user-selectable.
	PresetMonitor LinkPreset = "monitor"
	// PresetHandRaiser = {read,inject} — SYSTEM-CREATABLE ONLY (implicit on
	// pipeline launch, docs/LINKED-SESSIONS-DESIGN.md decision 6); never
	// offered via the user-facing link chooser or the user CRUD route.
	PresetHandRaiser LinkPreset = "hand_raiser"
	// PresetLeader = {read,inject,orchestrate} — user-selectable.
	PresetLeader LinkPreset = "leader"
)

// PresetGrants maps each preset to its grant set.
var PresetGrants = map[LinkPreset][]Grant{
	PresetMonitor:    {GrantRead},
	PresetHandRaiser: {GrantRead, GrantInject},
	PresetLeader:     {GrantRead, GrantInject, GrantOrchestrate},
}

// LinkCadence controls how often a `read` grant's digest push fires
// (docs/LINKED-SESSIONS-DESIGN.md decision 4).
type LinkCadence string

const (
	// CadencePerTurn pushes a compressed digest to the monitor on every
	// completed turn of the subject's agent.
	CadencePerTurn LinkCadence = "per_turn"
	// CadenceOnSignal pushes nothing per turn; the monitor is woken only by
	// attention signals from the subject — a permission request, a crash, a
	// task reaching done, blocked, wont_do or needs_review. Turn digests
	// never wake it: they queue on the link mailbox for check_mailbox.
	// (Waking on a turn's content is to return as an intent-based
	// classifier; 2026-10-19.) Default: a monitor should pay for a turn
	// only when the subject actually needs attention.
	CadenceOnSignal LinkCadence = "on_signal"
	// CadenceManualOnly pushes nothing; the monitor's user pulls via the
	// subject's existing transcript/summary reads instead.
	CadenceManualOnly LinkCadence = "manual_only"
)

// LinkCadences is the full registry, for validation and enumeration.
var LinkCadences = []LinkCadence{CadencePerTurn, CadenceOnSignal, CadenceManualOnly}

// Valid reports whether c is a registered cadence.
func (c LinkCadence) Valid() bool {
	for _, v := range LinkCadences {
		if c == v {
			return true
		}
	}
	return false
}

// OrchestrateStatus reports whether a link's orchestrate grant has actually
// materialized as live MCP tools on the granting session's running provider
// process. Server-computed only — never client-settable via LinkRequest.
type OrchestrateStatus string

const (
	// OrchestratePending: the orchestrate grant is held, but the tools won't
	// appear until the granting session's provider process next restarts
	// (e.g. a provider that can't take mid-session MCP changes, or a live
	// refresh that hasn't completed yet).
	OrchestratePending OrchestrateStatus = "pending_restart"
	// OrchestrateActive: the orchestrate grant is held and live on the
	// granting session's running provider process right now.
	OrchestrateActive OrchestrateStatus = "active"
	// OrchestrateUnsupported: the granting session's provider declares MCP
	// servers unsupported outright — a terminal state; restarting never
	// helps.
	OrchestrateUnsupported OrchestrateStatus = "unsupported"
	// OrchestrateFailed: the orchestrate grant is held, but the tools
	// couldn't be started (e.g. the orchestrate MCP helper binary didn't
	// launch). Link.OrchestrateError carries the reason. A later refresh or
	// restart retries, so it isn't terminal the way unsupported is, but it
	// needs something fixed first; it is not the passive wait pending_restart
	// describes.
	OrchestrateFailed OrchestrateStatus = "failed"
)

// OrchestrateStatuses is the full registry, for validation and enumeration.
var OrchestrateStatuses = []OrchestrateStatus{OrchestratePending, OrchestrateActive, OrchestrateUnsupported, OrchestrateFailed}

// Valid reports whether s is a registered orchestrate status.
func (s OrchestrateStatus) Valid() bool {
	for _, v := range OrchestrateStatuses {
		if s == v {
			return true
		}
	}
	return false
}

// Link is a directed grant set from GrantingSessionID to SubjectSessionID
// (docs/LINKED-SESSIONS-DESIGN.md). Leader→subject orientation only; no
// peer/bidirectional links. Session deletion never cascades to links — a
// link outliving one of its endpoints is a dangling reference by design.
type Link struct {
	ID                string      `json:"id"`
	GrantingSessionID string      `json:"granting_session_id"`
	SubjectSessionID  string      `json:"subject_session_id"`
	Grants            []Grant     `json:"grants"`
	Cadence           LinkCadence `json:"cadence"`
	// Implicit marks a link the system created without user action
	// (docs/LINKED-SESSIONS-DESIGN.md decision 6) — currently only the
	// self-referential hand-raiser grant on a session's own pipeline run.
	// Auto-removed when the condition that created it ends; never
	// user-editable.
	Implicit bool `json:"implicit,omitempty"`
	// OrchestrateStatus reports whether the orchestrate grant (if any) has
	// materialized as live tools yet. Empty when the link carries no
	// orchestrate grant. Server-computed; see OrchestrateStatus.
	OrchestrateStatus OrchestrateStatus `json:"orchestrate_status,omitempty"`
	// OrchestrateError is why the tools failed to start, set only while
	// OrchestrateStatus is "failed". Server-computed, like the status.
	OrchestrateError string    `json:"orchestrate_error,omitempty"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

// HasGrant reports whether l carries g.
func (l Link) HasGrant(g Grant) bool {
	for _, v := range l.Grants {
		if v == g {
			return true
		}
	}
	return false
}

// LinkDigest is one queued mailbox entry on an on_signal link
// (docs/LINKED-SESSIONS-DESIGN.md decision 4): a compressed turn digest
// that accumulates instead of forcing a monitor turn, flushing as context
// on the link's next signal wake or on-demand drain. FromTurn/ToTurn are
// the digest's turn anchors (0 = unknown), so a reader can drill into the
// range it covers.
type LinkDigest struct {
	Text      string    `json:"text"`
	FromTurn  int       `json:"from_turn,omitempty"`
	ToTurn    int       `json:"to_turn,omitempty"`
	CreatedAt time.Time `json:"created_at"`

	// Deferred marks an entry queued because its recipient's child wasn't
	// running when the push happened (docs/LINKED-SESSIONS-DESIGN.md,
	// "Delivery to sleeping sessions"), as opposed to on_signal backlog,
	// which keeps waiting for check_mailbox or the next signal wake.
	// Deferred entries replay on the recipient's next start: Text is the
	// push body and Attribution the one it carried, stored whole.
	Deferred bool `json:"deferred,omitempty"`
	// Noop says a deferred push may close with the NOOP offer.
	Noop        bool         `json:"noop,omitempty"`
	Attribution *Attribution `json:"attribution,omitempty"`
}

// NewSessionPlaceholder marks the to-be-created session's slot in a staged
// LinkRequest on POST /sessions (either endpoint). Never persisted — the
// Router substitutes the real ID before the link reaches the Data Service.
// The `$` prefix cannot collide with real `ses_*` IDs.
const NewSessionPlaceholder = "$new"

// LinkRequest is POST /links and PUT /links/{id}'s body: either an explicit
// grant set, or a preset the server resolves server-side. Never persisted
// as-is — Link.Grants is the only stored shape.
type LinkRequest struct {
	GrantingSessionID string      `json:"granting_session_id"`
	SubjectSessionID  string      `json:"subject_session_id"`
	Grants            []Grant     `json:"grants,omitempty"`
	Preset            LinkPreset  `json:"preset,omitempty"`
	Cadence           LinkCadence `json:"cadence,omitempty"`
}
