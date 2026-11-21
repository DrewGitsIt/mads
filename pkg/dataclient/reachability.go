package dataclient

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"
)

// unreachableEvery is how often a still-down Data Service gets another log
// line while every call keeps failing to connect.
const unreachableEvery = 30 * time.Second

// WithUnreachableLog reports the Data Service going unreachable and coming
// back through logf: one line on the first failed connect, at most one per
// 30 s after that while it stays down (with how many failures it left out),
// and one line on the first call that gets through again. Every request
// this client (and its As siblings) makes counts, streams included. A call
// abandoned by its own context (the caller went away) doesn't.
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
	lastLogged time.Time
	suppressed int
}

// observe records one request's transport outcome: err is what
// http.Client.Do returned (nil when the Data Service answered, whatever the
// status).
func (r *reachability) observe(ctx context.Context, what string, err error) {
	if r == nil {
		return
	}
	if err != nil && (ctx.Err() != nil || errors.Is(err, context.Canceled)) {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	if err == nil {
		if r.down {
			r.down = false
			r.logf("data service reachable again after %s (%s)", now.Sub(r.since).Round(time.Second), what)
		}
		r.suppressed = 0
		return
	}
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
