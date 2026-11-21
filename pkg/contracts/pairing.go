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
}

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
	TailnetNode string    `json:"tailnet_node,omitempty"`
	State       PairState `json:"state"`
	// NameCollides: a device already paired or viewed here has this name;
	// the name is the joiner's claim, so the prompt should say so.
	NameCollides bool `json:"name_collides,omitempty"`
	// Level is what the host granted, once approved ("" is none).
	Level GrantLevel `json:"level,omitempty"`
	// PeerID is the row approval pinned.
	PeerID    string     `json:"peer_id,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
	ExpiresAt time.Time  `json:"expires_at"`
	DecidedAt *time.Time `json:"decided_at,omitempty"`
}

// PairDecision is POST /pairing/requests/{id}/approve: the level to grant
// ("none" or "" for none; never admin, Q2) and optionally the name to pin
// the device under (default: the name it gave).
type PairDecision struct {
	Level GrantLevel `json:"level"`
	Name  string     `json:"name,omitempty"`
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
}

// MeshSettings are this host's mesh settings, set by its own user in
// Settings → Paired devices (GET/PUT /devices/settings on the Data
// Service; device.settings_changed carries them, MeshSettingsChange). The
// Mesh Service follows them live.
type MeshSettings struct {
	// RequireTailnet is "Only accept devices on my tailnet": a paired
	// device must also pass the tailnet check (D2). Nil: never set here,
	// so the Mesh Service's -require-tailnet default (true) holds. An
	// explicit -require-tailnet on the Mesh Service's command line
	// overrides it.
	RequireTailnet *bool `json:"require_tailnet,omitempty"`
}

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
