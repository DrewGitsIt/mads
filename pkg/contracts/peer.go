package contracts

import (
	"net/url"
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
	// Addresses are where this node looks for the device: a short list of
	// candidates (docs/MESH-NETWORK-DESIGN.md F3), at most
	// MaxPeerAddresses. A row written before them has none and reads as
	// one legacy candidate made from MeshURL (Candidates); nothing is
	// rewritten until the row next changes. MeshURL stays, kept equal to
	// LastGood (or the first address) by the Data Service, so an older
	// build reading the row still finds a working address.
	Addresses []MeshAddress `json:"addresses,omitempty"`
	// LastGood is the address that last worked.
	LastGood *PeerPath `json:"last_good,omitempty"`
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

// MaxPeerAddresses bounds a row's address list.
const MaxPeerAddresses = 8

// NetAddress is one address another device may try for a device
// (meshnet.Candidate's wire form): codes, hellos, pairing requests and
// introductions carry lists of these. An unknown Via is skipped, never
// fatal.
type NetAddress struct {
	// Via is the adapter: "direct" (TCP to Addr) is the only one so far.
	Via string `json:"via"`
	// Addr is host:port.
	Addr string `json:"addr"`
	// Kind is overlay, lan or public; empty when not known.
	Kind  string `json:"kind,omitempty"`
	Label string `json:"label,omitempty"`
	// Network is the host's id for the network it's on ("tailscale").
	Network string `json:"network,omitempty"`
}

// MeshAddress is one of a row's addresses, with where it came from.
type MeshAddress struct {
	NetAddress
	// Source is code, hello, user, joiner, introduced, discovered or
	// legacy (made from MeshURL).
	Source   AddressSource `json:"source"`
	AddedAt  time.Time     `json:"added_at"`
	LastOKAt *time.Time    `json:"last_ok_at,omitempty"`
}

// AddressSource is where a row's address came from.
type AddressSource string

const (
	SourceCode       AddressSource = "code"
	SourceHello      AddressSource = "hello"
	SourceUser       AddressSource = "user"
	SourceJoiner     AddressSource = "joiner"
	SourceIntroduced AddressSource = "introduced"
	SourceDiscovered AddressSource = "discovered"
	SourceLegacy     AddressSource = "legacy"
)

// Valid reports whether s is a known source.
func (s AddressSource) Valid() bool {
	switch s {
	case SourceCode, SourceHello, SourceUser, SourceJoiner, SourceIntroduced, SourceDiscovered, SourceLegacy:
		return true
	}
	return false
}

// PeerPath is the address that last worked, when, and how fast.
type PeerPath struct {
	Addr string    `json:"addr"`
	At   time.Time `json:"at"`
	RTT  int64     `json:"rtt_ms,omitempty"`
}

// Candidates is p's address list, read-time migrated: a row with no
// Addresses reads as one legacy candidate from MeshURL (none without one).
func (p Peer) Candidates() []MeshAddress {
	if len(p.Addresses) > 0 {
		return p.Addresses
	}
	if host, ok := MeshURLHost(p.MeshURL); ok {
		return []MeshAddress{{NetAddress: NetAddress{Via: "direct", Addr: host}, Source: SourceLegacy, AddedAt: p.CreatedAt}}
	}
	return nil
}

// MeshURLHost is the host:port of a stored http://host:port mesh_url.
func MeshURLHost(meshURL string) (string, bool) {
	u, err := url.Parse(meshURL)
	if err != nil || u.Host == "" || u.Port() == "" {
		return "", false
	}
	return u.Host, true
}

// MeshURLOf is the stored mesh_url form of a host:port address.
func MeshURLOf(addr string) string { return "http://" + addr }

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
	// TailnetCapped is always false since the tailnet policy stopped
	// capping levels (docs/MESH-NETWORK-DESIGN.md); kept one release.
	Granted       GrantLevel `json:"granted,omitempty"`
	TailnetCapped bool       `json:"tailnet_capped,omitempty"`
	// TailscaleKeyExpiry is when the device's Tailscale key expires, as its
	// hello last said (nil: off, or not on Tailscale): the early warning,
	// and after it, "Probably signed out of Tailscale".
	TailscaleKeyExpiry *time.Time `json:"tailscale_key_expiry,omitempty"`
	// PairedThereAt is the paired_at of the row's pairing that the device
	// was seen to have (a hello answered while it stood). R4's automatic
	// removal needs it equal to the row's current paired_at, so a pairing
	// the other device never confirmed (an introduction, a discovered
	// pairing whose joiner hasn't confirmed its check code yet, or a
	// re-pairing since) is never undone (docs/MESH-NETWORK-DESIGN.md F6).
	PairedThereAt *time.Time `json:"paired_there_at,omitempty"`
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
	// PairDiscovered: found while the host's setup window was open, and
	// approved on the host with the check code (or Tailscale vouching for
	// both ends) (docs/MESH-NETWORK-DESIGN.md U4).
	PairDiscovered PairMethod = "discovered"
)

// Valid reports whether m is a known method.
func (m PairMethod) Valid() bool {
	switch m {
	case PairAccept, PairCeremony, PairCode, PairIntroduction, PairDiscovered:
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
	// By says who removed a device, on device.unpaired, when it wasn't
	// this host's user (docs/MESH-NETWORK-DESIGN.md F6): "itself" (it
	// asked to be forgotten, /mesh/forget) or "reconcile" (it no longer
	// had this device paired, R4). A re-pairing in place is device.paired
	// with Repair set.
	By     string `json:"by,omitempty"`
	Repair bool   `json:"repair,omitempty"`
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
	// PeerNoSharedNetwork: none of the device's addresses is on a network
	// this device is on now ("Not reachable from this network"). Nothing
	// is dialled.
	PeerNoSharedNetwork PeerReachability = "no_shared_network"
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
	// Path is the address in use now (the Status tooltip: "Connected via
	// Tailscale · 38 ms"). Nil when not connected.
	Path *PathStatus `json:"path,omitempty"`
	// Addresses is the row's address list, each with its live state (only
	// under Troubleshoot connection). It replaces the row's own
	// addresses in this view.
	Addresses []AddressStatus `json:"addresses,omitempty"`
	// Diagnosis refines the state for the UI: "tailscale_signed_out" (it
	// stopped answering over Tailscale after its key's last reported
	// expiry), "tailscale_key_expiring" (connected, expiring within 7 days).
	Diagnosis string `json:"diagnosis,omitempty"`
}

// PathStatus is the address a connection uses.
type PathStatus struct {
	Addr  string    `json:"addr"`
	Kind  string    `json:"kind,omitempty"`
	Label string    `json:"label,omitempty"`
	RTT   int64     `json:"rtt_ms,omitempty"`
	Since time.Time `json:"since"`
}

// AddressStatus is one address with its live state: in_use, works,
// not_on_network (not on a network this device is on now), not_answering,
// other_device (another device answered: the pin didn't match), refused
// (the dial policy), untried.
type AddressStatus struct {
	MeshAddress
	State string `json:"state"`
	Error string `json:"error,omitempty"`
}

// Address states (AddressStatus.State).
const (
	AddrInUse        = "in_use"
	AddrWorks        = "works"
	AddrNotOnNetwork = "not_on_network"
	AddrNotAnswering = "not_answering"
	AddrOtherDevice  = "other_device"
	AddrRefused      = "refused"
	AddrUntried      = "untried"
)

// Diagnoses (PeerStatus.Diagnosis).
const (
	DiagnosisTailscaleSignedOut   = "tailscale_signed_out"
	DiagnosisTailscaleKeyExpiring = "tailscale_key_expiring"
)

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
	// PairingThere is whether the device has this one paired, as this
	// device last checked (docs/MESH-NETWORK-DESIGN.md F6, R3): paired,
	// not_paired, unknown (with PairingThereReason) or other_key (a
	// different key answered). Absent until checked.
	PairingThere       string     `json:"pairing_there,omitempty"`
	PairingThereReason string     `json:"pairing_there_reason,omitempty"`
	PairingCheckedAt   *time.Time `json:"pairing_checked_at,omitempty"`
}

// Pairing-there states (DeviceStatus.PairingThere).
const (
	PairingTherePaired    = "paired"
	PairingThereNotPaired = "not_paired"
	PairingThereUnknown   = "unknown"
	PairingThereOtherKey  = "other_key"
)

// PeerReachabilityChange is peer.reachability_changed's payload.
type PeerReachabilityChange struct {
	PeerID    string           `json:"peer_id"`
	Peer      string           `json:"peer"`
	From      PeerReachability `json:"from"`
	To        PeerReachability `json:"to"`
	Reason    string           `json:"reason,omitempty"`
	RTTMillis int64            `json:"rtt_ms,omitempty"`
	// Via is the path it's reached by now, when connected; a change of
	// path while connected is logged too ("Now reached via Tailscale (was
	// home network)", From and To both connected, ViaFrom the old one).
	Via     *NetAddress `json:"via,omitempty"`
	ViaFrom *NetAddress `json:"via_from,omitempty"`
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
