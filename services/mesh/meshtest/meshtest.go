// Package meshtest runs a real Mesh Service in-process for other services'
// tests: the TLS listener, the gate, the paired-device directory and the
// forwarder, with tailscaled replaced by a fake LocalAPI on a unix socket.
// Like services/data/datatest, it's the one exported doorway to the Mesh
// Service's internals, so a viewer's tests run against the real contract
// (mutual TLS with pinned keys, the tailnet factor, pairing by code)
// instead of a stand-in. It never touches a real tailnet.
package meshtest

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/pkg/devicekey"
	"github.com/drewkidwell/mads/pkg/meshnet"
	"github.com/drewkidwell/mads/services/mesh/internal/paired"
	"github.com/drewkidwell/mads/services/mesh/internal/server"
	"github.com/drewkidwell/mads/services/mesh/internal/tailnet"
)

// Options configures Start.
type Options struct {
	// Identity is the host's device key (its TLS certificate).
	Identity *devicekey.Identity
	// HostName is the node name tailscaled reports ("pop-os").
	HostName string
	// DataURL and Token are the host's Data Service, read for the paired
	// devices and written for audit, pairing and mesh.listening.
	DataURL, Token string
	// RouterURL is the host's Router, where admitted requests go.
	RouterURL string
	// Access is the mesh capability's access list every caller holds on
	// the fake tailnet (default ["control"]); Platform what hello reports
	// (default "linux").
	Access   []string
	Platform string
	// Networks is how many listeners it runs, each on its own port, each a
	// "network" of its own (lan:test-0, lan:test-1, ...): a viewer can be
	// made to move between them (Drop). Default 1.
	Networks int
}

// Mesh is a running Mesh Service.
type Mesh struct {
	// URL is its mesh_url as rows store it: http://127.0.0.1:port (the
	// first network's).
	URL string
	// Addrs are each network's host:port.
	Addrs []string
	nets  []*dropListener
}

// Drop takes network i away: its listener closes, and so does every
// connection it accepted (a Wi-Fi gone, not just a closed port).
func (m *Mesh) Drop(i int) { m.nets[i].drop() }

type dropListener struct {
	net.Listener
	mu      sync.Mutex
	conns   []net.Conn
	dropped bool
}

func (l *dropListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.dropped {
		_ = c.Close()
		return nil, net.ErrClosed
	}
	l.conns = append(l.conns, c)
	return c, nil
}

func (l *dropListener) drop() {
	l.mu.Lock()
	l.dropped = true
	conns := l.conns
	l.conns = nil
	l.mu.Unlock()
	_ = l.Close()
	for _, c := range conns {
		_ = c.Close()
	}
}

// Start runs a Mesh Service on 127.0.0.1 for the test's lifetime, tailnet
// identity required (every caller on 127.0.0.1 is the host's own user,
// untagged, holding the capability), and announces it (mesh.listening) as
// the real one does.
func Start(t *testing.T, opts Options) *Mesh {
	t.Helper()
	if opts.Access == nil {
		opts.Access = []string{"control"}
	}
	if opts.Platform == "" {
		opts.Platform = "linux"
	}
	sock := fakeLocalAPI(t, opts.HostName, opts.Access)
	tn := tailnet.New(sock)
	self, err := tn.Self(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	tlsConfig, err := devicekey.ServerConfig(opts.Identity)
	if err != nil {
		t.Fatal(err)
	}
	routerURL, err := url.Parse(opts.RouterURL)
	if err != nil {
		t.Fatal(err)
	}
	data := dataclient.New(opts.DataURL, "mesh", dataclient.WithToken(opts.Token))
	devices := paired.New(data)
	if opts.Networks <= 0 {
		opts.Networks = 1
	}
	m := &Mesh{}
	var addrs []contracts.NetAddress
	var nets []contracts.ListeningNetwork
	for i := 0; i < opts.Networks; i++ {
		raw, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		dl := &dropListener{Listener: raw}
		m.nets = append(m.nets, dl)
		m.Addrs = append(m.Addrs, raw.Addr().String())
		id := "lan:test-" + strconv.Itoa(i)
		addrs = append(addrs, contracts.NetAddress{Via: "direct", Addr: raw.Addr().String(), Kind: "lan", Label: "Test network " + strconv.Itoa(i), Network: id})
		nets = append(nets, contracts.ListeningNetwork{ID: id, Kind: "lan", Label: "Test network " + strconv.Itoa(i), Addrs: []string{"127.0.0.1"}, Chosen: true, Listening: true})
	}
	m.URL = "http://" + m.Addrs[0]
	gate := server.New(server.Options{
		Self: self, Tailnet: tn, RequireTailnet: true, Devices: devices, Audit: data,
		RouterURL: routerURL, Token: func() string { return opts.Token }, Device: opts.Identity.Fingerprint(),
		Platform: opts.Platform, HostsSessions: true, Pairing: data, WhoIsTTL: time.Second, Name: opts.HostName,
		Addresses: func() []contracts.NetAddress { return addrs }, AddressPolicy: &meshnet.DialPolicy{AllowLoopback: true},
	})
	devices.OnReload(gate.Revalidate)
	devices.OnDiscoverable(func(w contracts.Discoverable) { gate.SetWindow(w) })
	srv := &http.Server{Handler: gate, TLSConfig: gate.TLSConfig(tlsConfig), ConnContext: gate.ConnContext, ReadHeaderTimeout: 10 * time.Second}
	ctx, cancel := context.WithCancel(context.Background())
	go devices.Run(ctx)
	for _, dl := range m.nets {
		ln := gate.Listen(dl, server.DefaultListenLimits)
		go func() { _ = srv.ServeTLS(ln, "", "") }()
	}
	t.Cleanup(func() {
		cancel()
		_ = srv.Close()
	})
	payload, _ := json.Marshal(contracts.MeshListener{Node: opts.HostName, DNSName: self.DNSName, MeshURL: m.URL, Fingerprint: string(opts.Identity.Fingerprint()),
		Platform: opts.Platform, HostsSessions: true, RequireTailnet: true, Addresses: addrs, Port: m.nets[0].Addr().(*net.TCPAddr).Port,
		Networks: nets, TailnetCheckStatus: "on"})
	if err := data.Append(context.Background(), &contracts.Envelope{Type: contracts.MeshListening, Actor: "mesh", Payload: payload}); err != nil {
		t.Fatal(err)
	}
	// Ready once the directory has read the list.
	deadline := time.Now().Add(10 * time.Second)
	for !devices.Loaded() {
		if time.Now().After(deadline) {
			t.Fatal("meshtest: the paired directory never loaded")
		}
		time.Sleep(10 * time.Millisecond)
	}
	return m
}

// fakeLocalAPI answers tailscaled's status and whois on a unix socket:
// this node is 127.0.0.1, and every caller from 127.0.0.1 is its owner's.
func fakeLocalAPI(t *testing.T, host string, access []string) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "ts") // short: macOS caps socket paths
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "s")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	capValue, _ := json.Marshal(map[string]any{"access": access})
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch strings.TrimPrefix(r.URL.Path, "/localapi/v0/") {
		case "status":
			_ = json.NewEncoder(w).Encode(map[string]any{"Self": map[string]any{
				"HostName": host, "DNSName": host + ".example.ts.net.", "UserID": 42, "TailscaleIPs": []string{"127.0.0.1"},
			}})
		case "whois":
			addr := r.URL.Query().Get("addr")
			if !strings.HasPrefix(addr, "127.0.0.1:") {
				http.NotFound(w, r)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"Node":        map[string]any{"Name": "caller.example.ts.net.", "User": 42},
				"UserProfile": map[string]any{"ID": 42},
				"CapMap":      map[string]any{tailnet.MeshCap: []json.RawMessage{capValue}},
			})
		default:
			http.NotFound(w, r)
		}
	})}
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			t.Logf("meshtest: fake LocalAPI: %v", err)
		}
	}()
	t.Cleanup(func() { _ = srv.Close() })
	return sock
}
