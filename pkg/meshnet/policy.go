package meshnet

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"sync"
	"syscall"
)

// Which addresses this device will dial (docs/MESH-NETWORK-DESIGN.md S2).
// Codes, hellos, joiners, introductions and the user all give a device
// addresses now: a malicious peer or code mustn't be able to make this
// device open connections into places it shouldn't (SSRF). The policy
// lives here, in one place, and is checked at dial time against the IP
// actually connected (a net.Dialer Control hook), so a DNS name can't
// sneak past it by resolving to something else.
//
//   - Always refused: loopback, unspecified, link-local (which includes
//     cloud metadata at 169.254.169.254), multicast, broadcast, cloud
//     metadata over IPv6 (fd00:ec2::254), reserved 0/8 and 240/4, and the
//     IPv4-mapped, IPv4-compatible and NAT64 forms of those; ports below
//     1024 other than 443 (CheckShape).
//   - Allowed only on a network this device is on: private ranges
//     (10/8, 172.16/12, 192.168/16, fc00::/7) and 100.64.0.0/10 must fall
//     inside a prefix of one of this device's own networks.
//   - Public addresses are allowed.
//   - One exception: an address this device's own user added (`mads mesh
//     address add`, WithUserAdded) may be private and outside its
//     prefixes.
//
// Loopback is allowed only with AllowLoopback, which tests and the e2e
// harness set (LoopbackAllowed refuses it in a production build).

// ErrRefusedAddress is an address the dial policy refuses.
var ErrRefusedAddress = errors.New("refused by mads's dial policy")

// RefusedError says which address was refused, and why.
type RefusedError struct {
	Addr   string
	Reason string
}

func (e *RefusedError) Error() string {
	return fmt.Sprintf("%s: %s (%s)", ErrRefusedAddress.Error(), e.Addr, e.Reason)
}

func (e *RefusedError) Unwrap() error { return ErrRefusedAddress }

var (
	metadataV6 = netip.MustParseAddr("fd00:ec2::254")
	broadcast  = netip.MustParseAddr("255.255.255.255")
	reserved   = []netip.Prefix{
		netip.MustParsePrefix("0.0.0.0/8"),
		netip.MustParsePrefix("240.0.0.0/4"),
	}
	nat64 = netip.MustParsePrefix("64:ff9b::/96")
	// privateRanges are reachable only from inside: an address in one is
	// dialled only when this device has a network containing it.
	privateRanges = []netip.Prefix{
		netip.MustParsePrefix("10.0.0.0/8"),
		netip.MustParsePrefix("172.16.0.0/12"),
		netip.MustParsePrefix("192.168.0.0/16"),
		netip.MustParsePrefix("100.64.0.0/10"),
		netip.MustParsePrefix("fc00::/7"),
	}
	loopbackV4 = netip.MustParsePrefix("127.0.0.0/8")
)

// embeddedV4 is the IPv4 address an IPv6 address carries in a form some
// stack may route to it (IPv4-compatible ::a.b.c.d, NAT64 64:ff9b::/96).
func embeddedV4(ip netip.Addr) (netip.Addr, bool) {
	if !ip.Is6() || ip.Is4In6() {
		return netip.Addr{}, false
	}
	b := ip.As16()
	compat := true
	for _, x := range b[:12] {
		if x != 0 {
			compat = false
			break
		}
	}
	if compat || nat64.Contains(ip) {
		v4 := netip.AddrFrom4([4]byte{b[12], b[13], b[14], b[15]})
		if v4.IsUnspecified() && compat {
			return netip.Addr{}, false // :: itself, refused as unspecified
		}
		return v4, true
	}
	return netip.Addr{}, false
}

// refusedAlways is why ip is never dialled whatever the policy (loopback
// aside, which isLoopback covers), or "".
func refusedAlways(ip netip.Addr) string {
	ip = ip.Unmap()
	if v4, ok := embeddedV4(ip); ok {
		if why := refusedAlways(v4); why != "" {
			return why + " (embedded in " + ip.String() + ")"
		}
		if isLoopback(v4) {
			return "loopback (embedded in " + ip.String() + ")"
		}
	}
	switch {
	case !ip.IsValid():
		return "not an address"
	case ip.IsUnspecified():
		return "unspecified"
	case ip.IsLinkLocalUnicast():
		return "link-local (includes cloud metadata)"
	case ip.IsMulticast(), ip.IsLinkLocalMulticast(), ip.IsInterfaceLocalMulticast():
		return "multicast"
	case ip == broadcast:
		return "broadcast"
	case ip == metadataV6:
		return "cloud metadata"
	}
	for _, p := range reserved {
		if p.Contains(ip) {
			return "reserved"
		}
	}
	return ""
}

func isLoopback(ip netip.Addr) bool {
	ip = ip.Unmap()
	return ip.IsLoopback() || loopbackV4.Contains(ip)
}

// IsPrivate reports whether ip is in a range reachable only from inside
// a network (S2's prefix rule applies to it).
func IsPrivate(ip netip.Addr) bool {
	ip = ip.Unmap()
	for _, p := range privateRanges {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}

// DialPolicy is S2. The zero value refuses loopback and every private
// address (it knows no local networks).
type DialPolicy struct {
	// Local is this device's own networks' prefixes now: a private address
	// is dialled only inside one.
	Local func() []netip.Prefix
	// AllowLoopback lets tests and the e2e harness dial 127.0.0.1. Set it
	// only where LoopbackAllowed says so.
	AllowLoopback bool
}

// CheckIP says why ip may not be dialled ("" when it may). userAdded is
// S2's exception: an address this device's own user added.
func (p *DialPolicy) CheckIP(ip netip.Addr, userAdded bool) string {
	ip = ip.Unmap()
	if isLoopback(ip) {
		if p != nil && p.AllowLoopback {
			return ""
		}
		return "loopback"
	}
	if why := refusedAlways(ip); why != "" {
		return why
	}
	if !IsPrivate(ip) || userAdded {
		return ""
	}
	if p != nil && p.Local != nil {
		for _, pre := range p.Local() {
			if pre.Contains(ip) {
				return ""
			}
		}
	}
	return "a private address on a network this device isn't on"
}

// CheckAddress checks an address another device chose for this one when
// it arrives (S3's 422 bad_address): its shape, and, for an IP address,
// the rules that hold wherever this device is (loopback included). The
// prefix rule is left to dial time: networks come and go.
func (p *DialPolicy) CheckAddress(addr string) error {
	if err := CheckShape(addr); err != nil {
		return err
	}
	c := Candidate{Via: DirectVia, Addr: addr}
	if ip, ok := c.IP(); ok && isLoopback(ip) && (p == nil || !p.AllowLoopback) {
		return &RefusedError{Addr: addr, Reason: "loopback"}
	}
	return nil
}

type userAddedKey struct{}

// WithUserAdded marks a dial as one this device's own user asked for (an
// address it added): S2's exception to the prefix rule.
func WithUserAdded(ctx context.Context) context.Context {
	return context.WithValue(ctx, userAddedKey{}, true)
}

// UserAdded reports whether ctx carries WithUserAdded.
func UserAdded(ctx context.Context) bool {
	v, _ := ctx.Value(userAddedKey{}).(bool)
	return v
}

// Control is a net.Dialer Control hook that refuses any connection the
// policy doesn't allow, against the address actually being connected (after
// any DNS resolution).
func (p *DialPolicy) Control(userAdded bool) func(network, address string, c syscall.RawConn) error {
	return func(_, address string, _ syscall.RawConn) error {
		ap, err := netip.ParseAddrPort(address)
		if err != nil {
			return &RefusedError{Addr: address, Reason: "not an IP address and port"}
		}
		if why := p.CheckIP(ap.Addr(), userAdded); why != "" {
			return &RefusedError{Addr: address, Reason: why}
		}
		return nil
	}
}

// Dialer is a net.Dialer that applies the policy at connect time.
func (p *DialPolicy) Dialer(ctx context.Context) *net.Dialer {
	return &net.Dialer{Control: p.Control(UserAdded(ctx))}
}

// --- The e2e and test loopback switch -------------------------------------------

var (
	loopbackBuildMu sync.Mutex
	// loopbackBuild is set by a build with the mesh_loopback tag (the e2e
	// harness's), never by `make build`.
	loopbackBuild bool
)

// LoopbackAllowed reports whether this binary may dial loopback mesh
// addresses: a go test binary, or one built with -tags mesh_loopback
// (the Playwright harness's stand-in devices listen on 127.0.0.1). A
// production build refuses -mesh-allow-loopback.
func LoopbackAllowed() bool {
	loopbackBuildMu.Lock()
	defer loopbackBuildMu.Unlock()
	return loopbackBuild || testingBinary()
}
