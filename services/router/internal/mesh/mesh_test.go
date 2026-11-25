package mesh

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/pkg/devicekey"
	"github.com/drewkidwell/mads/services/data/datatest"
)

// fakePeer stands in for a peer's Mesh Service. Like the real one it
// speaks TLS 1.3 with its own device key, requires a client certificate,
// and refuses browser headers, so a test that forgets to strip them fails.
type fakePeer struct {
	*httptest.Server
	id *devicekey.Identity
	fp string // its device key's fingerprint, what a viewer pins
	// handshakes counts completed TLS handshakes; clients the fingerprints
	// of the client certificates presented.
	handshakes atomic.Int32
	clientsMu  sync.Mutex
	clients    []string

	mu       sync.Mutex
	hello    Hello
	status   int // /mesh/hello status; 0 means 200
	sessions []map[string]any
	seen     []string // "METHOD path?query" of every forwarded request
	// links is the peer's link table. bulkLinks is GET /links' status (0
	// serves the table), answered with linksCode as the error code;
	// linksDelay holds every link answer back; strayLinks are added to
	// every link answer.
	links      []map[string]any
	bulkLinks  int
	linksCode  string
	linksDelay time.Duration
	strayLinks []map[string]any
	// linksInFlight and maxLinksInFlight count concurrent link reads.
	linksInFlight, maxLinksInFlight int
	// redirect, when set, answers every request with a redirect there.
	redirect string
	// answer, when set, answers every request.
	answer http.HandlerFunc
}

// serveLinks answers the fake's link routes without holding f.mu while
// it waits, so concurrent reads overlap as they would on a real peer.
func (f *fakePeer) serveLinks(w http.ResponseWriter, r *http.Request, status int, ids []string) {
	f.mu.Lock()
	code, delay := f.linksCode, f.linksDelay
	rows := append(linksTouching(f.links, ids...), f.strayLinks...)
	f.linksInFlight++
	f.maxLinksInFlight = max(f.maxLinksInFlight, f.linksInFlight)
	f.mu.Unlock()
	defer func() {
		f.mu.Lock()
		f.linksInFlight--
		f.mu.Unlock()
	}()
	if delay > 0 {
		select {
		case <-time.After(delay):
		case <-r.Context().Done():
			return
		}
	}
	if status != 0 {
		writeJSON(w, status, map[string]string{"code": code, "error": "refused"})
		return
	}
	writeJSON(w, http.StatusOK, rows)
}

// linksTouching is the rows of links with any of ids on either side.
func linksTouching(links []map[string]any, ids ...string) []map[string]any {
	out := []map[string]any{}
	for _, l := range links {
		for _, id := range ids {
			if l["granting_session_id"] == id || l["subject_session_id"] == id {
				out = append(out, l)
				break
			}
		}
	}
	return out
}

func newFakePeer(t *testing.T, node string) *fakePeer {
	t.Helper()
	id := newIdentity(t)
	f := &fakePeer{id: id, fp: string(id.Fingerprint()), hello: Hello{Node: node, DNSName: node + ".ts.net", MadsCommit: "abc", MeshAPI: MeshAPI, Access: []string{"view", "control"}, Device: string(id.Fingerprint())}}
	f.Server = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := r.Header["Origin"]; ok || r.Header.Get("Sec-Fetch-Site") != "" {
			http.Error(w, `{"code":"browser_request"}`, http.StatusForbidden)
			return
		}
		f.mu.Lock()
		if f.answer != nil {
			answer := f.answer
			f.mu.Unlock()
			answer(w, r)
			return
		}
		if f.redirect != "" {
			to := f.redirect
			f.mu.Unlock()
			http.Redirect(w, r, to+r.URL.RequestURI(), http.StatusFound)
			return
		}
		if r.URL.Path != "/mesh/hello" {
			f.seen = append(f.seen, r.Method+" "+r.URL.RequestURI())
		}
		bulk := f.bulkLinks
		f.mu.Unlock()
		if r.URL.Path == "/links" {
			f.serveLinks(w, r, bulk, strings.Split(r.URL.Query().Get("session_ids"), ","))
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		switch {
		case r.URL.Path == "/mesh/hello":
			if f.status != 0 {
				w.WriteHeader(f.status)
				_, _ = w.Write([]byte(`{"code":"other_user","error":"mesh peers must belong to this node's owner"}`))
				return
			}
			writeJSON(w, http.StatusOK, f.hello)
		case r.URL.Path == "/sessions":
			writeJSON(w, http.StatusOK, f.sessions)
		case strings.HasSuffix(r.URL.Path, "/output"):
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("data: hi\n\n"))
			w.(http.Flusher).Flush()
			f.mu.Unlock()
			<-r.Context().Done()
			f.mu.Lock()
		case strings.HasPrefix(r.URL.Path, "/sessions/") && !strings.Contains(strings.TrimPrefix(r.URL.Path, "/sessions/"), "/"):
			id := strings.TrimPrefix(r.URL.Path, "/sessions/")
			for _, s := range f.sessions {
				if s["id"] == id {
					writeJSON(w, http.StatusOK, s)
					return
				}
			}
			http.NotFound(w, r)
		default:
			writeJSON(w, http.StatusOK, map[string]string{"peer": node, "path": r.URL.Path})
		}
	}))
	cfg, err := devicekey.ServerConfig(id)
	if err != nil {
		t.Fatal(err)
	}
	cfg.VerifyConnection = func(cs tls.ConnectionState) error {
		fp, err := devicekey.PeerFingerprint(cs)
		if err != nil {
			return err
		}
		f.handshakes.Add(1)
		f.clientsMu.Lock()
		f.clients = append(f.clients, string(fp))
		f.clientsMu.Unlock()
		return nil
	}
	f.TLS = cfg
	f.EnableHTTP2 = true
	f.StartTLS()
	t.Cleanup(f.Close)
	return f
}

// meshURL is the peer's address as a row stores it (http://host:port; it
// is dialled with TLS).
func (f *fakePeer) meshURL() string { return strings.Replace(f.URL, "https://", "http://", 1) }

// row is a viewed, pinned peer row for f.
func (f *fakePeer) row(name string) *contracts.Peer {
	return &contracts.Peer{Name: name, MeshURL: f.meshURL(), Fingerprint: f.fp}
}

func newIdentity(t *testing.T) *devicekey.Identity {
	t.Helper()
	id, err := devicekey.Generate()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func (f *fakePeer) forwarded() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.seen...)
}

type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time      { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *clock) Add(d time.Duration) { c.mu.Lock(); c.now = c.now.Add(d); c.mu.Unlock() }

type harness struct {
	id    *devicekey.Identity // this viewer's device key
	data  *dataclient.Client
	mgr   *Manager
	clk   *clock
	local http.Handler // the stand-in for the Router's own API
	srv   *httptest.Server

	mu         sync.Mutex
	localLinks []map[string]any
	localSeen  []string // "path?query" of every local GET /links
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	h := &harness{id: newIdentity(t), data: dataclient.New(datatest.Start(t), "test"), clk: &clock{now: time.Unix(1_000_000, 0)}}
	h.mgr = NewManager(h.data, Options{AllowLoopback: true, SelfName: "mac", Now: h.clk.Now, Debounce: 10 * time.Second, Identity: h.id})
	h.local = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/sessions":
			if r.URL.Query().Get("scope") != "" {
				t.Errorf("scope leaked to the local API: %s", r.URL.RawQuery)
			}
			writeJSON(w, http.StatusOK, []map[string]any{{"id": "ses_local", "name": "here"}})
		case "/links":
			h.mu.Lock()
			defer h.mu.Unlock()
			h.localSeen = append(h.localSeen, r.URL.RequestURI())
			writeJSON(w, http.StatusOK, linksTouching(h.localLinks, strings.Split(r.URL.Query().Get("session_ids"), ",")...))
		default:
			writeJSON(w, http.StatusOK, map[string]string{"served_by": "local", "path": r.URL.Path})
		}
	})
	h.srv = httptest.NewServer(h.mgr.Handler(h.local))
	t.Cleanup(h.srv.Close)
	return h
}

func (h *harness) addPeer(t *testing.T, p *fakePeer) contracts.PeerStatus {
	t.Helper()
	resp, err := http.Post(h.srv.URL+"/peers", "application/json", strings.NewReader(`{"mesh_url":"`+p.URL+`","fingerprint":"`+p.fp+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var st contracts.PeerStatus
	_ = json.NewDecoder(resp.Body).Decode(&st)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /peers: %d %+v", resp.StatusCode, st)
	}
	return st
}

func (h *harness) reachabilityEvents(t *testing.T) []contracts.PeerReachabilityChange {
	t.Helper()
	evs, err := h.data.Events(context.Background(), 0, "", []string{string(contracts.PeerReachabilityChanged)})
	if err != nil {
		t.Fatal(err)
	}
	var out []contracts.PeerReachabilityChange
	for _, e := range evs {
		var c contracts.PeerReachabilityChange
		_ = json.Unmarshal(e.Payload, &c)
		out = append(out, c)
	}
	return out
}

// get does a browser-style GET (Origin set, as the UI's fetch would).
func get(t *testing.T, url string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	req.Header.Set("Origin", "http://localhost:17373")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var b strings.Builder
	_, _ = bufio.NewReader(resp.Body).WriteTo(&b)
	return resp.StatusCode, b.String()
}

func TestProbeClassification(t *testing.T) {
	h := newHarness(t)
	p := newFakePeer(t, "pop-os")
	ctx := context.Background()
	row := *p.row("pop-os")

	if got := h.mgr.ProbePeer(ctx, row); got.State != contracts.PeerConnected || got.Hello.Node != "pop-os" {
		t.Fatalf("healthy peer: %+v", got)
	}
	p.mu.Lock()
	p.hello.Access = []string{}
	p.mu.Unlock()
	if got := h.mgr.ProbePeer(ctx, row); got.State != contracts.PeerDenied {
		t.Fatalf("no view access: %+v", got)
	}
	p.mu.Lock()
	p.hello.Access, p.hello.MeshAPI = []string{"view"}, MeshAPI+1
	p.mu.Unlock()
	if got := h.mgr.ProbePeer(ctx, row); got.State != contracts.PeerVersionMismatch {
		t.Fatalf("newer mesh_api: %+v", got)
	}
	p.mu.Lock()
	p.status = http.StatusForbidden
	p.mu.Unlock()
	if got := h.mgr.ProbePeer(ctx, row); got.State != contracts.PeerDenied || !strings.Contains(got.Reason, "owner") {
		t.Fatalf("refused by the peer: %+v", got)
	}

	// No key pinned: never dialled at all.
	before := p.handshakes.Load()
	unpinned := row
	unpinned.Fingerprint = ""
	if got := h.mgr.ProbePeer(ctx, unpinned); got.State != contracts.PeerUnpaired || !strings.Contains(got.Reason, "mads pair --accept") {
		t.Fatalf("unpinned peer: %+v", got)
	}
	// Pinned to another key: the handshake is refused, and the reason
	// names both keys. Another device answering at an address counts as
	// that address not answering (F4), not as the device refusing.
	wrong := row
	wrong.ID, wrong.Fingerprint = "per_wrong", testFingerprint(9)
	got := h.mgr.ProbePeer(ctx, wrong)
	if got.State != contracts.PeerUnreachable || !strings.Contains(got.Reason, "a different device answered") || !strings.Contains(got.Reason, devicekey.Fingerprint(p.fp).Short()) || !strings.Contains(got.Reason, devicekey.Fingerprint(testFingerprint(9)).Short()) {
		t.Fatalf("pinned to another key: %+v", got)
	}
	if p.handshakes.Load() != before {
		t.Fatalf("a refused pin or an unpinned row completed a handshake")
	}
	// The viewer presented its own device certificate.
	p.clientsMu.Lock()
	clients := append([]string(nil), p.clients...)
	p.clientsMu.Unlock()
	for _, c := range clients {
		if c != string(h.id.Fingerprint()) {
			t.Fatalf("the peer saw client key %s, want this viewer's", c)
		}
	}

	p.Close()
	if got := h.mgr.ProbePeer(ctx, row); got.State != contracts.PeerUnreachable {
		t.Fatalf("down peer: %+v", got)
	}
}

// No plaintext fallback: a peer whose Mesh Service speaks plain HTTP is
// never sent a request; it reads as unreachable.
func TestNoPlaintextFallback(t *testing.T) {
	h := newHarness(t)
	var served atomic.Int32
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		served.Add(1)
		writeJSON(w, http.StatusOK, Hello{Node: "pop-os", MeshAPI: MeshAPI, Access: []string{"view", "control"}})
	}))
	t.Cleanup(plain.Close)
	row := contracts.Peer{ID: "per_plain", Name: "pop-os", MeshURL: plain.URL, Fingerprint: testFingerprint(7)}
	if got := h.mgr.ProbePeer(context.Background(), row); got.State != contracts.PeerUnreachable {
		t.Fatalf("plain HTTP peer: %+v", got)
	}
	if err := h.data.CreatePeer(context.Background(), &row); err != nil {
		t.Fatal(err)
	}
	_ = h.mgr.Reload(context.Background())
	if code, _ := get(t, h.srv.URL+"/peers/pop-os/sessions"); code != http.StatusBadGateway {
		t.Fatalf("forward to a plain HTTP peer: %d", code)
	}
	if served.Load() != 0 {
		t.Fatalf("a plain HTTP Mesh Service was sent %d requests", served.Load())
	}
}

// One keep-alive (HTTP/2) connection per peer: probes, index reads,
// forwarded requests and a stream beside them share one TLS handshake.
func TestPeerConnectionsAreReused(t *testing.T) {
	h := newHarness(t)
	p := newFakePeer(t, "pop-os")
	p.sessions = []map[string]any{{"id": "ses_remote"}}
	h.addPeer(t, p)
	ctx := context.Background()
	h.mgr.RefreshIndex(ctx)

	// A long-lived stream stays open throughout.
	sctx, cancel := context.WithCancel(ctx)
	defer cancel()
	req, _ := http.NewRequestWithContext(sctx, http.MethodGet, h.srv.URL+"/sessions/ses_remote/output", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if line, _ := bufio.NewReader(resp.Body).ReadString('\n'); line != "data: hi\n" {
		t.Fatalf("stream: %q", line)
	}

	var wg sync.WaitGroup
	for range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			h.mgr.ProbeAll(ctx)
			if code, _ := get(t, h.srv.URL+"/sessions/ses_remote/transcript"); code != 200 {
				t.Errorf("forward: %d", code)
			}
			get(t, h.srv.URL+"/sessions?scope=all")
		}()
	}
	wg.Wait()
	if n := p.handshakes.Load(); n != 1 {
		t.Fatalf("%d TLS handshakes, want one connection reused", n)
	}
}

func TestAddPeerVetsBeforeSaving(t *testing.T) {
	h := newHarness(t)
	// No key, no device: there's nothing to verify it against.
	live := newFakePeer(t, "live")
	resp, err := http.Post(h.srv.URL+"/peers", "application/json", strings.NewReader(`{"mesh_url":"`+live.URL+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	var refusal struct{ Code, Reachability string }
	_ = json.NewDecoder(resp.Body).Decode(&refusal)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnprocessableEntity || refusal.Code != "peer_unpaired" || live.handshakes.Load() != 0 {
		t.Fatalf("a device with no fingerprint: %d %+v (handshakes %d)", resp.StatusCode, refusal, live.handshakes.Load())
	}
	if code := send(t, http.MethodPost, h.srv.URL+"/peers", `{"mesh_url":"`+live.URL+`","fingerprint":"nope"}`); code != http.StatusUnprocessableEntity {
		t.Fatalf("a malformed fingerprint: %d", code)
	}
	dead := newFakePeer(t, "dead")
	dead.Close()
	resp, err = http.Post(h.srv.URL+"/peers", "application/json", strings.NewReader(`{"mesh_url":"`+dead.URL+`","fingerprint":"`+dead.fp+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("unreachable device: %d, want 422", resp.StatusCode)
	}
	if list, _ := h.data.Peers(context.Background()); len(list) != 0 {
		t.Fatalf("an unvetted device was saved: %+v", list)
	}

	pop := newFakePeer(t, "pop-os")
	st := h.addPeer(t, pop)
	// Stored pinned, at its http:// address (dialled with TLS), granted
	// nothing here: viewing a device lets it do nothing on this host.
	if st.Name != "pop-os" || st.DNSName != "pop-os.ts.net" || st.Reachability != contracts.PeerConnected ||
		st.Fingerprint != pop.fp || st.MeshURL != pop.meshURL() || st.Grant != nil || !st.IsViewed() {
		t.Fatalf("added peer = %+v", st)
	}
	// Added means connected at once, and the first connect is logged
	// without waiting out the debounce.
	if evs := h.reachabilityEvents(t); len(evs) != 1 || evs[0].From != contracts.PeerUnknown || evs[0].To != contracts.PeerConnected {
		t.Fatalf("reachability events = %+v", evs)
	}

	code, body := get(t, h.srv.URL+"/peers")
	if code != 200 || !strings.Contains(body, `"reachability":"connected"`) {
		t.Fatalf("GET /peers: %d %s", code, body)
	}
	req, _ := http.NewRequest(http.MethodPut, h.srv.URL+"/peers/pop-os", strings.NewReader(`{"hidden":true}`))
	if resp, err := http.DefaultClient.Do(req); err != nil || resp.StatusCode != 200 {
		t.Fatalf("PUT hidden: %v %v", resp, err)
	}
	if st, _ := h.mgr.Status("pop-os"); !st.Hidden {
		t.Fatal("hidden didn't stick")
	}
	req, _ = http.NewRequest(http.MethodDelete, h.srv.URL+"/peers/pop-os", nil)
	if resp, err := http.DefaultClient.Do(req); err != nil || resp.StatusCode != http.StatusNoContent {
		var b strings.Builder
		_, _ = bufio.NewReader(resp.Body).WriteTo(&b)
		t.Fatalf("DELETE: %d %s %v", resp.StatusCode, b.String(), err)
	}
	if h.mgr.HasPeers() {
		t.Fatal("peer still listed after delete")
	}
}

func TestReachabilityIsDebounced(t *testing.T) {
	h := newHarness(t)
	p := newFakePeer(t, "pop-os")
	st := h.addPeer(t, p)
	ctx := context.Background()
	down := Probe{State: contracts.PeerUnreachable, Reason: "connection refused"}
	up := Probe{State: contracts.PeerConnected}

	// A blip shorter than the debounce is never logged.
	h.mgr.observe(ctx, st.ID, down)
	h.clk.Add(5 * time.Second)
	h.mgr.observe(ctx, st.ID, down)
	h.mgr.observe(ctx, st.ID, up)
	if evs := h.reachabilityEvents(t); len(evs) != 1 {
		t.Fatalf("a 5s blip was logged: %+v", evs)
	}
	// An outage that holds is logged once, then the recovery once.
	h.mgr.observe(ctx, st.ID, down)
	h.clk.Add(11 * time.Second)
	h.mgr.observe(ctx, st.ID, down)
	h.mgr.observe(ctx, st.ID, down)
	h.mgr.observe(ctx, st.ID, up)
	h.clk.Add(11 * time.Second)
	h.mgr.observe(ctx, st.ID, up)
	evs := h.reachabilityEvents(t)
	if len(evs) != 3 || evs[1].To != contracts.PeerUnreachable || evs[1].Reason != "connection refused" || evs[2].To != contracts.PeerConnected {
		t.Fatalf("events = %+v", evs)
	}
	// The recovery says which path it's on, as the first connect does.
	path, _ := h.mgr.pathStatus(contracts.Peer{Fingerprint: p.fp})
	if v := evs[2].Via; path == nil || v == nil || v.Addr != path.Addr || v.Addr != evs[0].Via.Addr {
		t.Fatalf("the recovery's via = %+v (path %+v)", v, path)
	}
}

func TestScopeAllMergesAndTagsHosts(t *testing.T) {
	h := newHarness(t)
	p := newFakePeer(t, "pop-os")
	p.sessions = []map[string]any{{"id": "ses_remote", "name": "there", "future_field": 7}}
	h.addPeer(t, p)

	code, body := get(t, h.srv.URL+"/sessions?scope=all&archived=1")
	if code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	var rows []struct {
		ID          string                `json:"id"`
		Host        contracts.SessionHost `json:"host"`
		FutureField int                   `json:"future_field"`
	}
	if err := json.Unmarshal([]byte(body), &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].ID != "ses_local" || !rows[0].Host.Self || rows[0].Host.Name != "mac" ||
		rows[1].ID != "ses_remote" || rows[1].Host.Name != "pop-os" || !rows[1].Host.Reachable || rows[1].FutureField != 7 {
		t.Fatalf("rows = %+v", rows)
	}
	// The peer got the same filter, minus scope, and no browser headers.
	if fw := p.forwarded(); len(fw) == 0 || fw[len(fw)-1] != "GET /sessions?archived=1" {
		t.Fatalf("peer saw %v", fw)
	}

	// Without scope=all, the local API answers alone, exactly as before.
	if _, body := get(t, h.srv.URL+"/sessions"); strings.Contains(body, "ses_remote") || strings.Contains(body, `"host"`) {
		t.Fatalf("plain GET /sessions changed: %s", body)
	}

	// A peer that goes away keeps its last-known rows, marked unreachable.
	p.Close()
	_, body = get(t, h.srv.URL+"/sessions?scope=all")
	if !strings.Contains(body, `"id":"ses_remote"`) || !strings.Contains(body, `"reachable":false`) {
		t.Fatalf("stale peer rows: %s", body)
	}
}

func TestSessionRoutesFollowTheSession(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	local := &contracts.Session{Name: "local one", Provider: "fake"}
	if err := h.data.CreateSession(ctx, local); err != nil {
		t.Fatal(err)
	}
	p := newFakePeer(t, "pop-os")
	// The peer claims the local session's id too: local must win.
	p.sessions = []map[string]any{{"id": "ses_remote"}, {"id": local.ID}}
	h.addPeer(t, p)
	h.mgr.RefreshIndex(ctx)

	code, body := get(t, h.srv.URL+"/sessions/ses_remote/transcript?limit=5")
	if code != 200 || !strings.Contains(body, `"peer":"pop-os"`) {
		t.Fatalf("remote session: %d %s", code, body)
	}
	if fw := p.forwarded(); fw[len(fw)-1] != "GET /sessions/ses_remote/transcript?limit=5" {
		t.Fatalf("peer saw %v", fw)
	}
	if _, body := get(t, h.srv.URL+"/sessions/"+local.ID+"/transcript"); !strings.Contains(body, `"served_by":"local"`) {
		t.Fatalf("a local session was routed away: %s", body)
	}
	for _, path := range []string{"/sessions/defaults", "/sessions", "/providers"} {
		if _, body := get(t, h.srv.URL+path); !strings.Contains(body, "local") && !strings.Contains(body, "here") {
			t.Errorf("%s should be served locally: %s", path, body)
		}
	}

	// A session created on the peer after the last index refresh is found
	// by asking the peers.
	p.mu.Lock()
	p.sessions = append(p.sessions, map[string]any{"id": "ses_brandnew"})
	p.mu.Unlock()
	if _, body := get(t, h.srv.URL+"/sessions/ses_brandnew"); !strings.Contains(body, "ses_brandnew") {
		t.Fatalf("unindexed peer session: %s", body)
	}
	if _, ok := h.mgr.Owner("ses_brandnew"); !ok {
		t.Fatal("resolved session wasn't indexed")
	}
}

func TestExplicitPeerProxy(t *testing.T) {
	h := newHarness(t)
	p := newFakePeer(t, "pop-os")
	h.addPeer(t, p)
	code, body := get(t, h.srv.URL+"/peers/pop-os/providers/claude-code/models?x=1")
	if code != 200 || !strings.Contains(body, `"path":"/providers/claude-code/models"`) {
		t.Fatalf("%d %s", code, body)
	}
	if code, _ := get(t, h.srv.URL+"/peers/nope/providers"); code != 404 {
		t.Fatalf("unknown peer: %d", code)
	}
}

func TestForwardedStreamsFlushPerEvent(t *testing.T) {
	h := newHarness(t)
	p := newFakePeer(t, "pop-os")
	p.sessions = []map[string]any{{"id": "ses_remote"}}
	h.addPeer(t, p)
	h.mgr.RefreshIndex(context.Background())

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, h.srv.URL+"/sessions/ses_remote/output?v=2", nil)
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Origin", "http://localhost:17373")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	line, err := bufio.NewReader(resp.Body).ReadString('\n')
	if err != nil || line != "data: hi\n" {
		t.Fatalf("first line %q, %v", line, err)
	}
}

func TestPeerListFollowsEvents(t *testing.T) {
	h := newHarness(t)
	p := newFakePeer(t, "pop-os")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go h.mgr.watchPeers(ctx)

	// Added behind the Router's back (another UI window, the CLI): the
	// peer.created event alone must bring it in.
	if err := h.data.CreatePeer(ctx, p.row("pop-os")); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for !h.mgr.HasPeers() {
		if time.Now().After(deadline) {
			t.Fatal("peer.created never reached the manager")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestReservedAndCandidates(t *testing.T) {
	h := newHarness(t)
	p := newFakePeer(t, "candidates")
	if code := send(t, http.MethodPost, h.srv.URL+"/peers", `{"mesh_url":"`+p.URL+`","fingerprint":"`+p.fp+`"}`); code != http.StatusUnprocessableEntity {
		t.Fatalf("a peer named candidates: %d", code)
	}

	pop := newFakePeer(t, "pop-os")
	h.addPeer(t, pop)
	h.mgr.statusSource = func(context.Context) ([]Candidate, error) {
		return []Candidate{
			{Name: "phone", DNSName: "phone.ts.net", Online: false},
			{Name: "spark", DNSName: "spark.ts.net", Online: true},
			{Name: "pop-os", DNSName: "pop-os.ts.net", Online: true},
		}, nil
	}
	code, body := get(t, h.srv.URL+"/peers/candidates")
	var list []Candidate
	_ = json.Unmarshal([]byte(body), &list)
	by := map[string]Candidate{}
	for _, c := range list {
		by[c.Name] = c
	}
	// Candidates come from the tailnet's status alone: nothing is dialled
	// before its key is pinned, so an online, unadded device is unpaired;
	// an added one shows its live state.
	if code != 200 || by["phone"].Reason != "offline" || by["spark"].Reachability != contracts.PeerUnpaired ||
		!by["pop-os"].Added || by["pop-os"].Reachability != contracts.PeerConnected {
		t.Fatalf("candidates: %d %s", code, body)
	}
}

// testFingerprint is a valid device fingerprint (pkg/devicekey).
func testFingerprint(seed byte) string {
	return string(devicekey.FingerprintOf(ed25519.PublicKey(bytes.Repeat([]byte{seed}, ed25519.PublicKeySize))))
}

// A paired-only device (a phone pinned with `mads pair`) isn't one this
// node views: the Router neither lists, probes nor routes to it.
func TestPairedOnlyDevicesAreNotViewed(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	no := false
	if err := h.data.CreatePeer(ctx, &contracts.Peer{Name: "phone", Viewed: &no, Fingerprint: testFingerprint(1),
		Grant: &contracts.PeerGrant{Level: contracts.GrantChat}}); err != nil {
		t.Fatal(err)
	}
	if err := h.mgr.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	if h.mgr.HasPeers() || len(h.mgr.Statuses()) != 0 {
		t.Fatalf("a paired-only device is viewed: %+v", h.mgr.Statuses())
	}
	if code, _ := get(t, h.srv.URL+"/peers/phone"); code != http.StatusNotFound {
		t.Fatalf("GET /peers/phone = %d, want 404", code)
	}
}

// Removing a paired device from "devices I view" stops viewing it but
// keeps the pairing; an unpaired one is deleted as before.
func TestRemovingAPairedViewedDeviceKeepsThePairing(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	st := h.addPeer(t, newFakePeer(t, "pop-os"))
	p, err := h.data.Peer(ctx, st.ID)
	if err != nil {
		t.Fatal(err)
	}
	p.Fingerprint = testFingerprint(2)
	p.Grant = &contracts.PeerGrant{Level: contracts.GrantControl}
	if err := h.data.UpdatePeer(ctx, &p); err != nil {
		t.Fatal(err)
	}
	_ = h.mgr.Reload(ctx)
	req, _ := http.NewRequest(http.MethodDelete, h.srv.URL+"/peers/pop-os", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("DELETE = %d", resp.StatusCode)
	}
	got, err := h.data.Peer(ctx, st.ID)
	if err != nil {
		t.Fatalf("the paired row went: %v", err)
	}
	if got.IsViewed() || got.Fingerprint != p.Fingerprint || got.Level() != contracts.GrantControl {
		t.Fatalf("after remove: %+v", got)
	}
	if h.mgr.HasPeers() {
		t.Fatal("still viewed")
	}
}

func send(t *testing.T, method, url, body string) int {
	t.Helper()
	req, _ := http.NewRequest(method, url, strings.NewReader(body))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}

// The app's edits go to the Data Service's current row, not this Router's
// copy: a revoke or a pairing made since the Router last reloaded is never
// undone by a hide toggle or a remove.
func TestPeerEditsWhileTheCacheIsStale(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	st := h.addPeer(t, newFakePeer(t, "pop-os"))
	// Adding pinned the device's key (a device.paired by the Router); from
	// here on, the app's edits must write none.
	added, err := h.data.LatestSeq(ctx, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	p, _ := h.data.Peer(ctx, st.ID)
	p.Fingerprint, p.Grant = testFingerprint(3), &contracts.PeerGrant{Level: contracts.GrantControl}
	if err := h.data.UpdatePeer(ctx, &p); err != nil {
		t.Fatal(err)
	}
	_ = h.mgr.Reload(ctx) // the Router's copy is paired at control

	// `mads pair --revoke` meanwhile; the Router hasn't reloaded.
	p, _ = h.data.Peer(ctx, st.ID)
	p.Fingerprint, p.Grant, p.Pairing = "", nil, nil
	if err := h.data.UpdatePeer(ctx, &p); err != nil {
		t.Fatal(err)
	}
	if code := send(t, http.MethodPut, h.srv.URL+"/peers/pop-os", `{"hidden":true}`); code != http.StatusOK {
		t.Fatalf("hide: %d", code)
	}
	got, _ := h.data.Peer(ctx, st.ID)
	if got.IsPaired() || got.Grant != nil || !got.Hidden {
		t.Fatalf("the hide toggle re-pinned a revoked device: %+v", got)
	}

	// `mads pair --accept` meanwhile; then the app removes the device
	// before the Router reloads: the fresh pairing survives.
	got.Fingerprint, got.Grant = testFingerprint(4), &contracts.PeerGrant{Level: contracts.GrantView}
	if err := h.data.UpdatePeer(ctx, &got); err != nil {
		t.Fatal(err)
	}
	if code := send(t, http.MethodDelete, h.srv.URL+"/peers/pop-os", ""); code != http.StatusNoContent {
		t.Fatalf("remove: %d", code)
	}
	after, err := h.data.Peer(ctx, st.ID)
	if err != nil || after.Fingerprint != testFingerprint(4) || after.IsViewed() {
		t.Fatalf("remove deleted a fresh pairing: %+v, %v", after, err)
	}
	evs, _ := h.data.Events(ctx, added, "", []string{"device.*"})
	for _, e := range evs {
		if e.Actor == "router" {
			t.Fatalf("the Router wrote a device event: %+v", e)
		}
	}
}

// Adding a device the app doesn't view yet, but that's paired here under
// the same name, gives that row its viewed side instead of a name clash —
// only when the device proves, in the TLS handshake, the key the row pins.
// A device merely naming itself like the row is refused, and the row keeps
// its key and grant.
func TestAddingAPairedOnlyDeviceViewsIt(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	genuine := newFakePeer(t, "pop-os")
	no := false
	row := &contracts.Peer{Name: "pop-os", Viewed: &no, Fingerprint: genuine.fp,
		Grant: &contracts.PeerGrant{Level: contracts.GrantChat}}
	if err := h.data.CreatePeer(ctx, row); err != nil {
		t.Fatal(err)
	}
	impostor := newFakePeer(t, "pop-os")
	for name, body := range map[string]string{
		// The row's pin is used, and the impostor can't prove it.
		"by name": `{"name":"pop-os","mesh_url":"` + impostor.URL + `"}`,
		// Its own key verifies, but that's another device: a name clash.
		"with its own key": `{"mesh_url":"` + impostor.URL + `","fingerprint":"` + impostor.fp + `"}`,
	} {
		if code := send(t, http.MethodPost, h.srv.URL+"/peers", body); code != http.StatusUnprocessableEntity {
			t.Fatalf("an impostor %s: %d, want refused", name, code)
		}
		got, _ := h.data.Peer(ctx, row.ID)
		if got.IsViewed() || got.MeshURL != "" || got.Fingerprint != row.Fingerprint || got.Level() != contracts.GrantChat {
			t.Fatalf("an impostor %s was glued to the paired row: %+v", name, got)
		}
	}
	if code := send(t, http.MethodPost, h.srv.URL+"/peers", `{"name":"pop-os","mesh_url":"`+genuine.URL+`"}`); code != http.StatusCreated {
		t.Fatalf("the genuine device by name: %d", code)
	}
	got, err := h.data.Peer(ctx, row.ID)
	if err != nil || !got.IsViewed() || got.MeshURL != genuine.meshURL() || got.Fingerprint != genuine.fp || got.Level() != contracts.GrantChat {
		t.Fatalf("after add: %+v, %v", got, err)
	}
	if list, _ := h.data.Peers(ctx); len(list) != 1 {
		t.Fatalf("rows = %d, want the one", len(list))
	}
}

// Q2 end to end through this Router: a request a peer's Mesh Service
// relayed (its marker present, even empty) can't make the Data Service
// grant admin, whatever Router handler writes it; the same write from the
// host's own user can. The handler here is a test stand-in for a peer
// management route: no production mesh route reaches /peers.
func TestMeshRelayedWritesNeverGrantAdmin(t *testing.T) {
	g := datatest.StartGuarded(t)
	data := dataclient.New(g.URL, "router", dataclient.WithToken(g.Token))
	mgr := NewManager(data, Options{AllowLoopback: true, SelfName: "mac"})
	seed := byte(10)
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		no := false
		seed++
		err := data.CreatePeer(r.Context(), &contracts.Peer{Name: fmt.Sprintf("laptop-%d", seed), Viewed: &no,
			Fingerprint: testFingerprint(seed), Grant: &contracts.PeerGrant{Level: contracts.GrantAdmin}})
		if err != nil {
			http.Error(w, err.Error(), http.StatusForbidden)
			return
		}
		w.WriteHeader(http.StatusCreated)
	})
	srv := httptest.NewServer(mgr.Handler(next))
	t.Cleanup(srv.Close)
	for name, hdr := range map[string][]string{"named": {"mac.example.ts.net"}, "empty": {""}} {
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/test/grant-admin", nil)
		req.Header[contracts.MeshRelayHeader] = hdr
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		var b strings.Builder
		_, _ = bufio.NewReader(resp.Body).WriteTo(&b)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden || !strings.Contains(b.String(), "403") {
			t.Fatalf("%s marker: %d %s, want the Data Service's 403", name, resp.StatusCode, b.String())
		}
	}
	if code := send(t, http.MethodPost, srv.URL+"/test/grant-admin", ""); code != http.StatusCreated {
		t.Fatalf("the host's own write: %d", code)
	}
}

// watchPeers starts following at the true newest peer.* event (past the
// Data Service's batch cap), and a stream that closes at once backs off
// rather than spinning.
func TestWatchPeersCursorAndBackoff(t *testing.T) {
	base := datatest.Start(t)
	writer := dataclient.New(base, "test")
	ctx := context.Background()
	// 520 peer.updated, written as the store writes them (POST /events
	// refuses registry events).
	row := &contracts.Peer{Name: "x", MeshURL: "http://x:1"}
	if err := writer.CreatePeer(ctx, row); err != nil {
		t.Fatal(err)
	}
	for i := range 520 {
		row.Hidden = i%2 == 0
		if err := writer.UpdatePeer(ctx, row); err != nil {
			t.Fatal(err)
		}
	}
	target, _ := url.Parse(base)
	proxy := httputil.NewSingleHostReverseProxy(target)
	var lists, streams atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/peers":
			lists.Add(1)
		case r.URL.Path == "/events" && strings.Contains(r.Header.Get("Accept"), "text/event-stream"):
			streams.Add(1)
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			return // open, then close at once
		}
		proxy.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	mgr := NewManager(dataclient.New(srv.URL, "router"), Options{ProbeInterval: time.Hour, IndexInterval: time.Hour})
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { defer close(done); mgr.watchPeers(runCtx) }()
	time.Sleep(2500 * time.Millisecond)
	cancel()
	<-done
	// Backing off 1s, 2s: opens at about 0s, 1s and 3s.
	if n := streams.Load(); n < 1 || n > 3 {
		t.Fatalf("%d stream opens in 2.5s, want a backed-off few", n)
	}
	// One list per (re)subscription, not one per replayed old event.
	if n, s := lists.Load(), streams.Load(); n != s {
		t.Fatalf("GET /peers %d times for %d subscriptions: old events were replayed", n, s)
	}
}

// A stream that held a while before ending (a rotation, a Data Service
// restart) resets watchPeers' backoff; only instant closes grow it.
func TestWatchPeersHealthyStreamEndingResetsTheBackoff(t *testing.T) {
	oldMin, oldMax, oldHealthy := watchBackoffMin, watchBackoffMax, watchHealthyAfter
	watchBackoffMin, watchBackoffMax, watchHealthyAfter = 10*time.Millisecond, time.Second, 50*time.Millisecond
	t.Cleanup(func() { watchBackoffMin, watchBackoffMax, watchHealthyAfter = oldMin, oldMax, oldHealthy })
	target, _ := url.Parse(datatest.Start(t))
	proxy := httputil.NewSingleHostReverseProxy(target)
	opens := &openTimes{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/events" && strings.Contains(r.Header.Get("Accept"), "text/event-stream") {
			opens.add()
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			w.(http.Flusher).Flush()
			select {
			case <-time.After(60 * time.Millisecond):
			case <-r.Context().Done():
			}
			return
		}
		proxy.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	mgr := NewManager(dataclient.New(srv.URL, "router"), Options{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); mgr.watchPeers(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	deadline := time.Now().Add(10 * time.Second)
	for opens.count() < 8 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	opens.assertSteady(t, 8)
}

// openTimes records when each stream was opened.
type openTimes struct {
	mu sync.Mutex
	at []time.Time
}

func (o *openTimes) add()       { o.mu.Lock(); o.at = append(o.at, time.Now()); o.mu.Unlock() }
func (o *openTimes) count() int { o.mu.Lock(); defer o.mu.Unlock(); return len(o.at) }

// assertSteady fails unless n opens came with no gap past 300ms: reset
// each time, a round is the 60ms hold plus the 10ms minimum; growing, the
// waits double to 640ms by the eighth.
func (o *openTimes) assertSteady(t *testing.T, n int) {
	t.Helper()
	o.mu.Lock()
	defer o.mu.Unlock()
	if len(o.at) < n {
		t.Fatalf("%d opens, want %d", len(o.at), n)
	}
	for i := 1; i < n; i++ {
		if gap := o.at[i].Sub(o.at[i-1]); gap > 300*time.Millisecond {
			t.Fatalf("open %d came %s after the last: the backoff grew across healthy streams", i, gap)
		}
	}
}
