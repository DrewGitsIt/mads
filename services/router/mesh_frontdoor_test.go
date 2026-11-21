package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/credstore"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/pkg/devicekey"
	"github.com/drewkidwell/mads/pkg/localauth"
	"github.com/drewkidwell/mads/pkg/localguard"
	"github.com/drewkidwell/mads/pkg/registryclient"
	"github.com/drewkidwell/mads/providers/fake"
	"github.com/drewkidwell/mads/providers/fastmodel"
	"github.com/drewkidwell/mads/providers/provider"
	"github.com/drewkidwell/mads/services/data/datatest"
	"github.com/drewkidwell/mads/services/router/internal/job"
	"github.com/drewkidwell/mads/services/router/internal/mesh"
	"github.com/drewkidwell/mads/services/router/internal/supervisor"
)

// recordingPeer stands in for a peer's Mesh Service and keeps every request
// it gets, whole, so a test can prove what crossed the mesh.
type recordingPeer struct {
	*httptest.Server
	fp   string // its device key's fingerprint
	mu   sync.Mutex
	seen []string // each request as text: request line, every header, body
	auth []string // the Authorization header of each request ("" if none)
}

const peerSession = "ses_onpeer1"

func newRecordingPeer(t *testing.T) *recordingPeer {
	t.Helper()
	p := &recordingPeer{}
	p.Server = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var b strings.Builder
		b.WriteString(r.Method + " " + r.URL.RequestURI() + "\n")
		for k, vs := range r.Header {
			b.WriteString(k + ": " + strings.Join(vs, ",") + "\n")
		}
		b.Write(body)
		p.mu.Lock()
		p.seen = append(p.seen, b.String())
		p.auth = append(p.auth, r.Header.Get("Authorization"))
		p.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		row := map[string]any{"id": peerSession, "name": "over there", "status": "active"}
		switch r.URL.Path {
		case "/mesh/hello":
			_ = json.NewEncoder(w).Encode(mesh.Hello{Node: "pop-os", DNSName: "pop-os.ts.net", MeshAPI: mesh.MeshAPI, Access: []string{"view", "control"}})
		case "/sessions":
			_ = json.NewEncoder(w).Encode([]any{row})
		case "/sessions/" + peerSession:
			_ = json.NewEncoder(w).Encode(row)
		case "/links":
			_ = json.NewEncoder(w).Encode([]any{})
		default:
			_ = json.NewEncoder(w).Encode(map[string]string{"served_by": "peer", "path": r.URL.Path})
		}
	}))
	// Like a real Mesh Service: TLS with its own device key, a client
	// certificate required.
	id, err := devicekey.Generate()
	if err != nil {
		t.Fatal(err)
	}
	if p.TLS, err = devicekey.ServerConfig(id); err != nil {
		t.Fatal(err)
	}
	p.fp = string(id.Fingerprint())
	p.EnableHTTP2 = true
	p.StartTLS()
	t.Cleanup(p.Close)
	return p
}

// addBody is POST /peers for p, pinned to its key.
func (p *recordingPeer) addBody() string {
	return `{"mesh_url":"` + p.URL + `","fingerprint":"` + p.fp + `"}`
}

func (p *recordingPeer) requests() (seen, auth []string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.seen...), append([]string(nil), p.auth...)
}

// newMeshRouterStack is newRouterStack with mesh routing wired in, as
// main.go does: localguard, the front door, then mesh, then the API.
func newMeshRouterStack(t *testing.T) (*routerStack, *mesh.Manager) {
	t.Helper()
	home := t.TempDir()
	tokens, err := localauth.LoadOrCreate(home)
	if err != nil {
		t.Fatal(err)
	}
	auth := newTestFrontDoor(tokens)
	// The Data Service client carries the token, as in production; the mesh
	// manager shares it for local reads, but must never use it for peers.
	data := dataclient.New(datatest.Start(t), "router", dataclient.WithTokenSource(tokens.Token))
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
	id, err := devicekey.Generate()
	if err != nil {
		t.Fatal(err)
	}
	mgr := mesh.NewManager(data, mesh.Options{SelfName: "mac", ProbeInterval: 20 * time.Millisecond, IndexInterval: 20 * time.Millisecond, Identity: id})
	h := newMux(data, sup, jobs, providers, creds, nil, nil, nil, nil, reg, t.TempDir(), nil, "", nil, "",
		identity{root: "/src/checkout"}, localguard.New("router", "17373", nil), nil, auth, mgr)
	return &routerStack{h: h, home: home, auth: auth, data: data, tokens: tokens}, mgr
}

// meshRoutes are requests the mesh layer serves or forwards, not the API.
var meshRoutes = []struct{ method, target string }{
	{"GET", "/peers"},
	{"POST", "/peers"},
	{"GET", "/peers/candidates"},
	{"GET", "/peers/pop-os"},
	{"PUT", "/peers/pop-os"},
	{"DELETE", "/peers/pop-os"},
	{"GET", "/peers/pop-os/sessions"},
	{"GET", "/sessions?scope=all"},
	{"GET", "/links?session_ids=" + peerSession},
	{"GET", "/sessions/" + peerSession},
	{"POST", "/sessions/" + peerSession + "/messages"},
	// No such route on this Router: without Claims it would fall to the
	// exempt static UI.
	{"GET", "/sessions/" + peerSession + "/a-route-only-the-peer-has"},
}

// The /peers API and implicit forwarding are reachable only through the
// front door: no token, a wrong one, or a session-scoped one gets nothing,
// and nothing reaches the peer.
func TestMeshRoutesAreBehindTheFrontDoor(t *testing.T) {
	s, _ := newMeshRouterStack(t)
	peer := newRecordingPeer(t)
	if w := s.do(t, "POST", "/peers", func(r *http.Request) {
		withBearer(s.tokens.Token())(r)
		body := peer.addBody()
		r.Body = io.NopCloser(strings.NewReader(body))
		r.ContentLength = int64(len(body))
	}); w.Code != http.StatusCreated {
		t.Fatalf("POST /peers with the token: %d %s", w.Code, w.Body)
	}
	before, _ := peer.requests()

	scoped, err := s.auth.scoped.Mint("ses_local1")
	if err != nil {
		t.Fatal(err)
	}
	routes := append(meshRoutes, struct{ method, target string }{
		// A scoped route: the door lets a scoped token by, the mesh layer
		// must not forward it.
		"GET", "/sessions/" + peerSession + "/orchestrate/summary",
	})
	for _, rt := range routes {
		for name, set := range map[string]func(*http.Request){
			"no token":    nil,
			"wrong token": withBearer("not-the-install-token-not-the-install-tok"),
		} {
			if w := s.do(t, rt.method, rt.target, set); w.Code != http.StatusUnauthorized {
				t.Errorf("%s %s (%s): %d, want 401", rt.method, rt.target, name, w.Code)
			}
		}
		if w := s.do(t, rt.method, rt.target, withBearer(scoped)); w.Code != http.StatusForbidden {
			t.Errorf("%s %s (scoped token): %d, want 403", rt.method, rt.target, w.Code)
		}
	}
	after, _ := peer.requests()
	for _, req := range after[len(before):] {
		if !strings.HasPrefix(req, "GET /mesh/hello") && !strings.HasPrefix(req, "GET /sessions\n") {
			t.Errorf("a refused request reached the peer:\n%s", req)
		}
	}
}

// The install token never leaves the machine: every request this Router
// makes to a peer's Mesh Service — probes, the session index, scope=all,
// forwarded UI calls, the explicit peer proxy — carries no Authorization and
// no copy of the token anywhere in its headers, path or body, even though
// the UI's requests arrived with it.
func TestInstallTokenNeverReachesAPeer(t *testing.T) {
	s, mgr := newMeshRouterStack(t)
	peer := newRecordingPeer(t)
	tok := s.tokens.Token()
	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan struct{})
	go func() { defer close(runDone); mgr.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-runDone })

	call := func(method, target, body string, want int) {
		t.Helper()
		w := s.do(t, method, target, func(r *http.Request) {
			withBearer(tok)(r)
			// What the UI page sends, and a second copy in a header no one
			// should forward blindly.
			r.Header.Set("Origin", "http://127.0.0.1:17373")
			r.Header.Set("Proxy-Authorization", "Bearer "+tok)
			if body != "" {
				r.Body = io.NopCloser(strings.NewReader(body))
				r.ContentLength = int64(len(body))
			}
		})
		if w.Code != want {
			t.Fatalf("%s %s: %d %s, want %d", method, target, w.Code, w.Body, want)
		}
	}
	call("POST", "/peers", peer.addBody(), http.StatusCreated)
	call("GET", "/peers", "", http.StatusOK)
	call("GET", "/sessions?scope=all", "", http.StatusOK)
	call("GET", "/sessions/"+peerSession, "", http.StatusOK)
	call("POST", "/sessions/"+peerSession+"/messages", `{"text":"hi"}`, http.StatusOK)
	call("GET", "/sessions/"+peerSession+"/a-route-only-the-peer-has", "", http.StatusOK)
	// A bulk links read naming the peer's session (indexed by now) fans out.
	cfg, err := s.data.Config(ctx)
	if err != nil {
		t.Fatal(err)
	}
	cfg.FeatureFlags.LinkedSessions = true
	if err := s.data.UpdateConfig(ctx, &cfg); err != nil {
		t.Fatal(err)
	}
	call("GET", "/links?session_ids=ses_local1,"+peerSession, "", http.StatusOK)
	if seen, _ := peer.requests(); !slices.ContainsFunc(seen, func(r string) bool { return strings.HasPrefix(r, "GET /links?session_ids="+peerSession+"\n") }) {
		t.Fatalf("the bulk links read didn't reach the peer: %v", seen)
	}
	call("GET", "/peers/pop-os/sessions", "", http.StatusOK)
	call("PUT", "/peers/pop-os", `{"name":"pop"}`, http.StatusOK)
	call("GET", "/peers/pop/version", "", http.StatusOK)

	// Let the background prober and indexer run a few rounds too (waited
	// for, not timed: a loaded machine takes longer).
	deadline := time.Now().Add(30 * time.Second)
	for {
		seen, _ := peer.requests()
		hellos, index := 0, 0
		for _, req := range seen {
			if strings.HasPrefix(req, "GET /mesh/hello") {
				hellos++
			}
			if strings.HasPrefix(req, "GET /sessions\n") {
				index++
			}
		}
		if hellos >= 3 && index >= 3 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("background probe/index didn't run: %d hellos, %d index reads", hellos, index)
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	<-runDone

	seen, auth := peer.requests()
	if len(seen) < 10 {
		t.Fatalf("the peer saw only %d requests", len(seen))
	}
	for i, req := range seen {
		if auth[i] != "" {
			t.Errorf("request carried Authorization %q:\n%s", auth[i], req)
		}
		if strings.Contains(req, tok) {
			t.Errorf("the install token reached the peer:\n%s", req)
		}
		if strings.Contains(strings.ToLower(req), "proxy-authorization") {
			t.Errorf("Proxy-Authorization reached the peer:\n%s", req)
		}
	}
}
