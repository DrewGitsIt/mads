package mesh

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/devicekey"
	"github.com/drewkidwell/mads/pkg/meshnet"
	"github.com/drewkidwell/mads/pkg/meshnet/mdns"
	"github.com/drewkidwell/mads/pkg/meshnet/tailscale"
	"github.com/drewkidwell/mads/pkg/paircode"
)

// Finding devices during Add devices (docs/MESH-NETWORK-DESIGN.md U4,
// S6). While the dialog polls GET /devices/discovered (at most ten
// minutes, stopped when it hasn't polled for 30 s), this Router searches
// this device's mesh networks for other mads devices whose own setup
// window is open:
//   - Tailscale: tailscaled's peer list; each online peer is asked GET
//     /mesh/discover at the mesh port (1.5 s, at most 16 at once). Only
//     devices in their window answer; the rest are counted, never shown.
//   - A LAN: the _mads._tcp mDNS browse; each advert's address is asked
//     the same.
//
// Discovery only suggests. Every answer comes over a TLS handshake in
// which this device presents its own key; an answer whose fingerprint
// isn't the key that handshake proved is dropped (someone answering as
// another device). Adding a found device dials it pinned to that key,
// shows a check code on both screens unless Tailscale vouches for both
// ends, and pins nothing here until this device's user confirms the code
// matched (POST /devices/pair/{id}/confirm).

// Finder finds mads devices on one network for a search round
// (meshnet.Finder's shape): it sends what it finds and closes the channel
// when the round is done.
type Finder interface {
	meshnet.Finder
	// Searches reports whether it searches n.
	Searches(n meshnet.Network) bool
}

// hiddenCounter is a Finder that knows how many devices on the network it
// didn't show ("3 other devices on your tailnet aren't shown").
type hiddenCounter interface {
	Hidden() int
}

// Search limits.
const (
	searchMax      = contracts.DiscoverableMax
	searchIdle     = 30 * time.Second
	searchRound    = 5 * time.Second
	discoverWithin = 1500 * time.Millisecond
	probeParallel  = 16
	maxFound       = 64
)

type search struct {
	started time.Time
	cancel  context.CancelFunc

	mu          sync.Mutex
	polled      time.Time
	done        bool
	found       map[devicekey.Fingerprint]*foundDevice
	hidden      int
	hiddenKnown bool
	networks    []meshnet.Network
}

type foundDevice struct {
	meshnet.Found
	NetworkLabel string
	seen         time.Time
}

// DiscoveredDevice is one row of GET /devices/discovered.
type DiscoveredDevice struct {
	Name        string `json:"name"`
	Platform    string `json:"platform,omitempty"`
	Fingerprint string `json:"fingerprint"`
	Short       string `json:"short"`
	// Network is the id of the network it was found on, NetworkLabel its
	// label ("Tailscale"), Via how ("tailscale", "mdns").
	Network      string `json:"network"`
	NetworkLabel string `json:"network_label,omitempty"`
	Via          string `json:"via"`
	// Paired: already paired here (PeerName is its row's name).
	Paired   bool   `json:"paired"`
	PeerName string `json:"peer_name,omitempty"`
	// Pairing is both sides' view (docs/MESH-NETWORK-DESIGN.md F6, R5):
	// none, both, here_only (it no longer has this device paired) or
	// there_only (it still has this device from an earlier pairing).
	Pairing string `json:"pairing"`
	// Compatible: it speaks this build's mesh API.
	Compatible bool      `json:"compatible"`
	SeenAt     time.Time `json:"seen_at"`
}

// DiscoveredList is GET /devices/discovered.
type DiscoveredList struct {
	Searching bool      `json:"searching"`
	StartedAt time.Time `json:"started_at"`
	// Networks are the networks being searched.
	Networks []NetworkRef       `json:"networks"`
	Devices  []DiscoveredDevice `json:"devices"`
	// HiddenCount is the tailnet peers that aren't mads devices in their
	// setup window; HiddenKnown says whether it could be counted (only the
	// tailnet's peer list says).
	HiddenCount int  `json:"hidden_count"`
	HiddenKnown bool `json:"hidden_known"`
}

// NetworkRef names a network.
type NetworkRef struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Kind  string `json:"kind,omitempty"`
}

// searchNetworks is what to search: this device's mesh networks that it's
// on now; with none chosen yet, every VPN and LAN it's on. only, when
// set, narrows to one.
func (m *Manager) searchNetworks(ctx context.Context, only string) []meshnet.Network {
	local := m.localNetworks()
	settings, err := m.data.MeshSettings(ctx)
	chosen := map[string]bool{}
	if err == nil {
		for _, c := range settings.Chosen() {
			chosen[c.ID] = true
		}
	}
	var out []meshnet.Network
	for _, n := range local {
		switch {
		case only != "":
			if n.ID == only {
				out = append(out, n)
			}
		case len(chosen) > 0:
			if chosen[n.ID] {
				out = append(out, n)
			}
		case n.Kind != meshnet.KindPublic:
			out = append(out, n)
		}
	}
	return out
}

func (m *Manager) finders() []Finder {
	if m.opts.Finders != nil {
		return m.opts.Finders
	}
	return []Finder{&tailnetFinder{m: m}, &mdnsFinder{m: m}}
}

// discovered serves GET /devices/discovered: it starts a search (or keeps
// the one running) and answers what it's found so far.
func (m *Manager) discovered(w http.ResponseWriter, r *http.Request) {
	now := m.opts.Now()
	bg := m.bgCtx()
	// Read before the search is published (another poll reads networks).
	nets := m.searchNetworks(r.Context(), r.URL.Query().Get("network"))
	m.mu.Lock()
	s := m.search
	if s == nil || s.isDone() {
		ctx, cancel := context.WithTimeout(bg, searchMax)
		s = &search{started: now, cancel: cancel, polled: now, found: map[devicekey.Fingerprint]*foundDevice{}, networks: nets}
		m.search = s
		m.mu.Unlock()
		go m.runSearch(ctx, s)
	} else {
		m.mu.Unlock()
	}
	s.mu.Lock()
	s.polled = now
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, m.discoveredList(r.Context(), s))
}

func (s *search) isDone() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.done
}

// stopSearch ends the search in progress (the dialog closed).
func (m *Manager) stopSearch() {
	m.mu.Lock()
	s := m.search
	m.search = nil
	m.mu.Unlock()
	if s != nil {
		s.cancel()
	}
}

func (m *Manager) runSearch(ctx context.Context, s *search) {
	defer func() {
		s.mu.Lock()
		s.done = true
		s.mu.Unlock()
		s.cancel()
	}()
	finders := m.finders()
	for round := 0; ; round++ {
		hidden, known := 0, false
		var wg sync.WaitGroup
		var hmu sync.Mutex
		for _, n := range s.networks {
			for _, f := range finders {
				if !f.Searches(n) {
					continue
				}
				wg.Add(1)
				go func() {
					defer wg.Done()
					rctx, cancel := context.WithTimeout(ctx, searchRound)
					defer cancel()
					ch, err := f.Find(rctx, n)
					if err != nil {
						return
					}
					for fd := range ch {
						s.add(fd, n, m.opts.Now())
					}
					if hc, ok := f.(hiddenCounter); ok {
						hmu.Lock()
						hidden += hc.Hidden()
						known = true
						hmu.Unlock()
					}
				}()
			}
		}
		wg.Wait()
		s.mu.Lock()
		s.hidden, s.hiddenKnown = hidden, known
		idle := m.opts.Now().Sub(s.polled) > searchIdle
		s.mu.Unlock()
		if idle {
			return
		}
		pause := m.opts.SearchBackoff[min(round, len(m.opts.SearchBackoff)-1)]
		select {
		case <-ctx.Done():
			return
		case <-time.After(pause):
		}
	}
}

func (s *search) add(f meshnet.Found, n meshnet.Network, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.found[f.Fingerprint]; !ok && len(s.found) >= maxFound {
		return
	}
	s.found[f.Fingerprint] = &foundDevice{Found: f, NetworkLabel: n.Label, seen: now}
}

func (m *Manager) discoveredList(ctx context.Context, s *search) DiscoveredList {
	peers, _ := m.data.Peers(ctx)
	byFP := map[string]contracts.Peer{}
	for _, p := range peers {
		if p.Fingerprint != "" {
			byFP[p.Fingerprint] = p
		}
	}
	now := m.opts.Now()
	fresh := map[string]bool{}
	for fp, p := range byFP {
		if at := m.pairedAt(p); !at.IsZero() && now.Sub(at) < pairGuard {
			fresh[fp] = true
		}
	}
	self := ""
	if m.opts.Identity != nil {
		self = string(m.opts.Identity.Fingerprint())
	}
	checked := map[string]string{}
	m.mu.Lock()
	for fp, t := range m.there {
		checked[fp] = t.state
	}
	m.mu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	out := DiscoveredList{Searching: !s.done, StartedAt: s.started, Networks: []NetworkRef{}, Devices: []DiscoveredDevice{},
		HiddenCount: s.hidden, HiddenKnown: s.hiddenKnown}
	for _, n := range s.networks {
		out.Networks = append(out.Networks, NetworkRef{ID: n.ID, Label: n.Label, Kind: string(n.Kind)})
	}
	for fp, f := range s.found {
		if string(fp) == self {
			continue
		}
		row, paired := byFP[string(fp)]
		name := row.Name
		out.Devices = append(out.Devices, DiscoveredDevice{
			Name: paircode.CleanText(f.Name), Platform: f.Platform, Fingerprint: string(fp), Short: fp.Short(),
			Network: f.Network, NetworkLabel: f.NetworkLabel, Via: f.Via, Paired: paired, PeerName: name,
			Pairing:    pairingOf(paired, f.PairedWithYou, checked[string(fp)], fresh[string(fp)]),
			Compatible: f.MeshAPI == MeshAPI, SeenAt: f.seen,
		})
	}
	slices.SortFunc(out.Devices, func(a, b DiscoveredDevice) int { return strings.Compare(a.Name+a.Fingerprint, b.Name+b.Fingerprint) })
	return out
}

// --- Probing /mesh/discover ------------------------------------------------------------

// Discover is a /mesh/discover answer.
type Discover struct {
	Mads        int    `json:"mads"`
	Name        string `json:"name"`
	Platform    string `json:"platform"`
	Fingerprint string `json:"fingerprint"`
	MeshAPI     int    `json:"mesh_api"`
	// PairedWithYou: it has this device's key paired (F6 R5); nil from an
	// older build, which doesn't say.
	PairedWithYou *bool `json:"paired_with_you,omitempty"`
}

// errNotDiscoverable is an address that didn't answer as a mads device in
// its setup window (no listener, not mads, outside its window, or an
// answer that doesn't match its own handshake).
var errNotDiscoverable = errors.New("not a mads device with its setup open")

// discoverAt asks addr for /mesh/discover over TLS, presenting this
// device's key and accepting any device key, and returns the answer only
// if its fingerprint is the key the handshake proved (S6: spoofed hosts).
func (m *Manager) discoverAt(ctx context.Context, c meshnet.Candidate) (Discover, error) {
	if m.opts.Identity == nil {
		return Discover{}, errNoIdentity
	}
	ctx, cancel := context.WithTimeout(ctx, discoverWithin)
	defer cancel()
	raw, err := m.adapter.Dial(ctx, "", c)
	if err != nil {
		return Discover{}, err
	}
	defer func() { _ = raw.Close() }()
	cert, err := m.opts.Identity.Certificate()
	if err != nil {
		return Discover{}, err
	}
	var proved devicekey.Fingerprint
	cfg := &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{cert},
		NextProtos:   []string{"http/1.1"},
		// Discovery pins nothing: the key the handshake proves is recorded
		// and checked against the answer, then pinned for the request only.
		InsecureSkipVerify: true, //nolint:gosec // G402: the device key is checked in VerifyConnection, then against the answer
		VerifyConnection: func(cs tls.ConnectionState) error {
			fp, err := devicekey.PeerFingerprint(cs)
			proved = fp
			return err
		},
	}
	tc := tls.Client(raw, cfg)
	if err := tc.HandshakeContext(ctx); err != nil {
		return Discover{}, err
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = tc.SetDeadline(deadline)
	}
	req := "GET /mesh/discover HTTP/1.1\r\nHost: " + c.Addr + "\r\nUser-Agent: mads\r\nConnection: close\r\n\r\n"
	if _, err := io.WriteString(tc, req); err != nil {
		return Discover{}, err
	}
	resp, err := http.ReadResponse(bufio.NewReader(tc), nil)
	if err != nil {
		return Discover{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return Discover{}, errNotDiscoverable
	}
	var d Discover
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<10)).Decode(&d); err != nil || d.Mads != 1 {
		return Discover{}, errNotDiscoverable
	}
	fp, err := devicekey.ParseFingerprint(d.Fingerprint)
	if err != nil || fp != proved {
		// It answered as a device whose key it didn't prove.
		return Discover{}, errNotDiscoverable
	}
	d.Name, d.Platform = paircode.CleanText(d.Name), strings.ToLower(d.Platform)
	if !contracts.ValidPlatform(d.Platform) {
		d.Platform = ""
	}
	return d, nil
}

// meshPort is the mesh port other devices listen on (the same on every
// network: this device's own setting).
func (m *Manager) meshPort(ctx context.Context) int {
	if s, err := m.data.MeshSettings(ctx); err == nil {
		return s.MeshPort()
	}
	return meshnet.DefaultPort
}

// tailnetFinder is the Tailscale peer list, probed.
type tailnetFinder struct {
	m      *Manager
	mu     sync.Mutex
	hidden int
}

func (f *tailnetFinder) Searches(n meshnet.Network) bool {
	return n.ID == meshnet.TailscaleID && f.m.opts.Tailscale != nil
}

func (f *tailnetFinder) Hidden() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.hidden
}

func (f *tailnetFinder) Find(ctx context.Context, n meshnet.Network) (<-chan meshnet.Found, error) {
	st, err := f.m.opts.Tailscale.Status(ctx, true)
	if err != nil {
		return nil, err
	}
	port := f.m.meshPort(ctx)
	out := make(chan meshnet.Found, 16)
	f.mu.Lock()
	f.hidden = 0
	f.mu.Unlock()
	go func() {
		defer close(out)
		sem := make(chan struct{}, probeParallel)
		var wg sync.WaitGroup
		for _, peer := range st.Peers() {
			var ip netip.Addr
			for _, a := range peer.TailscaleIPs {
				if a.Is4() {
					ip = a
					break
				}
			}
			if !peer.Online || !ip.IsValid() {
				f.count()
				continue
			}
			c := meshnet.Candidate{Via: meshnet.DirectVia, Addr: netip.AddrPortFrom(ip, uint16(port)).String(), Kind: meshnet.KindOverlay, Label: "Tailscale", Network: meshnet.TailscaleID}
			// The slot first, then the goroutine (S7).
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				f.count()
				continue
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer func() { <-sem }()
				d, err := f.m.discoverAt(ctx, c)
				if err != nil {
					f.count()
					return
				}
				fp, _ := devicekey.ParseFingerprint(d.Fingerprint)
				select {
				case out <- meshnet.Found{Name: d.Name, Platform: d.Platform, Fingerprint: fp, Candidates: []meshnet.Candidate{c}, Network: n.ID, Via: "tailscale", MeshAPI: d.MeshAPI, PairedWithYou: d.PairedWithYou}:
				case <-ctx.Done():
				}
			}()
		}
		wg.Wait()
	}()
	return out, nil
}

func (f *tailnetFinder) count() {
	f.mu.Lock()
	f.hidden++
	f.mu.Unlock()
}

// mdnsFinder is the _mads._tcp browse on a LAN, probed, at most
// probeParallel at once (round-1 review S7). listen and probe are seams
// for tests (default: a real socket on the interface, discoverAt).
type mdnsFinder struct {
	m      *Manager
	listen func(iface string) (mdns.Conn, error)
	probe  func(ctx context.Context, c meshnet.Candidate) (Discover, error)
}

func (f *mdnsFinder) Searches(n meshnet.Network) bool {
	return n.Kind == meshnet.KindLAN && n.Iface != ""
}

func (f *mdnsFinder) Find(ctx context.Context, n meshnet.Network) (<-chan meshnet.Found, error) {
	listen, probe := f.listen, f.probe
	if listen == nil {
		listen = mdns.Listen
	}
	if probe == nil {
		probe = f.m.discoverAt
	}
	c, err := listen(n.Iface)
	if err != nil {
		return nil, err
	}
	bctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	entries, err := mdns.Browse(bctx, c)
	if err != nil {
		cancel()
		return nil, err
	}
	out := make(chan meshnet.Found, 16)
	go func() {
		defer cancel()
		defer close(out)
		var wg sync.WaitGroup
		sem := make(chan struct{}, probeParallel)
		for e := range entries {
			for _, ip := range e.SelfAddrs() {
				cand := meshnet.Candidate{Via: meshnet.DirectVia, Addr: netip.AddrPortFrom(ip, uint16(e.Port)).String(), Kind: meshnet.KindLAN, Label: n.Label, Network: n.ID}
				// The slot is taken before the goroutine starts: a flood of
				// adverts never piles up goroutines.
				select {
				case sem <- struct{}{}:
				case <-ctx.Done():
					continue
				}
				wg.Add(1)
				go func() {
					defer wg.Done()
					defer func() { <-sem }()
					d, err := probe(ctx, cand)
					if err != nil {
						return
					}
					fp, _ := devicekey.ParseFingerprint(d.Fingerprint)
					if short := e.TXT["fp"]; short != "" && short != fp.Short() {
						return // the advert and the device that answered disagree
					}
					select {
					case out <- meshnet.Found{Name: d.Name, Platform: d.Platform, Fingerprint: fp, Candidates: []meshnet.Candidate{cand}, Network: n.ID, Via: "mdns", MeshAPI: d.MeshAPI, PairedWithYou: d.PairedWithYou}:
					case <-ctx.Done():
					}
				}()
			}
		}
		wg.Wait()
	}()
	return out, nil
}

// --- Asking a found device to pair -------------------------------------------------------

// requestDiscovered serves POST /devices/discovered/{fp}/request: dial the
// found device pinned to the key it announced, and ask it to pair (mode
// discovered), with this device's nonce and, over Tailscale, its own
// WhoIs of the host. Answers the join, with the check code both screens
// show.
func (m *Manager) requestDiscovered(w http.ResponseWriter, r *http.Request) {
	fp, err := devicekey.ParseFingerprint(r.PathValue("fp"))
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "bad_fingerprint", err.Error())
		return
	}
	if m.opts.Identity == nil {
		writeError(w, http.StatusServiceUnavailable, "no_device_key", errNoIdentity.Error())
		return
	}
	m.mu.Lock()
	s := m.search
	m.mu.Unlock()
	var found *foundDevice
	if s != nil {
		s.mu.Lock()
		if f, ok := s.found[fp]; ok {
			v := *f
			found = &v
		}
		s.mu.Unlock()
	}
	if found == nil {
		writeError(w, http.StatusNotFound, "not_found", "that device isn't among the devices found; open Add devices again")
		return
	}
	if fp == m.opts.Identity.Fingerprint() {
		writeError(w, http.StatusUnprocessableEntity, "pair_self", "that's this device")
		return
	}
	peers, err := m.data.Peers(r.Context())
	if err != nil {
		writeDeviceDataError(w, err)
		return
	}
	// A device already paired here re-pairs (F6 R2, R4): approved, its row
	// here is updated in place.
	repair := ""
	for _, p := range peers {
		if p.Fingerprint == string(fp) {
			repair = p.ID
		}
	}
	st, err := m.startDiscoveredJoin(r.Context(), found, repair)
	var pe *pairCodeError
	switch {
	case errors.As(err, &pe):
		writeErrorExtra(w, pe.status, pe.code, pe.msg, pe.extra)
	case err != nil:
		writeError(w, http.StatusBadGateway, "pair_failed", err.Error())
	default:
		writeJSON(w, http.StatusAccepted, st)
	}
}

func (m *Manager) startDiscoveredJoin(ctx context.Context, f *foundDevice, repair string) (JoinStatus, error) {
	// Where the host reaches this device back, before the dial's clock starts (T1).
	selfAddrs, selfHosts, _ := m.joinerSelf(ctx)
	now := m.opts.Now().UTC()
	host := contracts.Peer{Name: f.Name, Fingerprint: string(f.Fingerprint)}
	for _, c := range f.Candidates {
		host.Addresses = append(host.Addresses, contracts.MeshAddress{NetAddress: meshnet.ToNet(c), Source: contracts.SourceDiscovered, AddedAt: now})
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	c, err := m.conn(ctx, host)
	if err != nil {
		return JoinStatus{}, retryableJoin(joinDialError(host, err), 0)
	}
	nonce, err := paircode.NewNonce()
	if err != nil {
		return JoinStatus{}, err
	}
	// Commit, then reveal (C1): the host picks its nonce before it sees
	// this device's, so neither end can steer the six digits.
	commit, err := paircode.Commit(nonce, m.opts.Identity.Fingerprint(), f.Fingerprint)
	if err != nil {
		return JoinStatus{}, err
	}
	vouch := m.vouchFor(ctx, c.host)
	body := map[string]any{
		"mode": contracts.PairModeDiscovered, "name": m.opts.SelfName, "platform": m.platform(ctx), "commit": commit, "tailscale_vouch": vouch,
	}
	body["hosts_sessions"] = selfHosts
	if len(selfAddrs) > 0 {
		body["addresses"] = selfAddrs
	}
	post := func(in any, want int) ([]byte, error) {
		raw, _ := json.Marshal(in)
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://"+c.host+"/mesh/pair", bytes.NewReader(raw))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := c.client.Do(req)
		if err != nil {
			return nil, retryableJoin(joinDialError(host, err), 0)
		}
		defer func() { _ = resp.Body.Close() }()
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		if resp.StatusCode != want {
			code, msg := errorBody(b)
			status := resp.StatusCode
			if status < 400 || status >= 500 {
				status = http.StatusBadGateway
			}
			if code == "pairing_refused" {
				code, msg = "not_discoverable", paircode.CleanText(f.Name)+" isn't adding devices now: open Add devices (or mads mesh setup) there"
			}
			return nil, retryableJoin(&pairCodeError{status: status, code: firstNonEmpty(paircode.CleanText(code), "pair_failed"), msg: firstNonEmpty(paircode.CleanText(msg), paircode.CleanText(f.Name)+" answered "+resp.Status)}, retryAfterOf(resp.Header))
		}
		return b, nil
	}
	b, err := post(body, http.StatusOK)
	if err != nil {
		return JoinStatus{}, err
	}
	var committed struct {
		CommitID  string `json:"commit_id"`
		HostNonce string `json:"host_nonce"`
	}
	if err := json.Unmarshal(b, &committed); err != nil || committed.CommitID == "" {
		return JoinStatus{}, &pairCodeError{status: http.StatusBadGateway, code: "pair_failed", msg: paircode.CleanText(f.Name) + " answered something that isn't a pairing request"}
	}
	// The host's nonce is fixed now; only then is this device's revealed.
	b, err = post(map[string]any{"mode": contracts.PairModeDiscovered, "commit_id": committed.CommitID, "nonce": nonce}, http.StatusCreated)
	if err != nil {
		return JoinStatus{}, err
	}
	var st struct {
		RequestID string              `json:"request_id"`
		State     contracts.PairState `json:"state"`
		ExpiresAt time.Time           `json:"expires_at"`
		Vouched   bool                `json:"vouched"`
	}
	if err := json.Unmarshal(b, &st); err != nil || st.RequestID == "" {
		return JoinStatus{}, &pairCodeError{status: http.StatusBadGateway, code: "pair_failed", msg: paircode.CleanText(f.Name) + " answered something that isn't a pairing request"}
	}
	st2 := struct{ HostNonce string }{committed.HostNonce}
	check, err := paircode.CheckCode(f.Fingerprint, m.opts.Identity.Fingerprint(), nonce, st2.HostNonce)
	if err != nil {
		return JoinStatus{}, &pairCodeError{status: http.StatusBadGateway, code: "pair_failed", msg: paircode.CleanText(f.Name) + " answered without its half of the check code"}
	}
	// Vouched only when both ends say so: the host's WhoIs of this device
	// (its answer) and this device's of the host (vouch).
	vouched := st.Vouched && vouch
	key := f.Fingerprint.Short() + ":" + st.RequestID
	j := &join{started: m.opts.Now(), host: host, mode: contracts.PairModeDiscovered, repair: repair, status: JoinStatus{
		ID: key, RequestID: st.RequestID, State: contracts.PairPending, ExpiresAt: st.ExpiresAt, Self: m.opts.Identity.Fingerprint().Short(),
		Host: PairHost{Name: paircode.CleanText(f.Name), Fingerprint: string(f.Fingerprint)}, Mode: contracts.PairModeDiscovered,
		CheckCode: check, Vouched: vouched, Network: f.NetworkLabel,
	}}
	j.via = c.host
	m.addJoin(key, j)
	go m.waitJoin(m.bgCtx(), j)
	return j.get(), nil
}

// vouchFor is this device's own WhoIs of the host at addr: "same tailnet
// user, not tagged", when addr is a tailnet address and tailscaled
// answers.
func (m *Manager) vouchFor(ctx context.Context, addr string) bool {
	if m.opts.Tailscale == nil {
		return false
	}
	// Reached over this device's own Tailscale network, not just an
	// address in Tailscale's range (round-1 review S3).
	if n, ok := m.networkOf(addr); !ok || n.ID != meshnet.TailscaleID {
		return false
	}
	st, err := m.opts.Tailscale.Status(ctx, false)
	if err != nil || st.Self == nil {
		return false
	}
	who, err := m.opts.Tailscale.WhoIs(ctx, addr)
	return err == nil && tailscale.Vouches(st.Self.UserID, who)
}

// adoptNetwork is joiner-first pairing (F2): with no mesh network chosen
// yet, the network this device reached the host over becomes its mesh
// network, once the host is pinned. A shared network (campus, hotel) is
// never adopted: the user chooses it in Set up mesh if they mean it. It
// answers the network adopted, or why not ("" when there was nothing to
// adopt).
func (m *Manager) adoptNetwork(ctx context.Context, addr string) (*NetworkRef, string) {
	if addr == "" {
		return nil, ""
	}
	s, err := m.data.MeshSettings(ctx)
	if err != nil || len(s.Chosen()) > 0 {
		return nil, ""
	}
	n, ok := m.networkOf(addr)
	if !ok || n.Kind == meshnet.KindPublic {
		return nil, ""
	}
	if n.Shared {
		log.Printf("mesh: not making %s this device's mesh network: it looks shared (choose it in Set up mesh to use it)", n.Label)
		return nil, "shared"
	}
	if n.Kind == meshnet.KindLAN && n.GatewayHW == "" {
		log.Printf("mesh: not making %s this device's mesh network: %s", n.Label, noGatewayHW)
		return nil, "gateway_unknown"
	}
	choice := contracts.NetworkChoice{ID: n.ID, Name: n.SSID, SSID: n.SSID, Listen: true, GatewayHW: n.GatewayHW, DecidedAt: m.opts.Now().UTC()}
	if n.ID == meshnet.TailscaleID {
		choice.TailnetCheck = true // on by default
	}
	s.Networks = append(s.Networks[:0:0], choice)
	if _, err := m.data.SetMeshSettings(ctx, s); err != nil {
		log.Printf("mesh: adopt %s as this device's mesh network: %v", n.Label, err)
		return nil, ""
	}
	log.Printf("mesh: this device's mesh network is now %s (it paired over it)", n.Label)
	return &NetworkRef{ID: n.ID, Label: n.Label, Kind: string(n.Kind)}, ""
}

// networkOf is the network of this device's that addr is on.
func (m *Manager) networkOf(addr string) (meshnet.Network, bool) {
	c := meshnet.Candidate{Via: meshnet.DirectVia, Addr: addr}
	local := m.localNetworks()
	if ip, ok := c.IP(); ok {
		// The longest prefix wins (R2): a LAN's /24 over a VPN's route.
		return meshnet.MostSpecific(local, ip)
	}
	if strings.HasSuffix(strings.ToLower(strings.TrimSuffix(c.Host(), ".")), ".ts.net") {
		for _, n := range local {
			if n.ID == meshnet.TailscaleID {
				return n, true
			}
		}
	}
	return meshnet.Network{}, false
}

// writeErrorExtra is writeError with more fields (the UI's tips).
func writeErrorExtra(w http.ResponseWriter, status int, code, msg string, extra map[string]any) {
	if len(extra) == 0 {
		writeError(w, status, code, msg)
		return
	}
	body := map[string]any{"code": code, "error": msg}
	for k, v := range extra {
		body[k] = v
	}
	writeJSON(w, status, body)
}
