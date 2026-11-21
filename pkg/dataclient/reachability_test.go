package dataclient

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type fakeLog struct{ lines []string }

func (l *fakeLog) logf(format string, args ...any) { l.lines = append(l.lines, fmt.Sprintf(format, args...)) }

func TestReachabilityRateLimitsAnOutageAndLogsRecovery(t *testing.T) {
	var log fakeLog
	clock := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
	r := &reachability{logf: log.logf, now: func() time.Time { return clock }}
	refused := errors.New("dial tcp 127.0.0.1:17374: connect: connection refused")
	ctx := context.Background()

	r.observe(ctx, "GET /events/head", nil) // up: nothing to say
	if len(log.lines) != 0 {
		t.Fatalf("logged while healthy: %q", log.lines)
	}

	r.observe(ctx, "GET /events/head", refused)
	if len(log.lines) != 1 || !strings.Contains(log.lines[0], "unreachable: GET /events/head") {
		t.Fatalf("first failure: %q", log.lines)
	}
	for i := 0; i < 5; i++ {
		clock = clock.Add(5 * time.Second)
		r.observe(ctx, "GET /events (stream)", refused)
	}
	if len(log.lines) != 1 {
		t.Fatalf("repeats inside 30 s should be suppressed: %q", log.lines)
	}
	clock = clock.Add(5 * time.Second) // 30 s since the first line
	r.observe(ctx, "GET /events/head", refused)
	if len(log.lines) != 2 || !strings.Contains(log.lines[1], "still unreachable after 30s (5 more failed calls)") {
		t.Fatalf("rate-limited repeat: %q", log.lines)
	}

	// A call its caller abandoned says nothing about the Data Service.
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	r.observe(cancelled, "GET /sessions", context.Canceled)

	clock = clock.Add(10 * time.Second)
	r.observe(ctx, "GET /events/head", nil)
	if len(log.lines) != 3 || !strings.Contains(log.lines[2], "reachable again after 40s") {
		t.Fatalf("recovery: %q", log.lines)
	}
	r.observe(ctx, "GET /events/head", nil)
	if len(log.lines) != 3 {
		t.Fatalf("logged again while healthy: %q", log.lines)
	}

	// A new outage logs at once again.
	r.observe(ctx, "GET /events/head", refused)
	if len(log.lines) != 4 || !strings.Contains(log.lines[3], "unreachable: GET /events/head") {
		t.Fatalf("second outage: %q", log.lines)
	}
}

// End to end through the client: a refused connect on both the head and the
// stream logs once, and the first call that gets through logs the recovery.
func TestWithUnreachableLogCoversDoAndStream(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close() // nothing listening: connection refused

	var log fakeLog
	c := New("http://"+addr, "router", WithUnreachableLog(log.logf))
	ctx := context.Background()
	if _, err := c.EventsHead(ctx); err == nil {
		t.Fatal("EventsHead: want an error with nothing listening")
	}
	if _, err := c.As("other").Stream(ctx, 0, "", []string{"session.created"}); err == nil {
		t.Fatal("Stream: want an error with nothing listening")
	}
	if len(log.lines) != 1 || !strings.Contains(log.lines[0], "GET /events/head") {
		t.Fatalf("want one line for the outage, got %q", log.lines)
	}

	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"seq":7}`))
	}))
	srv.Listener.Close()
	srv.Listener, err = net.Listen("tcp", addr)
	if err != nil {
		t.Skipf("couldn't rebind %s: %v", addr, err)
	}
	srv.Start()
	defer srv.Close()
	if head, err := c.EventsHead(ctx); err != nil || head != 7 {
		t.Fatalf("EventsHead after recovery = %d, %v", head, err)
	}
	if len(log.lines) != 2 || !strings.Contains(log.lines[1], "reachable again") {
		t.Fatalf("want a recovery line, got %q", log.lines)
	}
}

func TestNoUnreachableLogByDefault(t *testing.T) {
	c := New("http://127.0.0.1:1", "x")
	if c.reach != nil {
		t.Fatal("a client without WithUnreachableLog should not track reachability")
	}
	_, _ = c.EventsHead(context.Background()) // must not panic on a nil tracker
}
