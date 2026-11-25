package meshnet

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/netip"
	"os"
	"strings"

	"github.com/drewkidwell/mads/pkg/devicekey"
)

// Test hooks (round-1 review, testability): a black-box tester runs real
// Mesh Services and Routers on loopback, with the networks they're "on"
// from a file, so the Tailscale listener, vouching, per-network budgets and
// mDNS (on lo0/lo) can be exercised without touching a real interface.
// Both are refused outside test and mesh_loopback builds by their flags'
// callers (LoopbackAllowed).

// LoopbackOnly is an adapter that binds only loopback addresses: a test
// build's Mesh Service never listens on a real network by accident.
type LoopbackOnly struct{ Adapter }

func (l LoopbackOnly) Listen(ctx context.Context, n Network, port int) ([]net.Listener, error) {
	for _, a := range n.Addrs {
		if !isLoopback(a) {
			return nil, fmt.Errorf("%s: refusing to bind %s: -mesh-allow-loopback binds loopback only", n.ID, a)
		}
	}
	return l.Adapter.Listen(ctx, n, port)
}

// ReadNetworks reads a -fake-networks file: a JSON list of Network, every
// address loopback. It's read again on every scan, so a tester can move a
// device between networks by rewriting it.
func ReadNetworks(path string) ([]Network, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var ns []Network
	if err := json.Unmarshal(raw, &ns); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	for _, n := range ns {
		for _, a := range n.Addrs {
			if !isLoopback(a) {
				return nil, fmt.Errorf("%s: network %s: %s isn't a loopback address", path, n.ID, a)
			}
		}
		// Its interface and prefixes too (T8): an mDNS advert or a dial
		// policy must never reach past loopback.
		if n.Iface != "" && !strings.HasPrefix(n.Iface, "lo") {
			return nil, fmt.Errorf("%s: network %s: interface %s isn't loopback", path, n.ID, n.Iface)
		}
		for _, p := range n.Prefixes {
			if !loopbackV4.Contains(p.Addr()) && p.Addr() != netip.IPv6Loopback() || (p.Addr().Is4() && p.Bits() < 8) || (p.Addr().Is6() && p.Bits() < 128) {
				return nil, fmt.Errorf("%s: network %s: prefix %s isn't within loopback", path, n.ID, p)
			}
		}
	}
	return ns, nil
}

// FileNetworks is the direct adapter with its networks from a file
// (ReadNetworks) instead of this machine's interfaces.
type FileNetworks struct {
	Direct *Direct
	Path   string
}

var _ Adapter = FileNetworks{}

func (f FileNetworks) ID() string { return DirectVia }

func (f FileNetworks) Detect(context.Context) ([]Network, error) { return ReadNetworks(f.Path) }

func (f FileNetworks) Listen(ctx context.Context, n Network, port int) ([]net.Listener, error) {
	return f.Direct.Listen(ctx, n, port)
}

func (f FileNetworks) Advertise(n Network, ls []net.Listener) []Candidate {
	return f.Direct.Advertise(n, ls)
}

func (f FileNetworks) Dial(ctx context.Context, peer devicekey.Fingerprint, c Candidate) (net.Conn, error) {
	return f.Direct.Dial(ctx, peer, c)
}
