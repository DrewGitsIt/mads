package mesh

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/devicekey"
	"github.com/drewkidwell/mads/pkg/meshnet"
)

// Every connection to a peer is mutual TLS with pinned keys
// (docs/MESH-AUTH-DESIGN.md D2): this Router presents its device
// certificate (Options.Identity) and accepts the peer only if its key is
// the fingerprint the peer's row pins. A row with no fingerprint is never
// dialled: there is no plaintext fallback and no unverified TLS.
//
// A peer has a short list of candidate addresses (its row's addresses, or
// one legacy candidate from mesh_url). To connect (docs/MESH-NETWORK-DESIGN.md
// F3):
//  1. Filter: an address that isn't on a network this device is on now is
//     skipped (S2's dial policy decides), so failure is fast.
//  2. Order: the last address that worked first, then same LAN, VPN,
//     public, then the host's order.
//  3. Race, staggered 250 ms: the first to finish the TLS handshake with
//     the pinned key wins (a plain TCP connect isn't success: on another
//     network the same private address may be someone else's machine).
//  4. 3 s an attempt, 6 s the race.
//  5. The winner is the peer's one keep-alive transport (HTTP/2 where the
//     peer offers it), keyed pin@address, so probes, index reads, links
//     and forwarded requests share it; last_good is written when it
//     changes.
//  6. Every 60 s, while connected on an address that isn't the best, the
//     better ones are tried; one that answers twice in a row takes new
//     requests, and the old connection stays until its streams end or 10
//     minutes pass.
//  7. A failed request on the path in use, or a change in this device's
//     own networks, re-races at once.

// errUnpaired is a viewed peer with no pinned fingerprint.
var errUnpaired = errors.New("no device key is pinned for it")

// errNoIdentity is this Router running without a device key.
var errNoIdentity = errors.New("this device has no device key (see the Router's log)")

// noSharedNetworkError is a peer none of whose addresses is on a network
// this device is on now (F4's no_shared_network): nothing was dialled.
type noSharedNetworkError struct {
	// Networks are the peer's networks, by label ("Tailscale", "Wi-Fi").
	Networks []string
}

func (e *noSharedNetworkError) Error() string {
	if len(e.Networks) == 0 {
		return "none of its addresses is on a network this device is on now"
	}
	return "it uses " + strings.Join(e.Networks, " and ") + ", which this device isn't on now"
}

// noAddressError is a peer with no address at all.
var errNoAddress = errors.New("no address is known for it")

// peerConn is one pinned transport to one address.
type peerConn struct {
	key  string
	host string // the address, host:port
	tr   *http.Transport
	// rt is tr, refusing any request that isn't https to host: what every
	// request to the peer, forwarded ones included, goes through.
	rt     http.RoundTripper
	client *http.Client

	mu sync.Mutex
	// pre is a race winner's connection, handed to the transport's first
	// dial so the race's handshake isn't wasted.
	pre net.Conn
	// open is every connection the transport holds, so a retired one can
	// be closed when its 10 minutes are up.
	open map[net.Conn]struct{}
	// retireAt is when a replaced transport is closed regardless.
	retireAt time.Time
}

func (c *peerConn) takePre() net.Conn {
	c.mu.Lock()
	defer c.mu.Unlock()
	pre := c.pre
	c.pre = nil
	return pre
}

func (c *peerConn) handoff(conn net.Conn) {
	c.mu.Lock()
	old := c.pre
	c.pre = conn
	c.mu.Unlock()
	if old != nil {
		_ = old.Close()
	}
}

func (c *peerConn) track(conn net.Conn) net.Conn {
	c.mu.Lock()
	c.open[conn] = struct{}{}
	c.mu.Unlock()
	// http.Transport finds the negotiated protocol (HTTP/2) only on a
	// *tls.Conn before Go 1.27, so a TLS connection is handed over as is,
	// and its closing is seen on the TCP connection under it (dialPinned).
	if tc, ok := conn.(*tls.Conn); ok {
		if n, ok := tc.NetConn().(*notifyConn); ok {
			n.onClose(func() {
				c.mu.Lock()
				delete(c.open, conn)
				c.mu.Unlock()
			})
			return tc
		}
	}
	return &trackedConn{Conn: conn, c: c}
}

// notifyConn is the TCP connection under a pinned TLS connection: it says
// when it closes, so peerConn.track can hand http.Transport the *tls.Conn
// itself.
type notifyConn struct {
	net.Conn
	mu     sync.Mutex
	closed bool
	notify func()
}

func (n *notifyConn) Close() error {
	n.mu.Lock()
	f := n.notify
	first := !n.closed
	n.closed = true
	n.mu.Unlock()
	if first && f != nil {
		f()
	}
	return n.Conn.Close()
}

// onClose sets what runs when it closes (at once, if it already has).
func (n *notifyConn) onClose(f func()) {
	n.mu.Lock()
	closed := n.closed
	if !closed {
		n.notify = f
	}
	n.mu.Unlock()
	if closed {
		f()
	}
}

// closeAll ends every connection (a retired transport past its time).
func (c *peerConn) closeAll() {
	c.tr.CloseIdleConnections()
	c.mu.Lock()
	open := c.open
	c.open = map[net.Conn]struct{}{}
	pre := c.pre
	c.pre = nil
	c.mu.Unlock()
	for conn := range open {
		_ = conn.Close()
	}
	if pre != nil {
		_ = pre.Close()
	}
}

type trackedConn struct {
	net.Conn
	c    *peerConn
	once sync.Once
}

func (t *trackedConn) Close() error {
	t.once.Do(func() {
		t.c.mu.Lock()
		delete(t.c.open, t.Conn)
		t.c.mu.Unlock()
	})
	return t.Conn.Close()
}

// ConnectionState lets http.Transport see the TLS state (and so HTTP/2)
// from Go 1.27; earlier, only a *tls.Conn is looked at (track).
func (t *trackedConn) ConnectionState() tls.ConnectionState {
	if tc, ok := t.Conn.(*tls.Conn); ok {
		return tc.ConnectionState()
	}
	return tls.ConnectionState{}
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

// connKey is a transport's identity: the pinned key at an address. A peer
// re-pinned or moved gets a new transport.
func connKey(pin, host string) string { return pin + "@" + strings.ToLower(host) }

// --- Paths ----------------------------------------------------------------------

// addrLive is one address's live state, as the last race or request saw it.
type addrLive struct {
	state   string
	err     string
	lastOK  time.Time
	checked time.Time
}

// pathState is one peer's (by fingerprint) path: the address in use and
// what each address did last.
type pathState struct {
	// race serializes races for this peer: one at a time, others wait.
	race chan struct{}

	mu      sync.Mutex
	cur     string // the address in use, host:port; "" none
	curAddr contracts.MeshAddress
	// last is the address in use before the last invalidate: a re-race
	// that lands elsewhere is a path change (logged with via).
	last   string
	since  time.Time
	rtt    time.Duration
	states map[string]*addrLive // by lower-case address
	// upgrade counts each better address's answers in a row (F3 step 6).
	upgrade map[string]int
	// lastRaceErr is why the last race found nothing.
	lastRaceErr error
}

func newPathState() *pathState {
	return &pathState{race: make(chan struct{}, 1), states: map[string]*addrLive{}, upgrade: map[string]int{}}
}

func (ps *pathState) setState(addr, state, why string, at time.Time) {
	k := strings.ToLower(addr)
	s, ok := ps.states[k]
	if !ok {
		s = &addrLive{}
		ps.states[k] = s
	}
	s.state, s.err, s.checked = state, why, at
	if state == contracts.AddrInUse || state == contracts.AddrWorks {
		s.lastOK = at
	}
}

func (m *Manager) pathFor(fp string) *pathState {
	m.connsMu.Lock()
	defer m.connsMu.Unlock()
	ps, ok := m.paths[fp]
	if !ok {
		ps = newPathState()
		m.paths[fp] = ps
	}
	return ps
}

// invalidate drops fp's path in use (it failed, or this device's networks
// changed): the next request races again.
func (m *Manager) invalidate(fp, why string) {
	m.connsMu.Lock()
	ps := m.paths[fp]
	m.connsMu.Unlock()
	if ps == nil {
		return
	}
	ps.mu.Lock()
	if ps.cur != "" {
		ps.setState(ps.cur, contracts.AddrNotAnswering, why, m.opts.Now())
		ps.last, ps.cur = ps.cur, ""
	}
	ps.mu.Unlock()
}

// invalidateAll re-races every peer (this device's networks changed).
func (m *Manager) invalidateAll(why string) {
	m.connsMu.Lock()
	fps := make([]string, 0, len(m.paths))
	for fp := range m.paths {
		fps = append(fps, fp)
	}
	m.connsMu.Unlock()
	for _, fp := range fps {
		m.invalidate(fp, why)
	}
}

// candidates is p's addresses in race order (F3 step 2): last_good first
// when the host still reports it; a last_good it stopped reporting ranks
// after the reported ones (F5).
func (m *Manager) candidates(p contracts.Peer) []contracts.MeshAddress {
	list := p.Candidates()
	if len(list) == 0 {
		return nil
	}
	lastGood := ""
	if p.LastGood != nil {
		lastGood = p.LastGood.Addr
	}
	reported := slices.ContainsFunc(list, func(a contracts.MeshAddress) bool { return a.Source == contracts.SourceHello })
	stale := ""
	for _, a := range list {
		if lastGood != "" && strings.EqualFold(a.Addr, lastGood) && reported && a.Source != contracts.SourceHello && a.Source != contracts.SourceUser {
			stale, lastGood = a.Addr, ""
		}
	}
	cands := make([]meshnet.Candidate, 0, len(list))
	byKey := map[string]contracts.MeshAddress{}
	for _, a := range list {
		c := meshnet.FromNet(a.NetAddress)
		if c.Via == "" {
			c.Via = meshnet.DirectVia
		}
		cands = append(cands, c)
		byKey[c.Key()] = a
	}
	ranked := meshnet.Rank(cands, lastGood)
	out := make([]contracts.MeshAddress, 0, len(ranked))
	var tail []contracts.MeshAddress
	for _, c := range ranked {
		a := byKey[c.Key()]
		if stale != "" && strings.EqualFold(a.Addr, stale) {
			tail = append(tail, a)
			continue
		}
		out = append(out, a)
	}
	return append(out, tail...)
}

// conn returns p's transport on its path in use, racing its candidates
// for one when there's none.
func (m *Manager) conn(ctx context.Context, p contracts.Peer) (*peerConn, error) {
	c, _, err := m.connFresh(ctx, p)
	return c, err
}

// connFresh is conn, and whether it raced just now (a failure on a path
// that didn't race is worth one retry with a fresh race).
func (m *Manager) connFresh(ctx context.Context, p contracts.Peer) (*peerConn, bool, error) {
	if p.Fingerprint == "" {
		return nil, false, errUnpaired
	}
	if m.opts.Identity == nil {
		return nil, false, errNoIdentity
	}
	ps := m.pathFor(p.Fingerprint)
	if c := m.current(p, ps); c != nil {
		return c, false, nil
	}
	select {
	case ps.race <- struct{}{}:
	case <-ctx.Done():
		return nil, false, ctx.Err()
	}
	defer func() { <-ps.race }()
	// Another caller may have raced while this one waited.
	if c := m.current(p, ps); c != nil {
		return c, false, nil
	}
	c, err := m.racePeer(ctx, p, ps)
	return c, true, err
}

// current is p's transport on its path in use, if it has one that's still
// one of its addresses.
func (m *Manager) current(p contracts.Peer, ps *pathState) *peerConn {
	ps.mu.Lock()
	cur := ps.cur
	ps.mu.Unlock()
	if cur == "" {
		return nil
	}
	if !slices.ContainsFunc(p.Candidates(), func(a contracts.MeshAddress) bool { return strings.EqualFold(a.Addr, cur) }) {
		return nil // the row no longer lists it
	}
	return m.transport(p.Fingerprint, cur, false)
}

// racePeer races p's candidates on networks this device is on and makes
// the winner p's path.
func (m *Manager) racePeer(ctx context.Context, p contracts.Peer, ps *pathState) (*peerConn, error) {
	all := m.candidates(p)
	if len(all) == 0 {
		return nil, errNoAddress
	}
	local := m.localNetworks()
	now := m.opts.Now()
	var try []meshnet.Candidate
	user := map[string]bool{}
	var labels []string
	ps.mu.Lock()
	for _, a := range all {
		c := meshnet.FromNet(a.NetAddress)
		if c.Via == "" {
			c.Via = meshnet.DirectVia
		}
		isUser := a.Source == contracts.SourceUser
		if label := addrLabel(a); label != "" && !slices.Contains(labels, label) {
			labels = append(labels, label)
		}
		if !meshnet.OnNetwork(c, local, m.policy, isUser) {
			ps.setState(a.Addr, contracts.AddrNotOnNetwork, "not on a network this device is on now", now)
			continue
		}
		user[strings.ToLower(c.Addr)] = isUser
		try = append(try, c)
	}
	ps.mu.Unlock()
	if len(try) == 0 {
		err := &noSharedNetworkError{Networks: labels}
		ps.mu.Lock()
		ps.lastRaceErr = err
		ps.mu.Unlock()
		return nil, err
	}
	cfg, err := m.pinnedConfig(p.Fingerprint)
	if err != nil {
		return nil, err
	}
	won, idx, attempts, err := meshnet.Race(ctx, try, m.opts.Race, func(actx context.Context, c meshnet.Candidate) (*tls.Conn, error) {
		return m.dialPinned(actx, devicekey.Fingerprint(p.Fingerprint), c, cfg, user[strings.ToLower(c.Addr)])
	}, func(tc *tls.Conn) { _ = tc.Close() })
	at := m.opts.Now()
	ps.mu.Lock()
	for _, a := range attempts {
		switch {
		case a.Won:
			ps.setState(a.Candidate.Addr, contracts.AddrInUse, "", at)
		case !a.Started:
		case a.Err != nil:
			state, why := attemptState(a.Err)
			ps.setState(a.Candidate.Addr, state, why, at)
		}
	}
	if err != nil {
		ps.lastRaceErr = err
		ps.mu.Unlock()
		return nil, err
	}
	winner := try[idx]
	var addr contracts.MeshAddress
	for _, a := range all {
		if strings.EqualFold(a.Addr, winner.Addr) {
			addr = a
		}
	}
	prev := firstNonEmpty(ps.cur, ps.last)
	ps.cur, ps.curAddr, ps.since, ps.rtt, ps.lastRaceErr, ps.last = winner.Addr, addr, at, attempts[idx].Took, nil, ""
	ps.upgrade = map[string]int{}
	ps.mu.Unlock()
	c := m.transport(p.Fingerprint, winner.Addr, user[strings.ToLower(winner.Addr)])
	c.handoff(won)
	m.notePath(p, addr, prev, attempts[idx].Took)
	return c, nil
}

// attemptState is a failed attempt's address state.
func attemptState(err error) (string, string) {
	var pin *devicekey.PinError
	var refused *meshnet.RefusedError
	switch {
	case errors.As(err, &pin):
		return contracts.AddrOtherDevice, "a different device answered there (" + pin.Got.Short() + ")"
	case errors.As(err, &refused):
		return contracts.AddrRefused, refused.Reason
	case errors.Is(err, meshnet.ErrAttemptTimeout), errors.Is(err, context.DeadlineExceeded):
		return contracts.AddrNotAnswering, "no answer in time"
	}
	return contracts.AddrNotAnswering, unwrapDialError(err)
}

func addrLabel(a contracts.MeshAddress) string {
	if a.Label != "" {
		return a.Label
	}
	switch meshnet.InferKind(meshnet.FromNet(a.NetAddress)) {
	case meshnet.KindOverlay:
		if strings.HasSuffix(strings.ToLower(meshnet.FromNet(a.NetAddress).Host()), ".ts.net") || isTailnetIP(a) {
			return "Tailscale"
		}
		return "a VPN"
	case meshnet.KindLAN:
		return "a local network"
	case meshnet.KindPublic:
		return "the internet"
	}
	return ""
}

func isTailnetIP(a contracts.MeshAddress) bool {
	ip, ok := meshnet.FromNet(a.NetAddress).IP()
	if !ok {
		return false
	}
	for _, p := range meshnet.TailnetRanges {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}

// pinnedConfig is the TLS config for dialling fp: this device's
// certificate, the pin, and ALPN for HTTP/2 (the race dials TLS itself).
func (m *Manager) pinnedConfig(fp string) (*tls.Config, error) {
	cfg, err := devicekey.ClientConfig(m.opts.Identity, devicekey.Fingerprint(fp))
	if err != nil {
		return nil, err
	}
	cfg.NextProtos = []string{"h2", "http/1.1"}
	return cfg, nil
}

// dialPinned opens TCP to c under the dial policy and completes the TLS
// handshake with the pinned key: only that counts as an answer.
func (m *Manager) dialPinned(ctx context.Context, fp devicekey.Fingerprint, c meshnet.Candidate, cfg *tls.Config, userAdded bool) (*tls.Conn, error) {
	if userAdded {
		ctx = meshnet.WithUserAdded(ctx)
	}
	raw, err := m.adapter.Dial(ctx, fp, c)
	if err != nil {
		return nil, err
	}
	tc := tls.Client(&notifyConn{Conn: raw}, cfg)
	if err := tc.HandshakeContext(ctx); err != nil {
		_ = raw.Close()
		return nil, err
	}
	return tc, nil
}

// transport returns the pinned transport for fp at addr, building it on
// first use.
func (m *Manager) transport(fp, addr string, userAdded bool) *peerConn {
	key := connKey(fp, addr)
	m.connsMu.Lock()
	defer m.connsMu.Unlock()
	if c, ok := m.conns[key]; ok {
		c.mu.Lock()
		c.retireAt = time.Time{}
		c.mu.Unlock()
		return c
	}
	c := &peerConn{key: key, host: addr, open: map[net.Conn]struct{}{}}
	c.tr = &http.Transport{
		DialTLSContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			if pre := c.takePre(); pre != nil {
				return c.track(pre), nil
			}
			cfg, err := m.pinnedConfig(fp)
			if err != nil {
				return nil, err
			}
			dctx, cancel := context.WithTimeout(ctx, meshnet.DefaultAttempt+2*time.Second)
			defer cancel()
			tc, err := m.dialPinned(dctx, devicekey.Fingerprint(fp), meshnet.Candidate{Via: meshnet.DirectVia, Addr: addr}, cfg, userAdded)
			if err != nil {
				return nil, err
			}
			return c.track(tc), nil
		},
		ForceAttemptHTTP2:   true,
		TLSHandshakeTimeout: 10 * time.Second,
		IdleConnTimeout:     90 * time.Second,
		MaxIdleConnsPerHost: 4,
	}
	c.rt = &pinnedTransport{base: c.tr, host: addr}
	c.client = &http.Client{
		Transport: c.rt,
		Timeout:   10 * time.Second,
		// A peer's answer is its answer: a redirect is never followed,
		// least of all to another scheme or host.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	m.conns[key] = c
	return c
}

// dropConns closes the transports nothing uses any more (a peer removed,
// re-pinned or moved to another address), keeping a replaced one up to
// 10 minutes while its streams finish (F3 step 6).
func (m *Manager) dropConns(viewed map[string]*peerState, paired map[string]*pairedState) {
	keep := map[string]bool{}
	fps := map[string]bool{}
	for _, st := range viewed {
		fps[st.peer.Fingerprint] = true
	}
	for _, st := range paired {
		fps[st.peer.Fingerprint] = true
	}
	now := m.opts.Now()
	m.connsMu.Lock()
	for fp, ps := range m.paths {
		if !fps[fp] {
			delete(m.paths, fp)
			continue
		}
		ps.mu.Lock()
		if ps.cur != "" {
			keep[connKey(fp, ps.cur)] = true
		}
		ps.mu.Unlock()
	}
	var closing []*peerConn
	for key, c := range m.conns {
		if keep[key] {
			continue
		}
		fp, _, _ := strings.Cut(key, "@")
		c.mu.Lock()
		if c.retireAt.IsZero() {
			c.retireAt = now.Add(retireAfter)
		}
		expired := !now.Before(c.retireAt) || !fps[fp]
		c.mu.Unlock()
		c.tr.CloseIdleConnections()
		if expired {
			closing = append(closing, c)
			delete(m.conns, key)
		}
	}
	m.connsMu.Unlock()
	for _, c := range closing {
		c.closeAll()
	}
}

// retireAfter is how long a replaced path's connection may keep its
// streams.
const retireAfter = 10 * time.Minute

// peerRequest is a request to p's Mesh Service at pathAndQuery, and the
// pinned client to send it with.
func (m *Manager) peerRequest(ctx context.Context, method string, p contracts.Peer, pathAndQuery string, body io.Reader) (*http.Request, *http.Client, error) {
	c, err := m.conn(ctx, p)
	if err != nil {
		return nil, nil, err
	}
	req, err := http.NewRequestWithContext(ctx, method, "https://"+c.host+pathAndQuery, body)
	if err != nil {
		return nil, nil, err
	}
	return req, c.client, nil
}

// dialFailure classifies a failed request to a peer for its reachability
// (F4): no address on a network this device is on is no_shared_network
// ("Not reachable from this network"); addresses tried and none answering
// as the pinned device is unreachable ("Offline"), whether nothing
// answered or another device did.
func dialFailure(p contracts.Peer, err error) (contracts.PeerReachability, string) {
	var pin *devicekey.PinError
	var noShared *noSharedNetworkError
	var race *meshnet.RaceError
	name := firstNonEmpty(p.Name, "it")
	switch {
	case errors.Is(err, errUnpaired):
		return contracts.PeerUnpaired, "no key is pinned for " + name + ": run `mads device` on " + name +
			", then here `mads pair --accept <fingerprint> --name " + firstNonEmpty(p.Name, "<name>") + "`"
	case errors.Is(err, errNoIdentity):
		return contracts.PeerUnreachable, err.Error()
	case errors.As(err, &noShared):
		return contracts.PeerNoSharedNetwork, name + " isn't reachable from this network: " + noShared.Error()
	case errors.Is(err, errNoAddress):
		return contracts.PeerUnreachable, err.Error()
	case errors.As(err, &race):
		var others []string
		for _, a := range race.Attempts {
			if errors.As(a.Err, &pin) {
				others = append(others, a.Candidate.Addr)
			}
		}
		if len(others) > 0 && len(others) == startedCount(race.Attempts) {
			return contracts.PeerUnreachable, "a different device answered at " + strings.Join(others, ", ") + " (" + pin.Error() + ")"
		}
		return contracts.PeerUnreachable, name + " isn't answering: " + race.Error()
	case errors.As(err, &pin):
		// Something answered on the path in use, but it isn't the device
		// pinned there: a re-keyed device, or an impostor.
		return contracts.PeerUnreachable, "a different device answered: " + pin.Error()
	}
	return contracts.PeerUnreachable, unwrapDialError(err)
}

func startedCount(as []meshnet.Attempt) int {
	n := 0
	for _, a := range as {
		if a.Started {
			n++
		}
	}
	return n
}

// connsState is the Manager's transports, by connKey, and paths, by
// fingerprint.
type connsState struct {
	connsMu sync.Mutex
	conns   map[string]*peerConn
	paths   map[string]*pathState
}
