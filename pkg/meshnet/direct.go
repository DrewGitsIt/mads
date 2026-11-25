package meshnet

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strconv"

	"github.com/drewkidwell/mads/pkg/devicekey"
)

// Direct is the generic adapter: interface detection, plain TCP listen
// and dial. It covers a LAN, a VPC, public addresses and every VPN
// installed on the OS: a VPN of any brand is just an interface with an
// address.
type Direct struct {
	// Sys is what detection reads (OSSystem in production).
	Sys System
	// Helpers describe what detection finds (Tailscale).
	Helpers []Helper
	// Policy is S2, applied to every Dial at connect time.
	Policy *DialPolicy
}

var _ Adapter = (*Direct)(nil)

func (d *Direct) ID() string { return DirectVia }

func (d *Direct) Detect(ctx context.Context) ([]Network, error) {
	return Detect(ctx, d.Sys, d.Helpers...)
}

// Listen binds each of n's addresses on port: never a wildcard. It
// returns what bound, and an error naming what didn't.
func (d *Direct) Listen(ctx context.Context, n Network, port int) ([]net.Listener, error) {
	var lc net.ListenConfig
	var out []net.Listener
	var errs []error
	for _, a := range n.Addrs {
		if !a.IsValid() || a.IsUnspecified() {
			errs = append(errs, fmt.Errorf("%s: refusing to bind a wildcard address", n.ID))
			continue
		}
		addr := net.JoinHostPort(a.String(), strconv.Itoa(port))
		l, err := lc.Listen(ctx, "tcp", addr)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		out = append(out, l)
	}
	return out, errors.Join(errs...)
}

// Advertise is one candidate per listener, IPv4 first.
func (d *Direct) Advertise(n Network, ls []net.Listener) []Candidate {
	var v4, v6 []Candidate
	for _, l := range ls {
		ap, err := netip.ParseAddrPort(l.Addr().String())
		if err != nil {
			continue
		}
		c := Candidate{Via: DirectVia, Addr: ap.String(), Kind: n.Kind, Label: n.Label, Network: n.ID}
		if ap.Addr().Unmap().Is4() {
			v4 = append(v4, c)
		} else {
			v6 = append(v6, c)
		}
	}
	return append(v4, v6...)
}

// Dial opens TCP to c under the dial policy. The peer's key isn't needed
// to route a direct connection; the TLS pin is checked by the caller.
func (d *Direct) Dial(ctx context.Context, _ devicekey.Fingerprint, c Candidate) (net.Conn, error) {
	if c.Via != "" && c.Via != DirectVia {
		return nil, fmt.Errorf("the direct adapter can't dial a %q address", c.Via)
	}
	if err := CheckShape(c.Addr); err != nil {
		return nil, err
	}
	return d.Policy.Dialer(ctx).DialContext(ctx, "tcp", c.Addr)
}
