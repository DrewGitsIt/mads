package server

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/devicekey"
	"github.com/drewkidwell/mads/pkg/localauth"
	"github.com/drewkidwell/mads/services/mesh/internal/tailnet"
)

// doorRouter is a stand-in Router behind the real front door
// (docs/LOCAL-FRONT-DOOR-DESIGN.md): every route needs this node's install
// token. It records the bearer each admitted request arrived with.
type doorRouter struct {
	home   string
	tokens *localauth.Store
	url    *url.URL

	mu      sync.Mutex
	bearers []string
}

func newDoorRouter(t *testing.T) *doorRouter {
	t.Helper()
	d := &doorRouter{home: t.TempDir()}
	var err error
	if d.tokens, err = localauth.LoadOrCreate(d.home); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /sessions", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[]`))
	})
	mux.HandleFunc("POST /sessions/{id}/messages", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	door := localauth.NewDoor(localauth.DoorOptions{
		Name:     "router",
		Store:    d.tokens,
		Classify: func(r *http.Request) string { _, p := mux.Handler(r); return p },
	})
	// Record the bearer before the door strips it.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d.mu.Lock()
		d.bearers = append(d.bearers, r.Header.Get("Authorization"))
		d.mu.Unlock()
		door.Wrap(mux).ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	d.url, _ = url.Parse(srv.URL)
	return d
}

func (d *doorRouter) lastBearer(t *testing.T) string {
	t.Helper()
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.bearers) == 0 {
		t.Fatal("the Router saw no request")
	}
	return d.bearers[len(d.bearers)-1]
}

// tokenDevice is the paired control device the token tests forward for.
var tokenDevice = func() *devicekey.Identity {
	id, err := devicekey.Generate()
	if err != nil {
		panic(err)
	}
	return id
}()

func newTokenServer(d *doorRouter, token func() string) *Server {
	devs := &fakeDevices{byFP: map[string]contracts.Peer{}, loaded: true}
	devs.pair(tokenDevice, "mac", contracts.GrantControl, contracts.PeerCeilings{})
	return New(Options{
		Self:           tailnet.Self{HostName: "pop-os", DNSName: "pop-os.ts.net", UserID: owner, IPv4: "100.0.0.9"},
		Tailnet:        &fakeTailnet{callers: map[string]tailnet.Caller{"100.0.0.1": full}},
		RequireTailnet: true,
		Devices:        devs,
		RouterURL:      d.url,
		Token:          token,
	})
}

func forward(t *testing.T, s *Server, method, target string, header map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, target, nil)
	r.RemoteAddr = "100.0.0.1:41000"
	r.TLS = tlsState(t, tokenDevice)
	for k, v := range header {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}

// The Mesh Service authenticates to its own Router with this node's install
// token, read from $MADS_HOME/local.token.
func TestForwardSendsThisNodesInstallToken(t *testing.T) {
	d := newDoorRouter(t)
	s := newTokenServer(d, localauth.FileSource(localauth.Path(d.home)))
	for _, tc := range []struct{ method, target string }{
		{"GET", "/sessions"},
		{"POST", "/sessions/ses_1/messages"},
	} {
		if w := forward(t, s, tc.method, tc.target, nil); w.Code != http.StatusOK {
			t.Fatalf("%s %s: %d %s", tc.method, tc.target, w.Code, w.Body)
		}
		if got, want := d.lastBearer(t), "Bearer "+d.tokens.Token(); got != want {
			t.Fatalf("%s %s: Router saw Authorization %q, want this node's install token", tc.method, tc.target, got)
		}
	}
}

// A caller's own Authorization never reaches the Router: not a bogus one,
// and not one that happens to be a valid credential somewhere else. The
// bearer the Router sees is always this node's.
func TestForwardDropsTheCallersAuthorization(t *testing.T) {
	d := newDoorRouter(t)
	s := newTokenServer(d, localauth.FileSource(localauth.Path(d.home)))
	for _, h := range []string{"Bearer the-viewers-own-install-token-xxxxxxxxxxxxxx", "Basic dXNlcjpwYXNz", "Bearer " + d.tokens.Token()} {
		w := forward(t, s, "GET", "/sessions", map[string]string{"Authorization": h, "Proxy-Authorization": "Bearer x"})
		if w.Code != http.StatusOK {
			t.Fatalf("Authorization %q: %d %s", h, w.Code, w.Body)
		}
		if got := d.lastBearer(t); got != "Bearer "+d.tokens.Token() {
			t.Fatalf("Authorization %q: Router saw %q", h, got)
		}
	}
	// With no token of its own, the caller's still isn't passed through,
	// even when it is this Router's token: a peer can't authenticate itself.
	s = newTokenServer(d, nil)
	if w := forward(t, s, "GET", "/sessions", map[string]string{"Authorization": "Bearer " + d.tokens.Token()}); w.Code != http.StatusUnauthorized {
		t.Fatalf("caller-supplied token passed through: %d %s", w.Code, w.Body)
	}
	if got := d.lastBearer(t); got != "" {
		t.Fatalf("Router saw Authorization %q from the caller", got)
	}
}

// `mads token rotate` is followed without a restart: the next forward sends
// the new token.
func TestForwardFollowsTokenRotation(t *testing.T) {
	d := newDoorRouter(t)
	s := newTokenServer(d, localauth.FileSource(localauth.Path(d.home)))
	if w := forward(t, s, "GET", "/sessions", nil); w.Code != http.StatusOK {
		t.Fatalf("before rotation: %d %s", w.Code, w.Body)
	}
	old := d.tokens.Token()
	next, err := localauth.Rotate(d.home)
	if err != nil {
		t.Fatal(err)
	}
	if next == old {
		t.Fatal("rotation kept the token")
	}
	if w := forward(t, s, "GET", "/sessions", nil); w.Code != http.StatusOK {
		t.Fatalf("after rotation: %d %s", w.Code, w.Body)
	}
	if got := d.lastBearer(t); got != "Bearer "+next {
		t.Fatalf("after rotation the Router saw %q, want the new token", got)
	}
}

// A Mesh Service with no token to send (none on disk) gets the Router's
// 401 back, not a silent success.
func TestForwardWithoutTokenIsRefusedByTheRouter(t *testing.T) {
	d := newDoorRouter(t)
	s := newTokenServer(d, localauth.FileSource(localauth.Path(t.TempDir())))
	if w := forward(t, s, "GET", "/sessions", nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("no token file: %d %s, want the Router's 401", w.Code, w.Body)
	}
}
