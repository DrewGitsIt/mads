package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/credstore"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/pkg/localauth"
	"github.com/drewkidwell/mads/pkg/localguard"
	"github.com/drewkidwell/mads/pkg/registryclient"
	"github.com/drewkidwell/mads/providers/fake"
	"github.com/drewkidwell/mads/providers/fastmodel"
	"github.com/drewkidwell/mads/providers/provider"
	"github.com/drewkidwell/mads/services/data/datatest"
	"github.com/drewkidwell/mads/services/router/internal/api"
	"github.com/drewkidwell/mads/services/router/internal/job"
	"github.com/drewkidwell/mads/services/router/internal/supervisor"
)

// routerRoutes reads every pattern the Router registers straight from its
// source (as the Mesh Service's route table test does), so a new route
// can't slip past the front-door guard by omission.
func routerRoutes(t *testing.T) []string {
	t.Helper()
	src, err := os.ReadFile("internal/api/api.go")
	if err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile(`mux\.Handle(?:Func)?\("([^"]+)"`)
	seen := map[string]bool{}
	for _, m := range re.FindAllStringSubmatch(string(src), -1) {
		seen[m[1]] = true
	}
	if len(seen) < 100 {
		t.Fatalf("found only %d Router routes; has the registration moved out of api.go?", len(seen))
	}
	out := make([]string, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// fill makes a concrete request target from a pattern, every {wildcard}
// replaced by id.
func fill(pattern, id string) (method, path string) {
	method, path, ok := strings.Cut(pattern, " ")
	if !ok {
		method, path = http.MethodGet, pattern
	}
	return method, regexp.MustCompile(`\{[^}]+\}`).ReplaceAllString(path, id)
}

type routerStack struct {
	h      http.Handler
	home   string
	auth   frontDoor
	data   *dataclient.Client
	tokens *localauth.Store
}

// newRouterStack is the Router's real HTTP stack (main.go's newMux, with
// localguard and the front door) over a real in-process Data Service.
func newRouterStack(t *testing.T) *routerStack {
	t.Helper()
	home := t.TempDir()
	tokens, err := localauth.LoadOrCreate(home)
	if err != nil {
		t.Fatal(err)
	}
	auth := newTestFrontDoor(tokens)
	data := dataclient.New(datatest.Start(t), "router")
	sup := supervisor.New(data, nil, nil, nil)
	t.Cleanup(func() { sup.Shutdown(context.Background()) })
	reg := registryclient.New("")
	jobs := job.New(data, fastmodel.New(fake.New(), fastmodel.Config{}), reg)
	providers := provider.NewRegistry()
	if err := providers.Register(fake.New()); err != nil {
		t.Fatal(err)
	}
	creds, err := credstore.Open(filepath.Join(t.TempDir(), "credentials.json"))
	if err != nil {
		t.Fatal(err)
	}
	h := newMux(data, sup, jobs, providers, creds, nil, nil, nil, nil, reg, t.TempDir(), nil, "", nil, "",
		identity{root: "/src/checkout"}, localguard.New("router", "17373", nil), nil, nil, auth, nil)
	return &routerStack{h: h, home: home, auth: auth, data: data, tokens: tokens}
}

func (s *routerStack) do(t *testing.T, method, target string, set func(*http.Request)) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, target, strings.NewReader("{}"))
	r.Host = "127.0.0.1:17373"
	r.Header.Set("Content-Type", "application/json")
	if set != nil {
		set(r)
	}
	w := httptest.NewRecorder()
	s.h.ServeHTTP(w, r)
	return w
}

func withBearer(tok string) func(*http.Request) {
	return func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+tok) }
}

// newTestFrontDoor is main.go's front door state, wired the same way.
func newTestFrontDoor(tokens *localauth.Store) frontDoor {
	auth := frontDoor{tokens: tokens, scoped: localauth.NewRegistry()}
	tokens.OnRotate(auth.scoped.RevokeAll)
	return auth
}

// withCookie presents tok as a cookie, which no door reads (H7).
func withCookie(tok string) func(*http.Request) {
	return func(r *http.Request) { r.AddCookie(&http.Cookie{Name: "mads_local", Value: tok}) }
}

// TestEveryRouterRouteNeedsTheToken is the guard: every API route the
// Router registers refuses a caller with no token or the wrong one, and a
// session-scoped token anywhere but its orchestrate routes — except the
// doc's exemptions (docs/LOCAL-FRONT-DOOR-DESIGN.md D3).
func TestEveryRouterRouteNeedsTheToken(t *testing.T) {
	s := newRouterStack(t)
	scoped, err := s.auth.scoped.Mint("x1")
	if err != nil {
		t.Fatal(err)
	}
	exempt := map[string]bool{}
	for _, p := range api.ExemptRoutes {
		exempt[p] = true
	}
	scopedOK := map[string]bool{}
	for _, p := range api.ScopedRoutes {
		scopedOK[p] = true
	}
	if strings.Join(api.ExemptRoutes, ",") != "/,GET /health" {
		t.Fatalf("ExemptRoutes = %v: the doc's exemptions are exactly the static UI and GET /health (H7 removed /auth/enter)", api.ExemptRoutes)
	}
	registered := map[string]bool{}
	for _, pattern := range routerRoutes(t) {
		registered[pattern] = true
		if exempt[pattern] {
			continue
		}
		method, path := fill(pattern, "x1")
		for name, set := range map[string]func(*http.Request){
			"no token":     nil,
			"wrong bearer": withBearer("not-the-install-token-not-the-install-tok"),
			"wrong cookie": withCookie("not-the-install-token-not-the-install-tok"),
			// H7: no cookie is a credential, the install token least of all.
			"install token as the cookie": withCookie(s.tokens.Token()),
		} {
			if w := s.do(t, method, path, set); w.Code != http.StatusUnauthorized {
				t.Errorf("%s (%s): code %d, want 401", pattern, name, w.Code)
			}
		}
		w := s.do(t, method, path, withBearer(scoped))
		if scopedOK[pattern] {
			if w.Code == http.StatusUnauthorized || w.Code == http.StatusForbidden {
				t.Errorf("%s with its own session's scoped token: %d", pattern, w.Code)
			}
		} else if w.Code != http.StatusForbidden {
			t.Errorf("%s with a scoped token: code %d, want 403", pattern, w.Code)
		}
	}
	// Every exemption and scoped route is a real route, and every
	// orchestrate route is classified for scoped tokens on purpose.
	for _, p := range append(append([]string{}, api.ExemptRoutes...), api.ScopedRoutes...) {
		if !registered[p] {
			t.Errorf("front-door table names %q, which the Router doesn't register", p)
		}
	}
	for p := range registered {
		if strings.Contains(p, "/orchestrate/") && !scopedOK[p] {
			t.Errorf("orchestrate route %q isn't in api.ScopedRoutes: decide whether the orchestrate MCP server may reach it", p)
		}
	}
}

func TestExemptRoutesAndUnknownPaths(t *testing.T) {
	s := newRouterStack(t)
	// The static UI (the mux's "/" pattern): no token needed.
	for _, path := range []string{"/", "/index.html", "/assets/x.js"} {
		if w := s.do(t, "GET", path, nil); w.Code == http.StatusUnauthorized {
			t.Errorf("static %s refused", path)
		}
	}
	// /health: open, but only service and status without the token.
	w := s.do(t, "GET", "/health", nil)
	var body map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || w.Code != 200 || len(body) != 2 || body["root"] != "" {
		t.Fatalf("anonymous /health: %d %s", w.Code, w.Body)
	}
	w = s.do(t, "GET", "/health", withBearer(s.tokens.Token()))
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || body["root"] != "/src/checkout" {
		t.Fatalf("authenticated /health: %d %s", w.Code, w.Body)
	}
	// An unclean path ServeMux would redirect is not a way in.
	if w := s.do(t, "GET", "/sessions/../credentials", nil); w.Code != http.StatusUnauthorized {
		t.Errorf("unclean path: %d, want 401", w.Code)
	}
	// The install token by bearer works on an API route (any scheme case).
	for name, set := range map[string]func(*http.Request){"bearer": withBearer(s.tokens.Token()), "lowercase scheme": func(r *http.Request) {
		r.Header.Set("Authorization", "bearer "+s.tokens.Token())
	}} {
		if w := s.do(t, "GET", "/sessions", set); w.Code != http.StatusOK {
			t.Errorf("GET /sessions with the install token (%s): %d %s", name, w.Code, w.Body)
		}
	}
	// The 401 says what to do.
	w = s.do(t, "GET", "/sessions", nil)
	if !strings.Contains(w.Body.String(), `"code":"unauthorized"`) || !strings.Contains(w.Body.String(), "mads api") {
		t.Errorf("401 body %s", w.Body)
	}
}

// TestScopedTokenStaysInItsSession: session A's token is refused on
// session B's orchestrate routes, and on a link B granted, whatever
// X-Mads-Actor it claims.
func TestScopedTokenStaysInItsSession(t *testing.T) {
	s := newRouterStack(t)
	ctx := context.Background()
	a := contracts.Session{Name: "a", Provider: "fake"}
	b := contracts.Session{Name: "b", Provider: "fake"}
	c := contracts.Session{Name: "c", Provider: "fake"}
	d := contracts.Session{Name: "d", Provider: "fake"}
	for _, sess := range []*contracts.Session{&a, &b, &c, &d} {
		if err := s.data.CreateSession(ctx, sess); err != nil {
			t.Fatal(err)
		}
	}
	linkByA, err := s.data.CreateLink(ctx, contracts.LinkRequest{GrantingSessionID: a.ID, SubjectSessionID: c.ID, Grants: []contracts.Grant{contracts.GrantOrchestrate}})
	if err != nil {
		t.Fatal(err)
	}
	linkByB, err := s.data.CreateLink(ctx, contracts.LinkRequest{GrantingSessionID: b.ID, SubjectSessionID: d.ID, Grants: []contracts.Grant{contracts.GrantOrchestrate}})
	if err != nil {
		t.Fatal(err)
	}
	tokA, err := s.auth.scoped.Mint(a.ID)
	if err != nil {
		t.Fatal(err)
	}
	spoofB := func(r *http.Request) {
		r.Header.Set("Authorization", "Bearer "+tokA)
		r.Header.Set("X-Mads-Actor", "session-"+b.ID)
	}
	for _, pattern := range api.ScopedRoutes {
		if strings.HasPrefix(pattern, "POST /links/") {
			continue
		}
		method, path := fill(pattern, b.ID)
		if w := s.do(t, method, path, spoofB); w.Code != http.StatusForbidden {
			t.Errorf("%s on session B with A's token: %d, want 403", pattern, w.Code)
		}
		method, path = fill(pattern, a.ID)
		if w := s.do(t, method, path, withBearer(tokA)); w.Code == http.StatusForbidden || w.Code == http.StatusUnauthorized {
			t.Errorf("%s on its own session: %d", pattern, w.Code)
		}
	}
	if w := s.do(t, "POST", "/links/"+linkByB.ID+"/orchestrate/message", spoofB); w.Code != http.StatusForbidden {
		t.Errorf("message over B's link with A's token: %d, want 403", w.Code)
	}
	// Over its own link it passes the scope check (and then meets the
	// linked-sessions flag, off here: 404).
	if w := s.do(t, "POST", "/links/"+linkByA.ID+"/orchestrate/message", withBearer(tokA)); w.Code != http.StatusNotFound {
		t.Errorf("message over A's own link: %d, want 404 (past the scope check)", w.Code)
	}
	// The user's install token is not held to any session.
	method, path := fill(api.ScopedRoutes[0], b.ID)
	if w := s.do(t, method, path, withBearer(s.tokens.Token())); w.Code == http.StatusForbidden || w.Code == http.StatusUnauthorized {
		t.Errorf("install token on B's orchestrate route: %d", w.Code)
	}
}

// TestRotationLogsEverythingOut: after `mads token rotate`, the old token
// and every scoped token stop working.
func TestRotationLogsEverythingOut(t *testing.T) {
	s := newRouterStack(t)
	old := s.tokens.Token()
	scoped, _ := s.auth.scoped.Mint("x1")
	fresh, err := localauth.Rotate(s.home)
	if err != nil {
		t.Fatal(err)
	}
	if w := s.do(t, "GET", "/sessions", withBearer(old)); w.Code != http.StatusUnauthorized {
		t.Errorf("old token after rotation: %d", w.Code)
	}
	method, path := fill(api.ScopedRoutes[0], "x1")
	if w := s.do(t, method, path, withBearer(scoped)); w.Code != http.StatusUnauthorized {
		t.Errorf("scoped token after rotation: %d", w.Code)
	}
	if w := s.do(t, "GET", "/sessions", withBearer(fresh)); w.Code != http.StatusOK {
		t.Errorf("new token after rotation: %d", w.Code)
	}
}

// TestRouterInsecureNoAuthIsRefusedOutsideTests: a real build can't turn
// the door off.
func TestRouterInsecureNoAuthIsRefusedOutsideTests(t *testing.T) {
	home := t.TempDir()
	cmd := exec.Command(bootRouterBin, "-insecure-no-auth", "-addr", "127.0.0.1:0", "-mads-home", home, "-data-url", "http://127.0.0.1:1") //nolint:gosec // test-built binary
	cmd.Env = bootCleanEnv(home)
	out, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(out), "-insecure-no-auth is refused") {
		t.Fatalf("router -insecure-no-auth: err %v\n%s", err, out)
	}
}

// TestViteDevProxyCoversEveryRouterPrefix: the Vite dev server proxies the
// Router by top-level path (ui/vite-router-proxy.ts, D7); a new prefix the
// proxy doesn't know would silently 404 under `npm run dev`.
func TestViteDevProxyCoversEveryRouterPrefix(t *testing.T) {
	src, err := os.ReadFile("../../ui/vite-router-proxy.ts")
	if err != nil {
		t.Fatal(err)
	}
	block := regexp.MustCompile(`(?s)ROUTER_API_PREFIXES = \[(.*?)\]`).FindSubmatch(src)
	if block == nil {
		t.Fatal("ROUTER_API_PREFIXES not found in ui/vite-router-proxy.ts")
	}
	listed := map[string]bool{}
	for _, m := range regexp.MustCompile(`"([^"]+)"`).FindAllSubmatch(block[1], -1) {
		listed[string(m[1])] = true
	}
	for _, pattern := range append(routerRoutes(t), meshLayerRoutes(t)...) {
		_, path := fill(pattern, "x")
		seg := strings.SplitN(strings.TrimPrefix(path, "/"), "/", 2)[0]
		if seg == "" {
			continue // the static UI
		}
		if !listed[seg] {
			t.Errorf("Router route %q: /%s isn't in ui/vite-router-proxy.ts's ROUTER_API_PREFIXES", pattern, seg)
		}
	}
}

// meshLayerRoutes is the routes the mesh layer (internal/mesh) answers in front
// of the API: its own mux's patterns, plus /peers, which it claims by
// prefix (isPeersPath) rather than registering.
func meshLayerRoutes(t *testing.T) []string {
	t.Helper()
	files, err := filepath.Glob("internal/mesh/*.go")
	if err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile(`mux\.Handle(?:Func)?\("([^"]+)"`)
	out := []string{"GET /peers"}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range re.FindAllStringSubmatch(string(src), -1) {
			out = append(out, m[1])
		}
	}
	if len(out) < 10 {
		t.Fatalf("found only %d mesh routes; has their registration moved?", len(out))
	}
	return out
}

// TestWindowWideReadsAreTheUsers: the window-wide GET /permission-requests
// (every live session's pending requests) and GET /events/head are the
// user's alone — a session's scoped token is refused (403), the install
// token is let through. TestEveryRouterRouteNeedsTheToken covers them in
// bulk; this names them, since one hands out every session's state at once.
func TestWindowWideReadsAreTheUsers(t *testing.T) {
	s := newRouterStack(t)
	scoped, err := s.auth.scoped.Mint("x1")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/permission-requests", "/events/head"} {
		if w := s.do(t, "GET", path, withBearer(scoped)); w.Code != http.StatusForbidden {
			t.Errorf("GET %s with a scoped token: %d, want 403", path, w.Code)
		}
		if w := s.do(t, "GET", path, nil); w.Code != http.StatusUnauthorized {
			t.Errorf("GET %s with no token: %d, want 401", path, w.Code)
		}
		if w := s.do(t, "GET", path, withBearer(s.tokens.Token())); w.Code != http.StatusOK {
			t.Errorf("GET %s with the install token: %d, want 200 (%s)", path, w.Code, w.Body)
		}
	}
}
