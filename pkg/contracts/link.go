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
	// completed turn of the subject's agent. Default.
	CadencePerTurn LinkCadence = "per_turn"
	// CadenceManualOnly pushes nothing; the monitor's user pulls via the
	// subject's existing transcript/summary reads instead.
	CadenceManualOnly LinkCadence = "manual_only"
)

// LinkCadences is the full registry, for validation and enumeration.
var LinkCadences = []LinkCadence{CadencePerTurn, CadenceManualOnly}

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
)

// OrchestrateStatuses is the full registry, for validation and enumeration.
var OrchestrateStatuses = []OrchestrateStatus{OrchestratePending, OrchestrateActive, OrchestrateUnsupported}

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
	CreatedAt         time.Time         `json:"created_at"`
	UpdatedAt         time.Time         `json:"updated_at"`
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
