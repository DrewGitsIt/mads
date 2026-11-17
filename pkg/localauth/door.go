package localauth

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// Kind says what credential a request carried.
type Kind int

const (
	// None: no valid credential (only exempt routes get this far).
	None Kind = iota
	// Install: the install token, by bearer — the user (the CLI, the services,
	// the app, whose main process adds it below the page; H7).
	Install
	// Scoped: a session-scoped token (D4) — one session's orchestrate MCP
	// server.
	Scoped
)

// Principal is who a request is from, as far as the door can tell.
type Principal struct {
	Kind Kind
	// SessionID is the session a Scoped token belongs to.
	SessionID string
}

// Actor is the X-Mads-Actor the door stamps on a scoped request
// ("session-<id>"), whatever the caller sent; empty for other kinds.
func (p Principal) Actor() string {
	if p.Kind == Scoped {
		return "session-" + p.SessionID
	}
	return ""
}

type ctxKey struct{}

// FromContext returns the principal the door attached to a request's
// context (None when there is none — e.g. an exempt route, unauthenticated).
func FromContext(ctx context.Context) Principal {
	p, _ := ctx.Value(ctxKey{}).(Principal)
	return p
}

// WithPrincipal returns ctx carrying p (tests that call handlers directly).
func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, ctxKey{}, p)
}

// UnauthorizedMessage is the 401 body's error text.
const UnauthorizedMessage = "this mads API needs your install token: use `mads api`, or send `Authorization: Bearer <token>` with the token in $MADS_HOME/local.token (the mads app does this for you)"

// DoorOptions configures a Door.
type DoorOptions struct {
	// Name labels log lines ("router", "data").
	Name string
	// Store is the install token.
	Store *Store
	// Scoped, when non-nil, lets session-scoped tokens through — but only
	// to ScopedRoutes.
	Scoped *Registry
	// WatchEvery is how often an open request re-checks its credential, so
	// a stream ends when its credential is revoked or expires (default 1s).
	// A rotation ends them at once.
	WatchEvery time.Duration
	// Classify returns the ServeMux pattern r would be served by (""
	// when none matches), the same lookup the Mesh Service uses, so an API
	// route and the static UI can't be confused. Typically
	// func(r) string { _, p := mux.Handler(r); return p }.
	Classify func(r *http.Request) string
	// Exempt are the patterns served without a credential. The door still
	// identifies a caller there (FromContext), so a handler can show more
	// to the user (GET /health).
	Exempt []string
	// ScopedRoutes are the patterns a scoped token may reach. Which
	// session's resources it may touch is the handler's check (the path's
	// {id} must be the token's session); the door only keeps it off every
	// other route.
	ScopedRoutes []string
}

// Door is the HTTP middleware in front of a service's whole API.
type Door struct {
	opts     DoorOptions
	exempt   map[string]bool
	scoped   map[string]bool
	insecure bool

	mu     sync.Mutex
	active map[*activeReq]struct{} // authenticated requests still running
}

// activeReq is an open request a revocation can end, with the credential
// it was admitted on.
type activeReq struct {
	cancel context.CancelFunc
	tok    string
	p      Principal
}

// NewDoor returns the door opts describes.
func NewDoor(opts DoorOptions) *Door {
	d := &Door{opts: opts, exempt: map[string]bool{}, scoped: map[string]bool{}, active: map[*activeReq]struct{}{}}
	if d.opts.WatchEvery <= 0 {
		d.opts.WatchEvery = time.Second
	}
	if opts.Store != nil {
		// Every credential this door accepts dies with the install token.
		opts.Store.OnRotate(d.endAll)
	}
	for _, p := range opts.Exempt {
		d.exempt[p] = true
	}
	for _, p := range opts.ScopedRoutes {
		d.scoped[p] = true
	}
	return d
}

// Insecure returns a door that lets everything through as the install
// token's holder — the -insecure-no-auth escape hatch, for unit tests that
// construct a service's mux directly. It refuses (panics) outside a `go
// test` binary, so a production build can never switch the door off.
func Insecure() *Door {
	if !InsecureAllowed() {
		panic("localauth: -insecure-no-auth is only available in go test builds")
	}
	return &Door{insecure: true}
}

// InsecureAllowed reports whether this binary may run with no door: only
// a `go test` build.
func InsecureAllowed() bool { return testing.Testing() }

// presented extracts the bearer — the only way a credential is presented
// (docs/LOCAL-FRONT-DOOR-DESIGN.md H7: no cookie). An Authorization header
// with another scheme presents nothing.
func presented(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if h == "" {
		return ""
	}
	scheme, rest, ok := strings.Cut(h, " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return ""
	}
	return strings.TrimSpace(rest)
}

// principalFor is who a bearer is: the install token or a scoped token.
func (d *Door) principalFor(tok string) Principal {
	if tok == "" {
		return Principal{}
	}
	if d.opts.Store != nil && d.opts.Store.Check(tok) {
		return Principal{Kind: Install}
	}
	if d.opts.Scoped != nil {
		if id, ok := d.opts.Scoped.Lookup(tok); ok {
			return Principal{Kind: Scoped, SessionID: id}
		}
	}
	return Principal{}
}

// Authenticate returns who r is from.
func (d *Door) Authenticate(r *http.Request) Principal {
	if d.insecure {
		return Principal{Kind: Install}
	}
	return d.principalFor(presented(r))
}

// endAll ends every open request whose credential the rotation killed. A
// request already admitted on the *new* token — it can arrive while the
// rotation hooks are still running — is left alone (round 2).
func (d *Door) endAll() {
	d.mu.Lock()
	active := make([]*activeReq, 0, len(d.active))
	for a := range d.active {
		active = append(active, a)
	}
	d.mu.Unlock()
	for _, a := range active {
		if d.principalFor(a.tok) != a.p {
			a.cancel()
		}
	}
}

// watch keeps r's context alive only while its credential stays valid:
// it is cancelled on rotation (endAll), and when a periodic re-check finds
// the credential revoked or expired — so a stream (SSE, a proxied child
// output) never outlives the credential that opened it.
func (d *Door) watch(r *http.Request, tok string, p Principal) (*http.Request, func()) {
	ctx, cancel := context.WithCancel(r.Context())
	a := &activeReq{cancel: cancel, tok: tok, p: p}
	d.mu.Lock()
	d.active[a] = struct{}{}
	d.mu.Unlock()
	go func() {
		t := time.NewTicker(d.opts.WatchEvery)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if d.principalFor(tok) != p {
					log.Printf("%s: ending %s %s: its credential was revoked or expired", d.opts.Name, r.Method, r.URL.Path)
					cancel()
					return
				}
			}
		}
	}()
	return r.WithContext(ctx), func() {
		cancel()
		d.mu.Lock()
		delete(d.active, a)
		d.mu.Unlock()
	}
}

// Wrap refuses every request to a non-exempt route that carries no valid
// credential (401), and a scoped token anywhere off ScopedRoutes (403),
// before next sees it. A scoped request's X-Mads-Actor is overwritten with
// the token's session; the caller's own claim is ignored.
func (d *Door) Wrap(next http.Handler) http.Handler {
	if d.insecure {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(WithPrincipal(r.Context(), Principal{Kind: Install})))
		})
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pattern := ""
		if d.opts.Classify != nil {
			pattern = d.opts.Classify(r)
		}
		tok := presented(r)
		p := d.principalFor(tok)
		switch {
		case d.exempt[pattern]:
			// Open to all; the principal (possibly None) rides along.
		case p.Kind == None:
			log.Printf("%s: refused %s %s: no valid token", d.opts.Name, r.Method, r.URL.Path)
			writeRefusal(w, http.StatusUnauthorized, "unauthorized", UnauthorizedMessage)
			return
		case p.Kind == Scoped && !d.scoped[pattern]:
			log.Printf("%s: refused %s %s: session-scoped token off its routes", d.opts.Name, r.Method, r.URL.Path)
			writeRefusal(w, http.StatusForbidden, "forbidden", "this token only reaches its own session's orchestrate routes")
			return
		}
		// The credential has done its job; nothing downstream (a proxied
		// call to a child, a log line) needs to see it.
		r.Header.Del("Authorization")
		if p.Kind == Scoped {
			r.Header.Set("X-Mads-Actor", p.Actor())
		}
		if p.Kind != None {
			var done func()
			r, done = d.watch(r, tok, p)
			defer done()
		}
		next.ServeHTTP(w, r.WithContext(WithPrincipal(r.Context(), p)))
	})
}

func writeRefusal(w http.ResponseWriter, code int, kind, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"code": kind, "error": msg})
}

// WriteForbidden answers a scoped caller reaching for another session's
// resources (the handler-side half of the scope check).
func WriteForbidden(w http.ResponseWriter, msg string) {
	writeRefusal(w, http.StatusForbidden, "forbidden", msg)
}
