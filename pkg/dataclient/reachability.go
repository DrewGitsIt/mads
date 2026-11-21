package dataclient

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync"
	"syscall"
	"time"
)

// unreachableEvery is how often a still-down Data Service gets another log
// line while every call keeps failing to connect.
const unreachableEvery = 30 * time.Second

// WithUnreachableLog reports the Data Service going unreachable and coming
// back through logf: one line on the first failed connect, at most one per
// 30 s after that while it stays down (with how many failures it left out),
// and one line on the first call that gets through again. Every request
// this client (and its As siblings) makes counts, streams included. Only a
// failure to connect counts as unreachable (see isUnreachable): a slow call
// that hits the client's timeout, or a call abandoned by its own context
// (the caller went away), says nothing about whether the service is up.
func WithUnreachableLog(logf func(format string, args ...any)) Option {
	return func(c *Client) { c.reach = &reachability{logf: logf, now: time.Now} }
}

// reachability is the WithUnreachableLog state, shared by a client and its
// As siblings.
type reachability struct {
	logf func(format string, args ...any)
	now  func() time.Time

	mu         sync.Mutex
	down       bool
	since      time.Time // when the outage started
	lastFailed time.Time // when the latest failed connect was observed
	lastLogged time.Time
	suppressed int
}

// begin is the start time to hand observe for a request about to be sent.
func (r *reachability) begin() time.Time {
	if r == nil {
		return time.Time{}
	}
	return r.now()
}

// isUnreachable reports whether err is a failure to reach the Data Service
// at all: the dial failed (refused, timed out connecting, no route) or the
// name didn't resolve. Anything after a connection was made (a timeout
// waiting on a slow answer, a reset mid-response) is not.
func isUnreachable(err error) bool {
	var op *net.OpError
	if errors.As(err, &op) && op.Op == "dial" {
		return true
	}
	var dns *net.DNSError
	if errors.As(err, &dns) {
		return true
	}
	return errors.Is(err, syscall.ECONNREFUSED)
}

// observe records one request's transport outcome: start is what begin
// returned before the request was sent, err is what http.Client.Do returned
// (nil when the Data Service answered, whatever the status).
func (r *reachability) observe(ctx context.Context, what string, start time.Time, err error) {
	if r == nil {
		return
	}
	if err != nil && (ctx.Err() != nil || errors.Is(err, context.Canceled) || !isUnreachable(err)) {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	if err == nil {
		// Only a call sent after the latest failure proves the service is
		// back: one that was already in flight across the outage can land
		// between two failures and would log a recovery that isn't one.
		if r.down && start.After(r.lastFailed) {
			r.down = false
			r.logf("data service reachable again after %s (%s)", now.Sub(r.since).Round(time.Second), what)
		}
		if !r.down {
			r.suppressed = 0
		}
		return
	}
	r.lastFailed = now
	if !r.down {
		r.down = true
		r.since = now
		r.lastLogged = now
		r.suppressed = 0
		r.logf("data service unreachable: %s: %v", what, err)
		return
	}
	if now.Sub(r.lastLogged) < unreachableEvery {
		r.suppressed++
		return
	}
	r.logf("data service still unreachable after %s (%d more failed calls): %s: %v",
		now.Sub(r.since).Round(time.Second), r.suppressed, what, err)
	r.lastLogged = now
	r.suppressed = 0
}

// stripQuery drops a request path's query string, which can be long (an
// event stream's types) and says nothing about reachability.
func stripQuery(path string) string {
	if i := strings.IndexByte(path, '?'); i >= 0 {
		return path[:i]
	}
	return path
}
