package contracts

import "time"

// Pairing by code (docs/MESH-AUTH-DESIGN.md D6). The host makes a
// single-use code; a joiner presents its secret to the host's Mesh Service
// over mutual TLS; the host approves or declines the request that makes.
// The Data Service keeps codes and requests in memory only, and of a code
// only the SHA-256 of its secret: the secret itself is never stored, in
// an event or a log.

// PairingCodeTTL is how long a pairing code works; PairRequestTTL how long
// a request it made waits for the host's answer.
const (
	PairingCodeTTL = 5 * time.Minute
	PairRequestTTL = 5 * time.Minute
	// MaxPairingCodes is how many codes may be outstanding on a host at
	// once; making another retires the oldest.
	MaxPairingCodes = 5
)

// PairingCode is a code as the Data Service knows it: never the secret.
// Also device.code_created's payload.
type PairingCode struct {
	ID        string    `json:"id"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
}

// PairingCodeCreate is POST /pairing/codes on the Data Service: the
// SHA-256 of the code's secret, hex.
type PairingCodeCreate struct {
	SecretHash string `json:"secret_hash"`
}

// PairRedeem is POST /pairing/redeem on the Data Service, sent by the
// Mesh Service for a device that presented a code's secret: what the
// device proved (its key's fingerprint) and what it says about itself.
type PairRedeem struct {
	SecretHash    string `json:"secret_hash"`
	Fingerprint   string `json:"fingerprint"`
	Name          string `json:"name,omitempty"`
	Platform      string `json:"platform,omitempty"`
	HostsSessions bool   `json:"hosts_sessions,omitempty"`
	MeshURL       string `json:"mesh_url,omitempty"`
	// Remote is the address the device connected from, for the approval
	// prompt.
	Remote string `json:"remote,omitempty"`
	// TailnetNode is the device's tailnet node name as tailscaled's WhoIs
	// verified it (empty when the tailnet isn't required): unlike Name, not
	// the device's own claim.
	TailnetNode string `json:"tailnet_node,omitempty"`
	// Addresses are the joiner's own addresses (was one mesh_url): where
	// this host could reach it back. The Mesh Service checked each one's
	// shape (S3).
	Addresses []NetAddress `json:"addresses,omitempty"`
	// Network is the id of the mesh network the request arrived on
	// ("tailscale", "lan:..."), and NetworkLabel its label.
	Network      string `json:"network,omitempty"`
	NetworkLabel string `json:"network_label,omitempty"`
}

// PairDiscoveredRequest is POST /pairing/discovered on the Data Service,
// sent by the Mesh Service for a device that found this one while its
// setup window was open and asks to pair (docs/MESH-NETWORK-DESIGN.md U4):
// no secret, so it's accepted only while the window is open, at most
// MaxDiscoveredPending at once, and the host's user compares CheckCode
// (unless Vouched) before approving.
type PairDiscoveredRequest struct {
	Fingerprint   string       `json:"fingerprint"`
	Name          string       `json:"name,omitempty"`
	Platform      string       `json:"platform,omitempty"`
	HostsSessions bool         `json:"hosts_sessions,omitempty"`
	Addresses     []NetAddress `json:"addresses,omitempty"`
	Remote        string       `json:"remote,omitempty"`
	TailnetNode   string       `json:"tailnet_node,omitempty"`
	Network       string       `json:"network,omitempty"`
	NetworkLabel  string       `json:"network_label,omitempty"`
	// CheckCode is the six digits both screens show.
	CheckCode string `json:"check_code"`
	// Vouched: the request came over Tailscale and both ends' WhoIs say
	// "same tailnet user, not tagged", so there's no number to compare.
	Vouched bool `json:"vouched,omitempty"`
}

// MaxDiscoveredPending is how many discovered pairing requests may wait
// at once.
const MaxDiscoveredPending = 3

// DiscoverableMax is how long a setup window stays open without renewal;
// DiscoverableAbsoluteMax how long renewals can keep one open since it was
// opened (round-1 review S5): past it, the window must be opened again.
const (
	DiscoverableMax         = 10 * time.Minute
	DiscoverableAbsoluteMax = 30 * time.Minute
)

// Discoverable is device.discoverable's payload, and PUT
// /devices/discoverable's body on the Data Service: this device's setup
// window is open until Until (zero: closed). Renew extends an open window
// (refused when it's closed); without it, the request opens a new one.
type Discoverable struct {
	Until time.Time `json:"until"`
	By    string    `json:"by,omitempty"`
	Renew bool      `json:"renew,omitempty"`
	// OpenedAt is when this window was first opened; ClosedAt when it was
	// closed early (zero: it ran out on its own).
	OpenedAt time.Time  `json:"opened_at,omitzero"`
	ClosedAt *time.Time `json:"closed_at,omitempty"`
	// Reason says why it closed early, when it wasn't its user:
	// "abandoned_pairings" (several pairings started and never finished).
	Reason string `json:"reason,omitempty"`
}

// PairingAbandoned is device.pairing_abandoned's payload.
type PairingAbandoned struct {
	Abandoned int      `json:"abandoned"`
	Started   int      `json:"started"`
	Sources   []string `json:"sources,omitempty"`
}

// End is when the window ended or will end.
func (d Discoverable) End() time.Time {
	if d.ClosedAt != nil {
		return *d.ClosedAt
	}
	return d.Until
}

// Open reports whether the window is open at now.
func (d Discoverable) Open(now time.Time) bool { return now.Before(d.Until) }

// DeviceDiscovery is device.discovered's payload (audit only): this
// device answered a discovery probe, or got a discovered pairing request.
// Debounced per address.
type DeviceDiscovery struct {
	Kind        string `json:"kind"` // probe | request
	Remote      string `json:"remote"`
	Fingerprint string `json:"fingerprint,omitempty"`
	Name        string `json:"name,omitempty"`
	Network     string `json:"network,omitempty"`
	Repeats     int    `json:"repeats,omitempty"`
}

// DeviceUncapped is device.level_uncapped's payload.
type DeviceUncapped struct {
	Fingerprint string     `json:"fingerprint"`
	PeerID      string     `json:"peer_id,omitempty"`
	Name        string     `json:"name,omitempty"`
	Level       GrantLevel `json:"level"`
	// Capability is the level the tailnet policy's mesh capability named
	// ("" none); HadCapability false: the policy didn't name the device at
	// all (it was refused before).
	Capability    GrantLevel `json:"capability,omitempty"`
	HadCapability bool       `json:"had_capability"`
}

// PairMode is how a pairing request was made.
type PairMode string

const (
	PairModeCode       PairMode = "code"
	PairModeDiscovered PairMode = "discovered"
)

// PairState is a pairing request's state.
type PairState string

const (
	PairPending  PairState = "pending"
	PairApproved PairState = "approved"
	PairDeclined PairState = "declined"
	PairExpired  PairState = "expired"
)

// PairRequest is a device waiting for (or given) this host's answer. Also
// the payload of device.pair_requested and device.pair_declined.
type PairRequest struct {
	ID            string `json:"id"`
	Fingerprint   string `json:"fingerprint"`
	Name          string `json:"name"`
	Platform      string `json:"platform,omitempty"`
	HostsSessions bool   `json:"hosts_sessions,omitempty"`
	MeshURL       string `json:"mesh_url,omitempty"`
	Remote        string `json:"remote,omitempty"`
	// TailnetNode is the WhoIs-verified node name (PairRedeem).
	TailnetNode string `json:"tailnet_node,omitempty"`
	// Mode is code or discovered ("" reads as code).
	Mode PairMode `json:"mode,omitempty"`
	// CheckCode is a discovered request's six digits, shown on both
	// screens; Vouched says Tailscale vouched for both ends instead.
	CheckCode string `json:"check_code,omitempty"`
	Vouched   bool   `json:"vouched,omitempty"`
	// VouchWithheld says why a request Tailscale vouched for still needs
	// its check code compared (round-1 review M3): "another_request" (more
	// than one discovered request is waiting: Tailscale says each is one of
	// this user's nodes, not which is the device in front of you) or
	// "name_collides".
	VouchWithheld string `json:"vouch_withheld,omitempty"`
	// JoinerMismatch: the joiner's user said the check code it showed
	// wasn't this host's, so the pairing was undone here (round-1 review).
	JoinerMismatch bool `json:"joiner_mismatch,omitempty"`
	// Addresses are the joiner's own addresses.
	Addresses []NetAddress `json:"addresses,omitempty"`
	// Network names the mesh network it arrived on.
	Network      string    `json:"network,omitempty"`
	NetworkLabel string    `json:"network_label,omitempty"`
	State        PairState `json:"state"`
	// NameCollides: a device already paired or viewed here has this name;
	// the name is the joiner's claim, so the prompt should say so.
	NameCollides bool `json:"name_collides,omitempty"`
	// Level is what the host granted, once approved ("" is none).
	Level GrantLevel `json:"level,omitempty"`
	// PeerID is the row approval pinned.
	PeerID string `json:"peer_id,omitempty"`
	// Repair is set when the joiner's key is already paired here
	// (docs/MESH-NETWORK-DESIGN.md F6, R2): its row and its level now.
	// Approval updates that row in place instead of pinning a new one.
	Repair    *PairRepair `json:"repair,omitempty"`
	CreatedAt time.Time   `json:"created_at"`
	ExpiresAt time.Time   `json:"expires_at"`
	DecidedAt *time.Time  `json:"decided_at,omitempty"`
}

// PairRepair is a re-pairing request's existing row: its id and the level
// it has here now ("" none).
type PairRepair struct {
	PeerID string     `json:"peer_id"`
	Level  GrantLevel `json:"level"`
}

// PairForget is POST /pairing/forget on the Data Service, sent by the
// Mesh Service for a paired device that asked to be forgotten
// (/mesh/forget, F6 R1): the fingerprint its TLS handshake proved.
type PairForget struct {
	Fingerprint string `json:"fingerprint"`
}

// PairDecision is POST /pairing/requests/{id}/approve: the level to grant
// ("none" or "" for none; never admin, Q2) and optionally the name to pin
// the device under (default: the name it gave).
type PairDecision struct {
	Level GrantLevel `json:"level"`
	Name  string     `json:"name,omitempty"`
	// For a discovered request (round-2 review R5): CheckCodeConfirmed,
	// this host's user compared the check code with the joiner's screen;
	// ShownVouched, the prompt said Tailscale vouched (no number). An
	// approval stands only if the code was confirmed, or the request is
	// still vouched for as shown.
	CheckCodeConfirmed bool `json:"check_code_confirmed,omitempty"`
	ShownVouched       bool `json:"shown_vouched,omitempty"`
}

// MeshListener is mesh.listening's payload: where this node's Mesh
// Service listens and what it reports about this host.
type MeshListener struct {
	Node           string `json:"node"`
	DNSName        string `json:"dns_name,omitempty"`
	MeshURL        string `json:"mesh_url"`
	Fingerprint    string `json:"fingerprint"`
	Platform       string `json:"platform,omitempty"`
	HostsSessions  bool   `json:"hosts_sessions"`
	RequireTailnet bool   `json:"require_tailnet"`
	// RequireTailnetFlag: -require-tailnet was given on the Mesh Service's
	// command line, which overrides the stored setting (MeshSettings) for
	// as long as it runs that way.
	RequireTailnetFlag bool `json:"require_tailnet_flag,omitempty"`
	// Networks is every network this host detected, and whether (and
	// where) it listens on each (docs/MESH-NETWORK-DESIGN.md, "What the
	// Mesh Service reports"). Absent from an older Mesh Service's.
	Networks []ListeningNetwork `json:"networks,omitempty"`
	// Addresses is where other devices reach this host now, in its
	// preferred order: what hellos and pairing codes carry. Empty with
	// Networks set: listening nowhere (no mesh network chosen yet, or not
	// on it now).
	Addresses []NetAddress `json:"addresses,omitempty"`
	// Port is the mesh port.
	Port int `json:"port,omitempty"`
	// TailnetCheck is the Tailscale network's identity check as applied
	// now; TailnetCheckStatus is on, off, or unavailable (on, but
	// tailscaled can't be asked: every connection on the Tailscale
	// network is refused).
	TailnetCheckStatus string `json:"tailnet_check_status,omitempty"`
	// Tailscale is tailscaled as the Mesh Service last read it.
	Tailscale *TailscaleState `json:"tailscale,omitempty"`
	// At is when this report was made.
	At time.Time `json:"at,omitzero"`
}

// ListeningNetwork is one network in a mesh.listening report.
type ListeningNetwork struct {
	ID    string   `json:"id"`
	Kind  string   `json:"kind"`
	Label string   `json:"label"`
	Iface string   `json:"iface,omitempty"`
	Addrs []string `json:"addrs,omitempty"`
	// Chosen: the user chose it as a mesh network.
	Chosen bool `json:"chosen,omitempty"`
	// Listening: bound now; Error says why not, for a chosen one.
	Listening bool   `json:"listening,omitempty"`
	Error     string `json:"error,omitempty"`
	// ChangedFrom is the addresses it had before, when they just changed.
	ChangedFrom []string `json:"changed_from,omitempty"`
}

// TailscaleState is tailscaled's state as a mads service last read it.
type TailscaleState struct {
	// Present: tailscaled's LocalAPI answered.
	Present bool `json:"present"`
	// Endpoint names which tailscaled answered: socket,
	// macos-standalone, macos-app-store, macos-cli-shared.
	Endpoint     string `json:"endpoint,omitempty"`
	BackendState string `json:"backend_state,omitempty"`
	// NeedsLogin: tailscaled needs this device to sign in again.
	NeedsLogin bool `json:"needs_login,omitempty"`
	// AuthURL is the pending sign-in link, when there is one.
	AuthURL  string `json:"auth_url,omitempty"`
	DNSName  string `json:"dns_name,omitempty"`
	HostName string `json:"host_name,omitempty"`
	Tailnet  string `json:"tailnet,omitempty"`
	// KeyExpiry is this device's key expiry; nil with KeyExpiryOff.
	KeyExpiry    *time.Time `json:"key_expiry,omitempty"`
	KeyExpiryOff bool       `json:"key_expiry_off,omitempty"`
	// Error says why it couldn't be read.
	Error string `json:"error,omitempty"`
}

// MeshSettings are this host's mesh settings, set by its own user in
// Settings → Paired devices (GET/PUT /devices/settings on the Data
// Service, the mesh_settings row; device.settings_changed carries them,
// MeshSettingsChange, written in the same transaction). The Mesh Service
// follows them live and never writes them.
type MeshSettings struct {
	// Networks are this device's network choices
	// (docs/MESH-NETWORK-DESIGN.md U2): the ones with Listen set are its
	// mesh networks, where it accepts connections. Empty: none chosen yet
	// (a fresh install listens nowhere). The MVP chooses exactly one; the
	// list leaves room for more without a migration.
	Networks []NetworkChoice `json:"networks"`
	// Port is the mesh port on every network (0: 17380).
	Port int `json:"port,omitempty"`
	// Public is "Also reachable from the internet" (Advanced options):
	// listen on this device's public addresses too. Off by default.
	Public bool `json:"public,omitempty"`
	// RequireTailnet is the setting before mesh networks (the Tailscale
	// network's check). Written beside Networks for a rollback to an
	// older build, which reads only this; read by migration 0038, then
	// unused.
	RequireTailnet *bool `json:"require_tailnet,omitempty"`
}

// NetworkChoice is one network the user decided about.
type NetworkChoice struct {
	// ID is meshnet.Network.ID: "tailscale", "lan:<subnet>/<gateway>".
	ID string `json:"id"`
	// Name is the Wi-Fi name, or one the user gave.
	Name string `json:"name,omitempty"`
	// Listen: chosen as a mesh network.
	Listen bool `json:"listen"`
	// TailnetCheck is the Tailscale identity check (only on the Tailscale
	// network): refuse-only, same tailnet user and untagged.
	TailnetCheck bool `json:"tailnet_check,omitempty"`
	// GatewayHW is the LAN gateway's hardware address when chosen, where
	// it was known (Q3): a different router with the same addresses isn't
	// the same network.
	GatewayHW string `json:"gateway_hw,omitempty"`
	// SSID is the Wi-Fi network's name when chosen, where the OS gave it:
	// another Wi-Fi with the same addresses isn't the network chosen.
	SSID      string    `json:"ssid,omitempty"`
	DecidedAt time.Time `json:"decided_at"`
}

// DefaultMeshPort is the mesh port when MeshSettings.Port is 0.
const DefaultMeshPort = 17380

// MeshPort is s's port.
func (s MeshSettings) MeshPort() int {
	if s.Port <= 0 {
		return DefaultMeshPort
	}
	return s.Port
}

// Chosen is the networks s listens on.
func (s MeshSettings) Chosen() []NetworkChoice {
	var out []NetworkChoice
	for _, n := range s.Networks {
		if n.Listen {
			out = append(out, n)
		}
	}
	return out
}

// Choice is s's choice for network id, if any.
func (s MeshSettings) Choice(id string) (NetworkChoice, bool) {
	for _, n := range s.Networks {
		if n.ID == id {
			return n, true
		}
	}
	return NetworkChoice{}, false
}

// TailnetCheck reports whether the Tailscale network's identity check is
// on: it's chosen and its check is set.
func (s MeshSettings) TailnetCheck() bool {
	c, ok := s.Choice("tailscale")
	return ok && c.Listen && c.TailnetCheck
}

// MaxNetworkChoices bounds MeshSettings.Networks.
const MaxNetworkChoices = 16

// MeshSettingsChange is device.settings_changed's payload: the settings
// as set, and who set them. The envelope's actor is always the store's
// fixed one ("settings"), never the caller's, so the event can't be
// confused with one written through POST /events (which refuses it).
type MeshSettingsChange struct {
	MeshSettings
	By string `json:"by,omitempty"`
}

// DeviceMessage is session.device_message's payload.
type DeviceMessage struct {
	// Queued: the session was mid-turn, and the message waits behind it.
	Queued bool `json:"queued,omitempty"`
}

// DeviceLastUsed is one device actor's newest event: GET /devices/last-used
// on the Data Service.
type DeviceLastUsed struct {
	Actor string    `json:"actor"`
	Seq   int64     `json:"seq"`
	At    time.Time `json:"at"`
}
