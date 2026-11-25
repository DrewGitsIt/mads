package mesh

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"log"
	"slices"
	"strings"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/pkg/devicekey"
	"github.com/drewkidwell/mads/pkg/meshnet"
)

// --- This device's networks -------------------------------------------------------

func (m *Manager) system() meshnet.System {
	if m.opts.System != nil {
		return *m.opts.System
	}
	return meshnet.OSSystem()
}

// localNetworks is this device's networks now (read at most every
// NetworkEvery; a local read, nothing is sent).
func (m *Manager) localNetworks() []meshnet.Network {
	m.localMu.Lock()
	fresh := m.localRead && m.opts.Now().Sub(m.localAt) < m.opts.NetworkEvery
	nets := m.local
	m.localMu.Unlock()
	if fresh {
		return nets
	}
	m.refreshLocal()
	m.localMu.Lock()
	defer m.localMu.Unlock()
	return m.local
}

// refreshLocal re-reads this device's networks and reports whether they
// changed since the last read.
func (m *Manager) refreshLocal() bool {
	if m.opts.LocalNetworks != nil {
		nets := m.opts.LocalNetworks()
		m.localMu.Lock()
		defer m.localMu.Unlock()
		changed := m.localRead && !meshnet.SameNetworks(m.local, nets)
		m.local, m.localAt, m.localRead = nets, m.opts.Now(), true
		return changed
	}
	var helpers []meshnet.Helper
	if m.tsHelper != nil {
		helpers = append(helpers, m.tsHelper)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	nets, err := meshnet.Detect(ctx, m.system(), helpers...)
	if err != nil {
		log.Printf("mesh: read this device's networks: %v", err)
		return false
	}
	m.localMu.Lock()
	defer m.localMu.Unlock()
	changed := m.localRead && !meshnet.SameNetworks(m.local, nets)
	m.local, m.localAt, m.localRead = nets, m.opts.Now(), true
	return changed
}

// --- The path in use -------------------------------------------------------------------

// lastGoodEvery bounds how often an unchanged last_good is rewritten (its
// time refreshed): the live state is in memory; the row's copy only needs
// to survive a restart roughly current. A changed address is written at
// once.
const lastGoodEvery = time.Hour

// notePath records a race's winner: last_good on the row, when the
// address changed or the stored one is old, and a path change in the log
// ("Now reached via Tailscale (was home network)").
func (m *Manager) notePath(p contracts.Peer, a contracts.MeshAddress, prev string, rtt time.Duration) {
	now := m.opts.Now().UTC()
	m.mu.Lock()
	row, ok := m.all[p.ID]
	if !ok {
		row = p
	}
	last := m.lastGoodAt[p.ID]
	same := row.LastGood != nil && strings.EqualFold(row.LastGood.Addr, a.Addr)
	if same && (now.Sub(row.LastGood.At) < lastGoodEvery || now.Sub(last) < time.Minute) {
		m.mu.Unlock()
		return
	}
	m.lastGoodAt[p.ID] = now
	m.mu.Unlock()
	if prev != "" && !strings.EqualFold(prev, a.Addr) {
		log.Printf("mesh: %s now reached via %s at %s (was %s)", p.Name, firstNonEmpty(addrLabel(a), "an address"), a.Addr, prev)
		// A path change while connected is in the log too ("Now reached
		// via Tailscale (was home network)").
		var from contracts.NetAddress
		for _, c := range row.Candidates() {
			if strings.EqualFold(c.Addr, prev) {
				from = c.NetAddress
			}
		}
		from.Addr, from.Label = prev, firstNonEmpty(from.Label, addrLabel(contracts.MeshAddress{NetAddress: from}))
		to := a.NetAddress
		to.Label = firstNonEmpty(to.Label, addrLabel(a))
		change := contracts.PeerReachabilityChange{PeerID: p.ID, Peer: p.Name, From: contracts.PeerConnected, To: contracts.PeerConnected,
			Reason: "now reached via " + firstNonEmpty(to.Label, to.Addr), RTTMillis: rtt.Milliseconds(), Via: &to, ViaFrom: &from}
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			payload, _ := json.Marshal(change)
			if err := m.data.Append(ctx, &contracts.Envelope{Type: contracts.PeerReachabilityChanged, Actor: "router", Payload: payload}); err != nil {
				log.Printf("mesh: log %s's path change: %v", p.Name, err)
			}
		}()
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		err := m.editPeer(ctx, p.ID, func(cur *contracts.Peer) (bool, error) {
			if cur.Fingerprint != p.Fingerprint {
				return false, errNotEditable
			}
			if len(cur.Addresses) == 0 {
				// The row is changing anyway: its legacy address becomes a
				// list (the read-time migration made real).
				cur.Addresses = cur.Candidates()
			}
			found := false
			for i := range cur.Addresses {
				if strings.EqualFold(cur.Addresses[i].Addr, a.Addr) {
					t := now
					cur.Addresses[i].LastOKAt = &t
					found = true
				}
			}
			if !found {
				return false, errNotEditable
			}
			cur.LastGood = &contracts.PeerPath{Addr: a.Addr, At: now, RTT: rtt.Milliseconds()}
			return false, nil
		})
		if err != nil && !errors.Is(err, errNotEditable) && !errors.Is(err, dataclient.ErrNotFound) {
			log.Printf("mesh: record %s's last good address: %v", p.Name, err)
		}
	}()
}

// upgradePaths is F3 step 6: for each peer connected on an address that
// isn't its best on a network this device is on, try the better ones;
// one answering twice in a row takes new requests. The old connection
// keeps its streams (dropConns retires it within 10 minutes).
func (m *Manager) upgradePaths(ctx context.Context) {
	// Retired connections past their 10 minutes go on this clock too, not
	// only when the peer list changes.
	defer func() {
		m.mu.Lock()
		viewed, paired := m.peers, m.paired
		m.mu.Unlock()
		m.dropConns(viewed, paired)
	}()
	m.mu.Lock()
	var peers []contracts.Peer
	for _, st := range m.peers {
		peers = append(peers, st.peer)
	}
	for _, st := range m.paired {
		peers = append(peers, st.peer)
	}
	m.mu.Unlock()
	local := m.localNetworks()
	for _, p := range peers {
		if p.Fingerprint == "" || m.opts.Identity == nil {
			continue
		}
		ps := m.pathFor(p.Fingerprint)
		ps.mu.Lock()
		cur := ps.cur
		ps.mu.Unlock()
		if cur == "" {
			continue
		}
		var curCand meshnet.Candidate
		var better []meshnet.Candidate
		all := m.candidates(contracts.Peer{Addresses: p.Candidates()}) // network order, ignoring last_good
		for _, a := range all {
			c := meshnet.FromNet(a.NetAddress)
			if c.Via == "" {
				c.Via = meshnet.DirectVia
			}
			if strings.EqualFold(a.Addr, cur) {
				curCand = c
			}
		}
		for _, a := range all {
			c := meshnet.FromNet(a.NetAddress)
			if c.Via == "" {
				c.Via = meshnet.DirectVia
			}
			if strings.EqualFold(a.Addr, cur) || !meshnet.Better(c, curCand) || !meshnet.OnNetwork(c, local, m.policy, a.Source == contracts.SourceUser) {
				continue
			}
			better = append(better, c)
		}
		if len(better) == 0 {
			continue
		}
		cfg, err := m.pinnedConfig(p.Fingerprint)
		if err != nil {
			continue
		}
		user := map[string]bool{}
		for _, a := range all {
			user[strings.ToLower(a.Addr)] = a.Source == contracts.SourceUser
		}
		won, idx, _, err := meshnet.Race(ctx, better, m.opts.Race, func(actx context.Context, c meshnet.Candidate) (*tls.Conn, error) {
			return m.dialPinned(actx, devicekey.Fingerprint(p.Fingerprint), c, cfg, user[strings.ToLower(c.Addr)])
		}, func(tc *tls.Conn) { _ = tc.Close() })
		ps.mu.Lock()
		if err != nil {
			ps.upgrade = map[string]int{}
			ps.mu.Unlock()
			continue
		}
		addr := better[idx].Addr
		ps.upgrade[strings.ToLower(addr)]++
		ps.setState(addr, contracts.AddrWorks, "", m.opts.Now())
		if ps.upgrade[strings.ToLower(addr)] < 2 || !strings.EqualFold(ps.cur, cur) {
			ps.mu.Unlock()
			_ = won.Close()
			continue
		}
		// Answered twice in a row (Q8): new requests move to it.
		var row contracts.MeshAddress
		for _, a := range all {
			if strings.EqualFold(a.Addr, addr) {
				row = a
			}
		}
		ps.setState(cur, contracts.AddrWorks, "", m.opts.Now())
		ps.cur, ps.curAddr, ps.since, ps.upgrade = addr, row, m.opts.Now(), map[string]int{}
		ps.mu.Unlock()
		m.transport(p.Fingerprint, addr, user[strings.ToLower(addr)]).handoff(won)
		m.notePath(p, row, cur, 0)
	}
}

// pathStatus is a peer's path in use and each address's live state, for
// GET /peers and GET /devices (shown only in the Status tooltip and under
// Troubleshoot connection).
func (m *Manager) pathStatus(p contracts.Peer) (*contracts.PathStatus, []contracts.AddressStatus) {
	if p.Fingerprint == "" {
		return nil, nil
	}
	m.connsMu.Lock()
	ps := m.paths[p.Fingerprint]
	m.connsMu.Unlock()
	cands := p.Candidates()
	out := make([]contracts.AddressStatus, 0, len(cands))
	var path *contracts.PathStatus
	if ps != nil {
		ps.mu.Lock()
		defer ps.mu.Unlock()
		if ps.cur != "" {
			path = &contracts.PathStatus{Addr: ps.cur, Kind: string(meshnet.InferKind(meshnet.FromNet(ps.curAddr.NetAddress))), Label: addrLabel(ps.curAddr),
				RTT: ps.rtt.Milliseconds(), Since: ps.since}
		}
	}
	for _, a := range cands {
		s := contracts.AddressStatus{MeshAddress: a, State: contracts.AddrUntried}
		if ps != nil {
			if live, ok := ps.states[strings.ToLower(a.Addr)]; ok {
				s.State, s.Error = live.state, live.err
				if !live.lastOK.IsZero() && (s.LastOKAt == nil || live.lastOK.After(*s.LastOKAt)) {
					t := live.lastOK
					s.LastOKAt = &t
				}
			}
			if strings.EqualFold(ps.cur, a.Addr) {
				s.State, s.Error = contracts.AddrInUse, ""
			}
		}
		if s.Kind == "" {
			s.Kind = string(meshnet.InferKind(meshnet.FromNet(a.NetAddress)))
		}
		out = append(out, s)
	}
	return path, out
}

// --- What a host's hello says about its addresses (F5) --------------------------

// mergeAddresses is the row's new address list after a hello reporting
// reported: addresses the host reported replace every earlier
// host-reported one (hello, code, joiner, discovered, introduced, legacy);
// addresses this device's user added stay; last_good stays even when no
// longer reported. changed says whether anything differs (the row is
// written only then).
func mergeAddresses(cur []contracts.MeshAddress, lastGood *contracts.PeerPath, reported []contracts.NetAddress, pol *meshnet.DialPolicy, now time.Time) ([]contracts.MeshAddress, bool) {
	clean := meshnet.CleanNets(reported)
	var rep []contracts.NetAddress
	for _, a := range clean {
		if pol.CheckAddress(a.Addr) == nil {
			rep = append(rep, a)
		}
	}
	if len(rep) == 0 {
		return cur, false // an older host, or nothing usable: nothing changes
	}
	prev := map[string]contracts.MeshAddress{}
	for _, a := range cur {
		prev[strings.ToLower(a.Addr)] = a
	}
	var out []contracts.MeshAddress
	seen := map[string]bool{}
	for _, a := range cur {
		if a.Source == contracts.SourceUser {
			out = append(out, a)
			seen[strings.ToLower(a.Addr)] = true
		}
	}
	for _, a := range rep {
		k := strings.ToLower(a.Addr)
		if seen[k] || len(out) == contracts.MaxPeerAddresses {
			continue
		}
		e := contracts.MeshAddress{NetAddress: a, Source: contracts.SourceHello, AddedAt: now}
		if old, ok := prev[k]; ok {
			e.AddedAt, e.LastOKAt = old.AddedAt, old.LastOKAt
		}
		out = append(out, e)
		seen[k] = true
	}
	if lastGood != nil {
		k := strings.ToLower(lastGood.Addr)
		if old, ok := prev[k]; ok && !seen[k] {
			if len(out) == contracts.MaxPeerAddresses {
				out = out[:len(out)-1]
			}
			out = append(out, old)
		}
	}
	return out, !sameAddresses(cur, out)
}

func sameAddresses(a, b []contracts.MeshAddress) bool {
	return slices.EqualFunc(a, b, func(x, y contracts.MeshAddress) bool {
		return strings.EqualFold(x.Addr, y.Addr) && x.Via == y.Via && x.Kind == y.Kind && x.Label == y.Label && x.Network == y.Network && x.Source == y.Source
	})
}

// noteHelloAddresses folds a hello's addresses into p's row, writing only
// a change.
func (m *Manager) noteHelloAddresses(ctx context.Context, p contracts.Peer, reported []contracts.NetAddress) {
	if len(reported) == 0 {
		return
	}
	base := p.Candidates()
	if _, changed := mergeAddresses(base, p.LastGood, reported, m.policy, m.opts.Now().UTC()); !changed {
		return
	}
	err := m.editPeer(ctx, p.ID, func(cur *contracts.Peer) (bool, error) {
		if cur.Fingerprint != p.Fingerprint {
			return false, errNotEditable
		}
		next, changed := mergeAddresses(cur.Candidates(), cur.LastGood, reported, m.policy, m.opts.Now().UTC())
		if !changed {
			return false, errNotEditable
		}
		cur.Addresses = next
		return false, nil
	})
	switch {
	case err == nil:
		log.Printf("mesh: %s reports new addresses", p.Name)
	case errors.Is(err, errNotEditable), errors.Is(err, dataclient.ErrNotFound):
	default:
		log.Printf("mesh: record %s's addresses: %v", p.Name, err)
	}
}
