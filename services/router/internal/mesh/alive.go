package mesh

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"strings"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/devicekey"
	"github.com/drewkidwell/mads/pkg/meshnet"
)

// Whether this device's Mesh Service actually answers where it last said
// it listens (round-2 review T5): its report outlives it when it stops,
// so setup and status ask it, over the same TLS its peers use, with this
// device's own key, at GET /mesh/self. Only this device's own addresses
// are dialed (loopback, or an interface's): nothing leaves the machine.

// aliveWithin bounds one check; aliveFor is how long its answer is kept.
const (
	aliveWithin = 800 * time.Millisecond
	aliveFor    = 3 * time.Second
)

type aliveCheck struct {
	key string
	at  time.Time
	ok  bool
}

// ownAddress reports whether ip is this machine's: dialing it never
// leaves it. A var for tests.
var ownAddress = func(ip netip.Addr) bool {
	if ip.IsLoopback() {
		return true
	}
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return false
	}
	for _, a := range addrs {
		if p, err := netip.ParsePrefix(a.String()); err == nil && p.Addr().Unmap() == ip.Unmap() {
			return true
		}
	}
	return false
}

// serviceAnswers reports whether the Mesh Service answers on one of the
// addresses it reported (cached for aliveFor).
func (m *Manager) serviceAnswers(ctx context.Context, addrs []contracts.NetAddress) bool {
	var key []string
	for _, a := range addrs {
		key = append(key, a.Addr)
	}
	slices.Sort(key)
	k := strings.Join(key, ",")
	now := time.Now()
	m.aliveMu.Lock()
	if c := m.alive; c.key == k && now.Sub(c.at) < aliveFor {
		m.aliveMu.Unlock()
		return c.ok
	}
	m.aliveMu.Unlock()
	ok := false
	for _, a := range addrs {
		if a.Via != "" && a.Via != meshnet.DirectVia {
			continue
		}
		if m.selfAnswersAt(ctx, a.Addr) == nil {
			ok = true
			break
		}
	}
	m.aliveMu.Lock()
	m.alive = aliveCheck{key: k, at: time.Now(), ok: ok}
	m.aliveMu.Unlock()
	return ok
}

var errNotOwnAddress = errors.New("not this machine's address")

// selfAnswersAt is one GET /mesh/self at addr: its key must be this
// device's, its answer 204.
func (m *Manager) selfAnswersAt(ctx context.Context, addr string) error {
	if m.opts.Identity == nil {
		return errNoIdentity
	}
	// The shape every mesh dial has (a port ≥ 1024, or 443; final pass 6),
	// and this machine's own address only.
	if err := meshnet.CheckShape(addr); err != nil {
		return err
	}
	ap, err := netip.ParseAddrPort(addr)
	if err != nil || !ownAddress(ap.Addr()) {
		return errNotOwnAddress
	}
	cert, err := m.opts.Identity.Certificate()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, aliveWithin)
	defer cancel()
	var d net.Dialer
	raw, err := d.DialContext(ctx, "tcp", ap.String())
	if err != nil {
		return err
	}
	defer func() { _ = raw.Close() }()
	self := m.opts.Identity.Fingerprint()
	tc := tls.Client(raw, &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{cert},
		NextProtos:   []string{"http/1.1"},
		// The key is checked below: it must be this device's own.
		InsecureSkipVerify: true, //nolint:gosec // G402: the device key is checked in VerifyConnection
		VerifyConnection: func(cs tls.ConnectionState) error {
			fp, err := devicekey.PeerFingerprint(cs)
			if err == nil && fp != self {
				err = errors.New("another device's key answers there")
			}
			return err
		},
	})
	if err := tc.HandshakeContext(ctx); err != nil {
		return err
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = tc.SetDeadline(deadline)
	}
	if _, err := io.WriteString(tc, "GET /mesh/self HTTP/1.1\r\nHost: "+addr+"\r\nUser-Agent: mads\r\nConnection: close\r\n\r\n"); err != nil {
		return err
	}
	resp, err := http.ReadResponse(bufio.NewReader(tc), nil)
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return errors.New("answered " + resp.Status)
	}
	return nil
}
