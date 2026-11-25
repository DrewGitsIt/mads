package main

import (
	"net/http"
	"testing"
	"time"
)

func TestRefuseWildcard(t *testing.T) {
	for addr, ok := range map[string]bool{
		"100.64.0.70:17380": true,
		"127.0.0.1:17380":    true,
		":17380":             false,
		"0.0.0.0:17380":      false,
		"[::]:17380":         false,
		"nonsense":           false,
	} {
		if err := refuseWildcard(addr); (err == nil) != ok {
			t.Errorf("refuseWildcard(%q) = %v, want ok=%v", addr, err, ok)
		}
	}
}

// The mesh listener closes idle connections and bounds HTTP/2 streams per
// connection.
func TestHTTPServerBounds(t *testing.T) {
	srv := newHTTPServer(http.NotFoundHandler(), nil)
	if srv.IdleTimeout <= 0 || srv.IdleTimeout > 5*time.Minute {
		t.Fatalf("IdleTimeout = %s", srv.IdleTimeout)
	}
	if srv.HTTP2 == nil || srv.HTTP2.MaxConcurrentStreams <= 0 || srv.HTTP2.MaxConcurrentStreams > 1000 {
		t.Fatalf("HTTP2 = %+v", srv.HTTP2)
	}
	if srv.ReadHeaderTimeout <= 0 {
		t.Fatal("no ReadHeaderTimeout")
	}
}
