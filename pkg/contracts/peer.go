package contracts

import (
	"slices"
	"time"
)

// Peer is one device this node knows in mesh mode: a row of "Paired
// devices" (docs/MESH-AUTH-DESIGN.md D9). A row has up to two sides:
//
//   - Viewed: this node views the device, whose sessions show up here when
//     the session list is set to All devices (docs/MESH-MODE-DESIGN.md D3).
//     MeshURL is where it is read from.
//   - Paired: the device's key is pinned here (Fingerprint), with what it
//     may do on this host (Grant) and how it came to be paired (Pairing).
//
// A row may be either or both: a phone is paired but never viewed. Rows
// written before pairing existed carry neither field and read as viewed,
// unpaired — exactly what they meant then. The paired side is enforced:
// the Mesh Service admits a device only by its pinned key, at its grant
// (A4/A5), and this node dials a viewed device only with its Fingerprint
// pinned.
type Peer struct {
	ID string `json:"id"`
	// Name is how the UI labels the device's sessions ("pop-os"). Unique;
	// it is also the {peer} segment of the Router's /peers/{peer}/... proxy.
	Name string `json:"name"`
	// DNSName is the device's MagicDNS name, for display and diagnostics.
	DNSName string `json:"dns_name,omitempty"`
	// MeshURL is the device's Mesh Service, e.g. http://pop-os.tailnet.ts.net:17380.
	// Required when the device is viewed; a paired-only device may have none.
	// Stored as http://host:port (the form the Data Service validates, and
	// every stored row's), but always dialled with TLS: the scheme is
	// ignored.
	MeshURL string `json:"mesh_url"`
	// Hidden keeps the peer registered (and its reachability tracked) but
	// leaves its sessions out of the All devices list.
	Hidden bool `json:"hidden,omitempty"`
	// Viewed is whether this node views the device; nil (every row from
	// before pairing) reads as true. Use IsViewed.
	Viewed *bool `json:"viewed,omitempty"`

	// Fingerprint is the paired device's ID, the fingerprint of its device
	// key (pkg/devicekey: canonical, 52 base32 characters). Empty: not
	// paired. Unique among peers.
	Fingerprint string `json:"fingerprint,omitempty"`
	// PublicKey is the device's Ed25519 public key (base64), when known.
	// Optional: the fingerprint alone is what's pinned (D2 checks a peer's
	// certificate key against it), so pairing by fingerprint
	// (`mads pair --accept`) leaves it empty. When set it must match
	// Fingerprint.
	PublicKey string `json:"public_key,omitempty"`
	// Grant is what the paired device may do on this host (D3/D4). Nil on
	// a paired device is "none"; set only on a paired one.
	Grant *PeerGrant `json:"grant,omitempty"`
	// Pairing records how the device was paired. Written by the Data
	// Service when Fingerprint is first set or changes; a caller's value
	// is ignored except for How.
	Pairing *PeerPairing `json:"pairing,omitempty"`
	// Remote is what the device itself last said, about itself and about
	// this node: its platform, whether it hosts mads sessions, and what it
	// lets this node do there. Informational, never a grant: written by
	// this node's Router (from the device's /mesh/hello) and at pairing.
	Remote *PeerRemote `json:"remote,omitempty"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// IsViewed reports whether this node views p (Viewed, defaulting to true).
func (p Peer) IsViewed() bool { return p.Viewed == nil || *p.Viewed }

// PairedForAccess reports whether p is paired to be let in here (it has a
// grant, none included), as opposed to a device this node only views. A
// paired, unviewed row with no grant is one paired at none before grants
// carried levels: it reads as a grant at none.
func (p Peer) PairedForAccess() bool {
	return p.Grant != nil || (p.IsPaired() && !p.IsViewed())
}

// IsPaired reports whether p's device key is pinned here.
func (p Peer) IsPaired() bool { return p.Fingerprint != "" }

// Level is p's inbound grant level, GrantNone when it has none.
func (p Peer) Level() GrantLevel {
	if p.Grant == nil {
		return GrantNone
	}
	return p.Grant.Level
}

// GrantLevel is what a paired device may do on this host
// (docs/MESH-AUTH-DESIGN.md D3). Each level includes the ones before it.
type GrantLevel string

const (
	// GrantNone: paired, but may do nothing here. Either a nil Grant (no
	// ceilings), or a stored grant with Level "" that keeps its ceilings
	// for when it's raised again.
	GrantNone GrantLevel = ""
	// GrantView reads sessions, transcripts, live output and the session
	// list. The default for a new pairing.
	GrantView GrantLevel = "view"
	// GrantChat is view plus sending to, interrupting and answering
	// permission prompts for existing sessions at their existing trust (Q4).
	GrantChat GrantLevel = "chat"
	// GrantControl is everything a user does with sessions.
	GrantControl GrantLevel = "control"
	// GrantAdmin is control plus pairing, grants and introductions on this
	// host. Granted only on the host itself (Q2): the Data Service accepts
	// it only from the install token's holder, never through the mesh.
	GrantAdmin GrantLevel = "admin"
)

// GrantLevels is the registry, least to most access.
var GrantLevels = []GrantLevel{GrantView, GrantChat, GrantControl, GrantAdmin}

// Valid reports whether l is a registered (storable) level.
func (l GrantLevel) Valid() bool { return slices.Contains(GrantLevels, l) }

// PeerRemote is what a device reports, as this node last heard it.
type PeerRemote struct {
	// Platform is the device's OS as it reports it (runtime.GOOS: darwin,
	// linux, windows; ios or android for a phone). The UI derives the
	// device's kind from it (computer, phone, unknown); empty is unknown.
	Platform string `json:"platform,omitempty"`
	// HostsSessions is whether the device hosts mads sessions (runs a Mesh
	// Service other devices can view). False for a phone or a display.
	// Nil: not known.
	HostsSessions *bool `json:"hosts_sessions,omitempty"`
	// Level is what the device lets this node do there ("lets this
	// device ..."), from its /mesh/hello's access; "" is none. Meaningful
	// only with AsOf set.
	Level GrantLevel `json:"level,omitempty"`
	// AsOf is when Level was read. Nil: never reached ("Not reached
	// yet"). On the stored row it's when the answer last changed (rows
	// are written on change); GET /peers and GET /devices show when it
	// was last read.
	AsOf *time.Time `json:"as_of,omitempty"`
	// Granted is the device's grant to this node there before its tailnet
	// policy caps it; TailnetCapped says the policy holds Level below it
	// (e.g. admin granted, the policy's access ["control"]: acts as
	// control). The UI explains it; the fix is the tailnet policy.
	Granted       GrantLevel `json:"granted,omitempty"`
	TailnetCapped bool       `json:"tailnet_capped,omitempty"`
}

// MaxPlatformLen bounds a reported platform.
const MaxPlatformLen = 32

// ValidPlatform reports whether p is a platform name a device may report:
// lower-case letters, digits, '_' and '-', at most MaxPlatformLen.
func ValidPlatform(p string) bool {
	if len(p) > MaxPlatformLen {
		return false
	}
	for _, c := range p {
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '_' && c != '-' {
			return false
		}
	}
	return true
}

// PeerGrant is a paired device's inbound grant: a level plus ceilings.
// Level may be GrantNone (""): a device paired for access here that may do
// nothing for now, keeping its ceilings for when it's raised again. A nil
// Grant is none too, with no ceilings: a row with no grant, for a device
// this node only views or one paired before grants carried levels.
type PeerGrant struct {
	Level    GrantLevel   `json:"level"`
	Ceilings PeerCeilings `json:"ceilings,omitzero"`
}

// PeerCeilings are limits that hold whatever the level (D4). The zero
// value is the documented default for each: C1 allowed (Q1), C2 off, C3
// none.
type PeerCeilings struct {
	// FullTrustChat (C1) is what a chat device's message to a full-trust
	// session does. Empty reads as allowed.
	FullTrustChat FullTrustChat `json:"full_trust_chat,omitempty"`
	// NoShell (C2) keeps a control device out of the shell.
	NoShell bool `json:"no_shell,omitempty"`
	// MaxTrust (C3) is the highest trust a device may start or raise a
	// session to. Empty: no ceiling.
	MaxTrust SessionTrust `json:"max_trust,omitempty"`
}

// Looser reports whether c lets a device do anything than doesn't: C1
// from refused to held to allowed, C2 off, or C3 raised or removed.
func (c PeerCeilings) Looser(than PeerCeilings) bool {
	return c1Rank(c.FullTrustChat) > c1Rank(than.FullTrustChat) ||
		(than.NoShell && !c.NoShell) ||
		c3Rank(c.MaxTrust) < c3Rank(than.MaxTrust)
}

// TighterCeilings is the tighter of a and b, each ceiling on its own.
func TighterCeilings(a, b PeerCeilings) PeerCeilings {
	out := a
	if c1Rank(b.FullTrustChat) < c1Rank(a.FullTrustChat) {
		out.FullTrustChat = b.FullTrustChat
	}
	out.NoShell = a.NoShell || b.NoShell
	if c3Rank(b.MaxTrust) > c3Rank(a.MaxTrust) {
		out.MaxTrust = b.MaxTrust
	}
	return out
}

// c1Rank orders C1 settings, tightest (refused) first.
func c1Rank(c FullTrustChat) int {
	switch c {
	case FullTrustChatRefused:
		return 0
	case FullTrustChatHeld:
		return 1
	}
	return 2 // allowed, or empty
}

// c3Rank orders C3 ceilings, loosest (none) first: a higher index in
// SessionTrusts is less permissive.
func c3Rank(t SessionTrust) int {
	if t == "" {
		return -1
	}
	return slices.Index(SessionTrusts, t)
}

// FullTrustChat is ceiling C1's setting.
type FullTrustChat string

const (
	FullTrustChatAllowed FullTrustChat = "allowed"
	FullTrustChatRefused FullTrustChat = "refused"
	// FullTrustChatHeld holds the message until a control device approves.
	FullTrustChatHeld FullTrustChat = "held"
)

// Valid reports whether c is empty (the default, allowed) or a known setting.
func (c FullTrustChat) Valid() bool {
	switch c {
	case "", FullTrustChatAllowed, FullTrustChatRefused, FullTrustChatHeld:
		return true
	}
	return false
}

// PairMethod is how a device came to be paired (D5, D6).
type PairMethod string

const (
	// PairAccept: pinned by fingerprint on the host, `mads pair --accept`
	// (Q2's scripted, authorized_keys-like path).
	PairAccept PairMethod = "accept"
	// PairCeremony: the QR / short-code ceremony as first drafted; kept
	// valid for rows that carry it.
	PairCeremony PairMethod = "ceremony"
	// PairCode: the pairing code, approved on the host (D6).
	PairCode PairMethod = "code"
	// PairIntroduction: introduced by an admin device (A6).
	PairIntroduction PairMethod = "introduction"
)

// Valid reports whether m is a known method.
func (m PairMethod) Valid() bool {
	switch m {
	case PairAccept, PairCeremony, PairCode, PairIntroduction:
		return true
	}
	return false
}

// PeerPairing is a pairing's record.
type PeerPairing struct {
	PairedAt time.Time `json:"paired_at"`
	// PairedBy is the actor that pinned the key.
	PairedBy string     `json:"paired_by"`
	How      PairMethod `json:"how"`
}

// DeviceChange is the payload of device.paired, device.unpaired and
// device.grant_changed (D8): the audit trail of who may reach this host.
// Level and PreviousLevel are "" for none.
type DeviceChange struct {
	PeerID        string     `json:"peer_id"`
	Name          string     `json:"name"`
	Fingerprint   string     `json:"fingerprint"`
	Level         GrantLevel `json:"level,omitempty"`
	PreviousLevel GrantLevel `json:"previous_level,omitempty"`
	How           PairMethod `json:"how,omitempty"`
}

// MeshRelayHeader marks a request relayed for a mesh peer. The Mesh
// Service sets it (to the calling device's name here, its paired row's
// Name, not a tailnet node name) on everything it forwards to its Router,
// and the Router's Data Service client carries it on every write made for
// such a request (dataclient.WithMeshRelay), so the Data Service can tell
// a peer's write from the host user's own. Its presence is the signal,
// even with an empty value.
const MeshRelayHeader = "X-Mads-Mesh-Peer"

// MeshLevelHeader is the effective grant level (GrantLevel) of the paired
// device a mesh-relayed request came from. The Mesh Service sets it on
// everything it forwards, overwriting whatever the caller sent; the Router
// reads it only on a request that also carries MeshRelayHeader (e.g. to
// leave the device registry out of a non-admin device's GET /events).
const MeshLevelHeader = "X-Mads-Mesh-Level"

// MeshDeviceHeader is the full fingerprint of the paired device a
// mesh-relayed request came from, set by the Mesh Service like
// MeshLevelHeader. The Router carries it (with MeshRelayHeader) on every
// Data Service write made for the request, so the peer registry can hold
// an admin device's changes to what its own grant allows
// (docs/MESH-AUTH-DESIGN.md D5, Q2).
const MeshDeviceHeader = "X-Mads-Mesh-Device"

// DeviceRefusal is device.refused's payload (docs/MESH-AUTH-DESIGN.md D8):
// a device the Mesh Service turned away — a key that isn't paired here, or
// a request above its level or past a ceiling. Refusals are debounced per
// device, code and route, so Repeats counts the same refusals folded into
// this one since the last that was logged (and Dropped, refusals of any
// kind not logged at all because the log's overall rate was exceeded).
type DeviceRefusal struct {
	Fingerprint string     `json:"fingerprint"`
	PeerID      string     `json:"peer_id,omitempty"`
	Name        string     `json:"name,omitempty"`
	Level       GrantLevel `json:"level,omitempty"`
	// Code is the refusal's error code, e.g. unpaired_device, no_access,
	// no_shell, trust_ceiling, full_trust_chat_refused.
	Code    string `json:"code"`
	Method  string `json:"method,omitempty"`
	Route   string `json:"route,omitempty"`
	Remote  string `json:"remote,omitempty"`
	Repeats int    `json:"repeats,omitempty"`
	Dropped int    `json:"dropped,omitempty"`
}

// PeerReachability is a peer's live state as the Router last saw it. It is
// never stored on the Peer row; transitions are logged as
// peer.reachability_changed.
type PeerReachability string

const (
	// PeerUnknown: not probed yet since this Router started.
	PeerUnknown PeerReachability = "unknown"
	// PeerConnected: /mesh/hello answered and grants at least "view".
	PeerConnected PeerReachability = "connected"
	// PeerUnreachable: no answer (offline, asleep, Mesh Service not running).
	PeerUnreachable PeerReachability = "unreachable"
	// PeerDenied: it answered, but grants this node no "view" there, or
	// refused it outright (it hasn't paired this device, the tailnet check
	// failed), or the device answering isn't the one whose key is pinned.
	PeerDenied PeerReachability = "denied"
	// PeerVersionMismatch: it answered with a mesh_api this build doesn't speak.
	PeerVersionMismatch PeerReachability = "version_mismatch"
	// PeerUnpaired: this node has no fingerprint pinned for the device, so
	// it can't verify the device's key and never connects to it
	// (docs/MESH-AUTH-DESIGN.md D2: no plaintext, no unverified TLS).
	PeerUnpaired PeerReachability = "unpaired"
)

// PeerStatus is a Peer plus its live state: GET /peers on the Router.
type PeerStatus struct {
	Peer
	Reachability PeerReachability `json:"reachability"`
	Reason       string           `json:"reason,omitempty"`
	RTTMillis    int64            `json:"rtt_ms,omitempty"`
	MadsCommit   string           `json:"mads_commit,omitempty"`
	Access       []string         `json:"access,omitempty"`
	CheckedAt    *time.Time       `json:"checked_at,omitempty"`
}

// DeviceStatus is one row of Settings → Paired devices: GET /devices on the
// Router. Every row is listed, viewed or not; Reachability is set only for
// a device this node dials (viewed, or paired with a mesh address), and is
// empty for one it never does.
type DeviceStatus struct {
	PeerStatus
	// LastUsedAt is when the device last did something here that the
	// event log records (an event whose actor is the device). Nil: never.
	// Reads aren't recorded, so a device that only views reads as never.
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
}

// PeerReachabilityChange is peer.reachability_changed's payload.
type PeerReachabilityChange struct {
	PeerID    string           `json:"peer_id"`
	Peer      string           `json:"peer"`
	From      PeerReachability `json:"from"`
	To        PeerReachability `json:"to"`
	Reason    string           `json:"reason,omitempty"`
	RTTMillis int64            `json:"rtt_ms,omitempty"`
}

// SessionHost tags a session row with the node it lives on, on the Router's
// GET /sessions?scope=all (docs/MESH-MODE-DESIGN.md D4). Absent everywhere
// else, so single-device callers see exactly what they always did.
type SessionHost struct {
	Name      string `json:"name"`
	Self      bool   `json:"self"`
	PeerID    string `json:"peer_id,omitempty"`
	Reachable bool   `json:"reachable"`
}
