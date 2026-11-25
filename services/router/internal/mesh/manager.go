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
	"net/netip"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/pkg/devicekey"
	"github.com/drewkidwell/mads/pkg/meshnet"
	"github.com/drewkidwell/mads/pkg/meshnet/tailscale"
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
	// ReadyWait bounds how long a pairing request waits for this device's
	// Mesh Service to report where it listens, when a mesh network is
	// chosen but nothing is reported yet (it starts listening a few
	// seconds after the choice); default 15 s. Past it, the addresses are
	// the chosen network's own, at the mesh port.
	ReadyWait time.Duration
	// JoinMaxWait bounds a join's wait whatever the host says (default
	// contracts.PairRequestTTL); JoinGrace is how long past the request's
	// expiry the joiner keeps asking (default 30 s).
	JoinMaxWait, JoinGrace time.Duration
	Now                    func() time.Time
	// AllowLoopback lets peers at 127.0.0.1 be dialled: tests and the e2e
	// harness only (meshnet.LoopbackAllowed refuses it otherwise).
	AllowLoopback bool
	// System is what this device's own networks are read from (S2's
	// prefixes, F3's filter); nil reads the machine (meshnet.OSSystem).
	// LocalNetworks, when set, is used instead of any detection (tests).
	System        *meshnet.System
	LocalNetworks func() []meshnet.Network
	// Race tunes F3's race (zero: 250 ms stagger, 3 s an attempt, 6 s).
	Race meshnet.RaceOptions
	// Tailscale is this machine's tailscaled LocalAPI, for the tailnet
	// finder, pairing's vouching and the sign-in prompt; nil: none.
	Tailscale *tailscale.Client
	// Finders find mads devices during Add devices (nil: the tailnet's
	// peer list and mDNS on LAN mesh networks).
	Finders []Finder
	// SearchBackoff is the pause after each Add devices search round (5 s,
	// then 15 s from then on): the search probes every tailnet peer each
	// round, so it backs off.
	SearchBackoff []time.Duration
	// NetworkEvery is how often this device's networks are re-read (10 s);
	// UpgradeEvery how often a better path is tried (60 s).
	NetworkEvery, UpgradeEvery time.Duration
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
	// there is whether each paired device has this one paired, as it last
	// answered, by fingerprint; pinnedAt when this Router last pinned or
	// re-paired each (halfpaired.go, F6).
	there    map[string]thereState
	pinnedAt map[string]time.Time
	// approvedAt: when this Router approved each key's pairing request,
	// by fingerprint; it isn't dialled for pairGuard after (heldOff).
	approvedAt map[string]time.Time
	// pairedThere: when each paired device was seen to have this one
	// paired since its row's pairing (F6 R4), also kept on the row.
	pairedThere map[string]time.Time

	// One pinned transport per peer address, one path per peer (dial.go).
	connsState

	// policy is S2, adapter the direct adapter dialling under it.
	policy  *meshnet.DialPolicy
	adapter *meshnet.Direct
	// local is this device's networks, as last read (localNetworks).
	// alive caches whether this device's Mesh Service answers (T5).
	aliveMu   sync.Mutex
	alive     aliveCheck
	localMu   sync.Mutex
	local     []meshnet.Network
	localAt   time.Time
	localRead bool
	// lastGoodAt is when each peer's last_good was last written.
	lastGoodAt map[string]time.Time
	// search is the Add devices search in progress (discovery.go).
	search *search
	// tsHelper reads tailscaled for this device's networks (nil without
	// Options.Tailscale).
	tsHelper *tailscale.Helper
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
	if opts.ReadyWait == 0 {
		opts.ReadyWait = 15 * time.Second
	}
	if opts.JoinMaxWait == 0 {
		opts.JoinMaxWait = contracts.PairRequestTTL
	}
	if opts.JoinGrace == 0 {
		opts.JoinGrace = 30 * time.Second
	}
	if len(opts.SearchBackoff) == 0 {
		opts.SearchBackoff = []time.Duration{5 * time.Second, 15 * time.Second}
	}
	if opts.NetworkEvery == 0 {
		opts.NetworkEvery = 10 * time.Second
	}
	if opts.UpgradeEvery == 0 {
		opts.UpgradeEvery = 60 * time.Second
	}
	if opts.AllowLoopback && !meshnet.LoopbackAllowed() {
		log.Printf("mesh: refusing to dial loopback peers: only test builds may (-mesh-allow-loopback)")
		opts.AllowLoopback = false
	}
	m := &Manager{data: data, opts: opts, peers: map[string]*peerState{}, index: map[string]string{}, localIDs: map[string]localCheck{}, localChecking: map[string]bool{},
		all: map[string]contracts.Peer{}, paired: map[string]*pairedState{}, remote: map[string]remoteSeen{}, joins: map[string]*join{}, removed: map[string]string{},
		there: map[string]thereState{}, pinnedAt: map[string]time.Time{}, approvedAt: map[string]time.Time{}, pairedThere: map[string]time.Time{},
		connsState: connsState{conns: map[string]*peerConn{}, paths: map[string]*pathState{}}, lastGoodAt: map[string]time.Time{}}
	m.policy = &meshnet.DialPolicy{Local: func() []netip.Prefix { return meshnet.Prefixes(m.localNetworks()) }, AllowLoopback: opts.AllowLoopback}
	m.adapter = &meshnet.Direct{Policy: m.policy}
	if opts.Tailscale != nil {
		m.tsHelper = &tailscale.Helper{Client: opts.Tailscale}
	}
	return m
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
	nets := time.NewTicker(m.opts.NetworkEvery)
	defer nets.Stop()
	upgrade := time.NewTicker(m.opts.UpgradeEvery)
	defer upgrade.Stop()
	for {
		select {
		case <-nets.C:
			// This device's own networks changed (home → campus, a VPN up
			// or down): re-race every peer at once (F3 step 7).
			if m.refreshLocal() {
				m.invalidateAll("this device's networks changed")
				m.ProbeAll(ctx)
			}
		case <-upgrade.C:
			m.upgradePaths(ctx)
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
	fps := map[string]bool{}
	for _, p := range all {
		fps[p.Fingerprint] = true
	}
	for fp := range m.there {
		if !fps[fp] {
			delete(m.there, fp)
		}
	}
	for fp := range m.approvedAt {
		if !fps[fp] {
			delete(m.approvedAt, fp)
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
		out = append(out, m.liveLocked(st))
	}
	slices.SortFunc(out, func(a, b contracts.PeerStatus) int { return strings.Compare(a.Name, b.Name) })
	return out
}

// liveLocked is st's status with its path, address states and diagnosis.
// m.mu must be held.
func (m *Manager) liveLocked(st *peerState) contracts.PeerStatus {
	s := m.overlayLocked(st.status())
	s.Path, s.Addresses = m.pathStatus(st.peer)
	s.Diagnosis = diagnosis(s, m.opts.Now())
	return s
}

// Status is one peer's live state, by id or name.
func (m *Manager) Status(idOrName string) (contracts.PeerStatus, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	st := m.lookupLocked(idOrName)
	if st == nil {
		return contracts.PeerStatus{}, false
	}
	return m.liveLocked(st), true
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
	// tailnet policy caps it lower (always false since it never does).
	Grant         string `json:"grant,omitempty"`
	TailnetCapped bool   `json:"tailnet_capped,omitempty"`
	// Addresses is where the host listens now (docs/MESH-NETWORK-DESIGN.md
	// F5): merged into its row. Absent from an older host's.
	Addresses []contracts.NetAddress `json:"addresses,omitempty"`
	// TailscaleKeyExpiry is the host's Tailscale key expiry, when on.
	TailscaleKeyExpiry *time.Time `json:"tailscale_key_expiry,omitempty"`
}

// Probe is one /mesh/hello result, classified.
type Probe struct {
	State  contracts.PeerReachability
	Reason string
	RTT    time.Duration
	Hello  Hello
	// Status is the hello's HTTP status; 0 when nothing answered. Code is
	// the error code of an answer that wasn't 200 (unpaired_device, ...).
	Status int
	Code   string
	// PinMismatch: a different key answered (on the path in use, or at
	// every address tried).
	PinMismatch bool
}

// ProbePeer calls p's /mesh/hello over its pinned connection and
// classifies the answer. It is also what POST /peers uses to vet a device
// before adding it. A peer with no pinned fingerprint is never dialled: it
// is unpaired.
func (m *Manager) ProbePeer(ctx context.Context, p contracts.Peer) Probe {
	// A failed hello on the path in use re-races at once (F3 step 7), once.
	for attempt := 0; ; attempt++ {
		probe, raced := m.probeOnce(ctx, p)
		if probe.Status != 0 || raced || attempt > 0 || probe.State == contracts.PeerUnpaired || p.Fingerprint == "" {
			return probe
		}
		m.invalidate(p.Fingerprint, firstNonEmpty(probe.Reason, "it stopped answering"))
	}
}

// probeOnce is one hello: the race (when there's no path in use) has its
// own 6 s; the hello itself, 4 s on a working connection.
func (m *Manager) probeOnce(ctx context.Context, p contracts.Peer) (Probe, bool) {
	c, raced, err := m.connFresh(ctx, p)
	if err != nil {
		state, reason := dialFailure(p, err)
		return Probe{State: state, Reason: reason, PinMismatch: isPinMismatch(err)}, true
	}
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	start := m.opts.Now()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+c.host+"/mesh/hello", nil)
	if err != nil {
		return Probe{State: contracts.PeerUnreachable, Reason: err.Error()}, raced
	}
	resp, err := c.client.Do(req)
	if err != nil {
		state, reason := dialFailure(p, err)
		return Probe{State: state, Reason: reason, PinMismatch: isPinMismatch(err)}, raced
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
		p2.Code = e.Code
		if resp.StatusCode == http.StatusForbidden {
			p2.State, p2.Reason = contracts.PeerDenied, firstNonEmpty(e.Error, e.Code, "refused")
			return p2, raced
		}
		p2.State, p2.Reason = contracts.PeerUnreachable, fmt.Sprintf("mesh service answered %d %s", resp.StatusCode, e.Error)
		return p2, raced
	}
	if err := json.Unmarshal(body, &p2.Hello); err != nil {
		p2.State, p2.Reason = contracts.PeerUnreachable, "not a mads Mesh Service (bad /mesh/hello)"
		return p2, raced
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
	if p.ID != "" && p2.Hello.MeshAPI == MeshAPI {
		m.noteHelloAddresses(ctx, p, p2.Hello.Addresses)
	}
	return p2, raced
}

// ProbeAll probes every peer once, concurrently: the devices this node
// views, and the paired ones it doesn't but can dial, to learn what each
// lets this node do there.
func (m *Manager) ProbeAll(ctx context.Context) {
	m.mu.Lock()
	peers := make([]contracts.Peer, 0, len(m.peers))
	for _, st := range m.peers {
		if m.heldOffLocked(st.peer) {
			continue
		}
		peers = append(peers, st.peer)
	}
	others := make([]contracts.Peer, 0, len(m.paired))
	now := m.opts.Now()
	for _, st := range m.paired {
		if hostsNothing(st.peer) && !st.checkedAt.IsZero() && now.Sub(st.checkedAt) < rareProbeEvery {
			continue
		}
		if m.heldOffLocked(st.peer) {
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
			m.noteThere(ctx, p, probe, false)
		}()
	}
	for _, p := range others {
		wg.Add(1)
		go func() {
			defer wg.Done()
			probe := m.ProbePeer(ctx, p)
			m.observePaired(p.ID, probe)
			m.noteRemote(ctx, p, probe)
			// A grant-only row whose device no longer has this one paired
			// goes (F6 R4).
			m.noteThere(ctx, p, probe, true)
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
	// Any settled change to connected, the first or back after an outage
	// (debounced), says which path it's on.
	firstConnect := change != nil && change.To == contracts.PeerConnected
	if firstConnect {
		if path, _ := m.pathStatus(st.peer); path != nil {
			change.Via = &contracts.NetAddress{Via: "direct", Addr: path.Addr, Kind: path.Kind, Label: path.Label}
		}
	}
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
