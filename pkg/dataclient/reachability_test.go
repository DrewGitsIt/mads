package dataclient

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"syscall"
	"testing"
	"time"
)

type fakeLog struct{ lines []string }

func (l *fakeLog) logf(format string, args ...any) {
	l.lines = append(l.lines, fmt.Sprintf(format, args...))
}

func TestReachabilityRateLimitsAnOutageAndLogsRecovery(t *testing.T) {
	var log fakeLog
	clock := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
	r := &reachability{logf: log.logf, now: func() time.Time { return clock }}
	refused := &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}
	ctx := context.Background()

	r.observe(ctx, "GET /events/head", clock, nil) // up: nothing to say
	if len(log.lines) != 0 {
		t.Fatalf("logged while healthy: %q", log.lines)
	}

	r.observe(ctx, "GET /events/head", clock, refused)
	if len(log.lines) != 1 || !strings.Contains(log.lines[0], "unreachable: GET /events/head") {
		t.Fatalf("first failure: %q", log.lines)
	}
	for i := 0; i < 5; i++ {
		clock = clock.Add(5 * time.Second)
		r.observe(ctx, "GET /events (stream)", clock, refused)
	}
	if len(log.lines) != 1 {
		t.Fatalf("repeats inside 30 s should be suppressed: %q", log.lines)
	}
	clock = clock.Add(5 * time.Second) // 30 s since the first line
	r.observe(ctx, "GET /events/head", clock, refused)
	if len(log.lines) != 2 || !strings.Contains(log.lines[1], "still unreachable after 30s (5 more failed calls)") {
		t.Fatalf("rate-limited repeat: %q", log.lines)
	}

	// A call its caller abandoned says nothing about the Data Service.
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	r.observe(cancelled, "GET /sessions", clock, context.Canceled)

	clock = clock.Add(10 * time.Second)
	r.observe(ctx, "GET /events/head", clock, nil)
	if len(log.lines) != 3 || !strings.Contains(log.lines[2], "reachable again after 40s") {
		t.Fatalf("recovery: %q", log.lines)
	}
	r.observe(ctx, "GET /events/head", clock, nil)
	if len(log.lines) != 3 {
		t.Fatalf("logged again while healthy: %q", log.lines)
	}

	// A new outage logs at once again.
	r.observe(ctx, "GET /events/head", clock, refused)
	if len(log.lines) != 4 || !strings.Contains(log.lines[3], "unreachable: GET /events/head") {
		t.Fatalf("second outage: %q", log.lines)
	}
}

// A slow call that hits the client's timeout, or one whose connection broke
// mid-answer, reached the service: neither is an outage.
func TestReachabilityCountsOnlyConnectFailures(t *testing.T) {
	var log fakeLog
	clock := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
	r := &reachability{logf: log.logf, now: func() time.Time { return clock }}
	ctx := context.Background()

	timeout := &url.Error{Op: "Get", URL: "http://127.0.0.1:1/sessions", Err: errTimeout{}}
	r.observe(ctx, "GET /sessions", clock, timeout)
	reset := &net.OpError{Op: "read", Net: "tcp", Err: syscall.ECONNRESET}
	r.observe(ctx, "GET /sessions", clock, reset)
	if len(log.lines) != 0 {
		t.Fatalf("logged a non-connect failure as an outage: %q", log.lines)
	}

	for _, err := range []error{
		&url.Error{Op: "Get", URL: "http://127.0.0.1:1/", Err: &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}},
		&url.Error{Op: "Get", URL: "http://nope.invalid/", Err: &net.DNSError{Err: "no such host", Name: "nope.invalid", IsNotFound: true}},
		fmt.Errorf("wrapped: %w", syscall.ECONNREFUSED),
	} {
		if !isUnreachable(err) {
			t.Errorf("isUnreachable(%v) = false, want true", err)
		}
	}
}

type errTimeout struct{}

func (errTimeout) Error() string {
	return "context deadline exceeded (Client.Timeout exceeded while awaiting headers)"
}
func (errTimeout) Timeout() bool   { return true }
func (errTimeout) Temporary() bool { return true }

// A call that was already in flight when the service went down, and lands
// between two failed connects, doesn't prove it is back; only a call sent
// after the latest failure does.
func TestReachabilityRecoveryNeedsACallSentAfterTheLastFailure(t *testing.T) {
	var log fakeLog
	clock := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
	r := &reachability{logf: log.logf, now: func() time.Time { return clock }}
	refused := &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}
	ctx := context.Background()

	inFlightStart := r.begin() // sent before the outage
	clock = clock.Add(time.Second)
	r.observe(ctx, "GET /events/head", clock, refused)
	clock = clock.Add(time.Second)
	r.observe(ctx, "GET /sessions", inFlightStart, nil) // lands during the outage
	if len(log.lines) != 1 {
		t.Fatalf("an in-flight call logged a recovery: %q", log.lines)
	}
	clock = clock.Add(time.Second)
	r.observe(ctx, "GET /events/head", clock, refused)
	if len(log.lines) != 1 {
		t.Fatalf("repeat failure inside 30 s: %q", log.lines)
	}

	clock = clock.Add(time.Second)
	sent := r.begin()
	r.observe(ctx, "GET /events/head", sent, nil)
	if len(log.lines) != 2 || !strings.Contains(log.lines[1], "reachable again after 3s") {
		t.Fatalf("recovery from a call sent after the last failure: %q", log.lines)
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
