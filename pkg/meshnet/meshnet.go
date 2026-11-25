// Package meshnet is how mads devices reach each other over a network they
// already share (docs/MESH-NETWORK-DESIGN.md, "The adapter interface").
//
// mads doesn't create connectivity: it uses the user's. Every network of
// the "your devices already share a network" class looks the same to it:
// the viewer opens a TCP connection to an address where the host listens,
// and pinned-key mutual TLS (pkg/devicekey) does the rest. So one generic
// adapter, Direct, covers a LAN, a VPC, a public address and every VPN
// installed on the OS. Tailscale is a Helper (it names its network), a
// Finder (it lists peers while devices are being added) and a Gate (an
// optional, refuse-only identity check); nothing depends on it.
//
// mads owns the TLS handshake, the candidate lists, the ranking (Rank), the
// race between candidates (Race) and the address policy (DialPolicy). An
// adapter only supplies byte streams and facts about networks, so later
// units (a relay, a tunnel, a "run this command" adapter) add adapters
// without touching TLS, grants or the UI:
//   - Dial takes the peer's key, so a key-routed relay can route;
//   - Listen returns listeners, so a relay can "listen" by registering
//     outbound;
//   - a Candidate with an unknown Via is skipped, never fatal;
//   - Finder and Gate are where other networks' peer lists and gates join.
package meshnet

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"github.com/drewkidwell/mads/pkg/devicekey"
)

// DefaultPort is the Mesh Service's port, the same on every network (Q10).
const DefaultPort = 17380

// MaxCandidates bounds an address list: a pairing code, a hello, a peer
// row.
const MaxCandidates = 8

// Kind is what sort of network an address is on. It decides the risk
// label and the dial priority.
type Kind string

const (
	// KindOverlay is a VPN of any brand: Tailscale, ZeroTier, NetBird,
	// WireGuard. Works wherever both devices have internet.
	KindOverlay Kind = "overlay"
	// KindLAN is a private address on Wi-Fi or Ethernet (or a VPC).
	KindLAN Kind = "lan"
	// KindPublic is an address on the internet.
	KindPublic Kind = "public"
)

// Valid reports whether k is a known kind.
func (k Kind) Valid() bool {
	switch k {
	case KindOverlay, KindLAN, KindPublic:
		return true
	}
	return false
}

// Priority is a kind's place in the dial order (F3 step 2): same LAN (10),
// then VPN or tailnet (20), then public (30). A relay will be 50. An
// unknown kind goes last.
func (k Kind) Priority() int {
	switch k {
	case KindLAN:
		return 10
	case KindOverlay:
		return 20
	case KindPublic:
		return 30
	}
	return 40
}

// DirectVia is the direct adapter's id: plain TCP to host:port.
const DirectVia = "direct"

// TailscaleID is the Tailscale network's stable id, whatever its interface
// or addresses: what a mesh setting names.
const TailscaleID = "tailscale"

// Network is one place this device can be reached, as detected now.
type Network struct {
	// ID is stable across restarts and address changes: "tailscale",
	// "lan:<subnet>/<gateway>", "vpn:<family>:<prefix>", "public:<iface>".
	ID   string `json:"id"`
	Kind Kind   `json:"kind"`
	// Label names it for people: "Tailscale", "Wi-Fi", "Wired network",
	// "VPN (ZeroTier)", "Public IPv6".
	Label string `json:"label"`
	Iface string `json:"iface,omitempty"`
	// Addrs are what a listener binds: this device's own addresses there.
	Addrs []netip.Addr `json:"addrs,omitempty"`
	// Prefixes are what counts as "on this network" (S2): an address
	// inside one is reachable from here.
	Prefixes []netip.Prefix `json:"prefixes,omitempty"`
	// Adapter is which adapter offers it ("direct").
	Adapter string `json:"adapter,omitempty"`

	// DNSName is this device's name on the network, where it has one (a
	// Tailscale MagicDNS name).
	DNSName string `json:"dns_name,omitempty"`
	// SSID is the Wi-Fi network's name, where the OS gives it.
	SSID string `json:"ssid,omitempty"`
	// Wireless: the interface is Wi-Fi (where the OS says).
	Wireless bool `json:"wireless,omitempty"`
	// Shared: it looks like a network many strangers share (campus,
	// hotel, a large subnet): a warning only, never a rule (Q14).
	Shared bool `json:"shared,omitempty"`
	// Gateway and GatewayHW recognise a LAN again after its addresses
	// change (Q3).
	Gateway   netip.Addr `json:"gateway,omitzero"`
	GatewayHW string     `json:"gateway_hw,omitempty"`
	// Confirmed: a Helper confirmed what the network is (tailscaled says
	// this interface is its own).
	Confirmed bool `json:"confirmed,omitempty"`
}

// Match is how specifically ip is on n: the longest of n's prefixes
// containing it, -1 when none does.
func (n Network) Match(ip netip.Addr) int {
	ip = ip.Unmap()
	best := -1
	for _, p := range n.Prefixes {
		if p.Contains(ip) && p.Bits() > best {
			best = p.Bits()
		}
	}
	return best
}

// MostSpecific is the network ip is most specifically on (longest prefix),
// if any: a LAN's /24 over a VPN's wide routes.
func MostSpecific(ns []Network, ip netip.Addr) (Network, bool) {
	best, bits := Network{}, -1
	for _, n := range ns {
		if b := n.Match(ip); b > bits {
			best, bits = n, b
		}
	}
	return best, bits >= 0
}

// Contains reports whether ip is on n (inside one of its prefixes).
func (n Network) Contains(ip netip.Addr) bool {
	ip = ip.Unmap()
	for _, p := range n.Prefixes {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}

// Candidate is one address another device may try for this one. Codes,
// hellos and peer rows carry lists of these. An unknown Via is skipped,
// never fatal, so a newer device's relay candidate doesn't break an older
// one.
type Candidate struct {
	// Via is the adapter id; "direct" is the only one in this increment.
	Via string `json:"via"`
	// Addr is host:port: "192.168.1.20:17380",
	// "pop-os.tail4c2e.ts.net:17380".
	Addr  string `json:"addr"`
	Kind  Kind   `json:"kind,omitempty"`
	Label string `json:"label,omitempty"`
	// Network is the host's id for the network this address is on, when
	// it said ("tailscale"): informational.
	Network string `json:"network,omitempty"`
}

// Key is what makes two candidates the same address.
func (c Candidate) Key() string { return c.Via + " " + strings.ToLower(c.Addr) }

// Host is the candidate address's host part.
func (c Candidate) Host() string {
	h, _, err := net.SplitHostPort(c.Addr)
	if err != nil {
		return c.Addr
	}
	return h
}

// IP is the candidate's host when it's an IP address.
func (c Candidate) IP() (netip.Addr, bool) {
	ip, err := netip.ParseAddr(c.Host())
	if err != nil {
		return netip.Addr{}, false
	}
	return ip.Unmap(), true
}

// Adapter carries bytes. It never sees TLS, keys' meaning or grants.
type Adapter interface {
	ID() string
	// Detect lists the networks this adapter can use now. Cheap: polled
	// every 10 s.
	Detect(ctx context.Context) ([]Network, error)
	// Listen binds n's addresses on port (0: any), never a wildcard.
	Listen(ctx context.Context, n Network, port int) ([]net.Listener, error)
	// Advertise is the candidates other devices may dial for ls.
	Advertise(n Network, ls []net.Listener) []Candidate
	// Dial opens a byte stream to c, which should reach the device whose
	// key is peer. Passing the key lets a key-routed relay route; mads
	// checks the pin on the TLS handshake whatever the adapter does.
	Dial(ctx context.Context, peer devicekey.Fingerprint, c Candidate) (net.Conn, error)
}

// Helper adds facts about networks another adapter found. Optional.
type Helper interface {
	// Describe may confirm, rename or relabel n in place (Tailscale
	// confirms a 100.64.0.0/10 interface is tailscaled's and adds its
	// MagicDNS name).
	Describe(ctx context.Context, n *Network)
}

// Finder lists other mads devices that are discoverable on a network
// right now (U4). Optional. It only suggests: a found device still has to
// complete the pinned handshake, the check code and the host's approval.
type Finder interface {
	Find(ctx context.Context, n Network) (<-chan Found, error)
}

// Found is one mads device a Finder found.
type Found struct {
	Name, Platform string
	// Fingerprint is as announced: pinned for the pairing request only.
	Fingerprint devicekey.Fingerprint
	Candidates  []Candidate
	// Network is the id of the network it was found on.
	Network string
	// Via says how it was found: "mdns" or "tailscale".
	Via string
	// MeshAPI is what it announced.
	MeshAPI int
	// PairedWithYou is whether it says it has the asking device paired
	// (nil: it didn't say, an older build).
	PairedWithYou *bool
}

// Gate is an optional check on one network's connections. It can only
// refuse. There is no "admit" and no level in its answer.
type Gate interface {
	// Check returns a refusal code ("" passes) for a connection from
	// remote arriving on n. An error is no verdict; the caller fails
	// closed past its grace.
	Check(ctx context.Context, n Network, remote netip.AddrPort) (refuse string, err error)
}

// Clock is time for Race and the loops that use it: real in production
// (RealClock), a fake one in tests.
type Clock interface {
	Now() time.Time
	After(d time.Duration) <-chan time.Time
}

// RealClock is the wall clock.
type RealClock struct{}

func (RealClock) Now() time.Time                         { return time.Now() }
func (RealClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

// --- Shapes ------------------------------------------------------------------

// ErrBadAddress is an address that isn't host:port of the shape mads
// accepts (S2's shape rules).
var ErrBadAddress = errors.New("not a host:port mads dials")

// CheckShape checks addr is host:port where host is an IP address or a DNS
// name and port is 1024–65535 or 443 (S2's shape rules: no scheme, no
// path, no user, no zone). Whether the address may be dialled is checked
// again at dial time, against the IP actually connected (DialPolicy).
func CheckShape(addr string) error {
	if len(addr) > 255+6 {
		return fmt.Errorf("%w: too long", ErrBadAddress)
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("%w: %q: %v", ErrBadAddress, addr, err)
	}
	p, err := strconv.Atoi(port)
	if err != nil || p < 1 || p > 65535 || strconv.Itoa(p) != port {
		return fmt.Errorf("%w: %q: bad port", ErrBadAddress, addr)
	}
	if p < 1024 && p != 443 {
		return fmt.Errorf("%w: %q: ports below 1024 other than 443 are refused", ErrBadAddress, addr)
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		if ip.Zone() != "" {
			return fmt.Errorf("%w: %q: no zoned addresses", ErrBadAddress, addr)
		}
		if why := refusedAlways(ip.Unmap()); why != "" {
			return fmt.Errorf("%w: %q: %s", ErrBadAddress, addr, why)
		}
		return nil
	}
	if !validDNSName(host) {
		return fmt.Errorf("%w: %q: malformed host name", ErrBadAddress, addr)
	}
	if h := strings.ToLower(strings.TrimSuffix(host, ".")); h == "localhost" || strings.HasSuffix(h, ".localhost") {
		return fmt.Errorf("%w: %q: loopback", ErrBadAddress, addr)
	}
	return nil
}

func validDNSName(host string) bool {
	host = strings.TrimSuffix(host, ".")
	if host == "" || len(host) > 253 {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if label == "" || len(label) > 63 || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
			return false
		}
		for _, c := range strings.ToLower(label) {
			if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' && c != '_' {
				return false
			}
		}
	}
	return true
}

// CleanCandidates is in with every candidate this build can't use
// dropped (an unknown Via, a bad shape, a duplicate), kinds it doesn't
// know cleared, labels cleaned, at most MaxCandidates. Another device's
// list goes through it before it's shown or stored.
func CleanCandidates(in []Candidate) []Candidate {
	var out []Candidate
	seen := map[string]bool{}
	for _, c := range in {
		if len(out) == MaxCandidates {
			break
		}
		if c.Via == "" {
			c.Via = DirectVia
		}
		if c.Via != DirectVia || CheckShape(c.Addr) != nil || seen[c.Key()] {
			continue
		}
		seen[c.Key()] = true
		if !c.Kind.Valid() {
			c.Kind = ""
		}
		c.Label = cleanLabel(c.Label)
		c.Network = cleanLabel(c.Network)
		out = append(out, c)
	}
	return out
}

// cleanLabel keeps a label short and printable.
func cleanLabel(s string) string {
	var b strings.Builder
	n := 0
	for _, r := range s {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) || (r >= 0x200b && r <= 0x200f) || (r >= 0x202a && r <= 0x202e) || (r >= 0x2066 && r <= 0x2069) {
			continue
		}
		if n == 64 {
			break
		}
		b.WriteRune(r)
		n++
	}
	return b.String()
}
