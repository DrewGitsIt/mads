// Command mesh is the Mesh Service: the gate that lets the Routers of a
// user's other devices reach this node's Router (docs/MESH-MODE-DESIGN.md,
// docs/MESH-AUTH-DESIGN.md, docs/MESH-NETWORK-DESIGN.md). It listens only
// on this device's mesh networks (the ones its user chose: a VPN such as
// Tailscale, a LAN, never one it wasn't told to), finding this machine's
// networks itself; it starts without tailscaled, which it asks only for
// names, peers and the optional identity check. It speaks TLS 1.3 with
// client certificates only: a caller is the paired device whose key it
// proves (pinned in the Data Service's paired devices, followed by
// internal/paired), authorised by that device's grant. On the Tailscale
// network an identity check (same tailnet user, untagged; on by default)
// can refuse a caller, never raise or lower its level. Allowed requests
// are forwarded to the loopback Router.
package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"flag"
	"log"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"os/signal"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/pkg/devicekey"
	"github.com/drewkidwell/mads/pkg/localauth"
	"github.com/drewkidwell/mads/pkg/meshnet"
	"github.com/drewkidwell/mads/pkg/meshnet/mdns"
	"github.com/drewkidwell/mads/pkg/meshnet/tailscale"
	"github.com/drewkidwell/mads/services/mesh/internal/netloop"
	"github.com/drewkidwell/mads/services/mesh/internal/paired"
	"github.com/drewkidwell/mads/services/mesh/internal/server"
)

// DefaultPort is the mesh port, the same on every network (Q10).
const DefaultPort = meshnet.DefaultPort

func main() {
	addr := flag.String("addr", "", "listen only on this address (ip:port), whatever this device's mesh network setting says: for tests and manual setups. Never 0.0.0.0")
	port := flag.Int("port", 0, "the mesh port on every network (default: this host's setting, 17380)")
	routerURL := flag.String("router-url", "http://localhost:17373", "this node's Router")
	dataURL := flag.String("data-url", "http://localhost:17374", "this node's Data Service, read for the paired-device list and mesh settings")
	socket := flag.String("localapi-socket", "", "tailscaled LocalAPI socket; default: found as tailscale's own CLI finds it (Linux "+tailscale.LinuxSocket+", the macOS app's port and token, or "+tailscale.DarwinSocket+")")
	whoisTTL := flag.Duration("whois-ttl", 10*time.Second, "how long a caller's tailnet identity is cached")
	fsRoot := flag.String("fs-root", "", "root of /mesh/fs/list; default $HOME. \"off\" disables folder browsing")
	madsHome := flag.String("mads-home", localauth.DefaultHome(), "local state root (env MADS_HOME); the install token is read from <mads-home>/local.token")
	tokenFile := flag.String("token-file", "", "the install token's file, read (and re-read on change) and sent to the local Router as its bearer (default <mads-home>/local.token)")
	requireTailnet := flag.Bool("require-tailnet", true, "the Tailscale network's identity check: on the Tailscale network, only this tailnet user's untagged devices (besides a paired device key). Refuse-only. Unset, this host's setting decides (Settings → Paired devices → Mesh network), followed live; given, it overrides the setting")
	platform := flag.String("platform", runtime.GOOS, "the platform /mesh/hello reports for this host (other devices derive its kind from it); a phone's app would say ios or android")
	hostsSessions := flag.Bool("hosts-sessions", true, "whether /mesh/hello says this host has mads sessions to view")
	meshName := flag.String("mesh-name", shortHostname(), "this host's mads name, as its Router's -mesh-name: how pairing answers name it")
	allowLoopback := flag.Bool("mesh-allow-loopback", false, "test builds only (go test, or -tags mesh_loopback): bind loopback addresses only, never a real network's, and accept loopback joiners")
	fakeNetworks := flag.String("fake-networks", "", "with -mesh-allow-loopback: a JSON file of the networks (meshnet.Network, loopback addresses only) this machine is on, read on every scan, instead of its interfaces")
	flag.Parse()
	if *allowLoopback && !meshnet.LoopbackAllowed() {
		log.Fatal("mesh: -mesh-allow-loopback is refused: only go test builds, or builds with -tags mesh_loopback, may use it")
	}
	if *fakeNetworks != "" && !*allowLoopback {
		log.Fatal("mesh: -fake-networks needs -mesh-allow-loopback (a test build)")
	}
	// -require-tailnet given explicitly pins it; unset, the stored setting
	// decides.
	requireTailnetFlag := false
	flag.Visit(func(f *flag.Flag) {
		if f.Name == "require-tailnet" {
			requireTailnetFlag = true
		}
	})
	// The token is read from its file, never inherited: drop any copy in
	// the environment so nothing this process might start sees it.
	localauth.TakeChildEnv()
	if *tokenFile == "" {
		*tokenFile = localauth.Path(*madsHome)
	}
	if *addr != "" {
		if err := refuseWildcard(*addr); err != nil {
			log.Fatalf("mesh: -addr %s: %v", *addr, err)
		}
		if host, _, _ := net.SplitHostPort(*addr); *allowLoopback && !isLoopbackHost(host) {
			log.Fatalf("mesh: -addr %s: -mesh-allow-loopback binds loopback only", *addr)
		}
	}

	target, err := url.Parse(*routerURL)
	if err != nil || target.Scheme == "" || target.Host == "" {
		log.Fatalf("mesh: -router-url %q: not a URL", *routerURL)
	}
	tokens := localauth.FileSource(*tokenFile)

	// One Mesh Service per mads home, whoever started it (app.sh, a
	// systemd unit, by hand).
	if err := lockInstance(*madsHome); err != nil {
		log.Fatalf("mesh: %v", err)
	}

	// This device's identity (D1): the key the TLS listener presents, which
	// other hosts pin. Without it there is nothing to serve.
	id, err := devicekey.LoadOrCreate(*madsHome)
	if err != nil {
		log.Fatalf("mesh: device key: %v", err)
	}
	tlsConfig, err := devicekey.ServerConfig(id)
	if err != nil {
		log.Fatalf("mesh: TLS certificate: %v", err)
	}

	var fsys *server.FS
	if *fsRoot != "off" {
		root := *fsRoot
		if root == "" {
			if root, err = os.UserHomeDir(); err != nil {
				log.Fatalf("mesh: no home directory for /mesh/fs/list (pass -fs-root off to disable): %v", err)
			}
		}
		if fsys, err = server.NewFS(root); err != nil {
			log.Fatalf("mesh: -fs-root %s: %v", root, err)
		}
	}

	// tailscaled is a helper, asked for names, peers and the identity
	// check; the Mesh Service runs without it.
	ts := tailscale.New(tailscale.Locator{Socket: *socket})
	helper := &tailscale.Helper{Client: ts}
	direct := &meshnet.Direct{Sys: meshnet.OSSystem(), Helpers: []meshnet.Helper{helper}}
	var adapter meshnet.Adapter = direct
	if *fakeNetworks != "" {
		adapter = meshnet.FileNetworks{Direct: direct, Path: *fakeNetworks}
	}
	if *allowLoopback {
		adapter = meshnet.LoopbackOnly{Adapter: adapter}
	}

	// Who may connect: this host's paired devices, followed from the Data
	// Service in the background (the listener never waits on it; until the
	// first load every device is refused).
	data := dataclient.New(*dataURL, "mesh", dataclient.WithTokenSource(tokens))
	devices := paired.New(data)
	var loop *netloop.Loop
	gate := server.New(server.Options{
		TailnetSelf:        func() (tailscale.Self, error) { return loop.TailscaleSelf() },
		Addresses:          func() []contracts.NetAddress { return loop.Addresses() },
		TailscaleKeyExpiry: func() *time.Time { return loop.KeyExpiry() },
		Tailnet:            ts,
		RequireTailnet:     *requireTailnet,
		Devices:            devices,
		Audit:              data,
		RouterURL:          target,
		Commit:             commit(),
		FS:                 fsys,
		WhoIsTTL:           *whoisTTL,
		Token:              tokens,
		Device:             id.Fingerprint(),
		Platform:           *platform,
		HostsSessions:      *hostsSessions,
		Pairing:            data,
		Name:               *meshName,
		AddressPolicy:      &meshnet.DialPolicy{AllowLoopback: *allowLoopback},
		CloseWindow: func(reason string) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if _, err := data.CloseDiscoverable(ctx, reason); err != nil {
				log.Printf("mesh: close the setup window at the Data Service: %v", err)
			}
		},
	})
	srv := newHTTPServer(gate, gate.TLSConfig(tlsConfig))
	srv.ConnContext = gate.ConnContext

	runCtx, stopRun := context.WithCancel(context.Background())
	defer stopRun()
	reports := newReporter(data)
	go reports.run(runCtx)
	// One connection budget per network (S1), kept across a listener's
	// moves.
	var budgetsMu sync.Mutex
	budgets := map[string]*server.Budget{}
	loop = netloop.New(netloop.Options{
		Adapter:   adapter,
		Tailscale: helper,
		Settings:  devices.Settings,
		Window:    gate.DiscoverableUntil,
		FixedAddr: *addr,
		Port:      *port,
		Bind: func(n meshnet.Network, l net.Listener) {
			budgetsMu.Lock()
			b, ok := budgets[n.ID]
			if !ok {
				b = server.NewBudget(server.DefaultListenLimits)
				budgets[n.ID] = b
			}
			budgetsMu.Unlock()
			ts := n.ID == meshnet.TailscaleID || (n.ID == "fixed" && inTailnet(n))
			ln := gate.ListenOn(l, b, server.Net{ID: n.ID, Label: n.Label, Tailscale: ts, TailnetOnly: ts && !*allowLoopback})
			go func() {
				// ServeTLS with the certificate already in TLSConfig (made in
				// memory from the device key: no certificate files), HTTP/2
				// included. Returns when the loop closes the listener.
				if err := srv.ServeTLS(ln, "", ""); err != nil && !errors.Is(err, http.ErrServerClosed) && !errors.Is(err, net.ErrClosed) {
					log.Printf("mesh: serve %s: %v", n.ID, err)
					loop.Kick()
				}
			}()
		},
		Report:       reports.offer,
		TailnetCheck: gate.RequireTailnet,
		Base: contracts.MeshListener{Node: *meshName, Fingerprint: string(id.Fingerprint()), Platform: *platform,
			HostsSessions: *hostsSessions, RequireTailnetFlag: requireTailnetFlag},
		Advertise: func(ctx context.Context, n meshnet.Network, p int, addrs []netip.Addr) error {
			c, err := mdns.Listen(n.Iface)
			if err != nil {
				return err
			}
			log.Printf("mesh: setup window: advertising _mads._tcp on %s (%s)", n.Label, n.Iface)
			return mdns.Advertise(ctx, c, mdns.Advert{Instance: *meshName, Host: *meshName, Port: p, Addrs: addrs,
				TXT: map[string]string{"v": "1", "n": *meshName, "p": *platform, "fp": id.Fingerprint().Short()}})
		},
	})
	// An unpairing or grant change ends that device's open streams.
	devices.OnReload(gate.Revalidate)
	// This host's settings, followed live: where it listens, and the
	// Tailscale check (unless -require-tailnet was given).
	devices.OnSettings(func(ms contracts.MeshSettings) {
		if !requireTailnetFlag {
			gate.SetRequireTailnet(ms.TailnetCheck())
		}
		if len(ms.Chosen()) == 0 && *addr == "" {
			log.Printf("mesh: no mesh network chosen yet: listening nowhere (Settings → Paired devices → Set up mesh, or mads mesh setup)")
		}
		loop.Kick()
	})
	// The setup window, opened and closed at the device.
	devices.OnDiscoverable(func(w contracts.Discoverable) {
		gate.SetWindow(w)
		loop.Kick()
	})
	log.Printf("mesh: this device is %s; forwarding to %s", id.Fingerprint().Short(), target)
	go devices.Run(runCtx)
	go loop.Run(runCtx)
	// The tailnet side sends no events: re-check open requests on the
	// Tailscale network on WhoIs's clock.
	every := *whoisTTL
	if every <= 0 {
		every = 10 * time.Second
	}
	go gate.RevalidateEvery(runCtx, every)

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, os.Interrupt)
	<-sig
	stopRun()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
}

func isLoopbackHost(h string) bool {
	ip, err := netip.ParseAddr(h)
	return err == nil && ip.Unmap().IsLoopback()
}

// inTailnet reports whether a fixed (-addr) network's address is a
// Tailscale one.
func inTailnet(n meshnet.Network) bool {
	for _, a := range n.Addrs {
		for _, p := range meshnet.TailnetRanges {
			if p.Contains(a) {
				return true
			}
		}
	}
	return false
}

// reporter appends mesh.listening, newest report first: one being retried
// is replaced by a newer one.
type reporter struct {
	data *dataclient.Client
	mu   sync.Mutex
	next *contracts.MeshListener
	wake chan struct{}
}

func newReporter(data *dataclient.Client) *reporter {
	return &reporter{data: data, wake: make(chan struct{}, 1)}
}

func (r *reporter) offer(l contracts.MeshListener) {
	r.mu.Lock()
	r.next = &l
	r.mu.Unlock()
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

func (r *reporter) run(ctx context.Context) {
	backoff := time.Second
	for {
		select {
		case <-ctx.Done():
			return
		case <-r.wake:
		}
		for {
			r.mu.Lock()
			l := r.next
			r.mu.Unlock()
			if l == nil {
				break
			}
			payload, _ := json.Marshal(l)
			err := r.data.Append(ctx, &contracts.Envelope{Type: contracts.MeshListening, Actor: "mesh", Payload: payload})
			if err == nil {
				r.mu.Lock()
				if r.next == l {
					r.next = nil
				}
				r.mu.Unlock()
				where := "nowhere"
				if len(l.Addresses) > 0 {
					var as []string
					for _, a := range l.Addresses {
						as = append(as, a.Addr)
					}
					where = strings.Join(as, ", ")
				}
				log.Printf("mesh: reported: reachable at %s (Tailscale check %s)", where, l.TailnetCheckStatus)
				backoff = time.Second
				continue
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			backoff = min(backoff*2, 30*time.Second)
		}
	}
}

// HTTP bounds for the mesh listener (connection bounds are
// server.DefaultListenLimits): idle connections close, and one connection
// can't open streams without end.
const (
	maxStreams = 128
	idle       = 2 * time.Minute
)

// newHTTPServer is the mesh listener's server: TLS from tlsConfig, idle
// connections closed, HTTP/2 streams per connection bounded.
func newHTTPServer(h http.Handler, tlsConfig *tls.Config) *http.Server {
	return &http.Server{
		Handler:           h,
		TLSConfig:         tlsConfig,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       idle,
		HTTP2:             &http.HTTP2Config{MaxConcurrentStreams: maxStreams},
		// No WriteTimeout: /events and /sessions/{id}/output are long-lived.
	}
}

// refuseWildcard keeps -addr off every interface but the one asked for: an
// empty or unspecified host would put the Router's API on every network.
func refuseWildcard(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return err
	}
	if ip := net.ParseIP(host); host == "" || (ip != nil && ip.IsUnspecified()) {
		return errors.New("the mesh listener must bind a specific address")
	}
	return nil
}

// shortHostname is this machine's short host name, the Router's
// -mesh-name default.
func shortHostname() string {
	h, err := os.Hostname()
	if err != nil {
		return ""
	}
	h, _, _ = strings.Cut(h, ".")
	return h
}

func commit() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}
	rev, dirty := "", false
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.modified":
			dirty = s.Value == "true"
		}
	}
	if len(rev) > 12 {
		rev = rev[:12]
	}
	if dirty && rev != "" {
		rev += "-dirty"
	}
	return rev
}
