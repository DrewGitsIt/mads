// Package mesh is the viewer side of mesh mode (docs/MESH-MODE-DESIGN.md
// D3, D4): the Router's knowledge of the devices it views, how reachable
// each one is, which sessions live where, and the HTTP layer that routes
// the UI's requests to them.
//
// Peers are a Data Service registry (peer.* events); this package keeps a
// live copy, probes each peer's Mesh Service (/mesh/hello), logs settled
// reachability changes as peer.reachability_changed, and indexes peers'
// session ids so /sessions/{id}/... can be routed without the UI knowing
// where a session lives. Every connection to a peer is mutual TLS, pinned
// to the fingerprint on its row (dial.go).
package mesh

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/pkg/devicekey"
)

// MeshAPI is the mesh wire version this Router speaks; it must match the
// peer Mesh Service's /mesh/hello mesh_api (services/mesh server.MeshAPI),
// or the peer reads version_mismatch and isn't used. 2 is the live-only
// event bus (GET /events/head over the mesh).
const MeshAPI = 2

// Options tunes a Manager. Zero values get the defaults in NewManager.
type Options struct {
	// SelfName labels this node's own sessions on GET /sessions?scope=all.
	SelfName string
	// ProbeInterval is how often each peer's /mesh/hello is called.
	ProbeInterval time.Duration
	// Debounce is how long a new reachability must hold before it's
	// logged (D3), so a flapping relayed link doesn't flood the log.
	Debounce time.Duration
	// IndexInterval is how often a connected peer's session list is
	// re-read to keep the session index current.
	IndexInterval time.Duration
	// Identity is this device's key, presented to every peer as its TLS
	// client certificate: what a peer's Mesh Service identifies this node
	// by (docs/MESH-AUTH-DESIGN.md D2). No local credential is ever sent to
	// a peer; this machine's install token never leaves it. Nil: no peer
	// can be reached.
	Identity *devicekey.Identity
	// Platform is what this device reports as its platform when it pairs
	// with a host (default runtime.GOOS).
	Platform string
	// PairPoll is how often a pairing in progress asks the host for its
	// answer (default 2s).
	PairPoll time.Duration
	Now      func() time.Time
}

// Manager is the Router's live view of its mesh peers.
type Manager struct {
	data *dataclient.Client
	opts Options

	mu    sync.Mutex
	peers map[string]*peerState // by peer id
	// index maps a session id to the id of the peer that owns it.
	index map[string]string
	// localIDs remembers which indexed ids the local Data Service knows
	// (serveLinks' local-first check).
	localIDs map[string]localCheck
	// localChecking marks ids with a background local check in flight.
	localChecking map[string]bool
	// runCtx is Run's: background work (links refreshes, local checks)
	// ends with it. Nil before Run.
	runCtx context.Context

	// statusSource lists tailnet devices for Candidates; nil runs the
	// tailscale CLI.
	statusSource StatusSource

	// all is every row of the peer list, viewed or not, by id: Settings →
	// Paired devices (GET /devices).
	all map[string]contracts.Peer
	// paired is the rows this node doesn't view but can dial (paired, with
	// a mesh address, hosting sessions as far as it knows), probed only to
	// learn what they let this node do there.
	paired map[string]*pairedState
	// remote is what each device's hello last said (remote.go), newer
	// than the row's copy, which is only written when it changes.
	remote map[string]remoteSeen
	// joins are pairings this node started as the joiner (pairing.go).
	joins map[string]*join
	// self caches this node's own mesh.listening (pairing.go).
	self selfCache
	// reloadMu serializes Reload.
	reloadMu sync.Mutex
	// removed names devices removed everywhere, by fingerprint, so a
	// retry (the device gone here) still names it.
	removed map[string]string

	// One pinned transport per peer (dial.go).
	connsState
}

type peerState struct {
	peer contracts.Peer

	// settled is the logged reachability; pending is a different observed
	// state waiting out the debounce.
	settled      contracts.PeerReachability
	pending      contracts.PeerReachability
	pendingSince time.Time

	reason    string
	rtt       time.Duration
	commit    string
	access    []string
	checkedAt time.Time

	// sessions is the last session list read from the peer, served (as
	// unreachable) when a live read fails.
	sessions []json.RawMessage
	// links is the peer's link reads (serveLinks); nil until the first.
	links *peerLinks
}

// NewManager builds a Manager. Call Run to start it.
func NewManager(data *dataclient.Client, opts Options) *Manager {
	if opts.ProbeInterval == 0 {
		opts.ProbeInterval = 5 * time.Second
	}
	if opts.Debounce == 0 {
		opts.Debounce = 10 * time.Second
	}
	if opts.IndexInterval == 0 {
		opts.IndexInterval = 15 * time.Second
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.SelfName == "" {
		opts.SelfName = "this device"
	}
	if opts.Platform == "" {
		opts.Platform = runtime.GOOS
	}
	if opts.PairPoll == 0 {
		opts.PairPoll = 2 * time.Second
	}
	return &Manager{data: data, opts: opts, peers: map[string]*peerState{}, index: map[string]string{}, localIDs: map[string]localCheck{}, localChecking: map[string]bool{},
		all: map[string]contracts.Peer{}, paired: map[string]*pairedState{}, remote: map[string]remoteSeen{}, joins: map[string]*join{}, removed: map[string]string{},
		connsState: connsState{conns: map[string]*peerConn{}}}
}

// Run loads the peer list, follows peer.* events, and probes until ctx ends.
func (m *Manager) Run(ctx context.Context) {
	m.mu.Lock()
	m.runCtx = ctx
	m.mu.Unlock()
	go m.watchPeers(ctx)
	probe := time.NewTicker(m.opts.ProbeInterval)
	defer probe.Stop()
	index := time.NewTicker(m.opts.IndexInterval)
	defer index.Stop()
	for {
		select {
		case <-ctx.Done():
			// What a device lets this node do, heard but held back by the
			// write debounce, isn't lost with this process.
			flush, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
			m.flushRemote(flush)
			cancel()
			return
		case <-probe.C:
			m.ProbeAll(ctx)
		case <-index.C:
			m.RefreshIndex(ctx)
		}
	}
}

// watchPeers reloads the peer list at start and on every peer.* change, so
// adding or removing a device takes effect without a restart.
func (m *Manager) watchPeers(ctx context.Context) {
	backoff := watchBackoffMin
	for ctx.Err() == nil {
		// Cursor first, then the list: a change landing between the two is
		// both in the list and replayed, never in neither.
		since, cursorErr := m.data.LatestSeq(ctx, "", peerEvents)
		if err := m.Reload(ctx); err != nil {
			log.Printf("mesh: load peers: %v", err)
		}
		// Without a cursor (the event log unreadable), never follow from
		// the start of history, replaying every peer.* event: reload the
		// list on a short backoff until the cursor reads.
		var ch <-chan contracts.Envelope
		err := cursorErr
		if err == nil {
			ch, err = m.data.Stream(ctx, since, "", peerEvents)
		} else {
			backoff = min(backoff, watchCursorRetryMax)
		}
		if err == nil {
			started := time.Now()
			for range ch {
				// Only a stream that delivers (or lasted, below) resets the
				// backoff: one that opens and closes at once must not spin.
				backoff = watchBackoffMin
				if err := m.Reload(ctx); err != nil {
					log.Printf("mesh: reload peers: %v", err)
				}
				m.ProbeAll(ctx)
			}
			// A stream that held a while was healthy (a token rotation or a
			// Data Service restart ended it): retry soon, not after a
			// backoff grown over earlier endings.
			if time.Since(started) >= watchHealthyAfter {
				backoff = watchBackoffMin
			}
			err = errors.New("event stream ended")
		}
		log.Printf("mesh: follow peer events: %v (retrying in %s)", err, backoff)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, watchBackoffMax)
	}
}

// watchPeers' backoff: from Min, doubling to Max while subscriptions fail
// or end at once; a stream that lasted HealthyAfter resets it. Vars so a
// test can shrink them.
var (
	watchBackoffMin   = time.Second
	watchBackoffMax   = 30 * time.Second
	watchHealthyAfter = 5 * time.Second
	// watchCursorRetryMax bounds the wait while the cursor can't be read,
	// each retry a list reload.
	watchCursorRetryMax = 5 * time.Second
)

// peerEvents are the peer list's changes. A fresh subscription starts at
// the newest (dataclient.LatestSeq): Reload reads the current list right
// after, so replaying older peer.* history would only reload again.
var peerEvents = []string{"peer.created", "peer.updated", "peer.deleted"}

// Reload replaces the peer list from the Data Service, keeping the live
// state of peers that are still there. The list holds paired-only rows
// too: every row goes into all (Settings → Paired devices), but only
// devices this node views become peers, probed and read sessions from. A
// paired-only device (a phone) is the Mesh Service's to let in; one this
// node can dial (dialsForRemote) is only probed to learn what it lets this
// node do there.
func (m *Manager) Reload(ctx context.Context) error {
	// One at a time, read and apply together: a reload whose read began
	// before a change (the follow loop's) must not land after one that
	// read it (a rename's own), putting the old rows back.
	m.reloadMu.Lock()
	defer m.reloadMu.Unlock()
	list, err := m.data.Peers(ctx)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	next := make(map[string]*peerState, len(list))
	all := make(map[string]contracts.Peer, len(list))
	paired := map[string]*pairedState{}
	for _, p := range list {
		all[p.ID] = p
		if !p.IsViewed() {
			if dialsForRemote(p) {
				st, ok := m.paired[p.ID]
				if !ok {
					st = &pairedState{}
				}
				st.peer = p
				paired[p.ID] = st
			}
			continue
		}
		if st, ok := m.peers[p.ID]; ok {
			st.peer = p
			next[p.ID] = st
			continue
		}
		next[p.ID] = &peerState{peer: p, settled: contracts.PeerUnknown}
	}
	for sid, pid := range m.index {
		if _, ok := next[pid]; !ok {
			delete(m.index, sid)
		}
	}
	for id, st := range m.peers {
		if _, ok := next[id]; !ok {
			st.dropLinksLocked()
		}
	}
	m.peers = next
	m.all, m.paired = all, paired
	for id := range m.remote {
		if _, ok := all[id]; !ok {
			delete(m.remote, id)
		}
	}
	m.dropConns(next, paired)
	return nil
}

// HasPeers reports whether any peer is registered; with none, the HTTP
// layer is a pass-through.
func (m *Manager) HasPeers() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.peers) > 0
}

// Statuses is every peer with its live state, for GET /peers.
func (m *Manager) Statuses() []contracts.PeerStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]contracts.PeerStatus, 0, len(m.peers))
	for _, st := range m.peers {
		out = append(out, m.overlayLocked(st.status()))
	}
	slices.SortFunc(out, func(a, b contracts.PeerStatus) int { return strings.Compare(a.Name, b.Name) })
	return out
}

// Status is one peer's live state, by id or name.
func (m *Manager) Status(idOrName string) (contracts.PeerStatus, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	st := m.lookupLocked(idOrName)
	if st == nil {
		return contracts.PeerStatus{}, false
	}
	return m.overlayLocked(st.status()), true
}

func (st *peerState) status() contracts.PeerStatus {
	s := contracts.PeerStatus{
		Peer:         st.peer,
		Reachability: st.settled,
		Reason:       st.reason,
		RTTMillis:    st.rtt.Milliseconds(),
		MadsCommit:   st.commit,
		Access:       slices.Clone(st.access),
	}
	if !st.checkedAt.IsZero() {
		t := st.checkedAt
		s.CheckedAt = &t
	}
	return s
}

func (m *Manager) lookupLocked(idOrName string) *peerState {
	if st, ok := m.peers[idOrName]; ok {
		return st
	}
	for _, st := range m.peers {
		if st.peer.Name == idOrName {
			return st
		}
	}
	return nil
}

func (m *Manager) peer(idOrName string) (contracts.Peer, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	st := m.lookupLocked(idOrName)
	if st == nil {
		return contracts.Peer{}, false
	}
	return st.peer, true
}

// --- Reachability ---------------------------------------------------------

// Hello is a peer Mesh Service's GET /mesh/hello.
type Hello struct {
	Node       string   `json:"node"`
	DNSName    string   `json:"dns_name"`
	MadsCommit string   `json:"mads_commit"`
	MeshAPI    int      `json:"mesh_api"`
	Access     []string `json:"access"`
	// Device is the peer's device fingerprint (docs/MESH-AUTH-DESIGN.md
	// D1).
	Device string `json:"device,omitempty"`
	// Platform and HostsSessions are what the peer reports about itself.
	Platform      string `json:"platform,omitempty"`
	HostsSessions *bool  `json:"hosts_sessions,omitempty"`
	// Grant and TailnetCapped: this node's grant there, and whether the
	// tailnet policy caps it lower.
	Grant         string `json:"grant,omitempty"`
	TailnetCapped bool   `json:"tailnet_capped,omitempty"`
}

// Probe is one /mesh/hello result, classified.
type Probe struct {
	State  contracts.PeerReachability
	Reason string
	RTT    time.Duration
	Hello  Hello
	// Status is the hello's HTTP status; 0 when nothing answered.
	Status int
}

// ProbePeer calls p's /mesh/hello over its pinned connection and
// classifies the answer. It is also what POST /peers uses to vet a device
// before adding it. A peer with no pinned fingerprint is never dialled: it
// is unpaired.
func (m *Manager) ProbePeer(ctx context.Context, p contracts.Peer) Probe {
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	start := m.opts.Now()
	req, client, err := m.peerRequest(ctx, http.MethodGet, p, "/mesh/hello", nil)
	if err != nil {
		state, reason := dialFailure(p, err)
		return Probe{State: state, Reason: reason}
	}
	resp, err := client.Do(req)
	if err != nil {
		state, reason := dialFailure(p, err)
		return Probe{State: state, Reason: reason}
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	p2 := Probe{RTT: m.opts.Now().Sub(start), Status: resp.StatusCode}
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Code  string `json:"code"`
			Error string `json:"error"`
		}
		_ = json.Unmarshal(body, &e)
		if resp.StatusCode == http.StatusForbidden {
			p2.State, p2.Reason = contracts.PeerDenied, firstNonEmpty(e.Error, e.Code, "refused")
			return p2
		}
		p2.State, p2.Reason = contracts.PeerUnreachable, fmt.Sprintf("mesh service answered %d %s", resp.StatusCode, e.Error)
		return p2
	}
	if err := json.Unmarshal(body, &p2.Hello); err != nil {
		p2.State, p2.Reason = contracts.PeerUnreachable, "not a mads Mesh Service (bad /mesh/hello)"
		return p2
	}
	switch {
	case p2.Hello.MeshAPI != MeshAPI:
		p2.State = contracts.PeerVersionMismatch
		p2.Reason = fmt.Sprintf("peer speaks mesh_api %d, this node %d (peer is on %s)", p2.Hello.MeshAPI, MeshAPI, firstNonEmpty(p2.Hello.MadsCommit, "an unknown build"))
	case !slices.Contains(p2.Hello.Access, "view"):
		p2.State = contracts.PeerDenied
		p2.Reason = firstNonEmpty(p2.Hello.Node, "the peer") + " grants this device no \"view\""
	default:
		p2.State = contracts.PeerConnected
	}
	return p2
}

// ProbeAll probes every peer once, concurrently: the devices this node
// views, and the paired ones it doesn't but can dial, to learn what each
// lets this node do there.
func (m *Manager) ProbeAll(ctx context.Context) {
	m.mu.Lock()
	peers := make([]contracts.Peer, 0, len(m.peers))
	for _, st := range m.peers {
		peers = append(peers, st.peer)
	}
	others := make([]contracts.Peer, 0, len(m.paired))
	now := m.opts.Now()
	for _, st := range m.paired {
		if hostsNothing(st.peer) && !st.checkedAt.IsZero() && now.Sub(st.checkedAt) < rareProbeEvery {
			continue
		}
		others = append(others, st.peer)
	}
	m.mu.Unlock()
	var wg sync.WaitGroup
	for _, p := range peers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			probe := m.ProbePeer(ctx, p)
			m.observe(ctx, p.ID, probe)
			m.noteRemote(ctx, p, probe)
		}()
	}
	for _, p := range others {
		wg.Add(1)
		go func() {
			defer wg.Done()
			probe := m.ProbePeer(ctx, p)
			m.observePaired(p.ID, probe)
			m.noteRemote(ctx, p, probe)
		}()
	}
	wg.Wait()
}

// observe folds one probe into a peer's state and logs a settled change.
func (m *Manager) observe(ctx context.Context, peerID string, p Probe) {
	now := m.opts.Now()
	m.mu.Lock()
	st, ok := m.peers[peerID]
	if !ok {
		m.mu.Unlock()
		return
	}
	st.reason, st.rtt, st.checkedAt = p.Reason, p.RTT, now
	if p.State == contracts.PeerConnected {
		st.commit, st.access = p.Hello.MadsCommit, p.Hello.Access
	}

	var change *contracts.PeerReachabilityChange
	switch {
	case p.State == st.settled:
		st.pending = ""
	case st.settled == contracts.PeerUnknown && (p.State == contracts.PeerConnected || p.State == contracts.PeerUnpaired):
		// The first successful connect is logged at once: it's the
		// "it works" line, and there is nothing to debounce against. Nor
		// is there for unpaired, which is this node's own configuration,
		// not a flaky link.
		change = st.settle(p)
	case st.pending != p.State:
		st.pending, st.pendingSince = p.State, now
		if m.opts.Debounce <= 0 {
			change = st.settle(p)
		}
	case now.Sub(st.pendingSince) >= m.opts.Debounce:
		change = st.settle(p)
	}
	firstConnect := change != nil && change.To == contracts.PeerConnected
	m.mu.Unlock()

	if change != nil {
		payload, _ := json.Marshal(change)
		err := m.data.Append(ctx, &contracts.Envelope{Type: contracts.PeerReachabilityChanged, Actor: "router", Payload: payload})
		if err != nil {
			log.Printf("mesh: log %s %s -> %s: %v", change.Peer, change.From, change.To, err)
		}
		log.Printf("mesh: peer %s %s -> %s %s", change.Peer, change.From, change.To, change.Reason)
	}
	if firstConnect {
		m.refreshPeerIndex(ctx, peerID)
	}
}

func (st *peerState) settle(p Probe) *contracts.PeerReachabilityChange {
	c := &contracts.PeerReachabilityChange{
		PeerID: st.peer.ID, Peer: st.peer.Name,
		From: st.settled, To: p.State, Reason: p.Reason, RTTMillis: p.RTT.Milliseconds(),
	}
	st.settled, st.pending = p.State, ""
	return c
}

// Connected reports whether a peer's settled state is connected.
func (m *Manager) Connected(peerID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	st, ok := m.peers[peerID]
	return ok && st.settled == contracts.PeerConnected
}

// --- Session index --------------------------------------------------------

// Owner returns the peer that owns a session id, if indexed.
func (m *Manager) Owner(sessionID string) (contracts.Peer, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	pid, ok := m.index[sessionID]
	if !ok {
		return contracts.Peer{}, false
	}
	st, ok := m.peers[pid]
	if !ok {
		return contracts.Peer{}, false
	}
	return st.peer, true
}

// RefreshIndex re-reads every connected peer's session list.
func (m *Manager) RefreshIndex(ctx context.Context) {
	m.mu.Lock()
	var ids []string
	for id, st := range m.peers {
		if st.settled == contracts.PeerConnected {
			ids = append(ids, id)
		}
	}
	m.mu.Unlock()
	for _, id := range ids {
		m.refreshPeerIndex(ctx, id)
	}
}

func (m *Manager) refreshPeerIndex(ctx context.Context, peerID string) {
	p, ok := m.peer(peerID)
	if !ok {
		return
	}
	if _, err := m.peerSessions(ctx, p, ""); err != nil {
		log.Printf("mesh: index %s: %v", p.Name, err)
	}
}

// peerSessions reads a peer's GET /sessions (with query) and records the
// rows' ids in the index and the rows as the peer's last-known list.
func (m *Manager) peerSessions(ctx context.Context, p contracts.Peer, rawQuery string) ([]json.RawMessage, error) {
	ctx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	u := "/sessions"
	if rawQuery != "" {
		u += "?" + rawQuery
	}
	req, client, err := m.peerRequest(ctx, http.MethodGet, p, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, errors.New(unwrapDialError(err))
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET /sessions: %d", resp.StatusCode)
	}
	var rows []json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&rows); err != nil {
		return nil, fmt.Errorf("GET /sessions: %w", err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	st, ok := m.peers[p.ID]
	if !ok {
		return rows, nil
	}
	// A filtered read (rawQuery) only adds to the index; only the full
	// list may drop ids, and only this peer's.
	if rawQuery == "" {
		for sid, pid := range m.index {
			if pid == p.ID {
				delete(m.index, sid)
			}
		}
		st.sessions = rows
	}
	live := map[string]bool{}
	for _, row := range rows {
		var head struct {
			ID string `json:"id"`
		}
		if json.Unmarshal(row, &head) == nil && head.ID != "" {
			m.index[head.ID] = p.ID
			live[head.ID] = true
		}
	}
	if rawQuery == "" {
		st.pruneLinksLocked(live)
	}
	return rows, nil
}

// lastSessions is a peer's last-known session list.
func (m *Manager) lastSessions(peerID string) []json.RawMessage {
	m.mu.Lock()
	defer m.mu.Unlock()
	if st, ok := m.peers[peerID]; ok {
		return st.sessions
	}
	return nil
}

// Resolve finds which connected peer owns sessionID by asking each one, for
// an id that isn't indexed yet (a session created on the peer since the last
// index refresh). It indexes and returns the owner.
func (m *Manager) Resolve(ctx context.Context, sessionID string) (contracts.Peer, bool) {
	m.mu.Lock()
	var candidates []contracts.Peer
	for _, st := range m.peers {
		if st.settled == contracts.PeerConnected {
			candidates = append(candidates, st.peer)
		}
	}
	m.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	found := make(chan contracts.Peer, len(candidates))
	var wg sync.WaitGroup
	for _, p := range candidates {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req, client, err := m.peerRequest(ctx, http.MethodGet, p, "/sessions/"+sessionID, nil)
			if err != nil {
				return
			}
			resp, err := client.Do(req)
			if err != nil {
				return
			}
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				found <- p
			}
		}()
	}
	go func() { wg.Wait(); close(found) }()
	p, ok := <-found
	if ok {
		m.mu.Lock()
		m.index[sessionID] = p.ID
		m.mu.Unlock()
	}
	return p, ok
}

func unwrapDialError(err error) string {
	msg := err.Error()
	// "Get \"http://...\": dial tcp 100.x:17380: connect: connection refused"
	if i := strings.LastIndex(msg, "\": "); i >= 0 {
		msg = msg[i+3:]
	}
	return msg
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}
