package tailscale_test

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/meshnet"
	"github.com/drewkidwell/mads/pkg/meshnet/tailscale"
	"github.com/drewkidwell/mads/pkg/meshnet/tailscale/tailscaletest"
)

func selfStatus(f *tailscaletest.Fake) {
	exp := time.Date(2027, 3, 3, 0, 0, 0, 0, time.UTC)
	f.Set(func(f *tailscaletest.Fake) {
		f.Status = tailscale.Status{BackendState: "Running", Self: tailscaletest.Node("mac", 42, "100.70.1.1", "fd7a:115c:a1e0::1")}
		f.Status.Self.KeyExpiry = &exp
		f.Status.Peer = map[string]tailscale.Node{"k1": *tailscaletest.Node("pop-os", 42, "100.101.4.7")}
		f.WhoIs["100.101.4.7"] = tailscaletest.WhoIsAnswer{Node: "pop-os.tail4c2e.ts.net", UserID: 42}
	})
}

func check(t *testing.T, c *tailscale.Client, variant string) {
	t.Helper()
	ctx := context.Background()
	self, err := c.Self(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if self.HostName != "mac" || self.IPv4 != "100.70.1.1" || self.UserID != 42 || self.KeyExpiry == nil {
		t.Fatalf("self = %+v", self)
	}
	st, err := c.Status(ctx, true)
	if err != nil || len(st.Peers()) != 1 || st.Peers()[0].HostName != "pop-os" {
		t.Fatalf("status = %+v, %v", st, err)
	}
	who, err := c.WhoIs(ctx, "100.101.4.7:5555")
	if err != nil || who.NodeName != "pop-os.tail4c2e.ts.net" || !tailscale.Vouches(42, who) {
		t.Fatalf("whois = %+v, %v", who, err)
	}
	if _, err := c.WhoIs(ctx, "192.168.1.9:5555"); !errors.Is(err, tailscale.ErrNotPeer) {
		t.Fatalf("whois stranger = %v", err)
	}
	e, ok := c.Endpoint()
	if !ok || e.Variant != variant {
		t.Fatalf("endpoint = %+v", e)
	}
}

func TestLinuxSocketDefaultPath(t *testing.T) {
	f, loc := tailscaletest.StartLinuxRoot(t)
	selfStatus(f)
	check(t, tailscale.New(loc), "socket")
}

func TestExplicitSocket(t *testing.T) {
	f, sock := tailscaletest.StartSocket(t)
	selfStatus(f)
	check(t, tailscale.NewSocket(sock), "socket")
}

// The standalone macOS app: /Library/Tailscale/ipnport's target is the
// port, sameuserproof-<port> the token, sent as basic auth.
func TestMacStandalone(t *testing.T) {
	f, loc := tailscaletest.StartMacStandalone(t, "s3cret")
	selfStatus(f)
	eps, err := loc.Locate()
	if err != nil || len(eps) != 1 || eps[0].Network != "tcp" || eps[0].Token != "s3cret" || eps[0].Variant != "macos-standalone" {
		t.Fatalf("locate = %+v, %v", eps, err)
	}
	check(t, tailscale.New(loc), "macos-standalone")
}

// The App Store app: sameuserproof-<port>-<token> in its group container;
// a stale file from an earlier run (nothing on its port) is passed over.
func TestMacAppStore(t *testing.T) {
	f, loc := tailscaletest.StartMacAppStore(t, "tok3n", true)
	selfStatus(f)
	eps, err := loc.Locate()
	if err != nil || len(eps) != 2 || eps[0].Token != "tok3n" {
		t.Fatalf("locate = %+v, %v (newest first)", eps, err)
	}
	check(t, tailscale.New(loc), "macos-app-store")
}

// Without a readable group container, the App Store app's endpoint is
// found the way tailscale's CLI finds it: lsof on IPNExtension.
func TestMacAppStoreLsof(t *testing.T) {
	loc := tailscale.Locator{GOOS: "darwin", Root: t.TempDir(), Home: "/Users/test", Getenv: func(string) string { return "" },
		Lsof: func() ([]byte, error) {
			return []byte("p123\nn/Users/test/Library/Group Containers/W5364U7YZB.io.tailscale.ipn.macos/sameuserproof-61234-abcdef\nn/dev/null\n"), nil
		}}
	eps, err := loc.Locate()
	if err != nil || len(eps) != 1 || eps[0].Address != "127.0.0.1:61234" || eps[0].Token != "abcdef" {
		t.Fatalf("locate = %+v, %v", eps, err)
	}
}

func TestMacCLISharedDirAndDaemonSocket(t *testing.T) {
	root := t.TempDir()
	shared := "/shared"
	if err := os.MkdirAll(filepath.Join(root, shared), 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(root, shared, "sameuserproof-4000-zzz"), nil, 0o600)
	loc := tailscale.Locator{GOOS: "darwin", Root: root, Home: "/Users/test", FileOwner: func(string) (int, error) { return 0, nil },
		Getenv: func(k string) string {
			if k == "TS_MACOS_CLI_SHARED_DIR" {
				return shared
			}
			return ""
		},
		Lsof: func() ([]byte, error) { return nil, errors.New("none") }}
	eps, err := loc.Locate()
	if err != nil || len(eps) != 1 || eps[0].Variant != "macos-cli-shared" || eps[0].Address != "127.0.0.1:4000" {
		t.Fatalf("locate = %+v, %v", eps, err)
	}
	// Nothing at all: ErrNotFound, and a client says so.
	empty := tailscale.Locator{GOOS: "darwin", Root: t.TempDir(), Home: "/Users/test", Getenv: func(string) string { return "" },
		Lsof: func() ([]byte, error) { return nil, errors.New("none") }}
	if _, err := empty.Locate(); !errors.Is(err, tailscale.ErrNotFound) {
		t.Fatal(err)
	}
	if _, err := tailscale.New(empty).Self(context.Background()); !errors.Is(err, tailscale.ErrNotFound) {
		t.Fatalf("self with no LocalAPI = %v", err)
	}
}

// A wrong token reads as no LocalAPI there (401), not a peer refusal.
func TestMacWrongToken(t *testing.T) {
	f, loc := tailscaletest.StartMacStandalone(t, "right")
	selfStatus(f)
	_ = os.WriteFile(filepath.Join(loc.Root, "Library", "Tailscale", "sameuserproof-"+portOf(t, loc)), []byte("wrong"), 0o640)
	if _, err := tailscale.New(loc).Self(context.Background()); err == nil {
		t.Fatal("a wrong token was accepted")
	}
}

func portOf(t *testing.T, loc tailscale.Locator) string {
	target, err := os.Readlink(filepath.Join(loc.Root, "Library", "Tailscale", "ipnport"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := strconv.Atoi(target); err != nil {
		t.Fatal(err)
	}
	return target
}

// The app restarts on a new port: the client locates again.
func TestRelocateOnPortChange(t *testing.T) {
	f1, loc := tailscaletest.StartMacStandalone(t, "one")
	selfStatus(f1)
	c := tailscale.New(loc)
	if _, err := c.Self(context.Background()); err != nil {
		t.Fatal(err)
	}
	f2, loc2 := tailscaletest.StartMacStandalone(t, "two")
	selfStatus(f2)
	// Point the first layout at the second server, as a restart would.
	dir := filepath.Join(loc.Root, "Library", "Tailscale")
	_ = os.Remove(filepath.Join(dir, "ipnport"))
	_ = os.Symlink(portOf(t, loc2), filepath.Join(dir, "ipnport"))
	_ = os.WriteFile(filepath.Join(dir, "sameuserproof-"+portOf(t, loc2)), []byte("two"), 0o640)
	f1.Close()
	if _, err := c.Self(context.Background()); err != nil {
		t.Fatalf("after restart: %v", err)
	}
	if f2.Count("status") == 0 {
		t.Fatal("never asked the new port")
	}
}

func TestStartLoginAndNeedsLogin(t *testing.T) {
	f, sock := tailscaletest.StartSocket(t)
	f.Set(func(f *tailscaletest.Fake) {
		f.Status = tailscale.Status{BackendState: "NeedsLogin"}
		f.LoginURL = "https://login.tailscale.com/a/abc123"
	})
	c := tailscale.NewSocket(sock)
	st, err := c.Status(context.Background(), false)
	if err != nil || !st.NeedsLogin() || st.AuthURL != "" {
		t.Fatalf("%+v %v", st, err)
	}
	if err := c.StartLogin(context.Background()); err != nil {
		t.Fatal(err)
	}
	st, _ = c.Status(context.Background(), false)
	if st.AuthURL != "https://login.tailscale.com/a/abc123" {
		t.Fatalf("auth url %q", st.AuthURL)
	}
}

func TestVouches(t *testing.T) {
	if tailscale.Vouches(42, tailscale.Caller{UserID: 42, Tags: []string{"tag:server"}}) {
		t.Error("tagged nodes have no user: never vouched")
	}
	if tailscale.Vouches(42, tailscale.Caller{UserID: 7}) || tailscale.Vouches(0, tailscale.Caller{}) {
		t.Error("other users never vouched")
	}
	if !tailscale.Vouches(42, tailscale.Caller{UserID: 42}) {
		t.Error("same user, untagged")
	}
}

func TestHelperConfirmsTailscale(t *testing.T) {
	f, sock := tailscaletest.StartSocket(t)
	selfStatus(f)
	h := &tailscale.Helper{Client: tailscale.NewSocket(sock)}
	n := meshnet.Network{ID: "vpn:utun3", Kind: meshnet.KindOverlay, Label: "VPN (utun3)", Addrs: []netip.Addr{netip.MustParseAddr("100.70.1.1")}}
	h.Describe(context.Background(), &n)
	if n.ID != meshnet.TailscaleID || !n.Confirmed || n.DNSName != "mac.tail4c2e.ts.net" || !n.Contains(netip.MustParseAddr("100.101.4.7")) {
		t.Fatalf("%+v", n)
	}
	// NetBird on 100.64/10: not tailscaled's address, left alone.
	nb := meshnet.Network{ID: "vpn:wt0", Kind: meshnet.KindOverlay, Label: "VPN (NetBird)", Addrs: []netip.Addr{netip.MustParseAddr("100.90.1.2")}}
	h.Describe(context.Background(), &nb)
	if nb.ID != "vpn:wt0" {
		t.Fatalf("%+v", nb)
	}
}

// Round-1 review M3: a subnet router or exit node of the same user isn't
// vouched for: it carries traffic that isn't its own.
func TestNoVouchForRouters(t *testing.T) {
	f, sock := tailscaletest.StartSocket(t)
	f.Set(func(f *tailscaletest.Fake) {
		f.WhoIs["100.1.1.1"] = tailscaletest.WhoIsAnswer{Node: "laptop", UserID: 42}
		f.WhoIs["100.1.1.2"] = tailscaletest.WhoIsAnswer{Node: "router", UserID: 42, Routes: []string{"192.168.1.0/24"}}
		f.WhoIs["100.1.1.3"] = tailscaletest.WhoIsAnswer{Node: "exit", UserID: 42, ExitNode: true}
	})
	c := tailscale.NewSocket(sock)
	for ip, want := range map[string]bool{"100.1.1.1": true, "100.1.1.2": false, "100.1.1.3": false} {
		who, err := c.WhoIs(context.Background(), ip+":1")
		if err != nil || tailscale.Vouches(42, who) != want {
			t.Errorf("%s: %+v %v", ip, who, err)
		}
	}
}

// Round-1 review S2: a stale port another local user took, or files that
// aren't root's, are no endpoint: the token is never sent there.
func TestMacEndpointOwnership(t *testing.T) {
	f, loc := tailscaletest.StartMacStandalone(t, "s3cret")
	selfStatus(f)
	stolen := loc
	stolen.ListenerOwner = func(int) ([]tailscale.Listener, error) {
		return []tailscale.Listener{{UID: 501, Command: "python3"}}, nil
	}
	if _, err := tailscale.New(stolen).Self(context.Background()); !errors.Is(err, tailscale.ErrUntrusted) {
		t.Fatalf("a listener that isn't tailscaled's: %v", err)
	}
	if f.Count("status") != 0 {
		t.Fatal("the token reached the impostor's port")
	}
	planted := loc
	planted.FileOwner = func(string) (int, error) { return 501, nil }
	if eps, err := planted.Locate(); err == nil {
		t.Fatalf("files not root's located: %+v", eps)
	}
	// The App Store app's extension runs as this user, under its name; a
	// process of this user's under another name isn't it.
	g, store := tailscaletest.StartMacAppStore(t, "tok", false)
	selfStatus(g)
	if _, err := tailscale.New(store).Self(context.Background()); err != nil {
		t.Fatalf("app store: %v", err)
	}
	other := store
	other.ListenerOwner = func(int) ([]tailscale.Listener, error) {
		return []tailscale.Listener{{UID: os.Getuid(), Command: "nc"}}, nil
	}
	if _, err := tailscale.New(other).Self(context.Background()); !errors.Is(err, tailscale.ErrUntrusted) {
		t.Fatalf("another process of this user's: %v", err)
	}
	// Round-2 review R8: every process listening must be tailscaled's, not
	// just the first lsof names.
	shared := store
	shared.ListenerOwner = func(int) ([]tailscale.Listener, error) {
		return []tailscale.Listener{{UID: os.Getuid(), Command: "IPNExtension"}, {UID: os.Getuid(), Command: "nc"}}, nil
	}
	if _, err := tailscale.New(shared).Self(context.Background()); !errors.Is(err, tailscale.ErrUntrusted) {
		t.Fatalf("a port shared with another process: %v", err)
	}
	if _, err := tailscale.New(tailscale.Locator{GOOS: "linux", Root: t.TempDir()}).Self(context.Background()); !errors.Is(err, tailscale.ErrNotFound) {
		t.Fatalf("nothing at all: %v", err)
	}
}

// A refused token isn't followed by trying more endpoints (S2): the stale
// App Store file's endpoint isn't sprayed after the newest one's 401.
func TestRefusedTokenStopsTheSearch(t *testing.T) {
	f, loc := tailscaletest.StartMacAppStore(t, "right", false)
	selfStatus(f)
	g, other := tailscaletest.StartMacAppStore(t, "other", false)
	selfStatus(g)
	// One container with both proofs: the newest names f's port with a
	// wrong token, the older g's real one.
	eps, _ := other.Locate()
	_, gport, _ := net.SplitHostPort(eps[0].Address)
	feps, _ := loc.Locate()
	_, fport, _ := net.SplitHostPort(feps[0].Address)
	dir := filepath.Join(loc.Root, "Users", "test", "Library", "Group Containers", "W5364U7YZB.io.tailscale.ipn.macos")
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		_ = os.Remove(filepath.Join(dir, e.Name()))
	}
	old := filepath.Join(dir, "sameuserproof-"+gport+"-other")
	_ = os.WriteFile(old, nil, 0o600)
	_ = os.Chtimes(old, time.Now().Add(-time.Hour), time.Now().Add(-time.Hour))
	_ = os.WriteFile(filepath.Join(dir, "sameuserproof-"+fport+"-wrong"), nil, 0o600)
	if _, err := tailscale.New(loc).Self(context.Background()); err == nil {
		t.Fatal("a refused token was followed by another endpoint's")
	}
	if g.Count("status") != 0 {
		t.Fatal("tried the next endpoint after a 401")
	}
}

// Round-2 review R8: after any failure a TCP endpoint is checked again
// before its token is next sent, not only every 10 s.
func TestReverifiedAfterAFailure(t *testing.T) {
	f, loc := tailscaletest.StartMacAppStore(t, "tok", false)
	selfStatus(f)
	var mu sync.Mutex
	checks := 0
	owner := loc.ListenerOwner
	loc.ListenerOwner = func(port int) ([]tailscale.Listener, error) {
		mu.Lock()
		checks++
		mu.Unlock()
		return owner(port)
	}
	c := tailscale.New(loc)
	ctx := context.Background()
	for range 2 {
		if _, err := c.Self(ctx); err != nil {
			t.Fatal(err)
		}
	}
	mu.Lock()
	if checks != 1 {
		t.Fatalf("%d checks for two answers in a row", checks)
	}
	mu.Unlock()
	f.Set(func(f *tailscaletest.Fake) { f.Down = true })
	_, _ = c.Self(ctx)
	f.Set(func(f *tailscaletest.Fake) { f.Down = false })
	if _, err := c.Self(ctx); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if checks < 2 {
		t.Fatalf("not checked again after a failure (%d checks)", checks)
	}
}
