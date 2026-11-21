package server

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"time"
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
}

// DefaultListenLimits suit a user's own handful of devices.
var DefaultListenLimits = ListenLimits{MaxConns: 64, MaxPerIP: 8, FirstRequest: 10 * time.Second}

// Listen wraps l with lim, and, with the tailnet required, the tailnet
// gate before the TLS handshake: a connection from an address WhoIs
// definitely refuses (not a tailnet node, another user's, tagged, no mesh
// capability) is closed before it reads a byte. Use with ConnContext.
func (s *Server) Listen(l net.Listener, lim ListenLimits) net.Listener {
	return &meshListener{Listener: l, s: s, lim: lim, sem: make(chan struct{}, lim.MaxConns), done: make(chan struct{}), perIP: map[string]int{}}
}

type meshListener struct {
	net.Listener
	s         *Server
	lim       ListenLimits
	sem       chan struct{}
	done      chan struct{}
	closeOnce sync.Once

	mu    sync.Mutex
	perIP map[string]int
}

func (l *meshListener) Accept() (net.Conn, error) {
	for {
		select {
		case l.sem <- struct{}{}:
		case <-l.done:
			return nil, net.ErrClosed
		}
		c, err := l.Listener.Accept()
		if err != nil {
			<-l.sem
			return nil, err
		}
		ip := hostOf(c.RemoteAddr().String())
		l.mu.Lock()
		if l.perIP[ip] >= l.lim.MaxPerIP {
			l.mu.Unlock()
			_ = c.Close()
			<-l.sem
			continue
		}
		l.perIP[ip]++
		l.mu.Unlock()
		mc := &meshConn{Conn: c, l: l, ip: ip}
		mc.timer = time.AfterFunc(l.lim.FirstRequest, mc.expire)
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
// the listener, closed unless admitted within FirstRequest.
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

	gateOnce sync.Once
	gateErr  error

	closeOnce sync.Once
}

// Read runs the tailnet gate on the first read: in the connection's own
// goroutine (the TLS handshake's first read), never in the accept loop, so
// a slow LocalAPI can't stall other connections.
func (c *meshConn) Read(b []byte) (int, error) {
	c.gateOnce.Do(func() {
		if c.l.s.requireTailnet.Load() && !c.l.s.admitAddr(c.RemoteAddr().String()) {
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
		c.l.mu.Lock()
		if c.l.perIP[c.ip]--; c.l.perIP[c.ip] <= 0 {
			delete(c.l.perIP, c.ip)
		}
		c.l.mu.Unlock()
		<-c.l.sem
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

func hostOf(addr string) string {
	if h, _, err := net.SplitHostPort(addr); err == nil {
		return h
	}
	return addr
}
