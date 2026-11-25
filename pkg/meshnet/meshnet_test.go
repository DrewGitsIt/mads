package meshnet

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func pfx(s string) netip.Prefix { return netip.MustParsePrefix(s) }

func fakeSys(ifs []Iface, gws []Gateway, wifi map[string]string) System {
	return System{
		Interfaces: func() ([]Iface, error) { return ifs, nil },
		Gateways:   func() []Gateway { return gws },
		Wireless: func(name string) (bool, string) {
			ssid, ok := wifi[name]
			return ok, ssid
		},
	}
}

func byID(ns []Network) map[string]Network {
	out := map[string]Network{}
	for _, n := range ns {
		out[n.ID] = n
	}
	return out
}

func TestDetectLinuxTable(t *testing.T) {
	sys := fakeSys([]Iface{
		{Name: "lo", Up: true, Loopback: true, Addrs: []netip.Prefix{pfx("127.0.0.1/8"), pfx("::1/128")}},
		{Name: "eth0", Up: true, Addrs: []netip.Prefix{pfx("192.168.1.20/24"), pfx("fe80::1/64"), pfx("fd12:3456::20/64"), pfx("2001:db8::20/64")}},
		{Name: "wlan0", Up: true, Addrs: []netip.Prefix{pfx("10.20.30.40/16")}},
		{Name: "tailscale0", Up: true, PointToPoint: true, Addrs: []netip.Prefix{pfx("100.101.4.7/32"), pfx("fd7a:115c:a1e0::4/128")}},
		{Name: "wt0", Up: true, Addrs: []netip.Prefix{pfx("100.90.1.2/16")}},
		{Name: "zt3jnxxx", Up: true, Addrs: []netip.Prefix{pfx("10.147.17.5/24")}},
		{Name: "docker0", Up: true, Addrs: []netip.Prefix{pfx("172.17.0.1/16")}},
		{Name: "br-1234", Up: true, Addrs: []netip.Prefix{pfx("172.18.0.1/16")}},
		{Name: "veth9", Up: true, Addrs: []netip.Prefix{pfx("169.254.3.3/16")}},
		{Name: "eth1", Up: false, Addrs: []netip.Prefix{pfx("192.168.9.9/24")}},
	}, []Gateway{{Iface: "eth0", IP: netip.MustParseAddr("192.168.1.1"), HW: "aa:bb:cc:dd:ee:ff"}}, map[string]string{"wlan0": "eduroam"})
	ns, err := Detect(context.Background(), sys)
	if err != nil {
		t.Fatal(err)
	}
	got := byID(ns)
	var ids []string
	for _, n := range ns {
		ids = append(ids, n.ID)
	}
	want := []string{"tailscale", "vpn:netbird:100.90.0.0/16", "vpn:zerotier:10.147.17.0/24", "lan:10.20.0.0/16", "lan:192.168.1.0/24/192.168.1.1", "public:eth0"}
	if strings.Join(ids, " ") != strings.Join(want, " ") {
		t.Fatalf("networks %v, want %v", ids, want)
	}
	ts := got["tailscale"]
	if ts.Kind != KindOverlay || ts.Label != "Tailscale" || len(ts.Addrs) != 2 || !ts.Contains(netip.MustParseAddr("100.64.9.9")) {
		t.Errorf("tailscale = %+v", ts)
	}
	if nb := got["vpn:netbird:100.90.0.0/16"]; nb.Label != "VPN (NetBird)" || nb.Kind != KindOverlay || !nb.Contains(netip.MustParseAddr("100.101.0.1")) {
		t.Errorf("NetBird on 100.64/10 must not read as Tailscale (its range is CGNAT space): %+v", nb)
	}
	if zt := got["vpn:zerotier:10.147.17.0/24"]; zt.Label != "VPN (ZeroTier)" {
		t.Errorf("zerotier = %+v", zt)
	}
	lan := got["lan:192.168.1.0/24/192.168.1.1"]
	if lan.Kind != KindLAN || lan.Label != "Wired network (eth0)" || lan.GatewayHW != "aa:bb:cc:dd:ee:ff" || lan.Shared {
		t.Errorf("eth0 lan = %+v", lan)
	}
	// The ULA rides with the LAN; the global IPv6 is public; link-local is gone.
	if len(lan.Addrs) != 2 || lan.Addrs[1] != netip.MustParseAddr("fd12:3456::20") {
		t.Errorf("eth0 lan addrs = %v", lan.Addrs)
	}
	if pub := got["public:eth0"]; pub.Kind != KindPublic || pub.Label != "Public IPv6" || len(pub.Addrs) != 1 {
		t.Errorf("public = %+v", pub)
	}
	wifi := got["lan:10.20.0.0/16"]
	if wifi.Label != `Wi-Fi "eduroam"` || !wifi.Shared || !wifi.Wireless {
		t.Errorf("campus wifi = %+v", wifi)
	}
	for _, n := range ns {
		if strings.Contains(n.Iface, "docker") || strings.Contains(n.Iface, "br-") || n.Iface == "eth1" || n.Iface == "veth9" {
			t.Errorf("hidden interface detected: %+v", n)
		}
	}
}

func TestDetectMacTable(t *testing.T) {
	sys := fakeSys([]Iface{
		{Name: "en0", Up: true, Addrs: []netip.Prefix{pfx("192.168.1.30/24")}},
		{Name: "awdl0", Up: true, Addrs: []netip.Prefix{pfx("fe80::5/64")}},
		{Name: "bridge100", Up: true, Addrs: []netip.Prefix{pfx("192.168.64.1/24")}},
		{Name: "utun4", Up: true, PointToPoint: true, Addrs: []netip.Prefix{pfx("100.70.1.1/32"), pfx("fd7a:115c:a1e0::9/128")}},
		{Name: "utun7", Up: true, PointToPoint: true, Addrs: []netip.Prefix{pfx("100.88.1.1/32")}},
	}, []Gateway{{Iface: "en0", IP: netip.MustParseAddr("192.168.1.1")}}, map[string]string{"en0": ""})
	ns, err := Detect(context.Background(), sys)
	if err != nil {
		t.Fatal(err)
	}
	got := byID(ns)
	if ts, ok := got["tailscale"]; !ok || ts.Iface != "utun4" {
		t.Errorf("utun with Tailscale's ULA must be Tailscale: %+v", ns)
	}
	other, ok := got["vpn:utun:100.88.1.1/32"]
	if !ok || other.Label != "VPN (utun7)" {
		t.Errorf("a utun on 100.64/10 alone is a VPN, not Tailscale: %+v", ns)
	}
	// Round-1 review S1: its /32 doesn't stand for the whole CGNAT range,
	// nor does a MagicDNS name count as on it.
	if other.Contains(netip.MustParseAddr("100.101.4.7")) ||
		OnNetwork(Candidate{Via: "direct", Addr: "pop-os.tail4c2e.ts.net:17380"}, []Network{other}, nil, false) {
		t.Errorf("an unconfirmed VPN widened to 100.64/10: %+v", other)
	}
	if lan := got["lan:192.168.1.0/24/192.168.1.1"]; lan.Label != "Wi-Fi" {
		t.Errorf("en0 Wi-Fi without SSID = %+v", lan)
	}
	if _, ok := got["lan:192.168.64.0/24"]; ok {
		t.Error("bridge100 (internet sharing) must be hidden")
	}
}

// A DHCP change on the same Wi-Fi keeps the network's id: the choice is
// remembered across it.
func TestNetworkIDStableAcrossDHCPChange(t *testing.T) {
	gw := []Gateway{{Iface: "en0", IP: netip.MustParseAddr("192.168.1.1")}}
	a, _ := Detect(context.Background(), fakeSys([]Iface{{Name: "en0", Up: true, Addrs: []netip.Prefix{pfx("192.168.1.20/24")}}}, gw, nil))
	b, _ := Detect(context.Background(), fakeSys([]Iface{{Name: "en0", Up: true, Addrs: []netip.Prefix{pfx("192.168.1.42/24")}}}, gw, nil))
	if a[0].ID != b[0].ID || a[0].ID != "lan:192.168.1.0/24/192.168.1.1" {
		t.Fatalf("ids %q vs %q", a[0].ID, b[0].ID)
	}
	if SameNetworks(a, b) {
		t.Fatal("the address changed: listeners must move")
	}
}

type helperFunc func(*Network)

func (h helperFunc) Describe(_ context.Context, n *Network) { h(n) }

func TestDetectHelpers(t *testing.T) {
	sys := fakeSys([]Iface{{Name: "utun3", Up: true, PointToPoint: true, Addrs: []netip.Prefix{pfx("100.70.1.1/32")}}}, nil, nil)
	ns, _ := Detect(context.Background(), sys, helperFunc(func(n *Network) {
		if n.Iface == "utun3" {
			n.ID, n.Label, n.Confirmed, n.DNSName = TailscaleID, "Tailscale", true, "mac.tail.ts.net"
		}
	}))
	if len(ns) != 1 || ns[0].ID != TailscaleID || !ns[0].Confirmed {
		t.Fatalf("%+v", ns)
	}
}

func TestDetectMergesSameLANOnTwoInterfaces(t *testing.T) {
	gw := []Gateway{{Iface: "en0", IP: netip.MustParseAddr("192.168.1.1")}, {Iface: "en5", IP: netip.MustParseAddr("192.168.1.1")}}
	ns, _ := Detect(context.Background(), fakeSys([]Iface{
		{Name: "en0", Up: true, Addrs: []netip.Prefix{pfx("192.168.1.20/24")}},
		{Name: "en5", Up: true, Addrs: []netip.Prefix{pfx("192.168.1.21/24")}},
	}, gw, nil))
	if len(ns) != 1 || len(ns[0].Addrs) != 2 {
		t.Fatalf("%+v", ns)
	}
}

// --- The dial policy (S2) ---------------------------------------------------------

func TestDialPolicy(t *testing.T) {
	local := []netip.Prefix{pfx("192.168.1.0/24"), pfx("100.64.0.0/10"), pfx("fd7a:115c:a1e0::/48")}
	pol := &DialPolicy{Local: func() []netip.Prefix { return local }}
	cases := []struct {
		ip        string
		userAdded bool
		ok        bool
	}{
		{"127.0.0.1", false, false},
		{"127.5.5.5", false, false},
		{"::1", false, false},
		{"::ffff:127.0.0.1", false, false},
		{"0.0.0.0", false, false},
		{"::", false, false},
		{"169.254.169.254", false, false},
		{"169.254.169.254", true, false}, // the user exception never reaches metadata
		{"::ffff:169.254.169.254", false, false},
		{"fe80::1", false, false},
		{"fd00:ec2::254", false, false},
		{"224.0.0.251", false, false},
		{"ff02::fb", false, false},
		{"255.255.255.255", false, false},
		{"240.0.0.1", false, false},
		{"0.1.2.3", false, false},
		{"::127.0.0.1", false, false},        // IPv4-compatible loopback
		{"64:ff9b::a9fe:a9fe", false, false}, // NAT64 metadata
		{"192.168.1.20", false, true},
		{"192.168.2.20", false, false}, // a home address seen from elsewhere
		{"192.168.2.20", true, true},   // the user's own exception
		{"10.0.0.5", false, false},
		{"10.0.0.5", true, true},
		{"100.101.4.7", false, true},
		{"fd7a:115c:a1e0::9", false, true},
		{"fd12::1", false, false},
		{"8.8.8.8", false, true},
		{"2001:4860::8888", false, true},
	}
	for _, c := range cases {
		why := pol.CheckIP(netip.MustParseAddr(c.ip), c.userAdded)
		if (why == "") != c.ok {
			t.Errorf("CheckIP(%s, userAdded=%v) = %q, want ok=%v", c.ip, c.userAdded, why, c.ok)
		}
	}
	if why := (&DialPolicy{AllowLoopback: true}).CheckIP(netip.MustParseAddr("127.0.0.1"), false); why != "" {
		t.Errorf("AllowLoopback: %q", why)
	}
	// The zero policy refuses private addresses: it knows no networks.
	if why := (&DialPolicy{}).CheckIP(netip.MustParseAddr("192.168.1.20"), false); why == "" {
		t.Error("zero policy dialled a private address")
	}
}

func TestCheckShape(t *testing.T) {
	good := []string{"192.168.1.20:17380", "[fd7a:115c:a1e0::9]:17380", "pop-os.tail4c2e.ts.net:17380", "example.com:443", "127.0.0.1:17380"}
	bad := []string{"", "pop-os", "http://pop-os:17380", "pop-os:80", "pop-os:22", "pop-os:0", "pop-os:99999", "pop-os:017380",
		"0.0.0.0:17380", "169.254.169.254:17380", "[fe80::1%en0]:17380", "224.0.0.1:17380", "bad host:17380", "-x.example:17380",
		"localhost:17380", "a.localhost:17380", "user@host:17380", "host/path:17380"}
	for _, a := range good {
		if err := CheckShape(a); err != nil {
			t.Errorf("CheckShape(%q) = %v", a, err)
		}
	}
	for _, a := range bad {
		if err := CheckShape(a); err == nil {
			t.Errorf("CheckShape(%q) passed", a)
		}
	}
	pol := &DialPolicy{}
	if err := pol.CheckAddress("127.0.0.1:17380"); err == nil {
		t.Error("an arriving loopback address must be refused (S3)")
	}
	if err := (&DialPolicy{AllowLoopback: true}).CheckAddress("127.0.0.1:17380"); err != nil {
		t.Errorf("loopback allowed: %v", err)
	}
}

func TestCleanCandidates(t *testing.T) {
	in := []Candidate{
		{Via: "relay", Addr: "r.example:443"},
		{Addr: "192.168.1.20:17380", Kind: "weird", Label: "Home\u202e\x01"},
		{Via: "direct", Addr: "192.168.1.20:17380"},
		{Via: "direct", Addr: "169.254.169.254:17380"},
	}
	for i := 0; i < 12; i++ {
		in = append(in, Candidate{Via: "direct", Addr: net.JoinHostPort("10.0.0."+string(rune('1'+i%9)), "1738"+string(rune('0'+i%10)))})
	}
	out := CleanCandidates(in)
	if len(out) > MaxCandidates {
		t.Fatalf("%d candidates", len(out))
	}
	if out[0].Addr != "192.168.1.20:17380" || out[0].Via != DirectVia || out[0].Kind != "" || out[0].Label != "Home" {
		t.Errorf("first = %+v", out[0])
	}
	for _, c := range out {
		if c.Via != DirectVia || strings.HasPrefix(c.Addr, "169.254") {
			t.Errorf("kept %+v", c)
		}
	}
}

// A DNS name that resolves to loopback is refused at dial time: the
// Control hook sees the address actually connected.
func TestDialRefusesResolvedLoopback(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	d := &Direct{Policy: &DialPolicy{}}
	// "localhost" itself fails the shape check; a name that resolves to
	// loopback is caught by the hook instead.
	ctl := d.Policy.Control(false)
	if err := ctl("tcp4", "127.0.0.1:"+port, nil); !errors.Is(err, ErrRefusedAddress) {
		t.Fatalf("control on loopback = %v", err)
	}
	if _, err := d.Dial(context.Background(), "", Candidate{Via: DirectVia, Addr: "127.0.0.1:" + port}); !errors.Is(err, ErrRefusedAddress) {
		t.Fatalf("dial loopback = %v", err)
	}
	if _, err := d.Dial(context.Background(), "", Candidate{Via: DirectVia, Addr: "localhost:" + port}); err == nil {
		t.Fatal("dial localhost passed")
	}
	allowed := &Direct{Policy: &DialPolicy{AllowLoopback: true}}
	c, err := allowed.Dial(context.Background(), "", Candidate{Via: DirectVia, Addr: "127.0.0.1:" + port})
	if err != nil {
		t.Fatalf("allowed loopback: %v", err)
	}
	_ = c.Close()
}

func TestDirectListenAdvertise(t *testing.T) {
	d := &Direct{}
	n := Network{ID: "lan:test", Kind: KindLAN, Label: "Test", Addrs: []netip.Addr{netip.MustParseAddr("127.0.0.1")}}
	ls, err := d.Listen(context.Background(), n, 0)
	if err != nil || len(ls) != 1 {
		t.Fatalf("listen: %v %v", ls, err)
	}
	defer func() { _ = ls[0].Close() }()
	cands := d.Advertise(n, ls)
	if len(cands) != 1 || cands[0].Kind != KindLAN || cands[0].Network != "lan:test" || !strings.HasPrefix(cands[0].Addr, "127.0.0.1:") {
		t.Fatalf("%+v", cands)
	}
	if _, err := d.Listen(context.Background(), Network{ID: "x", Addrs: []netip.Addr{netip.IPv4Unspecified()}}, 0); err == nil {
		t.Fatal("bound a wildcard")
	}
}

// --- Ranking ------------------------------------------------------------------------

func TestRank(t *testing.T) {
	cands := []Candidate{
		{Via: "direct", Addr: "8.8.8.8:17380"},
		{Via: "direct", Addr: "100.101.4.7:17380", Kind: KindOverlay},
		{Via: "direct", Addr: "192.168.1.20:17380", Kind: KindLAN},
		{Via: "direct", Addr: "pop-os.tail4c2e.ts.net:17380"},
		{Via: "direct", Addr: "192.168.1.21:17380", Kind: KindLAN},
	}
	got := Rank(cands, "100.101.4.7:17380")
	want := []string{"100.101.4.7:17380", "192.168.1.20:17380", "192.168.1.21:17380", "pop-os.tail4c2e.ts.net:17380", "8.8.8.8:17380"}
	for i, c := range got {
		if c.Addr != want[i] {
			t.Fatalf("rank = %v, want %v", got, want)
		}
	}
	if got := Rank(cands, ""); got[0].Addr != "192.168.1.20:17380" {
		t.Fatalf("no last good: %v", got)
	}
	if !Better(cands[2], cands[1]) || Better(cands[1], cands[2]) {
		t.Fatal("LAN beats VPN (Q8)")
	}
}

func TestOnNetwork(t *testing.T) {
	local := []Network{{ID: TailscaleID, Prefixes: TailnetRanges}, {ID: "lan:x", Prefixes: []netip.Prefix{pfx("192.168.1.0/24")}}}
	for _, c := range []struct {
		addr string
		ok   bool
	}{
		{"192.168.1.20:17380", true}, {"192.168.2.20:17380", false}, {"100.101.4.7:17380", true},
		{"pop-os.tail4c2e.ts.net:17380", true}, {"server.example.com:17380", true}, {"8.8.8.8:17380", true},
	} {
		if got := OnNetwork(Candidate{Via: "direct", Addr: c.addr}, local, nil, false); got != c.ok {
			t.Errorf("OnNetwork(%s) = %v", c.addr, got)
		}
	}
	if OnNetwork(Candidate{Via: "direct", Addr: "pop-os.tail4c2e.ts.net:17380"}, local[1:], nil, false) {
		t.Error("a MagicDNS name without Tailscale here")
	}
	if OnNetwork(Candidate{Via: "relay", Addr: "r:443"}, local, nil, false) {
		t.Error("unknown adapters are skipped")
	}
}

// --- The race -------------------------------------------------------------------------

// fakeClock fires timers only when advanced.
type fakeClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []fakeTimer
}

type fakeTimer struct {
	at time.Time
	ch chan time.Time
}

func newFakeClock() *fakeClock { return &fakeClock{now: time.Unix(1_000_000, 0)} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	ch := make(chan time.Time, 1)
	c.timers = append(c.timers, fakeTimer{at: c.now.Add(d), ch: ch})
	return ch
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	var keep []fakeTimer
	for _, t := range c.timers {
		if !t.at.After(c.now) {
			t.ch <- c.now
		} else {
			keep = append(keep, t)
		}
	}
	c.timers = keep
	c.mu.Unlock()
}

func (c *fakeClock) pending() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.timers)
}

// waitFor polls until f, failing after a second of real time.
func waitFor(t *testing.T, what string, f func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !f() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

type fakeDialer struct {
	mu      sync.Mutex
	started []string
	gates   map[string]chan error // closed or sent to finish an attempt
}

func newFakeDialer(addrs ...string) *fakeDialer {
	d := &fakeDialer{gates: map[string]chan error{}}
	for _, a := range addrs {
		d.gates[a] = make(chan error, 1)
	}
	return d
}

func (d *fakeDialer) dial(ctx context.Context, c Candidate) (string, error) {
	d.mu.Lock()
	d.started = append(d.started, c.Addr)
	g := d.gates[c.Addr]
	d.mu.Unlock()
	select {
	case err := <-g:
		if err != nil {
			return "", err
		}
		return c.Addr, nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func (d *fakeDialer) startedN() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.started)
}

func cands(addrs ...string) []Candidate {
	var out []Candidate
	for _, a := range addrs {
		out = append(out, Candidate{Via: DirectVia, Addr: a})
	}
	return out
}

type raceOut struct {
	v   string
	i   int
	at  []Attempt
	err error
}

func runRace(clock *fakeClock, d *fakeDialer, cs []Candidate) chan raceOut {
	out := make(chan raceOut, 1)
	go func() {
		v, i, at, err := Race(context.Background(), cs, RaceOptions{Clock: clock}, d.dial, nil)
		out <- raceOut{v, i, at, err}
	}()
	return out
}

// The next attempt starts 250 ms later without cancelling the first; the
// first to finish (the pinned handshake, in mads's dial) wins, even if it
// started second.
func TestRaceStaggerAndWinner(t *testing.T) {
	clock := newFakeClock()
	d := newFakeDialer("a:1", "b:2", "c:3")
	out := runRace(clock, d, cands("a:1", "b:2", "c:3"))
	waitFor(t, "first attempt", func() bool { return d.startedN() == 1 })
	clock.Advance(100 * time.Millisecond)
	time.Sleep(5 * time.Millisecond)
	if d.startedN() != 1 {
		t.Fatal("the second started before its stagger")
	}
	clock.Advance(150 * time.Millisecond)
	waitFor(t, "second attempt", func() bool { return d.startedN() == 2 })
	d.gates["b:2"] <- nil // the second finishes first
	r := <-out
	if r.err != nil || r.v != "b:2" || r.i != 1 || !r.at[1].Won || r.at[2].Started {
		t.Fatalf("race = %+v", r)
	}
}

// A pin mismatch (another device answered) at one address goes on to the
// next at once, without waiting out the stagger.
func TestRaceFailureStartsNext(t *testing.T) {
	clock := newFakeClock()
	d := newFakeDialer("a:1", "b:2")
	out := runRace(clock, d, cands("a:1", "b:2"))
	waitFor(t, "first attempt", func() bool { return d.startedN() == 1 })
	d.gates["a:1"] <- errors.New("the device presented key X, not the pinned Y")
	waitFor(t, "second attempt", func() bool { return d.startedN() == 2 })
	d.gates["b:2"] <- nil
	r := <-out
	if r.err != nil || r.v != "b:2" || r.at[0].Err == nil {
		t.Fatalf("race = %+v", r)
	}
}

func TestRaceAllFail(t *testing.T) {
	clock := newFakeClock()
	d := newFakeDialer("a:1", "b:2")
	out := runRace(clock, d, cands("a:1", "b:2"))
	waitFor(t, "first", func() bool { return d.startedN() == 1 })
	d.gates["a:1"] <- errors.New("refused")
	waitFor(t, "second", func() bool { return d.startedN() == 2 })
	d.gates["b:2"] <- errors.New("refused")
	r := <-out
	var re *RaceError
	if !errors.As(r.err, &re) || !errors.Is(r.err, ErrNoneAnswered) || len(re.Attempts) != 2 {
		t.Fatalf("race = %+v", r)
	}
}

// Each attempt has 3 s; the whole race 6 s.
func TestRaceTimeouts(t *testing.T) {
	clock := newFakeClock()
	d := newFakeDialer("a:1", "b:2", "c:3", "d:4")
	out := runRace(clock, d, cands("a:1", "b:2", "c:3", "d:4"))
	waitFor(t, "first", func() bool { return d.startedN() == 1 })
	// 250 ms steps until all four run.
	for i := 2; i <= 4; i++ {
		waitFor(t, "timers", func() bool { return clock.pending() >= 2 })
		clock.Advance(DefaultStagger)
		waitFor(t, "next attempt", func() bool { return d.startedN() == i })
	}
	clock.Advance(DefaultAttempt) // a, b, c and d time out on their own clocks...
	r := <-out
	var re *RaceError
	if !errors.As(r.err, &re) {
		t.Fatalf("race = %+v", r)
	}
	for _, a := range re.Attempts {
		if !errors.Is(a.Err, ErrAttemptTimeout) {
			t.Errorf("attempt %s: %v", a.Candidate.Addr, a.Err)
		}
	}

	clock = newFakeClock()
	d = newFakeDialer("x:1")
	out = runRace(clock, d, cands("x:1"))
	waitFor(t, "attempt", func() bool { return d.startedN() == 1 })
	clock.Advance(DefaultTotal)
	if r := <-out; !errors.Is(r.err, ErrRaceTimeout) && !errors.Is(r.err, ErrNoneAnswered) {
		t.Fatalf("total timeout: %v", r.err)
	}
}

func TestRaceReleasesLateWinners(t *testing.T) {
	clock := newFakeClock()
	released := make(chan string, 2)
	dial := func(ctx context.Context, c Candidate) (string, error) {
		if c.Addr == "slow:1" {
			time.Sleep(20 * time.Millisecond)
			return c.Addr, nil // finishes despite the cancel
		}
		return c.Addr, nil
	}
	go func() {
		for clock.pending() == 0 {
			time.Sleep(time.Millisecond)
		}
	}()
	v, _, _, err := Race(context.Background(), cands("fast:1"), RaceOptions{Clock: clock}, dial, func(s string) { released <- s })
	if err != nil || v != "fast:1" {
		t.Fatalf("%v %v", v, err)
	}
}

func TestRaceEmpty(t *testing.T) {
	if _, _, _, err := Race(context.Background(), nil, RaceOptions{}, func(context.Context, Candidate) (int, error) { return 0, nil }, nil); !errors.Is(err, ErrNoneAnswered) {
		t.Fatal(err)
	}
}

// Round-1 review M6: a VPN is known by its address prefix, so macOS
// renumbering utunN keeps it, and another VPN on the same name isn't it.
func TestVPNIdentityByPrefix(t *testing.T) {
	sys := func(name, addr string) System {
		return fakeSys([]Iface{{Name: name, Up: true, PointToPoint: true, Addrs: []netip.Prefix{pfx(addr)}}}, nil, nil)
	}
	a, _ := Detect(context.Background(), sys("utun3", "10.8.0.2/24"))
	b, _ := Detect(context.Background(), sys("utun5", "10.8.0.2/24"))
	c, _ := Detect(context.Background(), sys("utun3", "10.9.0.2/24"))
	if a[0].ID != b[0].ID || a[0].ID == c[0].ID || a[0].ID != "vpn:utun:10.8.0.0/24" {
		t.Fatalf("ids %s %s %s", a[0].ID, b[0].ID, c[0].ID)
	}
}

// Routes through a VPN's interface are on it: a /32 VPN address with its
// range routed there (S1: from the route table, not a guess).
func TestVPNRoutes(t *testing.T) {
	s := fakeSys([]Iface{{Name: "utun7", Up: true, PointToPoint: true, Addrs: []netip.Prefix{pfx("100.88.1.1/32")}}}, nil, nil)
	s.Routes = func() map[string][]netip.Prefix {
		return map[string][]netip.Prefix{"utun7": {pfx("100.64.0.0/10")}, "en0": {pfx("10.0.0.0/8")}}
	}
	ns, _ := Detect(context.Background(), s)
	if !ns[0].Contains(netip.MustParseAddr("100.101.4.7")) || ns[0].Contains(netip.MustParseAddr("10.1.1.1")) {
		t.Fatalf("%+v", ns[0])
	}
}

// The test hooks: loopback only, and a networks file refusing anything
// else.
func TestTestHooks(t *testing.T) {
	lo := LoopbackOnly{Adapter: &Direct{}}
	if _, err := lo.Listen(context.Background(), Network{ID: "x", Addrs: []netip.Addr{netip.MustParseAddr("100.101.4.7")}}, 0); err == nil {
		t.Fatal("bound a real address in loopback mode")
	}
	ls, err := lo.Listen(context.Background(), Network{ID: "x", Addrs: []netip.Addr{netip.MustParseAddr("127.0.0.1")}}, 0)
	if err != nil {
		t.Fatal(err)
	}
	_ = ls[0].Close()
	dir := t.TempDir()
	good := dir + "/good.json"
	_ = os.WriteFile(good, []byte(`[{"id":"tailscale","kind":"overlay","label":"Tailscale","iface":"lo0","addrs":["127.0.0.1"],"prefixes":["127.0.0.0/8"]}]`), 0o600)
	ns, err := FileNetworks{Direct: &Direct{}, Path: good}.Detect(context.Background())
	if err != nil || len(ns) != 1 || ns[0].ID != "tailscale" {
		t.Fatalf("%+v %v", ns, err)
	}
	bad := dir + "/bad.json"
	_ = os.WriteFile(bad, []byte(`[{"id":"lan:x","addrs":["192.168.1.2"]}]`), 0o600)
	if _, err := ReadNetworks(bad); err == nil {
		t.Fatal("a real address in a fake networks file")
	}
	// Round-2 review T8: nor a real interface or a non-loopback prefix.
	for i, body := range []string{
		`[{"id":"lan:x","iface":"en0","addrs":["127.0.0.1"]}]`,
		`[{"id":"lan:x","addrs":["127.0.0.1"],"prefixes":["192.168.1.0/24"]}]`,
		`[{"id":"lan:x","addrs":["127.0.0.1"],"prefixes":["0.0.0.0/0"]}]`,
		`[{"id":"lan:x","addrs":["::1"],"prefixes":["::/0"]}]`,
	} {
		f := fmt.Sprintf("%s/bad%d.json", dir, i)
		_ = os.WriteFile(f, []byte(body), 0o600)
		if _, err := ReadNetworks(f); err == nil {
			t.Errorf("accepted %s", body)
		}
	}
}

// Round-2 review R9: the VPN's family is part of its id, so a WireGuard
// and an OpenVPN tun on the same stock subnet aren't one network.
func TestVPNIdentityHasItsFamily(t *testing.T) {
	sys := func(name string) System {
		return fakeSys([]Iface{{Name: name, Up: true, PointToPoint: true, Addrs: []netip.Prefix{pfx("10.8.0.2/24")}}}, nil, nil)
	}
	wg, _ := Detect(context.Background(), sys("wg0"))
	tun, _ := Detect(context.Background(), sys("tun0"))
	if len(wg) != 1 || len(tun) != 1 || wg[0].ID == tun[0].ID || wg[0].ID != "vpn:wireguard:10.8.0.0/24" || tun[0].ID != "vpn:tun:10.8.0.0/24" {
		t.Fatalf("wg %+v tun %+v", wg, tun)
	}
}

// Round-2 review R3: a full tunnel's routes (0/1 + 128/1, a default, or
// anything wider than /8) never put the whole internet "on" the VPN.
func TestVPNIgnoresFullTunnelRoutes(t *testing.T) {
	sys := fakeSys([]Iface{{Name: "tun0", Up: true, PointToPoint: true, Addrs: []netip.Prefix{pfx("10.8.0.2/32")}}}, nil, nil)
	sys.Routes = func() map[string][]netip.Prefix {
		return map[string][]netip.Prefix{"tun0": {pfx("0.0.0.0/1"), pfx("128.0.0.0/1"), pfx("0.0.0.0/0"), pfx("10.0.0.0/7"), pfx("10.8.0.0/24")}}
	}
	ns, err := Detect(context.Background(), sys)
	if err != nil || len(ns) != 1 {
		t.Fatalf("%+v %v", ns, err)
	}
	for _, ip := range []string{"192.168.1.5", "8.8.8.8", "11.0.0.1"} {
		if ns[0].Contains(netip.MustParseAddr(ip)) {
			t.Errorf("%s isn't on the VPN: %v", ip, ns[0].Prefixes)
		}
	}
	if !ns[0].Contains(netip.MustParseAddr("10.8.0.9")) {
		t.Errorf("its own /24 route is: %v", ns[0].Prefixes)
	}
}

// Round-2 review R2: the most specific network an address is on wins.
func TestMostSpecificNetwork(t *testing.T) {
	vpn := Network{ID: "vpn", Prefixes: []netip.Prefix{pfx("192.168.0.0/16")}}
	lan := Network{ID: "lan", Prefixes: []netip.Prefix{pfx("192.168.1.0/24")}}
	for _, ns := range [][]Network{{vpn, lan}, {lan, vpn}} {
		if n, ok := MostSpecific(ns, netip.MustParseAddr("192.168.1.7")); !ok || n.ID != "lan" {
			t.Errorf("%v: got %s", ns, n.ID)
		}
	}
	if n, ok := MostSpecific([]Network{vpn, lan}, netip.MustParseAddr("192.168.9.7")); !ok || n.ID != "vpn" {
		t.Errorf("got %s", n.ID)
	}
	if _, ok := MostSpecific([]Network{vpn, lan}, netip.MustParseAddr("10.0.0.1")); ok {
		t.Error("on none")
	}
}
