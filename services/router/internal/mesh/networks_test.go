package mesh

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/pkg/devicekey"
	"github.com/drewkidwell/mads/pkg/meshnet"
	"github.com/drewkidwell/mads/pkg/meshnet/mdns"
	"github.com/drewkidwell/mads/pkg/meshnet/tailscale"
	"github.com/drewkidwell/mads/pkg/meshnet/tailscale/tailscaletest"
	"github.com/drewkidwell/mads/pkg/paircode"
	"github.com/drewkidwell/mads/services/mesh/meshtest"
)

// hostNode is a host whose Mesh Service listens on n "networks" (ports on
// 127.0.0.1).
func hostNode(t *testing.T, name string, networks int) *node {
	t.Helper()
	n := newNode(t, name, false)
	g := datatestOf(t, n)
	n.mesh = meshtest.Start(t, meshtest.Options{Identity: n.id, HostName: name, DataURL: g.url, Token: g.token, RouterURL: n.router.URL, Access: []string{"admin"}, Networks: networks})
	return n
}

type guardedRef struct{ url, token string }

// datatestOf recovers a node's Data Service (newNode keeps only a
// client): the client's base and token are what meshtest needs.
func datatestOf(t *testing.T, n *node) guardedRef {
	t.Helper()
	if n.dataURL == "" {
		t.Fatal("node without a Data Service URL")
	}
	return guardedRef{n.dataURL, n.token}
}

// viewRow is a viewed, pinned row for host with the given addresses.
func viewRow(host *node, addrs ...contracts.MeshAddress) *contracts.Peer {
	yes := true
	return &contracts.Peer{Name: host.name, Fingerprint: string(host.id.Fingerprint()), Viewed: &yes, Addresses: addrs}
}

func maddr(addr, kind string, src contracts.AddressSource) contracts.MeshAddress {
	return contracts.MeshAddress{NetAddress: contracts.NetAddress{Via: "direct", Addr: addr, Kind: kind}, Source: src, AddedAt: time.Now().UTC()}
}

// deadAddr is a loopback address nothing listens on.
func deadAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	a := l.Addr().String()
	_ = l.Close()
	return a
}

// The race (F3): a dead address first in rank fails at once and the next
// starts without waiting; the first pinned handshake wins, becomes the
// path in use, and is written as last_good. Dropping that network moves
// the viewer to the other on its next request.
func TestViewerRacesAndMovesBetweenNetworks(t *testing.T) {
	host := hostNode(t, "pop-os", 2)
	viewer := newNode(t, "mac", false)
	ctx := context.Background()
	row := viewRow(host, maddr(deadAddr(t), "lan", contracts.SourceCode), maddr(host.mesh.Addrs[0], "lan", contracts.SourceCode), maddr(host.mesh.Addrs[1], "overlay", contracts.SourceCode))
	if err := viewer.data.CreatePeer(ctx, row); err != nil {
		t.Fatal(err)
	}
	// The host lets the viewer view (it pins the viewer's key).
	no := false
	if err := host.data.CreatePeer(ctx, &contracts.Peer{Name: "mac", Fingerprint: string(viewer.id.Fingerprint()), Viewed: &no, Grant: &contracts.PeerGrant{Level: contracts.GrantView}}); err != nil {
		t.Fatal(err)
	}
	_ = viewer.mgr.Reload(ctx)
	var probe Probe
	waitFor(t, "connected", func() bool { probe = viewer.mgr.ProbePeer(ctx, *row); return probe.State == contracts.PeerConnected })
	st, _ := viewer.mgr.Status(row.ID)
	if st.Path == nil || st.Path.Addr != host.mesh.Addrs[0] {
		t.Fatalf("path %+v (want the LAN, %s)", st.Path, host.mesh.Addrs[0])
	}
	states := map[string]string{}
	for _, a := range st.Addresses {
		states[a.Addr] = a.State
	}
	if states[row.Addresses[0].Addr] != contracts.AddrNotAnswering || states[host.mesh.Addrs[0]] != contracts.AddrInUse {
		t.Fatalf("address states %v", states)
	}
	waitFor(t, "last_good written", func() bool {
		p, err := viewer.data.Peer(ctx, row.ID)
		return err == nil && p.LastGood != nil && p.LastGood.Addr == host.mesh.Addrs[0] && p.MeshURL == "http://"+host.mesh.Addrs[0]
	})
	// The hello's addresses replaced the code's (the dead one goes: the
	// host doesn't report it).
	waitFor(t, "hello addresses merged", func() bool {
		p, _ := viewer.data.Peer(ctx, row.ID)
		for _, a := range p.Addresses {
			if a.Addr == row.Addresses[0].Addr {
				return false
			}
		}
		return len(p.Addresses) == 2 && p.Addresses[0].Source == contracts.SourceHello
	})

	// The LAN goes away: the next probe re-races at once onto the other.
	host.mesh.Drop(0)
	cur, _ := viewer.data.Peer(ctx, row.ID)
	probe = viewer.mgr.ProbePeer(ctx, cur)
	if probe.State != contracts.PeerConnected {
		t.Fatalf("after the drop: %+v", probe)
	}
	st, _ = viewer.mgr.Status(row.ID)
	if st.Path == nil || st.Path.Addr != host.mesh.Addrs[1] {
		t.Fatalf("path after the drop: %+v", st.Path)
	}
	// The re-race's new path is logged with where it came from.
	waitFor(t, "the path change event", func() bool {
		evs, _ := viewer.data.Events(ctx, 0, "", []string{string(contracts.PeerReachabilityChanged)})
		for _, ev := range evs {
			var c contracts.PeerReachabilityChange
			_ = json.Unmarshal(ev.Payload, &c)
			if c.Via != nil && c.ViaFrom != nil && c.Via.Addr == host.mesh.Addrs[1] && c.ViaFrom.Addr == host.mesh.Addrs[0] {
				return true
			}
		}
		return false
	})
}

// F3 step 6: connected on a worse path (last_good put it first), the
// better one is tried every round and takes new requests after answering
// twice in a row.
func TestUpgradeAfterTwoAnswers(t *testing.T) {
	host := hostNode(t, "pop-os", 2)
	viewer := newNode(t, "mac", false)
	ctx := context.Background()
	row := viewRow(host, maddr(host.mesh.Addrs[0], "lan", contracts.SourceHello), maddr(host.mesh.Addrs[1], "overlay", contracts.SourceHello))
	row.LastGood = &contracts.PeerPath{Addr: host.mesh.Addrs[1], At: time.Now().UTC()}
	if err := viewer.data.CreatePeer(ctx, row); err != nil {
		t.Fatal(err)
	}
	no := false
	if err := host.data.CreatePeer(ctx, &contracts.Peer{Name: "mac", Fingerprint: string(viewer.id.Fingerprint()), Viewed: &no, Grant: &contracts.PeerGrant{Level: contracts.GrantView}}); err != nil {
		t.Fatal(err)
	}
	_ = viewer.mgr.Reload(ctx)
	waitFor(t, "connected", func() bool { return viewer.mgr.ProbePeer(ctx, *row).State == contracts.PeerConnected })
	path := func() string { st, _ := viewer.mgr.Status(row.ID); return st.Path.Addr }
	if path() != host.mesh.Addrs[1] {
		t.Fatalf("started on %s, want last_good", path())
	}
	viewer.mgr.upgradePaths(ctx)
	if path() != host.mesh.Addrs[1] {
		t.Fatal("moved after one answer")
	}
	viewer.mgr.upgradePaths(ctx)
	if path() != host.mesh.Addrs[0] {
		t.Fatalf("still on %s after two answers from the LAN", path())
	}
	if viewer.mgr.ProbePeer(ctx, *row).State != contracts.PeerConnected {
		t.Fatal("not connected on the new path")
	}
}

// F4: no address on a network this device is on is no_shared_network
// ("Not reachable from this network"), and nothing is dialled; tried and
// none answering is unreachable ("Offline").
func TestNoSharedNetworkVersusUnreachable(t *testing.T) {
	viewer := newNode(t, "mac", false)
	viewer.mgr.opts.LocalNetworks = func() []meshnet.Network { return nil }
	ctx := context.Background()
	host := newIdentity(t)
	yes := true
	row := &contracts.Peer{Name: "pop-os", Fingerprint: string(host.Fingerprint()), Viewed: &yes,
		Addresses: []contracts.MeshAddress{maddr("10.9.9.9:17380", "lan", contracts.SourceHello), maddr("100.101.4.7:17380", "overlay", contracts.SourceHello)}}
	row.Addresses[1].Label = "Tailscale"
	if err := viewer.data.CreatePeer(ctx, row); err != nil {
		t.Fatal(err)
	}
	p := viewer.mgr.ProbePeer(ctx, *row)
	if p.State != contracts.PeerNoSharedNetwork || !strings.Contains(p.Reason, "Tailscale") {
		t.Fatalf("no shared network: %+v", p)
	}
	_, states := viewer.mgr.pathStatus(*row)
	for _, s := range states {
		if s.State != contracts.AddrNotOnNetwork {
			t.Fatalf("address %s: %s", s.Addr, s.State)
		}
	}
	dead := &contracts.Peer{Name: "spark", Fingerprint: string(newIdentity(t).Fingerprint()), Viewed: &yes,
		Addresses: []contracts.MeshAddress{maddr(deadAddr(t), "lan", contracts.SourceHello)}}
	if err := viewer.data.CreatePeer(ctx, dead); err != nil {
		t.Fatal(err)
	}
	if p := viewer.mgr.ProbePeer(ctx, *dead); p.State != contracts.PeerUnreachable {
		t.Fatalf("dead: %+v", p)
	}
}

// This device's networks changing re-races every peer at once.
func TestNetworkChangeReRaces(t *testing.T) {
	host := hostNode(t, "pop-os", 1)
	viewer := newNode(t, "mac", false)
	var mu sync.Mutex
	nets := []meshnet.Network{{ID: "lan:a", Kind: meshnet.KindLAN, Addrs: []netip.Addr{netip.MustParseAddr("192.168.1.2")}, Prefixes: []netip.Prefix{netip.MustParsePrefix("192.168.1.0/24")}}}
	viewer.mgr.opts.LocalNetworks = func() []meshnet.Network { mu.Lock(); defer mu.Unlock(); return nets }
	ctx := context.Background()
	row := viewRow(host, maddr(host.mesh.Addrs[0], "lan", contracts.SourceHello))
	if err := viewer.data.CreatePeer(ctx, row); err != nil {
		t.Fatal(err)
	}
	no := false
	_ = host.data.CreatePeer(ctx, &contracts.Peer{Name: "mac", Fingerprint: string(viewer.id.Fingerprint()), Viewed: &no, Grant: &contracts.PeerGrant{Level: contracts.GrantView}})
	waitFor(t, "connected", func() bool { return viewer.mgr.ProbePeer(ctx, *row).State == contracts.PeerConnected })
	viewer.mgr.refreshLocal()
	mu.Lock()
	nets = []meshnet.Network{{ID: "lan:b", Kind: meshnet.KindLAN, Addrs: []netip.Addr{netip.MustParseAddr("10.0.0.2")}, Prefixes: []netip.Prefix{netip.MustParsePrefix("10.0.0.0/24")}}}
	mu.Unlock()
	if !viewer.mgr.refreshLocal() {
		t.Fatal("a change of networks wasn't noticed")
	}
	viewer.mgr.invalidateAll("test")
	if st, _ := viewer.mgr.Status(row.ID); st.Path != nil {
		t.Fatalf("the old path survived a network change: %+v", st.Path)
	}
}

// F5's merge: reported addresses replace host-reported ones, the user's
// stay, last_good stays when no longer reported; nothing changes for an
// older host's hello.
func TestMergeAddresses(t *testing.T) {
	now := time.Now().UTC()
	pol := &meshnet.DialPolicy{}
	cur := []contracts.MeshAddress{
		maddr("pop-os.tail.ts.net:17380", "", contracts.SourceLegacy),
		maddr("192.168.1.20:17380", "lan", contracts.SourceHello),
		maddr("10.0.0.9:17380", "lan", contracts.SourceUser),
	}
	lastGood := &contracts.PeerPath{Addr: "pop-os.tail.ts.net:17380"}
	next, changed := mergeAddresses(cur, lastGood, []contracts.NetAddress{
		{Via: "direct", Addr: "100.101.4.7:17380", Kind: "overlay", Label: "Tailscale"},
		{Via: "direct", Addr: "192.168.1.42:17380", Kind: "lan"},
		{Via: "direct", Addr: "127.0.0.1:17380"},       // refused: loopback
		{Via: "relay", Addr: "relay.example:443"},      // skipped
		{Via: "direct", Addr: "169.254.169.254:17380"}, // refused
	}, pol, now)
	if !changed {
		t.Fatal("unchanged")
	}
	var got []string
	for _, a := range next {
		got = append(got, a.Addr+"/"+string(a.Source))
	}
	want := "10.0.0.9:17380/user 100.101.4.7:17380/hello 192.168.1.42:17380/hello pop-os.tail.ts.net:17380/legacy"
	if strings.Join(got, " ") != want {
		t.Fatalf("merged %v, want %s", got, want)
	}
	if again, changed := mergeAddresses(next, lastGood, []contracts.NetAddress{{Via: "direct", Addr: "100.101.4.7:17380", Kind: "overlay", Label: "Tailscale"}, {Via: "direct", Addr: "192.168.1.42:17380", Kind: "lan"}}, pol, now); changed || len(again) != 4 {
		t.Fatalf("the same report changed the row: %v", again)
	}
	if _, changed := mergeAddresses(cur, lastGood, nil, pol, now); changed {
		t.Fatal("an older host's hello (no addresses) changed the row")
	}
	// The stale last_good ranks after the reported ones.
	ranked := (&Manager{}).candidates(contracts.Peer{Addresses: next, LastGood: lastGood})
	if ranked[len(ranked)-1].Addr != "pop-os.tail.ts.net:17380" {
		t.Fatalf("ranked %+v", ranked)
	}
}

func TestDiagnosis(t *testing.T) {
	now := time.Now()
	past, soon, later := now.Add(-time.Hour), now.Add(3*24*time.Hour), now.Add(60*24*time.Hour)
	ts := contracts.AddressStatus{MeshAddress: maddr("100.101.4.7:17380", "overlay", contracts.SourceHello), State: contracts.AddrNotAnswering}
	lan := contracts.AddressStatus{MeshAddress: maddr("192.168.1.20:17380", "lan", contracts.SourceHello), State: contracts.AddrNotAnswering}
	mk := func(state contracts.PeerReachability, exp *time.Time, addrs ...contracts.AddressStatus) contracts.PeerStatus {
		return contracts.PeerStatus{Peer: contracts.Peer{Remote: &contracts.PeerRemote{TailscaleKeyExpiry: exp}}, Reachability: state, Addresses: addrs}
	}
	for name, c := range map[string]struct {
		s    contracts.PeerStatus
		want string
	}{
		"signed out":        {mk(contracts.PeerUnreachable, &past, ts), contracts.DiagnosisTailscaleSignedOut},
		"not yet expired":   {mk(contracts.PeerUnreachable, &later, ts), ""},
		"tried the LAN too": {mk(contracts.PeerUnreachable, &past, ts, lan), ""},
		"expiring":          {mk(contracts.PeerConnected, &soon, ts), contracts.DiagnosisTailscaleKeyExpiring},
		"expiry far off":    {mk(contracts.PeerConnected, &later, ts), ""},
		"no expiry (off)":   {mk(contracts.PeerUnreachable, nil, ts), ""},
		"no shared network": {mk(contracts.PeerNoSharedNetwork, &past, ts), ""},
	} {
		if got := diagnosis(c.s, now); got != c.want {
			t.Errorf("%s: %q, want %q", name, got, c.want)
		}
	}
}

// --- Pairing by code, joiner-first (F2) -------------------------------------------------

// A code none of whose addresses is on a network this device is on:
// no_shared_network, with the host's networks and the tips key, and the
// code isn't used up.
func TestCodeJoinNoSharedNetwork(t *testing.T) {
	host := newNode(t, "pop-os", true)
	joiner := newNode(t, "mac", false)
	joiner.mgr.opts.LocalNetworks = func() []meshnet.Network { return nil }
	c := host.code(t)
	parsed, _ := paircode.Parse(c.Code)
	parsed.Addresses = []paircode.Address{{Via: "direct", Addr: "10.9.9.9:17380", Kind: "lan", Label: "Home"}, {Via: "direct", Addr: "100.101.4.7:17380", Kind: "overlay", Label: "Tailscale"}}
	var e struct {
		Code         string   `json:"code"`
		Error        string   `json:"error"`
		HostNetworks []string `json:"host_networks"`
		Tips         string   `json:"tips"`
	}
	if code := joiner.call(t, "POST", "/devices/pair", map[string]string{"code": paircode.Encode(parsed)}, &e); code != http.StatusUnprocessableEntity || e.Code != "no_shared_network" ||
		len(e.HostNetworks) != 2 || e.Tips != "no_shared_network" {
		t.Fatalf("%d %+v", code, e)
	}
	// Not used up: the real code still works.
	var st JoinStatus
	if code := joiner.call(t, "POST", "/devices/pair", map[string]string{"code": c.Code}, &st); code != http.StatusAccepted {
		t.Fatalf("the real code: %d", code)
	}
}

// Joiner-first: with no mesh network chosen yet, the network the host was
// reached over becomes this device's.
// Only once the host is pinned (round-1 review M1), and never a shared
// network, nor (round-2 R10) a LAN whose router's hardware address isn't
// known.
func TestCodeJoinAdoptsTheNetwork(t *testing.T) {
	for _, tc := range []struct {
		shared bool
		hw     string
	}{{false, "aa:aa:aa:aa:aa:aa"}, {true, "aa:aa:aa:aa:aa:aa"}, {false, ""}} {
		shared, hw := tc.shared, tc.hw
		host := newNode(t, "pop-os", true)
		joiner := newNode(t, "mac", false)
		loop := meshnet.Network{ID: "lan:127.0.0.0/8", Kind: meshnet.KindLAN, Label: "Test LAN", Shared: shared, GatewayHW: hw, Addrs: []netip.Addr{netip.MustParseAddr("127.0.0.1")}, Prefixes: []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")}}
		joiner.mgr.opts.LocalNetworks = func() []meshnet.Network { return []meshnet.Network{loop} }
		c := host.code(t)
		var st JoinStatus
		if code := joiner.call(t, "POST", "/devices/pair", map[string]string{"code": c.Code}, &st); code != http.StatusAccepted {
			t.Fatalf("join: %d", code)
		}
		if st.AdoptedNetwork != nil || st.Network != "Test LAN" {
			t.Fatalf("adopted before the host answered: %+v", st)
		}
		if s, _ := joiner.data.MeshSettings(context.Background()); len(s.Chosen()) != 0 {
			t.Fatalf("settings changed before the host answered: %+v", s)
		}
		var reqs []contracts.PairRequest
		host.call(t, "GET", "/devices/pair-requests", nil, &reqs)
		host.call(t, "POST", "/devices/pair-requests/"+reqs[0].ID+"/approve", contracts.PairDecision{Level: contracts.GrantView}, nil)
		waitFor(t, "the pin", func() bool {
			joiner.call(t, "GET", "/devices/pair/"+st.ID, nil, &st)
			return st.PeerID != ""
		})
		s, err := joiner.data.MeshSettings(context.Background())
		switch {
		case hw == "" && (st.AdoptedNetwork != nil || st.NotAdopted != "gateway_unknown" || len(s.Chosen()) != 0):
			t.Fatalf("a LAN without its router's hardware address was adopted: %+v settings %+v", st, s)
		case hw == "":
		case !shared && (st.AdoptedNetwork == nil || st.AdoptedNetwork.ID != loop.ID || err != nil || len(s.Chosen()) != 1):
			t.Fatalf("after the pin: %+v settings %+v %v", st, s, err)
		case shared && (st.AdoptedNetwork != nil || st.NotAdopted != "shared" || len(s.Chosen()) != 0):
			t.Fatalf("a shared network was adopted: %+v settings %+v", st, s)
		}
	}
}

// --- Discovery (U4, S6) ---------------------------------------------------------------------

// staticFinder "finds" addresses by probing them as the real finders do.
type staticFinder struct {
	m      *Manager
	addrs  []string
	hidden int
	mu     sync.Mutex
}

func (f *staticFinder) Searches(meshnet.Network) bool { return true }
func (f *staticFinder) Hidden() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.hidden
}
func (f *staticFinder) Find(ctx context.Context, n meshnet.Network) (<-chan meshnet.Found, error) {
	out := make(chan meshnet.Found, len(f.addrs))
	hidden := 0
	for _, a := range f.addrs {
		c := meshnet.Candidate{Via: "direct", Addr: a, Kind: meshnet.KindLAN, Label: n.Label, Network: n.ID}
		d, err := f.m.discoverAt(ctx, c)
		if err != nil {
			hidden++
			continue
		}
		fp, _ := devicekey.ParseFingerprint(d.Fingerprint)
		out <- meshnet.Found{Name: d.Name, Platform: d.Platform, Fingerprint: fp, Candidates: []meshnet.Candidate{c}, Network: n.ID, Via: "test", MeshAPI: d.MeshAPI, PairedWithYou: d.PairedWithYou}
	}
	f.mu.Lock()
	f.hidden = hidden
	f.mu.Unlock()
	close(out)
	return out, nil
}

var testLAN = meshnet.Network{ID: "lan:127.0.0.0/8", Kind: meshnet.KindLAN, Label: "Test LAN", GatewayHW: "aa:aa:aa:aa:aa:aa", Addrs: []netip.Addr{netip.MustParseAddr("127.0.0.1")}, Prefixes: []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")}}

func discoveredOf(t *testing.T, n *node) DiscoveredList {
	t.Helper()
	var l DiscoveredList
	if code := n.call(t, "GET", "/devices/discovered", nil, &l); code != http.StatusOK {
		t.Fatalf("discovered: %d", code)
	}
	return l
}

// The whole discovered pairing: only devices in their setup window are
// found (the rest counted), the request shows the same check code on both
// screens, approval pins nothing on the joiner until its user confirms
// the code, and "No, it was different" pins nothing.
func TestDiscoveredPairingEndToEnd(t *testing.T) {
	for _, match := range []bool{true, false} {
		host := newNode(t, "pop-os", true)
		joiner := newNode(t, "mac", false)
		joiner.mgr.opts.LocalNetworks = func() []meshnet.Network { return []meshnet.Network{testLAN} }
		f := &staticFinder{m: joiner.mgr, addrs: []string{strings.TrimPrefix(host.mesh.URL, "http://"), deadAddr(t)}}
		joiner.mgr.opts.Finders = []Finder{f}

		// The host's window is closed: nothing found, two hidden.
		waitFor(t, "a search round", func() bool { return discoveredOf(t, joiner).HiddenKnown })
		if l := discoveredOf(t, joiner); len(l.Devices) != 0 || l.HiddenCount != 2 {
			t.Fatalf("closed window: %+v", l)
		}
		if code := host.call(t, "POST", "/devices/discoverable", DiscoverableRequest{On: true}, nil); code != http.StatusOK {
			t.Fatalf("open window: %d", code)
		}
		var found DiscoveredDevice
		waitFor(t, "the host to be found", func() bool {
			l := discoveredOf(t, joiner)
			if len(l.Devices) == 1 {
				found = l.Devices[0]
				return true
			}
			return false
		})
		if found.Name != "pop-os" || found.Fingerprint != string(host.id.Fingerprint()) || found.Paired || !found.Compatible {
			t.Fatalf("found %+v", found)
		}
		var st JoinStatus
		if code := joiner.call(t, "POST", "/devices/discovered/"+found.Fingerprint+"/request", struct{}{}, &st); code != http.StatusAccepted {
			t.Fatalf("request: %d %+v", code, st)
		}
		if len(st.CheckCode) != 6 || st.Vouched || st.Mode != contracts.PairModeDiscovered {
			t.Fatalf("join %+v", st)
		}
		var reqs []contracts.PairRequest
		host.call(t, "GET", "/devices/pair-requests", nil, &reqs)
		if len(reqs) != 1 || reqs[0].CheckCode != st.CheckCode || reqs[0].Mode != contracts.PairModeDiscovered {
			t.Fatalf("host's requests %+v (joiner shows %s)", reqs, st.CheckCode)
		}
		if code := host.call(t, "POST", "/devices/pair-requests/"+reqs[0].ID+"/approve", contracts.PairDecision{Level: contracts.GrantControl, CheckCodeConfirmed: true}, nil); code != http.StatusOK {
			t.Fatalf("approve: %d", code)
		}
		waitFor(t, "approval to reach the joiner", func() bool {
			var cur JoinStatus
			joiner.call(t, "GET", "/devices/pair/"+st.ID, nil, &cur)
			return cur.Confirm == "needed"
		})
		if _, ok := joiner.device(t, string(host.id.Fingerprint())); ok {
			t.Fatal("pinned before the joiner confirmed the code")
		}
		if s, _ := joiner.data.MeshSettings(context.Background()); len(s.Chosen()) != 0 {
			t.Fatalf("the network was adopted before the code was confirmed: %+v", s)
		}
		var done JoinStatus
		if code := joiner.call(t, "POST", "/devices/pair/"+st.ID+"/confirm", map[string]bool{"match": match}, &done); code != http.StatusOK {
			t.Fatalf("confirm: %d", code)
		}
		row, pinned := joiner.device(t, string(host.id.Fingerprint()))
		switch {
		case match && (!pinned || done.Confirm != "matched" || row.Pairing == nil || row.Pairing.How != contracts.PairDiscovered || len(row.Addresses) == 0):
			t.Fatalf("matched: %+v pinned=%v row=%+v", done, pinned, row)
		case !match && (pinned || done.Confirm != "mismatch"):
			t.Fatalf("mismatch: %+v pinned=%v", done, pinned)
		}
		if s, _ := joiner.data.MeshSettings(context.Background()); (len(s.Chosen()) == 1) != match {
			t.Fatalf("match %v: settings %+v", match, s)
		}
		if !match {
			// The host is told, and undoes its side: no one-sided pairing.
			waitFor(t, "the host to undo its pairing", func() bool {
				_, ok := host.device(t, string(joiner.id.Fingerprint()))
				return !ok
			})
		}
		// Confirming twice answers the same.
		if code := joiner.call(t, "POST", "/devices/pair/"+st.ID+"/confirm", map[string]bool{"match": match}, &done); code != http.StatusOK {
			t.Fatalf("confirm again: %d", code)
		}
	}
}

// S6, spoofed hosts: a device answering /mesh/discover as another device
// (a fingerprint that isn't the key its handshake proved) is dropped.
func TestDiscoverDropsAnAnswerForAnotherKey(t *testing.T) {
	viewer := newNode(t, "mac", false)
	liar, victim := newIdentity(t), newIdentity(t)
	cfg, _ := devicekey.ServerConfig(liar)
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(Discover{Mads: 1, Name: "pop-os", Fingerprint: string(victim.Fingerprint()), MeshAPI: MeshAPI})
	}))
	srv.TLS = cfg
	srv.StartTLS()
	defer srv.Close()
	c := meshnet.Candidate{Via: "direct", Addr: strings.TrimPrefix(srv.URL, "https://")}
	if _, err := viewer.mgr.discoverAt(context.Background(), c); !errors.Is(err, errNotDiscoverable) {
		t.Fatalf("a spoofed answer: %v", err)
	}
	// Honest about its own key: found.
	honest := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(Discover{Mads: 1, Name: "liar", Platform: "LINUX!", Fingerprint: string(liar.Fingerprint()), MeshAPI: MeshAPI})
	}))
	honest.TLS = cfg
	honest.StartTLS()
	defer honest.Close()
	d, err := viewer.mgr.discoverAt(context.Background(), meshnet.Candidate{Via: "direct", Addr: strings.TrimPrefix(honest.URL, "https://")})
	if err != nil || d.Name != "liar" || d.Platform != "" {
		t.Fatalf("honest: %+v %v", d, err)
	}
	var _ = tls.VersionTLS13
}

// The tailnet finder: only peers answering as mads in their window are
// listed; the rest (offline, no answer) are counted.
func TestTailnetFinder(t *testing.T) {
	host := newNode(t, "pop-os", true)
	joiner := newNode(t, "mac", false)
	fake, sock := tailscaletest.StartSocket(t)
	port := host.mesh.URL[strings.LastIndex(host.mesh.URL, ":")+1:]
	fake.Set(func(f *tailscaletest.Fake) {
		f.Status = tailscale.Status{BackendState: "Running", Self: tailscaletest.Node("mac", 42, "100.70.1.1"), Peer: map[string]tailscale.Node{
			"a": *tailscaletest.Node("pop-os", 42, "127.0.0.1"),
			"b": {HostName: "camera", Online: true, TailscaleIPs: []netip.Addr{netip.MustParseAddr(strings.Split(deadAddr(t), ":")[0])}},
			"c": {HostName: "phone", Online: false, TailscaleIPs: []netip.Addr{netip.MustParseAddr("100.70.9.9")}},
		}}
	})
	joiner.mgr.opts.Tailscale = tailscale.NewSocket(sock)
	if _, err := joiner.data.SetMeshSettings(context.Background(), contracts.MeshSettings{Port: atoi(t, port)}); err != nil {
		t.Fatal(err)
	}
	_, _ = host.data.SetDiscoverable(context.Background(), time.Now().Add(5*time.Minute))
	f := &tailnetFinder{m: joiner.mgr}
	waitFor(t, "the host's window", func() bool {
		ch, err := f.Find(context.Background(), meshnet.Network{ID: meshnet.TailscaleID})
		if err != nil {
			return false
		}
		var got []meshnet.Found
		for x := range ch {
			got = append(got, x)
		}
		return len(got) == 1 && got[0].Name == "pop-os" && f.Hidden() == 2
	})
}

func atoi(t *testing.T, s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			t.Fatalf("not a number %q", s)
		}
		n = n*10 + int(c-'0')
	}
	return n
}

// Vouching on the joiner's side: its own WhoIs of the host's tailnet
// address says "same user, untagged".
func TestVouchFor(t *testing.T) {
	viewer := newNode(t, "mac", false)
	fake, sock := tailscaletest.StartSocket(t)
	fake.Set(func(f *tailscaletest.Fake) {
		f.Status = tailscale.Status{BackendState: "Running", Self: tailscaletest.Node("mac", 42, "100.70.1.1")}
		f.WhoIs["100.101.4.7"] = tailscaletest.WhoIsAnswer{Node: "pop-os", UserID: 42}
		f.WhoIs["100.101.4.8"] = tailscaletest.WhoIsAnswer{Node: "ci", UserID: 42, Tags: []string{"tag:ci"}}
		f.WhoIs["100.101.4.9"] = tailscaletest.WhoIsAnswer{Node: "friend", UserID: 7}
	})
	viewer.mgr.opts.Tailscale = tailscale.NewSocket(sock)
	ctx := context.Background()
	tsNet := meshnet.Network{ID: meshnet.TailscaleID, Kind: meshnet.KindOverlay, Prefixes: meshnet.TailnetRanges}
	viewer.mgr.opts.LocalNetworks = func() []meshnet.Network { return []meshnet.Network{tsNet} }
	for addr, want := range map[string]bool{"100.101.4.7:17380": true, "100.101.4.8:17380": false, "100.101.4.9:17380": false, "192.168.1.20:17380": false} {
		if got := viewer.mgr.vouchFor(ctx, addr); got != want {
			t.Errorf("vouchFor(%s) = %v", addr, got)
		}
	}
	// Round-1 review S3: a tailnet-range address reached over another VPN
	// (NetBird uses the same range) isn't vouched for.
	viewer.mgr.opts.LocalNetworks = func() []meshnet.Network {
		return []meshnet.Network{{ID: "vpn:100.90.0.0/16", Kind: meshnet.KindOverlay, Prefixes: []netip.Prefix{netip.MustParsePrefix("100.64.0.0/10")}}}
	}
	viewer.mgr.localMu.Lock()
	viewer.mgr.localRead = false
	viewer.mgr.localMu.Unlock()
	if viewer.mgr.vouchFor(ctx, "100.101.4.7:17380") {
		t.Error("vouched over a VPN that isn't Tailscale")
	}
	viewer.mgr.opts.Tailscale = nil
	if viewer.mgr.vouchFor(ctx, "100.101.4.7:17380") {
		t.Error("vouched without tailscaled")
	}
}

// --- This device's mesh network (U1, U2) ------------------------------------------------------

func TestSelfMeshAndSettings(t *testing.T) {
	n := newNode(t, "mac", false)
	ts := meshnet.Network{ID: meshnet.TailscaleID, Kind: meshnet.KindOverlay, Label: "Tailscale", Iface: "utun4", Addrs: []netip.Addr{netip.MustParseAddr("100.70.1.1")}}
	wifi := meshnet.Network{ID: "lan:10.20.0.0/16/10.20.0.1", Kind: meshnet.KindLAN, Label: `Wi-Fi "eduroam"`, Shared: true, Addrs: []netip.Addr{netip.MustParseAddr("10.20.30.40")}}
	pub := meshnet.Network{ID: "public:en0", Kind: meshnet.KindPublic, Label: "Public IPv6", Addrs: []netip.Addr{netip.MustParseAddr("2001:db8::1")}}
	n.mgr.opts.LocalNetworks = func() []meshnet.Network { return []meshnet.Network{ts, wifi, pub} }
	var self SelfDevice
	n.call(t, "GET", "/devices/self", nil, &self)
	if self.Mesh.Configured || len(self.Mesh.Networks) != 3 || self.RequireTailnet == nil || !*self.RequireTailnet {
		t.Fatalf("fresh: %+v", self)
	}
	views := map[string]NetworkView{}
	for _, v := range self.Mesh.Networks {
		views[v.ID] = v
	}
	if v := views["tailscale"]; !v.Recommended || v.WhoCanKnock != "tailnet_only" || v.Meaning != "vpn_anywhere" || v.Risk != "low" {
		t.Fatalf("tailscale view %+v", v)
	}
	if v := views[wifi.ID]; v.WhoCanKnock != "anyone_on_shared_network" || v.Risk != "high" || !v.Shared {
		t.Fatalf("campus wifi %+v", v)
	}
	var e apiErr
	if code := n.call(t, "PUT", "/devices/settings", map[string]any{"networks": []string{"tailscale", wifi.ID}}, &e); code != 422 || e.Code != "one_network" {
		t.Fatalf("two networks: %d %+v", code, e)
	}
	if code := n.call(t, "PUT", "/devices/settings", map[string]any{"networks": []string{"lan:nope"}}, &e); code != 422 || e.Code != "unknown_network" {
		t.Fatalf("unknown: %d %+v", code, e)
	}
	if code := n.call(t, "PUT", "/devices/settings", map[string]any{"networks": []string{pub.ID}}, &e); code != 422 || e.Code != "public_not_a_mesh_network" {
		t.Fatalf("public as the mesh network: %d %+v", code, e)
	}
	// Round-2 review R10: a LAN whose router's hardware address isn't
	// known can't be told from a look-alike: not saved.
	if code := n.call(t, "PUT", "/devices/settings", map[string]any{"networks": []string{wifi.ID}}, &e); code != 422 || e.Code != "gateway_unknown" {
		t.Fatalf("a LAN without its router's hardware address: %d %+v", code, e)
	}
	if code := n.call(t, "PUT", "/devices/settings", map[string]any{"networks": []string{"tailscale"}}, &self); code != 200 || !self.Mesh.Configured || !self.Mesh.TailnetCheck {
		t.Fatalf("tailscale (check on by default): %d %+v", code, self.Mesh)
	}
	if code := n.call(t, "PUT", "/devices/settings", map[string]any{"public": true}, &e); code != 422 || e.Code != "confirm_public" {
		t.Fatalf("public without confirming: %d %+v", code, e)
	}
	if code := n.call(t, "PUT", "/devices/settings", map[string]any{"public": true, "confirm_public": true, "tailnet_check": false}, &self); code != 200 || !self.Mesh.Public || self.Mesh.TailnetCheck {
		t.Fatalf("public + check off: %d %+v", code, self.Mesh)
	}
	// The earlier form still works.
	if code := n.call(t, "PUT", "/devices/settings", map[string]any{"require_tailnet": true}, &self); code != 200 || !self.Mesh.TailnetCheck || !*self.RequireTailnet {
		t.Fatalf("legacy: %d %+v", code, self.Mesh)
	}
	// Tailscale isn't here now: it stays chosen, unavailable.
	n.mgr.opts.LocalNetworks = func() []meshnet.Network { return []meshnet.Network{wifi} }
	n.mgr.localMu.Lock()
	n.mgr.localRead = false
	n.mgr.localMu.Unlock()
	n.call(t, "GET", "/devices/self", nil, &self)
	for _, v := range self.Mesh.Networks {
		if v.ID == "tailscale" && (!v.Chosen || v.Available || v.Error == "") {
			t.Fatalf("tailscale gone: %+v", v)
		}
	}
	// None: stop listening.
	if code := n.call(t, "PUT", "/devices/settings", map[string]any{"networks": []string{}}, &self); code != 200 || self.Mesh.Configured {
		t.Fatalf("none: %d %+v", code, self.Mesh)
	}
}

func TestDiscoverableRoute(t *testing.T) {
	n := newNode(t, "mac", false)
	var out map[string]any
	if code := n.call(t, "POST", "/devices/discoverable", DiscoverableRequest{On: true}, &out); code != 200 || out["on"] != true {
		t.Fatalf("on: %d %v", code, out)
	}
	var self SelfDevice
	n.call(t, "GET", "/devices/self", nil, &self)
	if self.Mesh.DiscoverableUntil == nil || self.Mesh.DiscoverableUntil.After(time.Now().Add(contracts.DiscoverableMax+time.Minute)) {
		t.Fatalf("until %v", self.Mesh.DiscoverableUntil)
	}
	if code := n.call(t, "POST", "/devices/discoverable", DiscoverableRequest{On: true, Renew: true}, &out); code != 200 || out["on"] != true {
		t.Fatalf("renew: %d %v", code, out)
	}
	if code := n.call(t, "POST", "/devices/discoverable", DiscoverableRequest{On: false}, &out); code != 200 || out["on"] != false {
		t.Fatalf("off: %d %v", code, out)
	}
	// A renewal never reopens a closed window (S5): that takes an open.
	var e apiErr
	if code := n.call(t, "POST", "/devices/discoverable", DiscoverableRequest{On: true, Renew: true}, &e); code != http.StatusConflict || e.Code != "window_closed" {
		t.Fatalf("renew after close: %d %+v", code, e)
	}
	if strings.Contains(e.Error, "30 minutes") {
		t.Fatalf("closed by its user, not by time: %q", e.Error)
	}
	// Final pass 5: one closed for abandoned pairings says so.
	if code := n.call(t, "POST", "/devices/discoverable", DiscoverableRequest{On: true}, &out); code != 200 {
		t.Fatalf("reopen: %d", code)
	}
	meshData := dataclient.New(n.dataURL, "mesh", dataclient.WithToken(n.token))
	if _, err := meshData.CloseDiscoverable(context.Background(), "abandoned_pairings"); err != nil {
		t.Fatal(err)
	}
	var closed struct {
		apiErr
		Reason string `json:"reason"`
	}
	if code := n.call(t, "POST", "/devices/discoverable", DiscoverableRequest{On: true, Renew: true}, &closed); code != http.StatusConflict || closed.Reason != "abandoned_pairings" ||
		!strings.Contains(closed.Error, "several pairings were started and never finished") {
		t.Fatalf("renew after an abandoned-pairings close: %d %+v", code, closed)
	}
	// Never opened by another device.
	req, _ := http.NewRequest("POST", n.router.URL+"/devices/discoverable", strings.NewReader(`{"on":true}`))
	req.Header[contracts.MeshRelayHeader] = []string{"phone"}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("relayed: %d", resp.StatusCode)
	}
}

func TestTailscaleLoginRoute(t *testing.T) {
	n := newNode(t, "mac", false)
	fake, sock := tailscaletest.StartSocket(t)
	fake.Set(func(f *tailscaletest.Fake) {
		f.Status = tailscale.Status{BackendState: "NeedsLogin"}
		f.LoginURL = "https://login.tailscale.com/a/xyz"
		f.ControlURL = "https://headscale.example.com"
	})
	n.mgr.opts.Tailscale = tailscale.NewSocket(sock)
	var self SelfDevice
	n.call(t, "GET", "/devices/self", nil, &self)
	if self.Mesh.Tailscale == nil || !self.Mesh.Tailscale.NeedsLogin || self.Mesh.Tailscale.AdminURL == "" {
		t.Fatalf("self tailscale %+v", self.Mesh.Tailscale)
	}
	// Where a sign-in link may point (the UI opens no other): the control server.
	if self.Mesh.Tailscale.ControlURL != "https://headscale.example.com" {
		t.Fatalf("self control url %q", self.Mesh.Tailscale.ControlURL)
	}
	var out map[string]any
	if code := n.call(t, "POST", "/devices/tailscale/login", struct{}{}, &out); code != 200 || out["auth_url"] != "https://login.tailscale.com/a/xyz" || out["control_url"] != "https://headscale.example.com" {
		t.Fatalf("login: %d %v", code, out)
	}
	if fake.Count("login-interactive") != 1 {
		t.Fatal("no sign-in started")
	}
	// A link that isn't https is never handed on, in the login answer or
	// in /devices/self.
	for _, bad := range []string{"http://evil.example/login", "javascript:alert(1)", "https://user@login.tailscale.com/a/xyz"} {
		fake.Set(func(f *tailscaletest.Fake) {
			f.Status = tailscale.Status{BackendState: "NeedsLogin", AuthURL: bad}
		})
		var e apiErr
		if code := n.call(t, "POST", "/devices/tailscale/login", struct{}{}, &e); code != http.StatusBadGateway || e.Code != "unexpected_auth_url" {
			t.Fatalf("login with %q: %d %+v", bad, code, e)
		}
		var self SelfDevice
		n.call(t, "GET", "/devices/self", nil, &self)
		if self.Mesh.Tailscale == nil || self.Mesh.Tailscale.AuthURL != "" {
			t.Fatalf("self with %q: %+v", bad, self.Mesh.Tailscale)
		}
	}
	// Round-1 review S8: signed in, a sign-in is never started (it would
	// sign this device out).
	fake.Set(func(f *tailscaletest.Fake) {
		f.Status = tailscale.Status{BackendState: "Running", Self: tailscaletest.Node("mac", 42, "100.70.1.1")}
	})
	var e apiErr
	if code := n.call(t, "POST", "/devices/tailscale/login", struct{}{}, &e); code != http.StatusConflict || e.Code != "not_needed" {
		t.Fatalf("running: %d %+v", code, e)
	}
	if fake.Count("login-interactive") != 1 {
		t.Fatal("a sign-in was started while signed in")
	}
}

// mads mesh address add: the user's own address (S2's exception), shape-
// checked, removable only when the user added it.
func TestUserAddresses(t *testing.T) {
	n := newNode(t, "mac", false)
	ctx := context.Background()
	host := newIdentity(t)
	yes := true
	row := &contracts.Peer{Name: "pop-os", Fingerprint: string(host.Fingerprint()), Viewed: &yes, Addresses: []contracts.MeshAddress{maddr("100.101.4.7:17380", "overlay", contracts.SourceHello)}}
	if err := n.data.CreatePeer(ctx, row); err != nil {
		t.Fatal(err)
	}
	fp := row.Fingerprint
	n.mgr.policy.AllowLoopback = false // as in production
	var e apiErr
	for _, bad := range []string{"169.254.169.254:17380", "127.0.0.1:17380", "10.0.0.5:22", "http://x:1"} {
		if code := n.call(t, "POST", "/devices/"+fp+"/addresses", AddressRequest{Addr: bad}, &e); code != 422 || e.Code != "bad_address" {
			t.Errorf("%s: %d %+v", bad, code, e)
		}
	}
	var out contracts.Peer
	if code := n.call(t, "POST", "/devices/"+fp+"/addresses", AddressRequest{Addr: "10.50.0.9:17380"}, &out); code != 200 || len(out.Addresses) != 2 || out.Addresses[1].Source != contracts.SourceUser {
		t.Fatalf("add: %d %+v", code, out)
	}
	// The user's private address outside this device's networks is still
	// one it will try (S2's exception).
	if !meshnet.OnNetwork(meshnet.Candidate{Via: "direct", Addr: "10.50.0.9:17380"}, nil, &meshnet.DialPolicy{}, true) {
		t.Fatal("the user's own address is filtered")
	}
	if code := n.call(t, "DELETE", "/devices/"+fp+"/addresses/100.101.4.7:17380", nil, &e); code != 409 || e.Code != "not_user_added" {
		t.Fatalf("remove a reported one: %d %+v", code, e)
	}
	if code := n.call(t, "DELETE", "/devices/"+fp+"/addresses/10.50.0.9:17380", nil, &out); code != 200 || len(out.Addresses) != 1 {
		t.Fatalf("remove: %d %+v", code, out)
	}
}

// Two stacks (the doc's "Two stacks in-process"): a host on two networks
// opens its setup window, a joiner finds it, both see the same check code,
// the host approves and the joiner confirms; then one network goes away
// and the viewer moves to the other.
func TestTwoStacksFindApproveAndMove(t *testing.T) {
	host := hostNode(t, "pop-os", 2)
	joiner := newNode(t, "mac", false)
	joiner.mgr.opts.LocalNetworks = func() []meshnet.Network { return []meshnet.Network{testLAN} }
	joiner.mgr.opts.Finders = []Finder{&staticFinder{m: joiner.mgr, addrs: []string{host.mesh.Addrs[0]}}}
	ctx := context.Background()
	if code := host.call(t, "POST", "/devices/discoverable", DiscoverableRequest{On: true}, nil); code != http.StatusOK {
		t.Fatal(code)
	}
	var found DiscoveredDevice
	waitFor(t, "the host to be found", func() bool {
		l := discoveredOf(t, joiner)
		if len(l.Devices) == 1 {
			found = l.Devices[0]
		}
		return len(l.Devices) == 1
	})
	var st JoinStatus
	if code := joiner.call(t, "POST", "/devices/discovered/"+found.Fingerprint+"/request", struct{}{}, &st); code != http.StatusAccepted {
		t.Fatalf("request: %d", code)
	}
	var reqs []contracts.PairRequest
	host.call(t, "GET", "/devices/pair-requests", nil, &reqs)
	if len(reqs) != 1 || reqs[0].CheckCode != st.CheckCode {
		t.Fatalf("check codes differ: host %+v, joiner %s", reqs, st.CheckCode)
	}
	host.call(t, "POST", "/devices/pair-requests/"+reqs[0].ID+"/approve", contracts.PairDecision{Level: contracts.GrantControl, CheckCodeConfirmed: true}, nil)
	waitFor(t, "the approval", func() bool {
		var cur JoinStatus
		joiner.call(t, "GET", "/devices/pair/"+st.ID, nil, &cur)
		return cur.Confirm == "needed"
	})
	joiner.call(t, "POST", "/devices/pair/"+st.ID+"/confirm", map[string]bool{"match": true}, &st)
	row, ok := joiner.device(t, string(host.id.Fingerprint()))
	if !ok || !row.IsViewed() {
		t.Fatalf("pinned %+v (viewed: the host lets it control)", row)
	}
	_ = joiner.mgr.Reload(ctx)
	cur, _ := joiner.data.Peer(ctx, row.ID)
	waitFor(t, "connected with the host's addresses", func() bool {
		cur, _ = joiner.data.Peer(ctx, row.ID)
		return joiner.mgr.ProbePeer(ctx, cur).State == contracts.PeerConnected && len(cur.Addresses) == 2
	})
	st0, _ := joiner.mgr.Status(row.ID)
	if st0.Path == nil || st0.Path.Addr != host.mesh.Addrs[0] {
		t.Fatalf("path %+v", st0.Path)
	}
	host.mesh.Drop(0)
	if p := joiner.mgr.ProbePeer(ctx, cur); p.State != contracts.PeerConnected {
		t.Fatalf("after dropping a network: %+v", p)
	}
	if st1, _ := joiner.mgr.Status(row.ID); st1.Path == nil || st1.Path.Addr != host.mesh.Addrs[1] {
		t.Fatalf("path after the drop: %+v", st1.Path)
	}
}

// C1: a device in the middle, answering discovery as the host, wants the
// joiner's six digits to equal the real host's (which it learned by
// asking the real host itself). It grinds its own nonce whenever it can
// see the joiner's before choosing; with the commitment it never can, so
// the joiner's code is out of its hands.
func TestCheckCodeCantBeGroundByTheHost(t *testing.T) {
	joiner := newNode(t, "mac", false)
	joiner.mgr.opts.LocalNetworks = func() []meshnet.Network { return []meshnet.Network{testLAN} }
	mitm := newIdentity(t)
	const target = "123456" // the code the real host shows
	ground := 0
	cfg, _ := devicekey.ServerConfig(mitm)
	var mu sync.Mutex
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		joinerFP, _ := devicekey.PeerFingerprint(*r.TLS)
		pick := func() string {
			nonce, _ := body["nonce"].(string)
			if nonce == "" {
				hn, _ := paircode.NewNonce() // nothing to grind against
				return hn
			}
			for i := 0; i < 4_000_000; i++ {
				hn, _ := paircode.NewNonce()
				if c, _ := paircode.CheckCode(mitm.Fingerprint(), joinerFP, nonce, hn); c == target {
					mu.Lock()
					ground++
					mu.Unlock()
					return hn
				}
			}
			hn, _ := paircode.NewNonce()
			return hn
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case body["commit_id"] != nil: // the reveal
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{"request_id": "pr_x", "state": "pending", "expires_at": time.Now().Add(time.Minute)})
		case body["commit"] != nil: // committed: answer a nonce, blind
			_ = json.NewEncoder(w).Encode(map[string]any{"commit_id": "c1", "host_nonce": pick(), "state": "committed"})
		default: // a joiner that sent its nonce first
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{"request_id": "pr_x", "state": "pending", "host_nonce": pick(), "expires_at": time.Now().Add(time.Minute)})
		}
	}))
	srv.TLS = cfg
	srv.StartTLS()
	defer srv.Close()
	addr := strings.TrimPrefix(srv.URL, "https://")
	for i := 0; i < 5; i++ {
		s := &search{started: time.Now(), cancel: func() {}, polled: time.Now(), found: map[devicekey.Fingerprint]*foundDevice{
			mitm.Fingerprint(): {Found: meshnet.Found{Name: "pop-os", Fingerprint: mitm.Fingerprint(), MeshAPI: MeshAPI,
				Candidates: []meshnet.Candidate{{Via: "direct", Addr: addr, Kind: meshnet.KindLAN}}}},
		}}
		joiner.mgr.mu.Lock()
		joiner.mgr.search = s
		joiner.mgr.mu.Unlock()
		var st JoinStatus
		if code := joiner.call(t, "POST", "/devices/discovered/"+string(mitm.Fingerprint())+"/request", struct{}{}, &st); code != http.StatusAccepted {
			t.Fatalf("request: %d %+v", code, st)
		}
		if st.CheckCode == target {
			t.Fatalf("the host steered the joiner's code to %s (ground %d times)", target, ground)
		}
	}
	if ground != 0 {
		t.Fatalf("the host saw the joiner's nonce before choosing its own %d times", ground)
	}
}

// Round-1 review M3, the joiner's side: a request Tailscale vouched for
// when it was made, whose host withheld the vouch at approval (another
// request was waiting), isn't pinned until this device's user confirms
// the code.
func TestWithheldVouchNeedsTheJoinersConfirm(t *testing.T) {
	joiner := newNode(t, "mac", false)
	hostID := newIdentity(t)
	cfg, _ := devicekey.ServerConfig(hostID)
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"request_id": "pr_1", "state": "approved", "level": "view", "vouched": false})
	}))
	srv.TLS = cfg
	srv.StartTLS()
	defer srv.Close()
	host := contracts.Peer{Name: "pop-os", Fingerprint: string(hostID.Fingerprint()),
		Addresses: []contracts.MeshAddress{maddr(strings.TrimPrefix(srv.URL, "https://"), "lan", contracts.SourceDiscovered)}}
	j := &join{host: host, mode: contracts.PairModeDiscovered, started: time.Now(),
		status: JoinStatus{ID: "k", RequestID: "pr_1", State: contracts.PairPending, ExpiresAt: time.Now().Add(time.Minute), Vouched: true, CheckCode: "123456", Mode: contracts.PairModeDiscovered}}
	joiner.mgr.addJoin("k", j)
	joiner.mgr.waitJoin(context.Background(), j)
	st := j.get()
	if st.Confirm != "needed" || st.Vouched || st.PeerID != "" {
		t.Fatalf("join %+v", st)
	}
	if _, ok := joiner.device(t, host.Fingerprint); ok {
		t.Fatal("pinned with the vouch withheld")
	}
}

// Round-1 tester T4: a network the Mesh Service listens on reads as
// listening by its addresses, whatever id its own detection gave it; and
// "isn't answering" names the port actually tried.
func TestListeningByAddressAndThePortInTips(t *testing.T) {
	// A real Mesh Service on 127.0.0.1, reporting network "lan:test0"; the
	// Router's own detection calls the network something else.
	n := newNode(t, "mac", true)
	lan := meshnet.Network{ID: "lan:127.0.0.0/8", Kind: meshnet.KindLAN, Label: "Wi-Fi", Addrs: []netip.Addr{netip.MustParseAddr("127.0.0.1")}}
	n.mgr.opts.LocalNetworks = func() []meshnet.Network { return []meshnet.Network{lan} }
	var self SelfDevice
	n.call(t, "GET", "/devices/self", nil, &self)
	if len(self.Mesh.Networks) != 1 || !self.Mesh.Networks[0].Listening || self.Mesh.ServiceAnswering == nil || !*self.Mesh.ServiceAnswering {
		t.Fatalf("networks %+v", self.Mesh)
	}
	// Round-2 review T5: its report outlives it; listening is what answers
	// now.
	n.mesh.Drop(0)
	n.mgr.aliveMu.Lock()
	n.mgr.alive = aliveCheck{}
	n.mgr.aliveMu.Unlock()
	self = SelfDevice{}
	n.call(t, "GET", "/devices/self", nil, &self)
	if len(self.Mesh.Networks) != 1 || self.Mesh.Networks[0].Listening || self.Mesh.Networks[0].Error != notAnswering ||
		self.Mesh.ServiceAnswering == nil || *self.Mesh.ServiceAnswering || !self.Mesh.ServiceSeen {
		t.Fatalf("after it stopped: %+v", self.Mesh)
	}
	host := contracts.Peer{Name: "pop-os", Addresses: []contracts.MeshAddress{maddr("192.168.1.9:9999", "lan", contracts.SourceCode)}}
	err := joinDialError(host, &meshnet.RaceError{Err: meshnet.ErrNoneAnswered, Attempts: []meshnet.Attempt{{Candidate: meshnet.Candidate{Addr: "192.168.1.9:9999"}, Started: true, Err: errors.New("refused")}}})
	var pe *pairCodeError
	if !errors.As(err, &pe) || pe.code != "host_unreachable" || !strings.Contains(pe.msg, "port 9999") {
		t.Fatalf("%v", err)
	}
}

// Round-1 review S7: a flood of adverts is probed at most 16 at a time.
func TestMDNSProbesCapped(t *testing.T) {
	n := newNode(t, "mac", false)
	lan := &mdns.MemNetwork{}
	var mu sync.Mutex
	running, most, goroutines := 0, 0, 0
	f := &mdnsFinder{m: n.mgr,
		listen: func(string) (mdns.Conn, error) { return lan.Join("192.168.1.30"), nil },
		probe: func(ctx context.Context, c meshnet.Candidate) (Discover, error) {
			mu.Lock()
			running++
			most = max(most, running)
			goroutines = max(goroutines, probeGoroutines())
			mu.Unlock()
			time.Sleep(30 * time.Millisecond)
			mu.Lock()
			running--
			mu.Unlock()
			return Discover{}, errNotDiscoverable
		}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	for i := 0; i < 60; i++ {
		ip := "192.168.1." + strconv.Itoa(100+i)
		a := mdns.Advert{Instance: "fake" + strconv.Itoa(i), Port: 17380, Addrs: []netip.Addr{netip.MustParseAddr(ip)}, TXT: map[string]string{"v": "1"}}
		go func() { _ = mdns.Advertise(ctx, lan.Join(ip), a) }()
	}
	ch, err := f.Find(context.Background(), meshnet.Network{ID: "lan:x", Kind: meshnet.KindLAN, Iface: "en0"})
	if err != nil {
		t.Fatal(err)
	}
	for range ch {
	}
	mu.Lock()
	defer mu.Unlock()
	if most == 0 || most > probeParallel {
		t.Fatalf("at most %d probes at once", most)
	}
	// Round-2 nit: the slot is taken before the goroutine starts, so
	// waiting probes aren't goroutines.
	if goroutines == 0 || goroutines > probeParallel+2 {
		t.Fatalf("%d probe goroutines at once", goroutines)
	}
}

// probeGoroutines counts the mDNS finder's probe goroutines (running or
// waiting).
func probeGoroutines() int {
	buf := make([]byte, 4<<20)
	return strings.Count(string(buf[:runtime.Stack(buf, true)]), "(*mdnsFinder).Find.func1.1(")
}

// The search backs off (5 s, then 15 s in production): it probes every
// tailnet peer each round.
func TestSearchBacksOff(t *testing.T) {
	n := newNode(t, "mac", false)
	n.mgr.opts.SearchBackoff = []time.Duration{200 * time.Millisecond, 600 * time.Millisecond}
	n.mgr.opts.LocalNetworks = func() []meshnet.Network { return []meshnet.Network{testLAN} }
	f := &countingFinder{}
	n.mgr.opts.Finders = []Finder{f}
	discoveredOf(t, n)
	time.Sleep(1100 * time.Millisecond)
	if r := f.rounds(); r < 2 || r > 3 {
		t.Fatalf("%d rounds in 1.1 s with a 200 ms then 600 ms backoff", r)
	}
	n.mgr.stopSearch()
}

type countingFinder struct {
	mu sync.Mutex
	n  int
}

func (f *countingFinder) Searches(meshnet.Network) bool { return true }
func (f *countingFinder) Find(context.Context, meshnet.Network) (<-chan meshnet.Found, error) {
	f.mu.Lock()
	f.n++
	f.mu.Unlock()
	ch := make(chan meshnet.Found)
	close(ch)
	return ch, nil
}
func (f *countingFinder) rounds() int { f.mu.Lock(); defer f.mu.Unlock(); return f.n }

// Concurrent first polls share one search without racing on its networks
// (run with -race).
func TestConcurrentDiscoveredPolls(t *testing.T) {
	n := newNode(t, "mac", false)
	n.mgr.opts.LocalNetworks = func() []meshnet.Network { return []meshnet.Network{testLAN} }
	n.mgr.opts.Finders = []Finder{&countingFinder{}}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); discoveredOf(t, n) }()
	}
	wg.Wait()
	n.mgr.stopSearch()
}

// Round-2 review R1, the Router's part: "a device started pairing" and a
// window closed for abandoned pairings show on the host; the joiner never
// waits longer than a request lives here, whatever the host says.
func TestPairingStartedAndTheJoinersWait(t *testing.T) {
	n := newNode(t, "pop-os", false)
	if code := n.call(t, "POST", "/devices/discoverable", DiscoverableRequest{On: true}, nil); code != 200 {
		t.Fatal(code)
	}
	meshData := dataclient.New(n.dataURL, "mesh", dataclient.WithToken(n.token))
	raw, _ := json.Marshal(contracts.DeviceDiscovery{Kind: "commit", Remote: "192.168.1.66:4000", Fingerprint: string(newIdentity(t).Fingerprint()), Name: "stranger"})
	if err := meshData.Append(context.Background(), &contracts.Envelope{Type: contracts.DevicePairStarted, Actor: "mesh", Payload: raw}); err != nil {
		t.Fatal(err)
	}
	var self SelfDevice
	n.call(t, "GET", "/devices/self", nil, &self)
	if len(self.Mesh.PairingStarted) != 1 || self.Mesh.PairingStarted[0].Name != "stranger" {
		t.Fatalf("pairing started %+v", self.Mesh.PairingStarted)
	}
	if _, err := meshData.CloseDiscoverable(context.Background(), "abandoned_pairings"); err != nil {
		t.Fatal(err)
	}
	self = SelfDevice{}
	n.call(t, "GET", "/devices/self", nil, &self)
	if self.Mesh.WindowClosedReason != "abandoned_pairings" || self.Mesh.DiscoverableUntil != nil {
		t.Fatalf("closed window %+v", self.Mesh)
	}

	// A host that says its request lives an hour, and never answers.
	n.mgr.opts.JoinMaxWait, n.mgr.opts.JoinGrace = 200*time.Millisecond, 100*time.Millisecond
	hostID := newIdentity(t)
	cfg, _ := devicekey.ServerConfig(hostID)
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"request_id": "pr_1", "state": "pending", "expires_at": time.Now().Add(time.Hour)})
	}))
	srv.TLS = cfg
	srv.StartTLS()
	defer srv.Close()
	host := contracts.Peer{Name: "slow", Fingerprint: string(hostID.Fingerprint()), Addresses: []contracts.MeshAddress{maddr(strings.TrimPrefix(srv.URL, "https://"), "lan", contracts.SourceDiscovered)}}
	j := &join{host: host, mode: contracts.PairModeDiscovered, started: time.Now(),
		status: JoinStatus{ID: "k", RequestID: "pr_1", State: contracts.PairPending, ExpiresAt: time.Now().Add(time.Hour)}}
	done := make(chan struct{})
	go func() { n.mgr.waitJoin(context.Background(), j); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the joiner waited as long as the host said")
	}
	if j.get().State != contracts.PairExpired {
		t.Fatalf("%+v", j.get())
	}
}

// Round-2 review R2: an address is on the most specific network holding
// it, whatever order detection listed them in: a LAN's /24 over a VPN
// routing the whole 192.168/16.
func TestNetworkOfLongestPrefix(t *testing.T) {
	n := newNode(t, "mac", false)
	vpn := meshnet.Network{ID: "vpn:wireguard:10.8.0.0/24", Kind: meshnet.KindOverlay, Prefixes: []netip.Prefix{netip.MustParsePrefix("10.8.0.0/24"), netip.MustParsePrefix("192.168.0.0/16")}}
	lan := meshnet.Network{ID: "lan:192.168.1.0/24/192.168.1.1", Kind: meshnet.KindLAN, Prefixes: []netip.Prefix{netip.MustParsePrefix("192.168.1.0/24")}}
	n.mgr.opts.LocalNetworks = func() []meshnet.Network { return []meshnet.Network{vpn, lan} }
	if got, ok := n.mgr.networkOf("192.168.1.20:17380"); !ok || got.ID != lan.ID {
		t.Fatalf("got %+v %v", got, ok)
	}
	if got, ok := n.mgr.networkOf("192.168.7.20:17380"); !ok || got.ID != vpn.ID {
		t.Fatalf("got %+v %v", got, ok)
	}
}

// Final pass 6: the liveness check dials only mesh-shaped addresses
// (a port ≥ 1024, or 443), even on this machine.
func TestSelfAnswersChecksShape(t *testing.T) {
	n := newNode(t, "mac", false)
	for _, a := range []string{"127.0.0.1:22", "127.0.0.1:80", "127.0.0.1:0"} {
		if err := n.mgr.selfAnswersAt(context.Background(), a); !errors.Is(err, meshnet.ErrBadAddress) {
			t.Errorf("%s: %v", a, err)
		}
	}
	if err := n.mgr.selfAnswersAt(context.Background(), "192.0.2.1:17380"); !errors.Is(err, errNotOwnAddress) {
		t.Errorf("not this machine's: %v", err)
	}
}

// A pairing request right after setup (T1): the Mesh Service hasn't
// reported where it listens yet, so the request waits for it (bounded), and
// past the wait gives the chosen network's own addresses: the host is never
// left with no address for this device.
func TestJoinerSelfWaitsForTheMeshService(t *testing.T) {
	ctx := context.Background()
	n := newNode(t, "mac", false)
	n.mgr.opts.ReadyWait = 400 * time.Millisecond
	n.mgr.opts.LocalNetworks = func() []meshnet.Network {
		return []meshnet.Network{{ID: meshnet.TailscaleID, Kind: meshnet.KindOverlay, Label: "Tailscale", Iface: "lo0",
			Addrs: []netip.Addr{netip.MustParseAddr("127.0.0.1")}, Prefixes: []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}}}
	}
	// No mesh network chosen: nothing to wait for.
	start := time.Now()
	if a, _, _ := n.mgr.joinerSelf(ctx); len(a) != 0 || time.Since(start) > 200*time.Millisecond {
		t.Fatalf("no network: %v after %v", a, time.Since(start))
	}
	if _, err := n.data.SetMeshSettings(ctx, contracts.MeshSettings{Port: 40123, Networks: []contracts.NetworkChoice{{ID: meshnet.TailscaleID, Listen: true, DecidedAt: time.Now()}}}); err != nil {
		t.Fatal(err)
	}
	// Chosen, never reported: past the wait, the network's own address at the mesh port.
	a, _, reported := n.mgr.joinerSelf(ctx)
	if reported || len(a) != 1 || a[0].Addr != "127.0.0.1:40123" || a[0].Network != meshnet.TailscaleID || a[0].Label != "Tailscale" {
		t.Fatalf("derived: %+v reported=%v", a, reported)
	}
	// Reported during the wait: what the Mesh Service says.
	go func() {
		time.Sleep(100 * time.Millisecond)
		raw, _ := json.Marshal(contracts.MeshListener{Node: "mac", HostsSessions: true, Networks: []contracts.ListeningNetwork{{ID: meshnet.TailscaleID, Listening: true}},
			Addresses: []contracts.NetAddress{{Via: "direct", Addr: "127.0.0.1:40999", Kind: "overlay", Label: "Tailscale", Network: meshnet.TailscaleID}}})
		// As the Mesh Service writes it (actor "mesh").
		meshData := dataclient.New(n.dataURL, "mesh", dataclient.WithToken(n.token))
		if err := meshData.Append(ctx, &contracts.Envelope{Type: contracts.MeshListening, Actor: "mesh", Payload: raw}); err != nil {
			t.Errorf("append: %v", err)
		}
	}()
	a, hosts, reported := n.mgr.joinerSelf(ctx)
	if !reported || !hosts || len(a) != 1 || a[0].Addr != "127.0.0.1:40999" {
		t.Fatalf("reported: %+v hosts=%v reported=%v", a, hosts, reported)
	}
}
