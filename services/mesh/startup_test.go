package main

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/pkg/devicekey"
	"github.com/drewkidwell/mads/services/data/datatest"
)

// startMesh builds and runs the real Mesh Service against a scratch Data
// Service and $MADS_HOME, with tailscaled pointed at a socket that doesn't
// exist (never this machine's real tailscaled), and stops it at cleanup.
func startMesh(t *testing.T, extra ...string) (datatest.Guarded, *dataclient.Client) {
	t.Helper()
	g := datatest.StartGuarded(t)
	return g, startMeshWith(t, g, extra...)
}

func startMeshWith(t *testing.T, g datatest.Guarded, extra ...string) *dataclient.Client {
	t.Helper()
	if testing.Short() {
		t.Skip("builds and runs the Mesh Service")
	}
	bin := filepath.Join(t.TempDir(), "mesh")
	if out, err := exec.Command("go", "build", "-tags", "mesh_loopback", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	args := append([]string{"-data-url", g.URL, "-mads-home", g.Home, "-localapi-socket", filepath.Join(t.TempDir(), "no-tailscaled.sock"),
		"-router-url", "http://127.0.0.1:1", "-fs-root", "off", "-mesh-name", "scratch"}, extra...)
	cmd := exec.Command(bin, args...)
	cmd.Env = append(os.Environ(), "MADS_HOME="+g.Home)
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Signal(syscall.SIGTERM)
		done := make(chan struct{})
		go func() { _ = cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			_ = cmd.Process.Kill()
			<-done
		}
	})
	return dataclient.New(g.URL, "test", dataclient.WithToken(g.Token))
}

func waitListening(t *testing.T, data *dataclient.Client, ok func(contracts.MeshListener) bool) contracts.MeshListener {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		evs, err := data.Events(context.Background(), 0, "", []string{string(contracts.MeshListening)})
		if err == nil {
			for i := len(evs) - 1; i >= 0; i-- {
				var l contracts.MeshListener
				if json.Unmarshal(evs[i].Payload, &l) == nil && ok(l) {
					return l
				}
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("no mesh.listening report")
	return contracts.MeshListener{}
}

// The Mesh Service starts with no tailscaled at all, and on a fresh
// install (no mesh network chosen) listens nowhere, saying so.
func TestStartsWithoutTailscaledListeningNowhere(t *testing.T) {
	g, data := startMesh(t)
	l := waitListening(t, data, func(l contracts.MeshListener) bool { return l.Fingerprint != "" })
	if len(l.Addresses) != 0 || l.MeshURL != "" {
		t.Fatalf("a fresh install listens at %+v", l.Addresses)
	}
	if l.Tailscale == nil || l.Tailscale.Present || l.Tailscale.Error == "" {
		t.Fatalf("tailscale state %+v", l.Tailscale)
	}
	id, err := devicekey.Load(g.Home)
	if err != nil || l.Fingerprint != string(id.Fingerprint()) {
		t.Fatalf("fingerprint %s vs %v", l.Fingerprint, err)
	}
}

// -addr pins one listener; the TLS listener there admits by device key.
func TestFixedAddrListens(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	_, data := startMesh(t, "-addr", addr)
	l := waitListening(t, data, func(l contracts.MeshListener) bool { return len(l.Addresses) == 1 })
	if l.Addresses[0].Addr != addr || l.MeshURL != "http://"+addr {
		t.Fatalf("report %+v", l)
	}
	c, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatalf("not listening at %s: %v", addr, err)
	}
	_ = c.Close()
}

// The test hooks: with -mesh-allow-loopback and -fake-networks a scratch
// Mesh Service listens on its chosen (fake) Tailscale network on loopback;
// a fake network with a real address is never bound.
func TestFakeNetworksLoopbackOnly(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	g := datatest.StartGuarded(t)
	data := dataclient.New(g.URL, "test", dataclient.WithToken(g.Token))
	if _, err := data.SetMeshSettings(context.Background(), contracts.MeshSettings{Port: port,
		Networks: []contracts.NetworkChoice{{ID: "tailscale", Listen: true}, {ID: "lan:real", Listen: false}}}); err != nil {
		t.Fatal(err)
	}
	nets := filepath.Join(t.TempDir(), "nets.json")
	if err := os.WriteFile(nets, []byte(`[{"id":"tailscale","kind":"overlay","label":"Tailscale","iface":"lo0","addrs":["127.0.0.1"],"prefixes":["127.0.0.0/8"]}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	startMeshWith(t, g, "-mesh-allow-loopback", "-fake-networks", nets)
	l := waitListening(t, data, func(l contracts.MeshListener) bool { return len(l.Addresses) == 1 })
	if l.Addresses[0].Addr != "127.0.0.1:"+strconv.Itoa(port) || l.Addresses[0].Network != "tailscale" {
		t.Fatalf("report %+v", l.Addresses)
	}
	// A fake network with a real address: the file is refused, nothing new
	// is bound.
	if err := os.WriteFile(nets, []byte(`[{"id":"tailscale","kind":"overlay","label":"Tailscale","addrs":["100.101.4.7"]}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	time.Sleep(11 * time.Second) // a scan
	evs, _ := data.Events(context.Background(), 0, "", []string{string(contracts.MeshListening)})
	for _, ev := range evs {
		var r contracts.MeshListener
		_ = json.Unmarshal(ev.Payload, &r)
		for _, a := range r.Addresses {
			if strings.HasPrefix(a.Addr, "100.") {
				t.Fatalf("bound a real address: %+v", r.Addresses)
			}
		}
	}
}

// -mesh-allow-loopback refuses a non-loopback -addr, and the flag needs a
// test build.
func TestLoopbackModeRefusesRealAddr(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the Mesh Service")
	}
	dir := t.TempDir()
	for _, c := range []struct {
		tags []string
		args []string
		want string
	}{
		{[]string{"-tags", "mesh_loopback"}, []string{"-mesh-allow-loopback", "-addr", "100.101.4.7:17380"}, "binds loopback only"},
		{nil, []string{"-mesh-allow-loopback"}, "is refused"},
		{[]string{"-tags", "mesh_loopback"}, []string{"-fake-networks", "x.json"}, "needs -mesh-allow-loopback"},
	} {
		bin := filepath.Join(dir, "mesh"+strconv.Itoa(len(c.tags)))
		args := append(append([]string{"build"}, c.tags...), "-o", bin, ".")
		if out, err := exec.Command("go", args...).CombinedOutput(); err != nil {
			t.Fatalf("build: %v %s", err, out)
		}
		out, err := exec.Command(bin, append(c.args, "-mads-home", dir, "-localapi-socket", filepath.Join(dir, "none.sock"))...).CombinedOutput()
		if err == nil || !strings.Contains(string(out), c.want) {
			t.Errorf("%v: %v %s", c.args, err, out)
		}
	}
}
