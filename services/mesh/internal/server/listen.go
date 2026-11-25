package server

import (
	"context"
	"crypto/tls"
	"errors"
	"log"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"

	"github.com/drewkidwell/mads/pkg/devicekey"
	"github.com/drewkidwell/mads/pkg/meshnet"
)

// ListenLimits bounds what any one client can hold on the mesh listener,
// so throwaway keys, bare TCP and slow handshakes can't lock paired
// devices out.
type ListenLimits struct {
	// MaxConns is the most connections open at once; the next waits.
	MaxConns int
	// MaxPerIP is the most from one address; past it a new one is closed
	// at once, so one client can't take the whole of MaxConns.
	MaxPerIP int
	// FirstRequest is how long a connection has to be admitted (a paired
	// device's request getting through the gate) before it's closed: a
	// handshake that never finishes, a TLS session that never sends a
	// request, an HTTP/2 preface held open, or a refused caller lingering.
	FirstRequest time.Duration
	// FailedPerMinute and Cooldown are the failed-handshake cool-down
	// (docs/MESH-NETWORK-DESIGN.md S1): an address whose connections close
	// FailedPerMinute times in a minute without carrying a request (no
	// certificate, the wrong key type, a scanner) is closed at accept for
	// Cooldown. Generous on purpose: many devices share one address behind
	// CGNAT. Zero turns it off.
	FailedPerMinute int
	Cooldown        time.Duration
}

// DefaultListenLimits suit a user's own handful of devices, per network.
var DefaultListenLimits = ListenLimits{MaxConns: 64, MaxPerIP: 8, FirstRequest: 10 * time.Second, FailedPerMinute: 10, Cooldown: 60 * time.Second}

// Net is the network a listener serves, as the gate sees it.
type Net struct {
	// ID is the network's id ("tailscale", "lan:..."), Label its label.
	ID, Label string
	// Tailscale: connections on it are tailnet connections, so the
	// Tailscale identity check (when on) and vouching apply to them. On
	// any other network a tailnet check would only ever refuse.
	Tailscale bool
	// TailnetOnly closes, at accept, a connection from outside Tailscale's
	// ranges (round-1 review S4): a packet for the 100.x address delivered
	// over the LAN isn't a tailnet connection, check or no check.
	TailnetOnly bool
}

// Budget is one network's connection budget: every listener on that
// network shares it, and no other network's flood can spend it (S1:
// "Limits per listener").
type Budget struct {
	lim  ListenLimits
	sem  chan struct{}
	now  func() time.Time
	mu   sync.Mutex
	ipN  map[string]int
	fail map[string]*failures
}

type failures struct {
	start time.Time
	n     int
	until time.Time
}

// NewBudget is a budget of lim.
func NewBudget(lim ListenLimits) *Budget {
	return &Budget{lim: lim, sem: make(chan struct{}, lim.MaxConns), ipN: map[string]int{}, fail: map[string]*failures{}, now: time.Now}
}

// coolingDown reports whether ip is closed at accept now.
func (b *Budget) coolingDown(ip string) bool {
	if b.lim.FailedPerMinute <= 0 {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	f, ok := b.fail[ip]
	return ok && b.now().Before(f.until)
}

// failed counts a connection from ip that closed without a request.
func (b *Budget) failed(ip string) {
	if b.lim.FailedPerMinute <= 0 {
		return
	}
	now := b.now()
	b.mu.Lock()
	defer b.mu.Unlock()
	f, ok := b.fail[ip]
	if !ok {
		if len(b.fail) >= refusalMemory {
			for k, v := range b.fail {
				if now.Sub(v.start) > time.Minute && now.After(v.until) {
					delete(b.fail, k)
				}
			}
			if len(b.fail) >= refusalMemory {
				clear(b.fail)
			}
		}
		f = &failures{start: now}
		b.fail[ip] = f
	}
	if now.Sub(f.start) > time.Minute {
		f.start, f.n = now, 0
	}
	f.n++
	if f.n >= b.lim.FailedPerMinute {
		f.until, f.start, f.n = now.Add(b.lim.Cooldown), now, 0
		log.Printf("mesh: %s: %d connections in a minute closed without a request (failed handshakes): closing its connections at accept for %s", ip, b.lim.FailedPerMinute, b.lim.Cooldown)
	}
}

// Listen wraps l with lim, as a Tailscale listener (the identity check,
// when on, applies to its connections): the Mesh Service's listener before
// mesh networks, and tests'. Use with ConnContext.
func (s *Server) Listen(l net.Listener, lim ListenLimits) net.Listener {
	return s.ListenOn(l, NewBudget(lim), Net{ID: "tailscale", Label: "Tailscale", Tailscale: true})
}

// ListenOn wraps l, a listener on network n, with its network's budget,
// and, with the identity check on and n Tailscale, the tailnet gate before
// the TLS handshake: a connection from an address WhoIs definitely refuses
// (not a tailnet node, another user's, tagged) is closed before it reads a
// byte. Use with ConnContext.
// TLSConfig is base for the mesh listeners: on each connection, a paired
// device's key completing the handshake marks it (meshConn.pairedTLS), so
// it isn't counted as a failed handshake. Nothing is admitted by it: the
// gate still checks every request.
func (s *Server) TLSConfig(base *tls.Config) *tls.Config {
	out := base.Clone()
	// Set here, not left to http.Server: the per-connection config below is
	// a copy of this one, and the server's own addition of "h2" would land
	// only on its copy, so HTTP/2 would never be agreed.
	if len(out.NextProtos) == 0 {
		out.NextProtos = []string{"h2", "http/1.1"}
	}
	per := out.Clone()
	out.GetConfigForClient = func(hi *tls.ClientHelloInfo) (*tls.Config, error) {
		mc, ok := hi.Conn.(*meshConn)
		if !ok {
			return nil, nil
		}
		c := per.Clone()
		prev := c.VerifyConnection
		c.VerifyConnection = func(cs tls.ConnectionState) error {
			if prev != nil {
				if err := prev(cs); err != nil {
					return err
				}
			}
			if fp, err := devicekey.PeerFingerprint(cs); err == nil && s.opts.Devices != nil {
				if _, ok := s.opts.Devices.Lookup(string(fp)); ok {
					mc.pairedTLS.Store(true)
				}
			}
			return nil
		}
		return c, nil
	}
	return out
}

func (s *Server) ListenOn(l net.Listener, b *Budget, n Net) net.Listener {
	return &meshListener{Listener: l, s: s, b: b, net: n, done: make(chan struct{})}
}

type meshListener struct {
	net.Listener
	s         *Server
	b         *Budget
	net       Net
	done      chan struct{}
	closeOnce sync.Once
}

func (l *meshListener) Accept() (net.Conn, error) {
	b := l.b
	for {
		select {
		case b.sem <- struct{}{}:
		case <-l.done:
			return nil, net.ErrClosed
		}
		c, err := l.Listener.Accept()
		if err != nil {
			<-b.sem
			return nil, err
		}
		ip := hostOf(c.RemoteAddr().String())
		if l.net.TailnetOnly && !inTailnetRanges(ip) {
			_ = c.Close()
			<-b.sem
			continue
		}
		if b.coolingDown(ip) {
			_ = c.Close()
			<-b.sem
			continue
		}
		b.mu.Lock()
		if b.ipN[ip] >= b.lim.MaxPerIP {
			b.mu.Unlock()
			_ = c.Close()
			<-b.sem
			continue
		}
		b.ipN[ip]++
		b.mu.Unlock()
		mc := &meshConn{Conn: c, l: l, ip: ip}
		mc.timer = time.AfterFunc(b.lim.FirstRequest, mc.expire)
		return mc, nil
	}
}

func (l *meshListener) Close() error {
	l.closeOnce.Do(func() { close(l.done) })
	return l.Listener.Close()
}

// errNotAdmitted is a connection the tailnet gate closed before its
// handshake.
var errNotAdmitted = errors.New("mesh: connection refused by the tailnet gate")

// meshConn is one accepted connection: counted against its address and
// its network's budget, closed unless admitted within FirstRequest.
type meshConn struct {
	net.Conn
	l     *meshListener
	ip    string
	timer *time.Timer
	// state is statePending until either the first admission or the
	// deadline wins it, once: an admitted connection is never closed by a
	// deadline firing at the same moment, and an expired one is never
	// marked admitted.
	state atomic.Int32
	// served: a request reached the gate on this connection (its TLS
	// handshake finished). One that closes without is a failed handshake
	// for the cool-down, unless a paired device's key completed the
	// handshake on it (pairedTLS): that device isn't probing, and one
	// speaking the wrong protocol (HTTP/1.1 after agreeing to HTTP/2, a
	// Router built with Go 1.26) mustn't shut every connection from its
	// address out.
	served    atomic.Bool
	pairedTLS atomic.Bool

	gateOnce sync.Once
	gateErr  error

	closeOnce sync.Once
}

// Read runs the tailnet gate on the first read: in the connection's own
// goroutine (the TLS handshake's first read), never in the accept loop, so
// a slow LocalAPI can't stall other connections.
func (c *meshConn) Read(b []byte) (int, error) {
	c.gateOnce.Do(func() {
		if c.l.net.Tailscale && c.l.s.requireTailnet.Load() && !c.l.s.admitAddr(c.RemoteAddr().String()) {
			c.gateErr = errNotAdmitted
			_ = c.Conn.Close()
		}
	})
	if c.gateErr != nil {
		return 0, c.gateErr
	}
	return c.Conn.Read(b)
}

func (c *meshConn) Close() error {
	err := c.Conn.Close()
	c.closeOnce.Do(func() {
		c.timer.Stop()
		b := c.l.b
		if !c.served.Load() && !c.pairedTLS.Load() && c.gateErr == nil {
			b.failed(c.ip)
		}
		b.mu.Lock()
		if b.ipN[c.ip]--; b.ipN[c.ip] <= 0 {
			delete(b.ipN, c.ip)
		}
		b.mu.Unlock()
		<-b.sem
	})
	return err
}

const (
	statePending int32 = iota
	stateAdmitted
	stateExpired
)

func (c *meshConn) admit() {
	if c.state.CompareAndSwap(statePending, stateAdmitted) {
		c.timer.Stop()
	}
}

// expire is the deadline: it closes the connection unless an admission
// got there first.
func (c *meshConn) expire() {
	if c.state.CompareAndSwap(statePending, stateExpired) {
		_ = c.Conn.Close()
	}
}

func (c *meshConn) isAdmitted() bool { return c.state.Load() == stateAdmitted }

type connKey struct{}

// ConnContext is http.Server.ConnContext for a Listen listener: it lets
// the gate mark a connection admitted, or refuse to keep it.
func (s *Server) ConnContext(ctx context.Context, c net.Conn) context.Context {
	if tc, ok := c.(*tls.Conn); ok {
		c = tc.NetConn()
	}
	if mc, ok := c.(*meshConn); ok {
		return context.WithValue(ctx, connKey{}, mc)
	}
	return ctx
}

func connFrom(ctx context.Context) *meshConn {
	mc, _ := ctx.Value(connKey{}).(*meshConn)
	return mc
}

// admitConn marks r's connection as a paired device's: it stays open.
func admitConn(ctx context.Context) {
	if mc := connFrom(ctx); mc != nil {
		mc.admit()
	}
}

// connAdmitted reports whether r's connection carries an admitted device.
// A connection not served through Listen (tests) counts as admitted.
func connAdmitted(ctx context.Context) bool {
	mc := connFrom(ctx)
	return mc == nil || mc.isAdmitted()
}

// connNet is the network r's connection came in on. A connection not
// served through Listen (tests) reads as a Tailscale one, as every
// connection was before mesh networks.
func connNet(ctx context.Context) Net {
	if mc := connFrom(ctx); mc != nil {
		return mc.l.net
	}
	return Net{ID: "tailscale", Label: "Tailscale", Tailscale: true}
}

// markServed notes that a request reached the gate on r's connection.
func markServed(ctx context.Context) {
	if mc := connFrom(ctx); mc != nil {
		mc.served.Store(true)
	}
}

func hostOf(addr string) string {
	if h, _, err := net.SplitHostPort(addr); err == nil {
		return h
	}
	return addr
}

func inTailnetRanges(ip string) bool {
	a, err := netip.ParseAddr(ip)
	if err != nil {
		return false
	}
	a = a.Unmap()
	for _, p := range meshnet.TailnetRanges {
		if p.Contains(a) {
			return true
		}
	}
	return false
}
