package server

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/devicekey"
	"github.com/drewkidwell/mads/services/mesh/internal/tailnet"
)

// listenFront serves f's gate over TLS through Listen with lim, as main
// does, counting connections the server sees.
func listenFront(t *testing.T, f *fixture, lim ListenLimits, conns *atomic.Int32) *httptest.Server {
	t.Helper()
	cfg, err := devicekey.ServerConfig(f.ids.host)
	if err != nil {
		t.Fatal(err)
	}
	front := httptest.NewUnstartedServer(f.srv)
	front.Listener = f.srv.Listen(front.Listener, lim)
	front.Config.ConnContext = f.srv.ConnContext
	if conns != nil {
		front.Config.ConnState = func(_ net.Conn, st http.ConnState) {
			if st == http.StateNew {
				conns.Add(1)
			}
		}
	}
	front.TLS = cfg
	front.EnableHTTP2 = true
	front.StartTLS()
	t.Cleanup(front.Close)
	return front
}

func addrOf(front *httptest.Server) string { return strings.TrimPrefix(front.URL, "https://") }

// closedWithin reports whether c's peer closes it within d.
func closedWithin(c net.Conn, d time.Duration) bool {
	_ = c.SetReadDeadline(time.Now().Add(d))
	buf := make([]byte, 4096)
	for {
		// What the server sends first (an HTTP/2 SETTINGS frame) isn't an
		// ending: read until the connection ends or the deadline passes.
		if _, err := c.Read(buf); err != nil {
			var ne net.Error
			return !errors.As(err, &ne) || !ne.Timeout()
		}
	}
}

// pipeConn is a meshConn over one end of a pipe, its listener's books set
// up as Accept would leave them.
func pipeConn(t *testing.T) (*meshConn, net.Conn) {
	t.Helper()
	a, b := net.Pipe()
	t.Cleanup(func() { _ = a.Close(); _ = b.Close() })
	bud := NewBudget(ListenLimits{MaxConns: 1, MaxPerIP: 1, FirstRequest: time.Hour})
	bud.ipN["pipe"] = 1
	bud.sem <- struct{}{}
	l := &meshListener{b: bud, net: Net{ID: "tailscale", Tailscale: true}}
	mc := &meshConn{Conn: a, l: l, ip: "pipe"}
	mc.timer = time.AfterFunc(time.Hour, mc.expire)
	t.Cleanup(func() { mc.timer.Stop() })
	return mc, b
}

func pipeClosed(b net.Conn) bool {
	_ = b.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
	_, err := b.Read(make([]byte, 1))
	ne, ok := err.(net.Error)
	return err != nil && (!ok || !ne.Timeout())
}

// Lock-out attacks on the listener: bare TCP connections, a
// throwaway key's TLS session holding an HTTP/2 preface open, and a TLS
// handshake that stalls, all filling the listener's slots. Each is closed
// within FirstRequest, and a paired device then gets in; one address
// can't hold more than MaxPerIP.
func TestListenerResistsLockOut(t *testing.T) {
	f := newFixture(t)
	f.tn.set("127.0.0.1", full)
	lim := ListenLimits{MaxConns: 4, MaxPerIP: 4, FirstRequest: 300 * time.Millisecond}
	front := listenFront(t, f, lim, nil)
	addr := addrOf(front)

	var attackers []net.Conn
	defer func() {
		for _, c := range attackers {
			_ = c.Close()
		}
	}()
	for range 2 { // bare TCP
		c, err := net.Dial("tcp", addr)
		if err != nil {
			t.Fatal(err)
		}
		attackers = append(attackers, c)
	}
	// A throwaway key, handshake done, HTTP/2 preface sent, then nothing.
	cert, _ := f.ids.stranger.Certificate()
	tc, err := tls.Dial("tcp", addr, &tls.Config{Certificates: []tls.Certificate{cert}, InsecureSkipVerify: true, NextProtos: []string{"h2"}, MinVersion: tls.VersionTLS13}) //nolint:gosec // test client
	if err != nil {
		t.Fatal(err)
	}
	_, _ = tc.Write([]byte("PRI * HTTP/2.0\r\n\r\nSM\r\n\r\n\x00\x00\x00\x04\x00\x00\x00\x00\x00"))
	attackers = append(attackers, tc)
	// A handshake that stalls after one byte.
	slow, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = slow.Write([]byte{0x16})
	attackers = append(attackers, slow)

	for i, c := range attackers {
		if !closedWithin(c, 3*time.Second) {
			t.Fatalf("attacker connection %d still open past FirstRequest", i)
		}
	}
	resp, err := meshClient(t, f.ids.mac, f.ids.host).Get(front.URL + "/sessions")
	if err != nil {
		t.Fatalf("the paired device couldn't get in after the attack: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("paired device: %d", resp.StatusCode)
	}

	// Past MaxPerIP a new connection from the same address is closed at once.
	g := newFixture(t)
	g.tn.set("127.0.0.1", full)
	small := listenFront(t, g, ListenLimits{MaxConns: 10, MaxPerIP: 2, FirstRequest: 5 * time.Second}, nil)
	var held []net.Conn
	for range 3 {
		c, err := net.Dial("tcp", addrOf(small))
		if err != nil {
			t.Fatal(err)
		}
		held = append(held, c)
	}
	defer func() {
		for _, c := range held {
			_ = c.Close()
		}
	}()
	if !closedWithin(held[2], time.Second) {
		t.Fatal("a third connection from one address was kept past MaxPerIP 2")
	}
	if closedWithin(held[0], 200*time.Millisecond) {
		t.Fatal("a connection within the per-address cap was closed early")
	}
}

// With the tailnet required, a node WhoIs definitely refuses is closed
// before its TLS handshake; a transient WhoIs failure leaves the
// connection to the request gate.
func TestListenerTailnetGateBeforeHandshake(t *testing.T) {
	f := newFixture(t)
	f.tn.set("127.0.0.1", stranger)
	front := listenFront(t, f, DefaultListenLimits, nil)
	cfg, _ := devicekey.ClientConfig(f.ids.mac, f.ids.host.Fingerprint())
	if c, err := tls.Dial("tcp", addrOf(front), cfg); err == nil {
		_ = c.Close()
		t.Fatal("another user's node completed a handshake")
	}
	f.tn.mu.Lock()
	f.tn.fail = errors.New("tailscaled restarting")
	f.tn.mu.Unlock()
	resp, err := meshClient(t, f.ids.mac, f.ids.host).Get(front.URL + "/sessions")
	if err != nil {
		t.Fatalf("a transient WhoIs failure closed the connection: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("transient failure: %d, want the request gate's 503", resp.StatusCode)
	}
}

// Admission and the deadline settle a connection once: whichever wins,
// the other changes nothing.
func TestAdmitAndDeadlineSettleOnce(t *testing.T) {
	mc, b := pipeConn(t)
	mc.admit()
	mc.expire() // the deadline firing just after: too late
	if !mc.isAdmitted() || pipeClosed(b) {
		t.Fatal("an admitted connection was closed by its deadline")
	}
	mc2, b2 := pipeConn(t)
	mc2.expire()
	mc2.admit() // a request just after the deadline: too late
	if mc2.isAdmitted() || !pipeClosed(b2) {
		t.Fatal("an expired connection was admitted")
	}
}

// A paired device granted nothing is answered (hello says so) but its
// connection isn't admitted: the deadline still closes it. A device with
// a grant keeps its connection past the deadline.
func TestGrantedNothingIsntAdmitted(t *testing.T) {
	f := newFixture(t)
	f.tn.set("127.0.0.1", full)
	var conns atomic.Int32
	front := listenFront(t, f, ListenLimits{MaxConns: 8, MaxPerIP: 8, FirstRequest: 300 * time.Millisecond}, &conns)
	get := func(c *http.Client, path string) {
		t.Helper()
		resp, err := c.Get(front.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatalf("%s: %d", path, resp.StatusCode)
		}
	}
	idle := meshClient(t, f.ids.idle, f.ids.host)
	get(idle, "/mesh/hello")
	time.Sleep(600 * time.Millisecond)
	get(idle, "/mesh/hello")
	if n := conns.Load(); n != 2 {
		t.Fatalf("a device granted nothing kept its connection past the deadline (%d connections, want 2)", n)
	}
	granted := meshClient(t, f.ids.mac, f.ids.host)
	get(granted, "/sessions")
	time.Sleep(600 * time.Millisecond)
	get(granted, "/sessions")
	if n := conns.Load(); n != 3 {
		t.Fatalf("a granted device lost its connection to the deadline (%d connections, want 3)", n)
	}
}

// After an unpaired_device refusal the connection is closed: a key the
// host doesn't know gets one answer per handshake.
func TestUnpairedRefusalClosesTheConnection(t *testing.T) {
	for _, h2 := range []bool{true, false} {
		f := newFixture(t)
		var conns atomic.Int32
		cfg, err := devicekey.ServerConfig(f.ids.host)
		if err != nil {
			t.Fatal(err)
		}
		f.tn.set("127.0.0.1", full)
		front := newTLSFront(t, f, cfg, h2, &conns)
		c := meshClient(t, f.ids.stranger, f.ids.host)
		for range 3 {
			resp, err := c.Get(front.URL + "/sessions")
			if err != nil {
				t.Fatal(err)
			}
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode != 403 {
				t.Fatalf("unpaired: %d", resp.StatusCode)
			}
		}
		if n := conns.Load(); n != 3 {
			t.Errorf("h2=%v: %d connections for 3 refused requests, want each closed", h2, n)
		}
		// A paired device keeps its connection.
		conns.Store(0)
		p := meshClient(t, f.ids.mac, f.ids.host)
		for range 3 {
			resp, err := p.Get(front.URL + "/sessions")
			if err != nil {
				t.Fatal(err)
			}
			_, _ = io.Copy(io.Discard, resp.Body) // read to the end, so HTTP/1.1 can reuse it
			_ = resp.Body.Close()
		}
		if n := conns.Load(); n != 1 {
			t.Errorf("h2=%v: a paired device used %d connections, want 1", h2, n)
		}
	}
}

// A request refused by the tailnet gate closes its connection too, unless
// the connection already carries an admitted device.
func TestTailnetRefusalClosesTheConnection(t *testing.T) {
	f := newFixture(t)
	// Passed at the handshake, refused per request (no WhoIs cache in the
	// fixture).
	f.srv.opts.Tailnet = &alternating{first: full, then: stranger}
	var conns atomic.Int32
	front := listenFront(t, f, DefaultListenLimits, &conns)
	c := meshClient(t, f.ids.stranger, f.ids.host)
	for range 3 {
		resp, err := c.Get(front.URL + "/sessions")
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}
	if n := conns.Load(); n != 3 {
		t.Fatalf("%d connections for 3 refused requests, want each closed", n)
	}
}

// alternating answers WhoIs with first, then, first, then...: a node that
// passes at each connection's handshake and is refused on its request.
type alternating struct {
	mu          sync.Mutex
	n           int
	first, then tailnet.Caller
}

func (f *alternating) WhoIs(context.Context, string) (tailnet.Caller, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.n++
	if f.n%2 == 1 {
		return f.first, nil
	}
	return f.then, nil
}
