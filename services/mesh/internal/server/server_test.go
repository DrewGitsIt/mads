package server

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/devicekey"
	"github.com/drewkidwell/mads/services/mesh/internal/tailnet"
)

const owner = 42

// fakeTailnet answers WhoIs from a table keyed by the request's remote IP.
type fakeTailnet struct {
	mu      sync.Mutex
	callers map[string]tailnet.Caller
	calls   int
	// fail, when set, is every WhoIs's error (a tailscaled in trouble);
	// hang makes every WhoIs wait for its context instead.
	fail error
	hang bool
}

func (f *fakeTailnet) WhoIs(ctx context.Context, remoteAddr string) (tailnet.Caller, error) {
	f.mu.Lock()
	hang := f.hang
	f.mu.Unlock()
	if hang {
		<-ctx.Done()
		return tailnet.Caller{}, ctx.Err()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.fail != nil {
		return tailnet.Caller{}, f.fail
	}
	ip := remoteAddr[:strings.LastIndex(remoteAddr, ":")]
	c, ok := f.callers[ip]
	if !ok {
		return tailnet.Caller{}, tailnet.ErrNotFound
	}
	return c, nil
}

func (f *fakeTailnet) set(ip string, c tailnet.Caller) {
	f.mu.Lock()
	f.callers[ip] = c
	f.mu.Unlock()
}

var (
	full     = tailnet.Caller{NodeName: "mac.ts.net", UserID: owner, HasCap: true, Access: []string{"control"}}
	viewOnly = tailnet.Caller{NodeName: "phone.ts.net", UserID: owner, HasCap: true, Access: []string{"view"}}
	noCap    = tailnet.Caller{NodeName: "spark.ts.net", UserID: owner}
	stranger = tailnet.Caller{NodeName: "friend.ts.net", UserID: 7, HasCap: true, Access: []string{"view"}}
	tagged   = tailnet.Caller{NodeName: "ci.ts.net", UserID: owner, Tags: []string{"tag:ci"}, HasCap: true, Access: []string{"view"}}
)

// fakeDevices is the paired directory.
type fakeDevices struct {
	mu     sync.Mutex
	byFP   map[string]contracts.Peer
	loaded bool
}

func (d *fakeDevices) Lookup(fp string) (contracts.Peer, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	p, ok := d.byFP[fp]
	return p, ok
}

func (d *fakeDevices) Loaded() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.loaded
}

// pair pins id here as name with level (GrantNone: paired, no grant) and
// ceilings.
func (d *fakeDevices) pair(id *devicekey.Identity, name string, level contracts.GrantLevel, ceil contracts.PeerCeilings) {
	p := contracts.Peer{ID: "per_" + name, Name: name, Fingerprint: string(id.Fingerprint())}
	if level != contracts.GrantNone {
		p.Grant = &contracts.PeerGrant{Level: level, Ceilings: ceil}
	}
	d.mu.Lock()
	d.byFP[p.Fingerprint] = p
	d.mu.Unlock()
}

func (d *fakeDevices) unpair(id *devicekey.Identity) {
	d.mu.Lock()
	delete(d.byFP, string(id.Fingerprint()))
	d.mu.Unlock()
}

// fakeAudit records appended events.
type fakeAudit struct {
	mu  sync.Mutex
	evs []contracts.DeviceRefusal
}

func (a *fakeAudit) Append(_ context.Context, env *contracts.Envelope) error {
	var r contracts.DeviceRefusal
	_ = json.Unmarshal(env.Payload, &r)
	a.mu.Lock()
	a.evs = append(a.evs, r)
	a.mu.Unlock()
	return nil
}

func (a *fakeAudit) events() []contracts.DeviceRefusal {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]contracts.DeviceRefusal(nil), a.evs...)
}

// seenRequest is what the fake Router saw.
type seenRequest struct {
	Method, Path, Host string
	Header             http.Header
	Body               string
}

// Devices of the fixture, by role.
type devices struct {
	host, mac, phone, chatter, idle, stranger *devicekey.Identity
}

func newDevices(t *testing.T) devices {
	t.Helper()
	gen := func() *devicekey.Identity {
		id, err := devicekey.Generate()
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	return devices{gen(), gen(), gen(), gen(), gen(), gen()}
}

type fixture struct {
	srv   *Server
	tn    *fakeTailnet
	dirs  *fakeDevices
	audit *fakeAudit
	ids   devices
	home  string

	mu   sync.Mutex
	seen []seenRequest
	// trust is each session's trust on the fake Router; defaultTrust what
	// its GET /sessions/defaults resolves.
	trust        map[string]contracts.SessionTrust
	defaultTrust contracts.SessionTrust
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{
		tn: &fakeTailnet{callers: map[string]tailnet.Caller{
			"100.0.0.1": full, "100.0.0.2": viewOnly, "100.0.0.3": noCap,
			"100.0.0.4": stranger, "100.0.0.5": tagged,
		}},
		dirs:         &fakeDevices{byFP: map[string]contracts.Peer{}, loaded: true},
		audit:        &fakeAudit{},
		ids:          newDevices(t),
		trust:        map[string]contracts.SessionTrust{"ses_full": contracts.TrustFull, "ses_ltd": contracts.TrustLimited},
		defaultTrust: contracts.TrustLimited,
	}
	f.dirs.pair(f.ids.mac, "mac", contracts.GrantControl, contracts.PeerCeilings{})
	f.dirs.pair(f.ids.phone, "phone", contracts.GrantView, contracts.PeerCeilings{})
	f.dirs.pair(f.ids.chatter, "chatter", contracts.GrantChat, contracts.PeerCeilings{})
	f.dirs.pair(f.ids.idle, "idle", contracts.GrantNone, contracts.PeerCeilings{})

	router := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.seen = append(f.seen, seenRequest{r.Method, r.URL.RequestURI(), r.Host, r.Header.Clone(), string(body)})
		trust, def := f.trust, f.defaultTrust
		f.mu.Unlock()
		switch {
		case r.URL.Path == "/events":
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("data: one\n\n"))
			w.(http.Flusher).Flush()
			<-r.Context().Done() // a live stream: never finishes on its own
			return
		case r.URL.Path == "/sessions/defaults":
			writeJSON(w, http.StatusOK, contracts.SessionDefaults{Trust: contracts.ResolvedSessionField{Value: string(def)}})
			return
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/sessions/") && strings.Count(r.URL.Path, "/") == 2:
			id := strings.TrimPrefix(r.URL.Path, "/sessions/")
			t, ok := trust[id]
			if !ok {
				http.NotFound(w, r)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"id": id, "trust": t})
			return
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(router.Close)
	routerURL, _ := url.Parse(router.URL)

	f.home = t.TempDir()
	fsys, err := NewFS(f.home)
	if err != nil {
		t.Fatal(err)
	}
	f.srv = New(Options{
		Self:           tailnet.Self{HostName: "pop-os", DNSName: "pop-os.ts.net", UserID: owner, IPv4: "100.0.0.9"},
		Tailnet:        f.tn,
		RequireTailnet: true,
		Devices:        f.dirs,
		Audit:          f.audit,
		RouterURL:      routerURL,
		Commit:         "abc123",
		FS:             fsys,
		Device:         f.ids.host.Fingerprint(),
	})
	return f
}

// tlsState is the connection state of a handshake in which id presented
// its device certificate.
func tlsState(t *testing.T, id *devicekey.Identity) *tls.ConnectionState {
	t.Helper()
	if id == nil {
		return &tls.ConnectionState{}
	}
	cert, err := id.Certificate()
	if err != nil {
		t.Fatal(err)
	}
	return &tls.ConnectionState{Version: tls.VersionTLS13, PeerCertificates: []*x509.Certificate{cert.Leaf}}
}

type req struct {
	from   string              // remote IP
	as     *devicekey.Identity // the client certificate's device; nil: none
	plain  bool                // no TLS at all
	method string
	target string
	header map[string]string
	body   string
}

func (f *fixture) serve(t *testing.T, q req) *httptest.ResponseRecorder {
	t.Helper()
	var body io.Reader
	if q.body != "" {
		body = strings.NewReader(q.body)
	}
	r := httptest.NewRequest(q.method, q.target, body)
	r.RemoteAddr = q.from + ":41000"
	if !q.plain {
		r.TLS = tlsState(t, q.as)
	}
	for k, v := range q.header {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	f.srv.ServeHTTP(w, r)
	return w
}

// do is a request from the mac (control, tailnet 100.0.0.1) unless as says
// otherwise.
func (f *fixture) do(t *testing.T, from, method, target string, header map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	return f.serve(t, req{from: from, as: f.deviceFor(from), method: method, target: target, header: header})
}

// deviceFor maps the fixture's tailnet addresses to a paired device.
func (f *fixture) deviceFor(from string) *devicekey.Identity {
	if from == "100.0.0.2" {
		return f.ids.phone
	}
	return f.ids.mac
}

func (f *fixture) seenRequests() []seenRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]seenRequest(nil), f.seen...)
}

func errorCode(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var e apiError
	_ = json.Unmarshal(w.Body.Bytes(), &e)
	return e.Code
}

func TestGateRefusals(t *testing.T) {
	f := newFixture(t)
	ids := f.ids
	for _, tc := range []struct {
		name string
		q    req
		code string
		want int
	}{
		{"browser Origin", req{from: "100.0.0.1", as: ids.mac, method: "GET", target: "/sessions", header: map[string]string{"Origin": "http://localhost:17373"}}, "browser_request", 403},
		{"browser Sec-Fetch", req{from: "100.0.0.1", as: ids.mac, method: "GET", target: "/sessions", header: map[string]string{"Sec-Fetch-Site": "cross-site"}}, "browser_request", 403},
		{"plaintext", req{from: "100.0.0.1", plain: true, method: "GET", target: "/sessions"}, "no_client_certificate", 403},
		{"no client certificate", req{from: "100.0.0.1", method: "GET", target: "/sessions"}, "no_client_certificate", 403},
		{"unpaired key", req{from: "100.0.0.1", as: ids.stranger, method: "GET", target: "/sessions"}, "unpaired_device", 403},
		{"unpaired key, hello", req{from: "100.0.0.1", as: ids.stranger, method: "GET", target: "/mesh/hello"}, "unpaired_device", 403},
		{"paired, not on the tailnet", req{from: "192.0.2.1", as: ids.mac, method: "GET", target: "/sessions"}, "not_tailnet", 403},
		{"paired, another user's node", req{from: "100.0.0.4", as: ids.mac, method: "GET", target: "/sessions"}, "other_user", 403},
		{"paired, tagged node", req{from: "100.0.0.5", as: ids.mac, method: "GET", target: "/sessions"}, "tagged_node", 403},
		{"paired, no capability", req{from: "100.0.0.3", as: ids.mac, method: "GET", target: "/sessions"}, "no_capability", 403},
		{"paired, granted nothing", req{from: "100.0.0.1", as: ids.idle, method: "GET", target: "/sessions"}, "no_access", 403},
		{"denied route", req{from: "100.0.0.1", as: ids.mac, method: "GET", target: "/credentials"}, "not_exposed", 403},
		{"host settings", req{from: "100.0.0.1", as: ids.mac, method: "PUT", target: "/config"}, "not_exposed", 403},
		{"no /peers over the mesh", req{from: "100.0.0.1", as: ids.mac, method: "GET", target: "/peers"}, "not_exposed", 403},
		{"static UI", req{from: "100.0.0.1", as: ids.mac, method: "GET", target: "/"}, "not_exposed", 403},
		{"unknown route", req{from: "100.0.0.1", as: ids.mac, method: "GET", target: "/nope"}, "not_exposed", 403},
		{"view can't chat", req{from: "100.0.0.1", as: ids.phone, method: "POST", target: "/sessions/ses_1/messages"}, "no_access", 403},
		{"view can't land", req{from: "100.0.0.1", as: ids.phone, method: "POST", target: "/sessions/ses_1/land"}, "no_access", 403},
		{"chat can't create", req{from: "100.0.0.1", as: ids.chatter, method: "POST", target: "/sessions"}, "no_access", 403},
		{"chat can't raise trust", req{from: "100.0.0.1", as: ids.chatter, method: "POST", target: "/sessions/ses_1/trust"}, "no_access", 403},
		{"chat can't type into the terminal", req{from: "100.0.0.1", as: ids.chatter, method: "POST", target: "/sessions/ses_1/input"}, "no_access", 403},
		{"chat can't archive", req{from: "100.0.0.1", as: ids.chatter, method: "DELETE", target: "/sessions/ses_1"}, "no_access", 403},
		{"chat can't browse folders", req{from: "100.0.0.1", as: ids.chatter, method: "GET", target: "/mesh/fs/list"}, "no_access", 403},
		// The tailnet capability caps the grant: the mac's key holds
		// control, but from the phone's node (capability view) it's view.
		{"capability below the grant", req{from: "100.0.0.2", as: ids.mac, method: "POST", target: "/sessions/ses_1/messages"}, "no_access", 403},
		{"unknown mesh route", req{from: "100.0.0.1", as: ids.mac, method: "GET", target: "/mesh/nope"}, "not_found", 404},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := f.serve(t, tc.q)
			if w.Code != tc.want || errorCode(t, w) != tc.code {
				t.Fatalf("got %d %q (%s), want %d %q", w.Code, errorCode(t, w), w.Body, tc.want, tc.code)
			}
		})
	}
	if seen := f.seenRequests(); len(seen) != 0 {
		t.Fatalf("refused requests must never reach the Router, but it saw %+v", seen)
	}
}

func TestDevicesNotLoadedRefusesEveryone(t *testing.T) {
	f := newFixture(t)
	f.dirs.mu.Lock()
	f.dirs.loaded = false
	f.dirs.mu.Unlock()
	if w := f.do(t, "100.0.0.1", "GET", "/sessions", nil); w.Code != http.StatusServiceUnavailable || errorCode(t, w) != "devices_unavailable" {
		t.Fatalf("before the paired list loads: %d %s", w.Code, w.Body)
	}
}

// Each level reaches its own routes and the ones below; with the tailnet
// off as a second factor, the grant alone decides.
func TestLevelsAdmit(t *testing.T) {
	f := newFixture(t)
	f.srv.SetRequireTailnet(false)
	for _, tc := range []struct {
		as             *devicekey.Identity
		method, target string
		ok             bool
	}{
		{f.ids.phone, "GET", "/sessions/ses_1/transcript", true},
		{f.ids.phone, "POST", "/sessions/ses_1/interrupt", false},
		{f.ids.chatter, "GET", "/sessions/ses_1/transcript", true},
		{f.ids.chatter, "POST", "/sessions/ses_ltd/messages", true},
		{f.ids.chatter, "POST", "/sessions/ses_1/permission-requests/r1/resolve", true},
		{f.ids.chatter, "POST", "/sessions/ses_1/interrupt", true},
		{f.ids.chatter, "POST", "/sessions/ses_1/rename", false},
		{f.ids.mac, "POST", "/sessions/ses_1/rename", true},
		{f.ids.mac, "POST", "/sessions/ses_1/land", true},
		{f.ids.mac, "POST", "/sessions/ses_1/input", true},
		// Off the tailnet entirely: the key is enough when it isn't required.
		{f.ids.mac, "GET", "/sessions", true},
	} {
		w := f.serve(t, req{from: "192.0.2.1", as: tc.as, method: tc.method, target: tc.target})
		if (w.Code == 200) != tc.ok {
			t.Errorf("%s %s as %s: %d %s, want ok=%v", tc.method, tc.target, tc.as.Fingerprint().Short(), w.Code, w.Body, tc.ok)
		}
	}
	if f.tn.calls != 0 {
		t.Fatalf("WhoIs was called %d times with -require-tailnet off", f.tn.calls)
	}
}

// The relay marker reaches the Router for every forward, even for a device
// whose name is blank: the Router and the Data Service go by its presence
// (contracts.MeshRelayHeader), so an empty name must not drop it.
func TestForwardMarksEveryRelayEvenWithoutAName(t *testing.T) {
	f := newFixture(t)
	nameless, _ := devicekey.Generate()
	f.dirs.pair(nameless, "", contracts.GrantView, contracts.PeerCeilings{})
	if w := f.serve(t, req{from: "100.0.0.1", as: nameless, method: "GET", target: "/sessions"}); w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	if PeerHeader != contracts.MeshRelayHeader {
		t.Fatalf("PeerHeader %q isn't the marker the Router reads (%q)", PeerHeader, contracts.MeshRelayHeader)
	}
	if _, ok := f.seenRequests()[0].Header[http.CanonicalHeaderKey(PeerHeader)]; !ok {
		t.Fatalf("%s didn't reach the Router for a nameless device", PeerHeader)
	}
}

// The authenticated device is what the Router sees: its name as the relay
// mark, its effective level, and its actor (D8) — never what the caller
// claimed under those names.
func TestForwardRewritesForLocalguardAndStampsTheDevice(t *testing.T) {
	f := newFixture(t)
	w := f.do(t, "100.0.0.1", "POST", "/sessions/ses_1/rename?x=1", map[string]string{
		PeerHeader:                "spoofed", // the caller can't name itself
		"X-Mads-Actor":            "user",    // nor pose as the host's user
		contracts.MeshLevelHeader: "admin",   // nor raise its level
		"Referer":                 "http://evil.example/",
		"Cookie":                  "a=b",
		"X-Other":                 "kept",
	})
	if w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	got := f.seenRequests()[0]
	if got.Path != "/sessions/ses_1/rename?x=1" || got.Method != "POST" {
		t.Fatalf("forwarded %s %s", got.Method, got.Path)
	}
	if !strings.HasPrefix(got.Host, "127.0.0.1:") {
		t.Fatalf("Host = %q, want the Router's loopback address", got.Host)
	}
	if v := got.Header.Values(PeerHeader); len(v) != 1 || v[0] != "mac" {
		t.Fatalf("%s = %q, want the paired device's name", PeerHeader, v)
	}
	if v, want := got.Header.Values("X-Mads-Actor"), "device:mac/"+f.ids.mac.Fingerprint().Short(); len(v) != 1 || v[0] != want {
		t.Fatalf("X-Mads-Actor = %q, want %q", v, want)
	}
	if v := got.Header.Values(contracts.MeshLevelHeader); len(v) != 1 || v[0] != "control" {
		t.Fatalf("%s = %q, want the effective level", contracts.MeshLevelHeader, v)
	}
	for _, h := range []string{"Origin", "Referer", "Cookie", "X-Forwarded-For"} {
		if v := got.Header.Get(h); v != "" {
			t.Errorf("%s reached the Router: %q", h, v)
		}
	}
	if got.Header.Get("X-Other") != "kept" {
		t.Error("ordinary headers must pass through")
	}
}

// meshClient is a real TLS client for device id against a server pinned
// as host, counting the TLS handshakes its connections make.
func meshClient(t *testing.T, id, host *devicekey.Identity) *http.Client {
	t.Helper()
	cfg, err := devicekey.ClientConfig(id, host.Fingerprint())
	if err != nil {
		t.Fatal(err)
	}
	tr := &http.Transport{TLSClientConfig: cfg, ForceAttemptHTTP2: true}
	t.Cleanup(tr.CloseIdleConnections)
	return &http.Client{Transport: tr}
}

// listen serves the fixture's gate over real TLS on loopback.
func (f *fixture) listen(t *testing.T) *httptest.Server {
	t.Helper()
	f.tn.set("127.0.0.1", full)
	cfg, err := devicekey.ServerConfig(f.ids.host)
	if err != nil {
		t.Fatal(err)
	}
	front := httptest.NewUnstartedServer(f.srv)
	front.TLS = cfg
	front.EnableHTTP2 = true
	front.StartTLS()
	t.Cleanup(front.Close)
	return front
}

// liveStream is an open GET /events: its first line, and done closed when
// the stream ends.
type liveStream struct {
	first string
	done  chan struct{}
}

func openEvents(t *testing.T, c *http.Client, base string) *liveStream {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	r, _ := http.NewRequestWithContext(ctx, "GET", base+"/events", nil)
	r.Header.Set("Accept", "text/event-stream")
	resp, err := c.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	if resp.StatusCode != 200 {
		t.Fatalf("GET /events: %d", resp.StatusCode)
	}
	rd := bufio.NewReader(resp.Body)
	s := &liveStream{done: make(chan struct{})}
	if s.first, err = rd.ReadString('\n'); err != nil {
		t.Fatalf("no event before the stream ended: %v", err)
	}
	go func() {
		defer close(s.done)
		for {
			if _, err := rd.ReadString('\n'); err != nil {
				return
			}
		}
	}()
	return s
}

// endsWithin reports whether the stream ends within d.
func (s *liveStream) endsWithin(d time.Duration) bool {
	select {
	case <-s.done:
		return true
	case <-time.After(d):
		return false
	}
}

// SSE must arrive per event, not when the stream ends: a real TLS round
// trip, reading the first event while the Router still holds it open.
func TestEventStreamIsFlushedPerEvent(t *testing.T) {
	f := newFixture(t)
	front := f.listen(t)
	if s := openEvents(t, meshClient(t, f.ids.mac, f.ids.host), front.URL); s.first != "data: one\n" {
		t.Fatalf("first line %q", s.first)
	}
}

// Unpairing a device ends its open streams at once (not only its next
// request), and its next request — on the same kept-alive connection —
// is refused.
func TestRevokedMidStreamEndsTheStream(t *testing.T) {
	f := newFixture(t)
	front := f.listen(t)
	c := meshClient(t, f.ids.mac, f.ids.host)
	mine := openEvents(t, c, front.URL)
	// Another device's stream is untouched by the mac's revocation.
	other := openEvents(t, meshClient(t, f.ids.phone, f.ids.host), front.URL)

	f.dirs.unpair(f.ids.mac)
	f.srv.Revalidate()
	if !mine.endsWithin(3 * time.Second) {
		t.Fatal("the revoked device's /events stream is still open")
	}
	resp, err := c.Get(front.URL + "/sessions")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != 403 || !strings.Contains(string(b), "unpaired_device") {
		t.Fatalf("after revocation: %d %s", resp.StatusCode, b)
	}
	if other.endsWithin(300 * time.Millisecond) {
		t.Fatal("another device's stream ended with the mac's revocation")
	}
}

// A grant change — a downgrade, or a ceiling added — ends the device's
// streams too; it reconnects under what it holds now.
func TestGrantChangeEndsStreams(t *testing.T) {
	for name, change := range map[string]func(f *fixture){
		"downgrade": func(f *fixture) {
			f.dirs.pair(f.ids.mac, "mac", contracts.GrantView, contracts.PeerCeilings{})
		},
		"ceiling added": func(f *fixture) {
			f.dirs.pair(f.ids.mac, "mac", contracts.GrantControl, contracts.PeerCeilings{NoShell: true})
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			front := f.listen(t)
			c := meshClient(t, f.ids.mac, f.ids.host)
			s := openEvents(t, c, front.URL)
			// A reload that changes nothing leaves the stream open.
			f.srv.Revalidate()
			if s.endsWithin(300 * time.Millisecond) {
				t.Fatal("a reload that changed nothing ended the stream")
			}
			change(f)
			f.srv.Revalidate()
			if !s.endsWithin(3 * time.Second) {
				t.Fatal("the stream outlived the grant that opened it")
			}
			// Reconnecting works, under the new grant.
			if again := openEvents(t, c, front.URL); again.first != "data: one\n" {
				t.Fatalf("reconnected stream: %q", again.first)
			}
		})
	}
}

func TestViewOnlyCanRead(t *testing.T) {
	f := newFixture(t)
	if w := f.do(t, "100.0.0.2", "GET", "/sessions/ses_1/transcript", nil); w.Code != 200 {
		t.Fatalf("view-only GET transcript: %d %s", w.Code, w.Body)
	}
}

func TestHello(t *testing.T) {
	f := newFixture(t)
	// A paired device always gets hello, even one granted nothing (it
	// learns it may do nothing here); the level is capped by the tailnet.
	for _, tc := range []struct {
		from string
		as   *devicekey.Identity
		want string
	}{
		{"100.0.0.1", f.ids.mac, "view,chat,control"},
		{"100.0.0.1", f.ids.chatter, "view,chat"},
		{"100.0.0.1", f.ids.idle, ""},
		{"100.0.0.2", f.ids.mac, "view"}, // capability view caps the grant
	} {
		w := f.serve(t, req{from: tc.from, as: tc.as, method: "GET", target: "/mesh/hello"})
		if w.Code != 200 {
			t.Fatalf("hello: %d %s", w.Code, w.Body)
		}
		var h Hello
		if err := json.Unmarshal(w.Body.Bytes(), &h); err != nil {
			t.Fatal(err)
		}
		want := tc.want
		if h.Node != "pop-os" || h.MeshAPI != MeshAPI || h.MadsCommit != "abc123" || strings.Join(h.Access, ",") != want || h.Device != string(f.ids.host.Fingerprint()) {
			t.Fatalf("hello = %+v, want access %q", h, want)
		}
	}
	if w := f.do(t, "100.0.0.4", "GET", "/mesh/hello", nil); w.Code != 403 {
		t.Fatalf("another user's node must not get hello, got %d", w.Code)
	}
}

func TestWhoIsIsCachedBriefly(t *testing.T) {
	f := newFixture(t)
	now := time.Unix(1000, 0)
	f.srv.opts.WhoIsTTL = 10 * time.Second
	f.srv.opts.Now = func() time.Time { return now }

	f.do(t, "100.0.0.1", "GET", "/sessions", nil)
	f.do(t, "100.0.0.1", "GET", "/sessions", nil)
	if f.tn.calls != 1 {
		t.Fatalf("WhoIs calls = %d, want 1 within the TTL", f.tn.calls)
	}
	// Revoke the capability; once the TTL passes the next request sees it.
	f.tn.set("100.0.0.1", noCap)
	now = now.Add(11 * time.Second)
	if w := f.do(t, "100.0.0.1", "GET", "/sessions", nil); w.Code != 403 {
		t.Fatalf("revoked capability still served after the TTL: %d", w.Code)
	}
}

func TestFSList(t *testing.T) {
	f := newFixture(t)
	mk := func(p string) {
		if err := os.MkdirAll(filepath.Join(f.home, p), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	mk("repo/mads")
	mk("Alpha")
	mk(".hidden")
	if err := os.WriteFile(filepath.Join(f.home, "file.txt"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	_ = os.Symlink(outside, filepath.Join(f.home, "escape"))
	_ = os.Symlink(filepath.Join(f.home, "repo"), filepath.Join(f.home, "repolink"))

	list := func(query string) (int, FSListing) {
		w := f.do(t, "100.0.0.1", "GET", "/mesh/fs/list"+query, nil)
		var l FSListing
		_ = json.Unmarshal(w.Body.Bytes(), &l)
		return w.Code, l
	}
	root := f.srv.opts.FS.Root

	code, l := list("")
	if code != 200 || l.Path != root || l.Parent != "" {
		t.Fatalf("root listing: %d %+v", code, l)
	}
	var names []string
	for _, e := range l.Entries {
		names = append(names, e.Name)
	}
	// Folders only, case-insensitive order, hidden and escaping links left out.
	if got := strings.Join(names, ","); got != "Alpha,repo,repolink" {
		t.Fatalf("entries = %s", got)
	}
	if l.Entries[2].Path != filepath.Join(root, "repo") {
		t.Fatalf("a link is listed under its resolved path, got %s", l.Entries[2].Path)
	}

	if _, l := list("?hidden=1"); len(l.Entries) != 4 {
		t.Fatalf("hidden=1 should include .hidden, got %+v", l.Entries)
	}
	if code, l := list("?path=" + url.QueryEscape(filepath.Join(root, "repo"))); code != 200 || l.Parent != root {
		t.Fatalf("subfolder: %d %+v", code, l)
	}
	for _, bad := range []string{outside, filepath.Join(root, "escape"), filepath.Join(root, "repo", "..", ".."), "relative/path"} {
		if code, _ := list("?path=" + url.QueryEscape(bad)); code == 200 {
			t.Errorf("path %q must be refused", bad)
		}
	}
	if code, _ := list("?path=" + url.QueryEscape(filepath.Join(root, "file.txt"))); code != 400 {
		t.Errorf("a file isn't a folder: got %d", code)
	}
}

// newTLSFront serves f's gate over TLS with cfg, counting connections.
func newTLSFront(t *testing.T, f *fixture, cfg *tls.Config, h2 bool, conns *atomic.Int32) *httptest.Server {
	t.Helper()
	front := httptest.NewUnstartedServer(f.srv)
	front.TLS = cfg
	front.EnableHTTP2 = h2
	front.Config.ConnState = func(_ net.Conn, st http.ConnState) {
		if st == http.StateNew {
			conns.Add(1)
		}
	}
	front.StartTLS()
	t.Cleanup(front.Close)
	return front
}

// The route table matches the cleaned path while the Router serves the
// path as sent, so any path where those could differ is refused; and C1
// reads the session id the way the Router will.
func TestUncleanPathsAreRefused(t *testing.T) {
	f := newFixture(t)
	f.dirs.pair(f.ids.chatter, "chatter", contracts.GrantChat, contracts.PeerCeilings{FullTrustChat: contracts.FullTrustChatRefused})
	for _, target := range []string{
		"/sessions/ses_ltd/../ses_full/messages",
		"/sessions/ses_ltd/./messages",
		"/sessions//ses_full/messages",
		"/sessions/ses_ltd%2F..%2Fses_full/messages",
		"/sessions/ses_ltd%2f..%2fses_full/messages",
	} {
		w := f.serve(t, req{from: "100.0.0.1", as: f.ids.chatter, method: "POST", target: target, body: `{"text":"hi"}`})
		if w.Code != http.StatusBadRequest || errorCode(t, w) != "bad_path" {
			t.Errorf("%s: %d %s, want 400 bad_path", target, w.Code, w.Body)
		}
	}
	if seen := f.seenRequests(); len(seen) != 0 {
		t.Fatalf("unclean paths reached the Router: %+v", seen)
	}
	// A trailing slash is kept as sent, not "cleaned" into another route.
	if w := f.do(t, "100.0.0.1", "GET", "/sessions/ses_1/", nil); errorCode(t, w) != "not_exposed" {
		t.Fatalf("trailing slash: %d %s", w.Code, w.Body)
	}
	for raw, want := range map[string]string{
		"/sessions/ses_full/messages":   "ses_full",
		"/sessions/ses%5Ffull/messages": "ses_full", // unescaped, as ServeMux does
		"/sessions/ses_full":            "ses_full",
	} {
		u, _ := url.Parse(raw)
		if got := sessionIDOf(u); got != want {
			t.Errorf("sessionIDOf(%s) = %q, want %q", raw, got, want)
		}
	}
}

// With the tailnet required, a caller off it (or another user's, or
// tagged) is turned away before its key is looked up: no unpaired_device
// event for it to spend the budget on.
func TestTailnetGateRunsBeforeTheKeyLookup(t *testing.T) {
	f := newFixture(t)
	for _, from := range []string{"192.0.2.1", "100.0.0.4", "100.0.0.5"} {
		w := f.serve(t, req{from: from, as: f.ids.stranger, method: "GET", target: "/sessions"})
		if code := errorCode(t, w); code == "unpaired_device" {
			t.Errorf("from %s: the key was looked up before the tailnet gate", from)
		}
	}
	time.Sleep(50 * time.Millisecond)
	if evs := f.audit.events(); len(evs) != 0 {
		t.Fatalf("refusals off the tailnet were audited: %+v", evs)
	}
}

// A capability lowered in the tailnet policy changes the effective level:
// the device's open streams end, as for a grant change.
func TestCapabilityDowngradeEndsStreams(t *testing.T) {
	f := newFixture(t)
	front := f.listen(t)
	s := openEvents(t, meshClient(t, f.ids.mac, f.ids.host), front.URL)
	f.srv.RevalidateTailnet()
	if s.endsWithin(300 * time.Millisecond) {
		t.Fatal("an unchanged capability ended the stream")
	}
	f.tn.set("127.0.0.1", tailnet.Caller{NodeName: "mac.ts.net", UserID: owner, HasCap: true, Access: []string{"view"}})
	f.srv.RevalidateTailnet()
	if !s.endsWithin(3 * time.Second) {
		t.Fatal("the stream outlived its device's capability")
	}
}

// A transient WhoIs failure ends no stream: the last good answer stands
// for three TTLs past its expiry. Past that the failure stands too.
func TestTransientWhoIsFailureKeepsStreams(t *testing.T) {
	f := newFixture(t)
	var mu sync.Mutex
	now := time.Unix(1000, 0)
	f.srv.opts.Now = func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	f.srv.opts.WhoIsTTL = 10 * time.Second
	advance := func(d time.Duration) { mu.Lock(); now = now.Add(d); mu.Unlock() }
	front := f.listen(t)
	s := openEvents(t, meshClient(t, f.ids.mac, f.ids.host), front.URL)

	f.tn.mu.Lock()
	f.tn.fail = errors.New("tailscaled restarting")
	f.tn.mu.Unlock()
	advance(15 * time.Second) // cache expired, within grace
	f.srv.RevalidateTailnet()
	if s.endsWithin(300 * time.Millisecond) {
		t.Fatal("a transient WhoIs failure ended a stream")
	}
	advance(30 * time.Second) // past three TTLs
	f.srv.RevalidateTailnet()
	if !s.endsWithin(3 * time.Second) {
		t.Fatal("a WhoIs failing past its grace left the stream open")
	}
}

// A revocation ends streams at once whatever tailscaled is doing: the
// directory's re-check reads no network, so a hung LocalAPI can't delay it.
func TestRevocationDoesntWaitOnWhoIs(t *testing.T) {
	f := newFixture(t)
	front := f.listen(t)
	s := openEvents(t, meshClient(t, f.ids.mac, f.ids.host), front.URL)
	f.tn.mu.Lock()
	f.tn.hang = true
	f.tn.mu.Unlock()
	f.dirs.unpair(f.ids.mac)
	start := time.Now()
	f.srv.Revalidate()
	if d := time.Since(start); d > 200*time.Millisecond {
		t.Fatalf("Revalidate took %s with tailscaled hung", d)
	}
	if !s.endsWithin(time.Second) {
		t.Fatal("the revoked device's stream outlived Revalidate")
	}
}
