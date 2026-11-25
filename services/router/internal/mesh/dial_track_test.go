package mesh

import (
	"crypto/tls"
	"net"
	"testing"
)

// A pinned TLS connection goes to http.Transport as the *tls.Conn itself:
// before Go 1.27 the transport finds HTTP/2 only on one, and a wrapper made
// it speak HTTP/1.1 to a Mesh Service that had agreed to HTTP/2 (pop-os's
// Router, built with Go 1.26, 2026-11-25). Its closing still leaves the
// set closeAll ends.
func TestTrackHandsOverTheTLSConn(t *testing.T) {
	a, b := net.Pipe()
	defer func() { _ = b.Close() }()
	tc := tls.Client(&notifyConn{Conn: a}, &tls.Config{InsecureSkipVerify: true}) //nolint:gosec // never handshakes
	c := &peerConn{open: map[net.Conn]struct{}{}}
	got := c.track(tc)
	if got != net.Conn(tc) {
		t.Fatalf("track returned %T, want the *tls.Conn itself", got)
	}
	if len(c.open) != 1 {
		t.Fatalf("open = %d", len(c.open))
	}
	_ = tc.Close()
	if len(c.open) != 0 {
		t.Fatalf("closed, still open: %d", len(c.open))
	}

	// Closed before it was tracked: never left in the set.
	x, y := net.Pipe()
	defer func() { _ = y.Close() }()
	tc2 := tls.Client(&notifyConn{Conn: x}, &tls.Config{InsecureSkipVerify: true}) //nolint:gosec // never handshakes
	_ = tc2.Close()
	c.track(tc2)
	if len(c.open) != 0 {
		t.Fatalf("an already closed conn stayed tracked: %d", len(c.open))
	}
}
