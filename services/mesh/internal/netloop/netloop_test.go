package netloop

import (
	"context"
	"net"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/devicekey"
	"github.com/drewkidwell/mads/pkg/meshnet"
)

// fakeAdapter detects whatever the test says and binds each network on
// 127.0.0.1 (an ephemeral port), recording what it was asked to bind.
type fakeAdapter struct {
	mu    sync.Mutex
	nets  []meshnet.Network
	binds []string // network ids, in order
}

func (f *fakeAdapter) ID() string { return "direct" }
func (f *fakeAdapter) Detect(context.Context) ([]meshnet.Network, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]meshnet.Network(nil), f.nets...), nil
}
func (f *fakeAdapter) Listen(_ context.Context, n meshnet.Network, _ int) ([]net.Listener, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	f.mu.Lock()
	f.binds = append(f.binds, n.ID)
	f.mu.Unlock()
	return []net.Listener{l}, nil
}
func (f *fakeAdapter) Advertise(n meshnet.Network, ls []net.Listener) []meshnet.Candidate {
	return (&meshnet.Direct{}).Advertise(n, ls)
}
func (f *fakeAdapter) Dial(context.Context, devicekey.Fingerprint, meshnet.Candidate) (net.Conn, error) {
	return nil, net.ErrClosed
}
func (f *fakeAdapter) set(ns ...meshnet.Network) {
	f.mu.Lock()
	f.nets = ns
	f.mu.Unlock()
}
func (f *fakeAdapter) bindCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.binds)
}

var (
	tsNet  = meshnet.Network{ID: "tailscale", Kind: meshnet.KindOverlay, Label: "Tailscale", Iface: "tailscale0", Addrs: []netip.Addr{netip.MustParseAddr("100.101.4.7")}}
	lanNet = meshnet.Network{ID: "lan:192.168.1.0/24/192.168.1.1", Kind: meshnet.KindLAN, Label: "Wi-Fi", Iface: "wlan0",
		Addrs: []netip.Addr{netip.MustParseAddr("192.168.1.20")}, GatewayHW: "aa:aa:aa:aa:aa:aa"}
	pubNet = meshnet.Network{ID: "public:eth0", Kind: meshnet.KindPublic, Label: "Public IPv6", Addrs: []netip.Addr{netip.MustParseAddr("2001:db8::1")}}
)

type harness struct {
	ad       *fakeAdapter
	loop     *Loop
	mu       sync.Mutex
	settings contracts.MeshSettings
	have     bool
	window   time.Time
	reports  []contracts.MeshListener
	adverts  map[string]int
	bound    []string
	now      time.Time
}

func newHarness(t *testing.T) *harness {
	h := &harness{ad: &fakeAdapter{}, adverts: map[string]int{}, now: time.Unix(1_000_000, 0)}
	h.loop = New(Options{
		Adapter:  h.ad,
		Settings: func() (contracts.MeshSettings, bool) { h.mu.Lock(); defer h.mu.Unlock(); return h.settings, h.have },
		Window:   func() time.Time { h.mu.Lock(); defer h.mu.Unlock(); return h.window },
		Bind:     func(n meshnet.Network, _ net.Listener) { h.mu.Lock(); h.bound = append(h.bound, n.ID); h.mu.Unlock() },
		Report:   func(r contracts.MeshListener) { h.mu.Lock(); h.reports = append(h.reports, r); h.mu.Unlock() },
		Advertise: func(ctx context.Context, n meshnet.Network, _ int, _ []netip.Addr) error {
			h.mu.Lock()
			h.adverts[n.ID]++
			h.mu.Unlock()
			<-ctx.Done()
			h.mu.Lock()
			h.adverts[n.ID]--
			h.mu.Unlock()
			return nil
		},
		Base:     contracts.MeshListener{Fingerprint: "FP", Platform: "linux", HostsSessions: true},
		Debounce: time.Nanosecond,
		// The clock moves a second per reading, so the report's debounce
		// never holds a test's report back.
		Now: func() time.Time { h.mu.Lock(); defer h.mu.Unlock(); h.now = h.now.Add(time.Second); return h.now },
	})
	t.Cleanup(h.loop.closeAll)
	return h
}

func (h *harness) choose(ids ...string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.settings = contracts.MeshSettings{}
	for _, id := range ids {
		c := contracts.NetworkChoice{ID: id, Listen: true}
		if strings.HasPrefix(id, "lan:") {
			c.GatewayHW = lanNet.GatewayHW // a LAN is chosen with its router's (R10)
		}
		h.settings.Networks = append(h.settings.Networks, c)
	}
	h.have = true
}

func (h *harness) lastReport(t *testing.T) contracts.MeshListener {
	t.Helper()
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.reports) == 0 {
		t.Fatal("no report")
	}
	return h.reports[len(h.reports)-1]
}

func (h *harness) advertising(id string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.adverts[id]
}

// A fresh install listens nowhere until a mesh network is chosen, and
// nothing is listened on before the settings are read at all.
func TestFreshInstallListensNowhere(t *testing.T) {
	h := newHarness(t)
	h.ad.set(tsNet, lanNet, pubNet)
	h.loop.Scan(context.Background())
	if h.ad.bindCount() != 0 {
		t.Fatal("listened before the settings were read")
	}
	h.choose()
	h.loop.Scan(context.Background())
	if h.ad.bindCount() != 0 || len(h.loop.Addresses()) != 0 {
		t.Fatal("a fresh install listened")
	}
	r := h.lastReport(t)
	if len(r.Networks) != 3 || len(r.Addresses) != 0 || r.MeshURL != "" {
		t.Fatalf("report %+v", r)
	}
	for _, n := range r.Networks {
		if n.Listening || n.Chosen {
			t.Fatalf("network %+v", n)
		}
	}
}

// Only chosen networks are listened on: never one the user didn't pick.
func TestListensOnlyOnChosenNetworks(t *testing.T) {
	h := newHarness(t)
	h.ad.set(tsNet, lanNet, pubNet)
	h.choose("tailscale")
	h.loop.Scan(context.Background())
	if h.ad.binds[0] != "tailscale" || h.ad.bindCount() != 1 {
		t.Fatalf("binds %v", h.ad.binds)
	}
	addrs := h.loop.Addresses()
	if len(addrs) != 1 || addrs[0].Kind != "overlay" || addrs[0].Network != "tailscale" {
		t.Fatalf("addresses %+v", addrs)
	}
	r := h.lastReport(t)
	for _, n := range r.Networks {
		if (n.ID == "tailscale") != n.Listening {
			t.Errorf("%s listening=%v", n.ID, n.Listening)
		}
	}
	// A rescan with nothing changed binds nothing new.
	h.loop.Scan(context.Background())
	if h.ad.bindCount() != 1 {
		t.Fatalf("rebound: %v", h.ad.binds)
	}
	// Also reachable from the internet: the public network too.
	h.mu.Lock()
	h.settings.Public = true
	h.mu.Unlock()
	h.loop.Scan(context.Background())
	if h.ad.bindCount() != 2 || h.ad.binds[1] != "public:eth0" {
		t.Fatalf("public: %v", h.ad.binds)
	}
}

// The host's DHCP address changes on the same Wi-Fi: the choice holds
// (same network id), the listener moves, and the report says from where.
func TestListenerMovesOnAddressChange(t *testing.T) {
	h := newHarness(t)
	h.ad.set(lanNet)
	h.choose(lanNet.ID)
	h.loop.Scan(context.Background())
	moved := lanNet
	moved.Addrs = []netip.Addr{netip.MustParseAddr("192.168.1.42")}
	h.ad.set(moved)
	h.loop.Scan(context.Background())
	if h.ad.bindCount() != 2 {
		t.Fatalf("binds %v", h.ad.binds)
	}
	r := h.lastReport(t)
	if len(r.Networks) != 1 || len(r.Networks[0].ChangedFrom) != 1 || r.Networks[0].ChangedFrom[0] != "192.168.1.20" || r.Networks[0].Addrs[0] != "192.168.1.42" {
		t.Fatalf("report %+v", r.Networks)
	}
}

// A chosen network this device isn't on now is reported, not listened on;
// a LAN with the same addresses behind another router isn't the one chosen.
func TestChosenNetworkMissingOrDifferentRouter(t *testing.T) {
	h := newHarness(t)
	h.ad.set(lanNet)
	h.mu.Lock()
	h.settings = contracts.MeshSettings{Networks: []contracts.NetworkChoice{{ID: "tailscale", Listen: true}, {ID: lanNet.ID, Listen: true, GatewayHW: "bb:bb:bb:bb:bb:bb"}}}
	h.have = true
	h.mu.Unlock()
	h.loop.Scan(context.Background())
	if h.ad.bindCount() != 0 {
		t.Fatalf("binds %v", h.ad.binds)
	}
	r := h.lastReport(t)
	why := map[string]string{}
	for _, n := range r.Networks {
		why[n.ID] = n.Error
	}
	if why["tailscale"] != "this device isn't on it now" || why[lanNet.ID] != "a different network with the same addresses" {
		t.Fatalf("report %+v", r.Networks)
	}
}

// The mDNS advert runs only while the setup window is open, only on LAN
// mesh networks, and stops when the window closes.
func TestAdvertOnlyInWindowOnLAN(t *testing.T) {
	h := newHarness(t)
	h.ad.set(tsNet, lanNet)
	h.choose("tailscale", lanNet.ID)
	h.loop.Scan(context.Background())
	if h.advertising(lanNet.ID) != 0 {
		t.Fatal("advertised outside the window")
	}
	h.mu.Lock()
	h.window = h.now.Add(5 * time.Minute)
	h.mu.Unlock()
	h.loop.Scan(context.Background())
	waitFor(t, func() bool { return h.advertising(lanNet.ID) == 1 })
	if h.advertising("tailscale") != 0 {
		t.Fatal("advertised on the tailnet (it's found through Tailscale's peer list)")
	}
	h.mu.Lock()
	h.now = h.window.Add(time.Second) // the window ran out
	h.mu.Unlock()
	h.loop.Scan(context.Background())
	waitFor(t, func() bool { return h.advertising(lanNet.ID) == 0 })
}

// The Tailscale check on with no tailscaled to ask: the report says so.
func TestReportsCheckUnavailable(t *testing.T) {
	h := newHarness(t)
	h.loop.opts.TailnetCheck = func() bool { return true }
	h.ad.set(tsNet)
	h.choose("tailscale")
	h.loop.Scan(context.Background())
	r := h.lastReport(t)
	if r.TailnetCheckStatus != "unavailable" || r.Tailscale == nil || r.Tailscale.Present || r.Tailscale.Error == "" {
		t.Fatalf("report %+v %+v", r.TailnetCheckStatus, r.Tailscale)
	}
	if _, err := h.loop.TailscaleSelf(); err == nil {
		t.Fatal("a self without tailscaled")
	}
}

// -addr pins the listener to one address whatever the settings.
func TestFixedAddr(t *testing.T) {
	h := newHarness(t)
	h.loop.opts.FixedAddr = "127.0.0.1:0"
	h.ad.set(lanNet)
	h.loop.Scan(context.Background())
	if h.ad.bindCount() != 1 || h.ad.binds[0] != "fixed" {
		t.Fatalf("binds %v", h.ad.binds)
	}
}

func waitFor(t *testing.T, f func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !f() {
		if time.Now().After(deadline) {
			t.Fatal("timed out")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// Round-1 review M5: a LAN chosen by its router's hardware address isn't
// listened on while that address is unknown, nor a Wi-Fi of another name.
func TestLookAlikeLANNotListened(t *testing.T) {
	h := newHarness(t)
	unknown := lanNet
	unknown.GatewayHW = ""
	h.ad.set(unknown)
	h.mu.Lock()
	h.settings = contracts.MeshSettings{Networks: []contracts.NetworkChoice{{ID: lanNet.ID, Listen: true, GatewayHW: "aa:aa:aa:aa:aa:aa"}}}
	h.have = true
	h.mu.Unlock()
	h.loop.Scan(context.Background())
	if h.ad.bindCount() != 0 {
		t.Fatal("listened on a LAN whose router couldn't be confirmed")
	}
	if r := h.lastReport(t); r.Networks[0].Error == "" {
		t.Fatalf("no reason given: %+v", r.Networks)
	}
	other := lanNet
	other.SSID = "Neighbours"
	h.ad.set(other)
	h.mu.Lock()
	h.settings = contracts.MeshSettings{Networks: []contracts.NetworkChoice{{ID: lanNet.ID, Listen: true, GatewayHW: lanNet.GatewayHW, SSID: "Kidwell-5G"}}}
	h.mu.Unlock()
	h.loop.Scan(context.Background())
	if h.ad.bindCount() != 0 {
		t.Fatal("listened on another Wi-Fi with the same addresses")
	}
	same := lanNet
	same.SSID = "Kidwell-5G"
	h.ad.set(same)
	h.loop.Scan(context.Background())
	if h.ad.bindCount() != 1 {
		t.Fatal("the chosen Wi-Fi wasn't listened on")
	}
}

// Round-2 review R10: a LAN choice stored without its router's hardware
// address (an older row, a hand-written one) isn't listened on, even when
// the router's address is known now: it's chosen again.
func TestLANChoiceWithoutGatewayNotListened(t *testing.T) {
	h := newHarness(t)
	h.ad.set(lanNet)
	h.mu.Lock()
	h.settings = contracts.MeshSettings{Networks: []contracts.NetworkChoice{{ID: lanNet.ID, Listen: true}}}
	h.have = true
	h.mu.Unlock()
	h.loop.Scan(context.Background())
	if h.ad.bindCount() != 0 {
		t.Fatal("listened on a LAN chosen without its router's hardware address")
	}
	if r := h.lastReport(t); len(r.Networks) != 1 || !strings.Contains(r.Networks[0].Error, "choose it again") {
		t.Fatalf("reason: %+v", r.Networks)
	}
}
