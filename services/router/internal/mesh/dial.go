package mesh

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/devicekey"
)

// Every connection to a peer is mutual TLS with pinned keys
// (docs/MESH-AUTH-DESIGN.md D2): this Router presents its device
// certificate (Options.Identity) and accepts the peer only if its key is
// the fingerprint the peer's row pins. A row with no fingerprint is never
// dialled: there is no plaintext fallback and no unverified TLS.
//
// One keep-alive transport per peer (HTTP/2 where the peer offers it), so
// probes, index reads, links and forwarded requests share a connection
// instead of paying a handshake each over a relayed link; long-lived
// streams multiplex beside them.

// errUnpaired is a viewed peer with no pinned fingerprint.
var errUnpaired = errors.New("no device key is pinned for it")

// errNoIdentity is this Router running without a device key.
var errNoIdentity = errors.New("this device has no device key (see the Router's log)")

// peerConn is one peer's pinned transport.
type peerConn struct {
	host string
	tr   *http.Transport // for closing idle connections
	// rt is tr, refusing any request that isn't https to host: what every
	// request to the peer, forwarded ones included, goes through.
	rt     http.RoundTripper
	client *http.Client
}

// pinnedTransport sends only https requests to the one host its pinned
// TLS config was made for, so nothing — a redirect, a rewritten URL — can
// take a peer's request off its verified connection.
type pinnedTransport struct {
	base http.RoundTripper
	host string
}

func (p *pinnedTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Scheme != "https" || r.URL.Host != p.host {
		if r.Body != nil {
			_ = r.Body.Close()
		}
		return nil, errors.New("refused a request off the peer's pinned connection: " + r.URL.Scheme + "://" + r.URL.Host)
	}
	return p.base.RoundTrip(r)
}

// meshHost is the host:port of a stored mesh_url. The stored form is
// http://host:port (what the Data Service validates, and how every row is
// stored), but the scheme is ignored: a peer is always dialled with TLS.
func meshHost(meshURL string) (string, error) {
	u, err := url.Parse(meshURL)
	if err != nil || u.Host == "" {
		return "", errors.New("bad mesh_url " + meshURL)
	}
	return u.Host, nil
}

// conn returns p's transport, building it on first use.
func (m *Manager) conn(p contracts.Peer) (*peerConn, error) {
	if p.Fingerprint == "" {
		return nil, errUnpaired
	}
	if m.opts.Identity == nil {
		return nil, errNoIdentity
	}
	host, err := meshHost(p.MeshURL)
	if err != nil {
		return nil, err
	}
	key := connKey(p.Fingerprint, host)
	m.connsMu.Lock()
	defer m.connsMu.Unlock()
	if c, ok := m.conns[key]; ok {
		return c, nil
	}
	cfg, err := devicekey.ClientConfig(m.opts.Identity, devicekey.Fingerprint(p.Fingerprint))
	if err != nil {
		return nil, err
	}
	dialer := &net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}
	tr := &http.Transport{
		DialContext:         dialer.DialContext,
		TLSClientConfig:     cfg,
		ForceAttemptHTTP2:   true,
		TLSHandshakeTimeout: 10 * time.Second,
		IdleConnTimeout:     90 * time.Second,
		MaxIdleConnsPerHost: 4,
	}
	rt := &pinnedTransport{base: tr, host: host}
	c := &peerConn{host: host, tr: tr, rt: rt, client: &http.Client{
		Transport: rt,
		Timeout:   10 * time.Second,
		// A peer's answer is its answer: a redirect is never followed,
		// least of all to another scheme or host.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
	m.conns[key] = c
	return c, nil
}

// connKey is a transport's identity: the pinned key at an address. A peer
// re-pinned or moved gets a new transport; POST /peers' vetting connection
// is the one the added row then uses.
func connKey(pin, host string) string { return pin + "@" + host }

// dropConns closes the transports no viewed peer uses any more (removed,
// re-pinned, moved, or a vetted device that wasn't added).
func (m *Manager) dropConns(viewed map[string]*peerState, paired map[string]*pairedState) {
	keep := map[string]bool{}
	for _, st := range viewed {
		if host, err := meshHost(st.peer.MeshURL); err == nil {
			keep[connKey(st.peer.Fingerprint, host)] = true
		}
	}
	for _, st := range paired {
		if host, err := meshHost(st.peer.MeshURL); err == nil {
			keep[connKey(st.peer.Fingerprint, host)] = true
		}
	}

	m.connsMu.Lock()
	defer m.connsMu.Unlock()
	for key, c := range m.conns {
		if !keep[key] {
			c.tr.CloseIdleConnections()
			delete(m.conns, key)
		}
	}
}

// peerRequest is a request to p's Mesh Service at pathAndQuery, and the
// pinned client to send it with.
func (m *Manager) peerRequest(ctx context.Context, method string, p contracts.Peer, pathAndQuery string, body io.Reader) (*http.Request, *http.Client, error) {
	c, err := m.conn(p)
	if err != nil {
		return nil, nil, err
	}
	req, err := http.NewRequestWithContext(ctx, method, "https://"+c.host+pathAndQuery, body)
	if err != nil {
		return nil, nil, err
	}
	return req, c.client, nil
}

// dialFailure classifies a failed request to a peer for its reachability.
func dialFailure(p contracts.Peer, err error) (contracts.PeerReachability, string) {
	var pin *devicekey.PinError
	switch {
	case errors.Is(err, errUnpaired):
		name := firstNonEmpty(p.Name, "it")
		return contracts.PeerUnpaired, "no key is pinned for " + name + ": run `mads device` on " + name +
			", then here `mads pair --accept <fingerprint> --name " + firstNonEmpty(p.Name, "<name>") + "`"
	case errors.Is(err, errNoIdentity):
		return contracts.PeerUnreachable, err.Error()
	case errors.As(err, &pin):
		// Something answered at that address, but it isn't the device
		// pinned there: a re-keyed device, or an impostor.
		return contracts.PeerDenied, pin.Error()
	}
	return contracts.PeerUnreachable, unwrapDialError(err)
}

// connsState is the Manager's transports, by connKey.
type connsState struct {
	connsMu sync.Mutex
	conns   map[string]*peerConn
}
