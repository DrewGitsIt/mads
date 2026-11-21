// Command mesh is the Mesh Service: the gate that lets the Routers of a
// user's other devices reach this node's Router (docs/MESH-MODE-DESIGN.md,
// docs/MESH-AUTH-DESIGN.md). It binds only to this node's tailnet address
// and speaks TLS 1.3 with client certificates only: a caller is the paired
// device whose key it proves (pinned in the Data Service's paired devices,
// followed by internal/paired), authorised by that device's grant. By
// default tailnet identity (WhoIs plus the app capability) is required as
// well, as a second factor (-require-tailnet). Allowed requests are
// forwarded to the loopback Router.
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
	"net/url"
	"os"
	"os/signal"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/pkg/devicekey"
	"github.com/drewkidwell/mads/pkg/localauth"
	"github.com/drewkidwell/mads/services/mesh/internal/paired"
	"github.com/drewkidwell/mads/services/mesh/internal/server"
	"github.com/drewkidwell/mads/services/mesh/internal/tailnet"
)

// DefaultPort is the mesh listener's port, the one the tailnet grant opens.
const DefaultPort = 17380

func main() {
	addr := flag.String("addr", "", "listen address; default is this node's tailnet IPv4 on -port. Never 0.0.0.0: the mesh is tailnet-only")
	port := flag.Int("port", DefaultPort, "listen port when -addr is empty")
	routerURL := flag.String("router-url", "http://localhost:17373", "this node's Router")
	dataURL := flag.String("data-url", "http://localhost:17374", "this node's Data Service, read for the paired-device list")
	socket := flag.String("localapi-socket", tailnet.DefaultSocket, "tailscaled LocalAPI socket")
	whoisTTL := flag.Duration("whois-ttl", 10*time.Second, "how long a caller's tailnet identity is cached; bounds how fast a policy change takes effect")
	fsRoot := flag.String("fs-root", "", "root of /mesh/fs/list; default $HOME. \"off\" disables folder browsing")
	madsHome := flag.String("mads-home", localauth.DefaultHome(), "local state root (env MADS_HOME); the install token is read from <mads-home>/local.token")
	tokenFile := flag.String("token-file", "", "the install token's file, read (and re-read on change) and sent to the local Router as its bearer (default <mads-home>/local.token)")
	requireTailnet := flag.Bool("require-tailnet", true, "also require tailnet identity (WhoIs: this node's user, untagged, holding the mesh capability) besides a paired device key; the capability's level caps the grant's. Unset, this host's \"Only accept devices on my tailnet\" setting (Settings → Paired devices) decides, followed live, and this is its default; given, it overrides the setting")
	platform := flag.String("platform", runtime.GOOS, "the platform /mesh/hello reports for this host (other devices derive its kind from it); a phone's app would say ios or android")
	hostsSessions := flag.Bool("hosts-sessions", true, "whether /mesh/hello says this host has mads sessions to view")
	meshName := flag.String("mesh-name", shortHostname(), "this host's mads name, as its Router's -mesh-name: how pairing answers name it")
	flag.Parse()
	// -require-tailnet given explicitly pins it; unset, the stored setting
	// decides (docs/MESH-AUTH-DESIGN.md: "Only accept devices on my tailnet").
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

	target, err := url.Parse(*routerURL)
	if err != nil || target.Scheme == "" || target.Host == "" {
		log.Fatalf("mesh: -router-url %q: not a URL", *routerURL)
	}

	tokens := localauth.FileSource(*tokenFile)

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

	tn := tailnet.New(*socket)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	self, err := tn.Self(ctx)
	cancel()
	if err != nil {
		log.Fatalf("mesh: reading this node from tailscaled (%s): %v", *socket, err)
	}

	listen := *addr
	if listen == "" {
		listen = net.JoinHostPort(self.IPv4, strconv.Itoa(*port))
	}
	if err := refuseWildcard(listen); err != nil {
		log.Fatalf("mesh: -addr %s: %v", listen, err)
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

	// Who may connect: this host's paired devices, followed from the Data
	// Service in the background (the listener never waits on it; until the
	// first load every device is refused).
	data := dataclient.New(*dataURL, "mesh", dataclient.WithTokenSource(tokens))
	devices := paired.New(data)
	gate := server.New(server.Options{
		Self:           self,
		Tailnet:        tn,
		RequireTailnet: *requireTailnet,
		Devices:        devices,
		Audit:          data,
		RouterURL:      target,
		Commit:         commit(),
		FS:             fsys,
		WhoIsTTL:       *whoisTTL,
		Token:          tokens,
		Device:         id.Fingerprint(),
		Platform:       *platform,
		HostsSessions:  *hostsSessions,
		Pairing:        data,
		Name:           *meshName,
	})
	// An unpairing or grant change ends that device's open streams.
	devices.OnReload(gate.Revalidate)
	// "Only accept devices on my tailnet", followed live with the paired
	// list: the stored setting when set, else the flag's default. An
	// explicit -require-tailnet overrides it.
	if !requireTailnetFlag {
		devices.OnSettings(func(ms contracts.MeshSettings) {
			on := *requireTailnet
			if ms.RequireTailnet != nil {
				on = *ms.RequireTailnet
			}
			gate.SetRequireTailnet(on)
		})
	}
	srv := newHTTPServer(gate, tlsConfig)
	srv.ConnContext = gate.ConnContext
	raw, err := net.Listen("tcp", listen)
	if err != nil {
		log.Fatalf("mesh: listen %s: %v", listen, err)
	}
	// Connections are capped overall and per address, closed unless a
	// paired device is admitted on them within seconds, and (with the
	// tailnet required) gated by WhoIs before their handshake.
	ln := gate.Listen(raw, server.DefaultListenLimits)
	second := "paired device key and tailnet identity"
	switch {
	case !requireTailnetFlag:
		second += " until this host's setting is read (Only accept devices on my tailnet)"
	case !*requireTailnet:
		second = "paired device key only (-require-tailnet=false)"
	}
	log.Printf("mesh: %s (%s) listening on %s (TLS; admits by %s), forwarding to %s", self.HostName, self.DNSName, ln.Addr(), second, target)
	log.Printf("mesh: this device is %s", id.Fingerprint().Short())

	runCtx, stopRun := context.WithCancel(context.Background())
	defer stopRun()
	// Say where this host is reached, for its Router to put in a pairing
	// code (D6). Retried until the Data Service takes it.
	go announce(runCtx, data, contracts.MeshListener{
		Node: self.HostName, DNSName: self.DNSName, MeshURL: advertisedURL(*addr, ln.Addr().String(), self.DNSName),
		Fingerprint: string(id.Fingerprint()), Platform: *platform, HostsSessions: *hostsSessions, RequireTailnet: *requireTailnet,
		RequireTailnetFlag: requireTailnetFlag,
	})
	go devices.Run(runCtx)
	// The tailnet side of a grant sends no events: re-check open requests
	// on its cache's clock, so a capability lowered in the policy ends
	// streams too.
	every := *whoisTTL
	if every <= 0 {
		every = 10 * time.Second
	}
	go gate.RevalidateEvery(runCtx, every)

	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGTERM, os.Interrupt)
		<-sig
		stopRun()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}()
	// ServeTLS with the certificate already in TLSConfig (made in memory
	// from the device key: no certificate files), HTTP/2 included.
	if err := srv.ServeTLS(ln, "", ""); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("mesh: %v", err)
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

// refuseWildcard keeps the mesh off every interface but the one asked for:
// an empty or unspecified host would put the Router's API on the LAN.
func refuseWildcard(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return err
	}
	if ip := net.ParseIP(host); host == "" || (ip != nil && ip.IsUnspecified()) {
		return errors.New("the mesh listener must bind a specific address, normally this node's tailnet IP")
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

// advertisedURL is the mesh_url other devices dial for this host: the
// listen address when -addr names one, else this node's MagicDNS name (or
// its tailnet IP) on the listener's port. Stored as http://host:port and
// always dialled with TLS, as every mesh_url is.
func advertisedURL(addrFlag, listening, dnsName string) string {
	host, port, err := net.SplitHostPort(listening)
	if err != nil {
		return "http://" + listening
	}
	if addrFlag == "" {
		if dns := strings.TrimSuffix(dnsName, "."); dns != "" {
			host = dns
		}
	}
	return "http://" + net.JoinHostPort(host, port)
}

// announce appends mesh.listening, retrying with backoff until it's taken
// or ctx ends.
func announce(ctx context.Context, data *dataclient.Client, l contracts.MeshListener) {
	payload, _ := json.Marshal(l)
	backoff := time.Second
	for {
		err := data.Append(ctx, &contracts.Envelope{Type: contracts.MeshListening, Actor: "mesh", Payload: payload})
		if err == nil {
			log.Printf("mesh: advertised as %s", l.MeshURL)
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, 30*time.Second)
	}
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
