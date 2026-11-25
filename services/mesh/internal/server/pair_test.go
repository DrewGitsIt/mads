package server

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/pkg/paircode"
)

// fakePairing is the Data Service's pairing store: one good secret.
type fakePairing struct {
	mu         sync.Mutex
	good       string // the hash that redeems
	redeemed   []contracts.PairRedeem
	discovered []contracts.PairDiscoveredRequest
	requests   map[string]contracts.PairRequest
	// pendingCap is the Data Service's limit on discovered requests.
	pendingCap int
	// forgotten is the keys /pairing/forget removed; paired, the ones it
	// would find (nil: every key).
	forgotten []string
	paired    map[string]bool
	forgetErr error
	// replay is F6 R7: the used code again, by the key that used it,
	// answers its request as it is now.
	replay bool
}

func (p *fakePairing) ForgetPairing(_ context.Context, fp string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.forgetErr != nil {
		return p.forgetErr
	}
	if p.paired != nil && !p.paired[fp] {
		return dataclient.ErrNotFound
	}
	p.forgotten = append(p.forgotten, fp)
	return nil
}

func (p *fakePairing) ReportPairMismatch(_ context.Context, id, fp string) (contracts.PairRequest, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	r, ok := p.requests[id]
	if !ok || r.Fingerprint != fp {
		return r, dataclient.ErrNotFound
	}
	r.State, r.JoinerMismatch = contracts.PairDeclined, true
	p.requests[id] = r
	return r, nil
}

func (p *fakePairing) CreateDiscoveredPairRequest(_ context.Context, in contracts.PairDiscoveredRequest) (contracts.PairRequest, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.pendingCap > 0 && len(p.discovered) >= p.pendingCap {
		return contracts.PairRequest{}, &dataclient.RateLimitedError{Msg: "this host already has requests waiting", RetryAfter: time.Minute}
	}
	p.discovered = append(p.discovered, in)
	r := contracts.PairRequest{ID: "pr_d" + string(rune('0'+len(p.discovered))), Fingerprint: in.Fingerprint, Name: in.Name, State: contracts.PairPending,
		Mode: contracts.PairModeDiscovered, CheckCode: in.CheckCode, Vouched: in.Vouched, ExpiresAt: time.Now().Add(time.Minute)}
	p.requests[r.ID] = r
	return r, nil
}

func (p *fakePairing) RedeemPairingCode(_ context.Context, in contracts.PairRedeem) (contracts.PairRequest, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.redeemed = append(p.redeemed, in)
	if r, ok := p.requests["pr_1"]; ok && p.replay && p.good == "" && r.Fingerprint == in.Fingerprint {
		return r, nil
	}
	if in.SecretHash != p.good {
		return contracts.PairRequest{}, &dataclient.ForbiddenError{Code: "pairing_refused", Msg: "refused"}
	}
	p.good = "" // single use
	r := contracts.PairRequest{ID: "pr_1", Fingerprint: in.Fingerprint, Name: in.Name, State: contracts.PairPending, ExpiresAt: time.Now().Add(time.Minute)}
	p.requests[r.ID] = r
	return r, nil
}

func (p *fakePairing) PairRequest(_ context.Context, id string) (contracts.PairRequest, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	r, ok := p.requests[id]
	if !ok {
		return r, dataclient.ErrNotFound
	}
	return r, nil
}

func (p *fakePairing) calls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.redeemed)
}

func newPairFixture(t *testing.T) (*fixture, *fakePairing, []byte) {
	t.Helper()
	f := newFixture(t)
	secret, _ := paircode.NewSecret()
	fp := &fakePairing{good: paircode.Hash(secret), requests: map[string]contracts.PairRequest{}}
	f.srv.opts.Pairing = fp
	f.srv.opts.Platform, f.srv.opts.HostsSessions = "linux", true
	f.srv.opts.Self.DNSName = "pop-os.tail1.ts.net"
	return f, fp, secret
}

func joinBody(secret []byte) string {
	raw, _ := json.Marshal(PairJoin{Secret: base64.RawURLEncoding.EncodeToString(secret), Name: "spark", Platform: "darwin", HostsSessions: true, MeshURL: "http://spark.tail1.ts.net:17380"})
	return string(raw)
}

// hello reports the host's platform and whether it hosts sessions; every
// forwarded request names the device by its full fingerprint too.
func TestHelloPlatformAndDeviceHeader(t *testing.T) {
	f, _, _ := newPairFixture(t)
	w := f.do(t, "100.0.0.1", "GET", "/mesh/hello", nil)
	var h Hello
	_ = json.Unmarshal(w.Body.Bytes(), &h)
	if w.Code != 200 || h.Platform != "linux" || !h.HostsSessions || h.Device != string(f.ids.host.Fingerprint()) {
		t.Fatalf("hello = %d %+v", w.Code, h)
	}
	if !strings.Contains(w.Body.String(), `"hosts_sessions":true`) {
		t.Fatalf("hello body = %s", w.Body)
	}
	f.serve(t, req{from: "100.0.0.1", as: f.ids.mac, method: "GET", target: "/sessions",
		header: map[string]string{contracts.MeshDeviceHeader: "SPOOFED"}})
	seen := f.seenRequests()
	if got := seen[len(seen)-1].Header.Get(contracts.MeshDeviceHeader); got != string(f.ids.mac.Fingerprint()) {
		t.Fatalf("forwarded %s = %q", contracts.MeshDeviceHeader, got)
	}
}

// An unpaired key may present a pairing code: the request it opens is its
// own, by the key it proved. Everything wrong gets one answer.
func TestPairJoin(t *testing.T) {
	f, fp, secret := newPairFixture(t)
	stranger := f.ids.stranger
	w := f.serve(t, req{from: "100.0.0.1", as: stranger, method: "POST", target: "/mesh/pair", body: joinBody(secret)})
	if w.Code != http.StatusCreated {
		t.Fatalf("join: %d %s", w.Code, w.Body)
	}
	var st PairStatus
	_ = json.Unmarshal(w.Body.Bytes(), &st)
	if st.State != contracts.PairPending || st.RequestID != "pr_1" || st.Host.Name != "pop-os" || st.Host.Fingerprint != string(f.ids.host.Fingerprint()) {
		t.Fatalf("status = %+v", st)
	}
	got := fp.redeemed[0]
	if got.Fingerprint != string(stranger.Fingerprint()) || got.SecretHash != paircode.Hash(secret) || got.Name != "spark" ||
		got.Platform != "darwin" || !got.HostsSessions || got.MeshURL != "http://spark.tail1.ts.net:17380" || !strings.HasPrefix(got.Remote, "100.0.0.1:") ||
		got.TailnetNode != "mac.ts.net" {
		t.Fatalf("redeemed %+v", got)
	}
	if strings.Contains(w.Body.String(), base64.RawURLEncoding.EncodeToString(secret)) {
		t.Fatal("the secret is echoed")
	}

	// The same secret again (used), a wrong one, and a malformed one: one
	// answer, word for word.
	other, _ := paircode.NewSecret()
	bodies := map[string]string{}
	for name, body := range map[string]string{
		"used":      joinBody(secret),
		"wrong":     joinBody(other),
		"malformed": `{"secret":"!!"}`,
	} {
		w := f.serve(t, req{from: "100.0.0.2", as: stranger, method: "POST", target: "/mesh/pair", body: body})
		if w.Code != http.StatusForbidden || errorCode(t, w) != "pairing_refused" {
			t.Fatalf("%s: %d %s", name, w.Code, w.Body)
		}
		bodies[name] = w.Body.String()
	}
	if bodies["used"] != bodies["wrong"] || bodies["wrong"] != bodies["malformed"] {
		t.Fatalf("answers differ: %v", bodies)
	}
	// Audited, debounced per key.
	time.Sleep(50 * time.Millisecond)
	n := 0
	for _, ev := range f.audit.events() {
		if ev.Code == "pairing_refused" {
			n++
			if ev.Fingerprint != string(stranger.Fingerprint()) {
				t.Fatalf("refusal names %s", ev.Fingerprint)
			}
		}
	}
	if n != 1 {
		t.Fatalf("%d pairing_refused events, want 1 (debounced)", n)
	}
}

func TestPairJoinRefusals(t *testing.T) {
	f, fp, secret := newPairFixture(t)
	for _, tc := range []struct {
		name string
		q    req
		want int
		code string
	}{
		{"off the tailnet", req{from: "192.0.2.1", as: f.ids.stranger}, 403, "not_tailnet"},
		{"this host's own key", req{from: "100.0.0.1", as: f.ids.host}, 403, "pair_self"},
	} {
		tc.q.method, tc.q.target, tc.q.body = "POST", "/mesh/pair", joinBody(secret)
		w := f.serve(t, tc.q)
		if w.Code != tc.want || errorCode(t, w) != tc.code {
			t.Errorf("%s: %d %s, want %d %s", tc.name, w.Code, w.Body, tc.want, tc.code)
		}
	}
	if fp.calls() != 0 {
		t.Fatalf("a refused caller reached the pairing store %d times", fp.calls())
	}
	// Other /mesh/pair paths and methods are nothing.
	for _, q := range []req{
		{method: "GET", target: "/mesh/pair"},
		{method: "POST", target: "/mesh/pair/pr_1"},
		{method: "GET", target: "/mesh/pairs"},
	} {
		q.from, q.as = "100.0.0.1", f.ids.stranger
		if w := f.serve(t, q); w.Code == http.StatusOK || w.Code == http.StatusCreated {
			t.Errorf("%s %s: %d", q.method, q.target, w.Code)
		}
	}
	// Without a pairing store there is no pairing.
	f.srv.opts.Pairing = nil
	if w := f.serve(t, req{from: "100.0.0.1", as: f.ids.stranger, method: "POST", target: "/mesh/pair", body: joinBody(secret)}); w.Code != http.StatusNotFound {
		t.Fatalf("no store: %d", w.Code)
	}
}

// Attempts are rate-limited per address, and overall.
func TestPairJoinRateLimited(t *testing.T) {
	f, _, _ := newPairFixture(t)
	now := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	f.srv.pairLimits.now = func() time.Time { return now }
	bad, _ := paircode.NewSecret()
	try := func(from string) int {
		return f.serve(t, req{from: from, as: f.ids.stranger, method: "POST", target: "/mesh/pair", body: joinBody(bad)}).Code
	}
	for i := range pairJoinPerAddr.burst {
		if c := try("100.0.0.1"); c != http.StatusForbidden {
			t.Fatalf("attempt %d: %d", i, c)
		}
	}
	if c := try("100.0.0.1"); c != http.StatusTooManyRequests {
		t.Fatalf("past the per-address burst: %d, want 429", c)
	}
	if c := try("100.0.0.2"); c != http.StatusForbidden {
		t.Fatalf("another address: %d", c)
	}
	now = now.Add(pairJoinPerAddr.every)
	if c := try("100.0.0.1"); c != http.StatusForbidden {
		t.Fatalf("after the refill: %d", c)
	}
	// The overall budget: many addresses, one host.
	limits := newPairLimits(func() time.Time { return now })
	allowed := 0
	for i := range 400 {
		if _, ok := limits.allowJoin("10.0." + strconv.Itoa(i/256) + "." + strconv.Itoa(i%256)); ok {
			allowed++
		}
	}
	if allowed != pairJoinGlobal.burst {
		t.Fatalf("overall: %d allowed, want %d", allowed, pairJoinGlobal.burst)
	}
}

// A request's status is its device's alone.
func TestPairStatus(t *testing.T) {
	f, fp, secret := newPairFixture(t)
	if w := f.serve(t, req{from: "100.0.0.1", as: f.ids.stranger, method: "POST", target: "/mesh/pair", body: joinBody(secret)}); w.Code != http.StatusCreated {
		t.Fatal(w.Code)
	}
	poll := func(as int) (int, PairStatus) {
		id := f.ids.stranger
		if as == 1 {
			id = f.ids.idle
		}
		w := f.serve(t, req{from: "100.0.0.1", as: id, method: "GET", target: "/mesh/pair/pr_1"})
		var st PairStatus
		_ = json.Unmarshal(w.Body.Bytes(), &st)
		return w.Code, st
	}
	if code, st := poll(0); code != 200 || st.State != contracts.PairPending || st.Level != "" {
		t.Fatalf("pending: %d %+v", code, st)
	}
	if code, _ := poll(1); code != http.StatusNotFound {
		t.Fatalf("another key: %d, want 404", code)
	}
	fp.mu.Lock()
	r := fp.requests["pr_1"]
	r.State, r.Level = contracts.PairApproved, contracts.GrantChat
	fp.requests["pr_1"] = r
	fp.mu.Unlock()
	// Approved: the device is paired here now, and still reads its answer.
	f.dirs.pair(f.ids.stranger, "spark", contracts.GrantChat, contracts.PeerCeilings{})
	if code, st := poll(0); code != 200 || st.State != contracts.PairApproved || st.Level != "chat" {
		t.Fatalf("approved: %d %+v", code, st)
	}
	if w := f.serve(t, req{from: "100.0.0.1", as: f.ids.stranger, method: "GET", target: "/mesh/pair/pr_nope"}); w.Code != http.StatusNotFound {
		t.Fatalf("unknown request: %d", w.Code)
	}
}

// The joiner's addresses are ones this host may dial (S3): anything that
// could make this host reach loopback, link-local, cloud metadata or a
// low port is refused (422 bad_address) before the code is touched. A
// private address passes here; the prefix rule is checked at dial time.
func TestPairJoinRefusesBadAddresses(t *testing.T) {
	f, fp, secret := newPairFixture(t)
	for _, u := range []string{"http://127.0.0.1:17380", "http://169.254.169.254:17380", "http://[fe80::1]:17380",
		"http://224.0.0.1:17380", "http://spark.tail1.ts.net:22", "http://0.0.0.0:17380", "https://spark:17380/x", "http://localhost:17380"} {
		f.srv.pairLimits = newPairLimits(time.Now) // not what this tests
		raw, _ := json.Marshal(PairJoin{Secret: base64.RawURLEncoding.EncodeToString(secret), Name: "spark", MeshURL: u})
		w := f.serve(t, req{from: "100.0.0.1", as: f.ids.stranger, method: "POST", target: "/mesh/pair", body: string(raw)})
		if w.Code != http.StatusUnprocessableEntity || errorCode(t, w) != "bad_address" {
			t.Errorf("%s: %d %s", u, w.Code, w.Body)
		}
	}
	for _, a := range []string{"127.0.0.1:17380", "169.254.169.254:17380", "[fd00:ec2::254]:17380"} {
		f.srv.pairLimits = newPairLimits(time.Now)
		raw, _ := json.Marshal(PairJoin{Secret: base64.RawURLEncoding.EncodeToString(secret), Name: "spark",
			Addresses: []contracts.NetAddress{{Via: "direct", Addr: "192.168.1.30:17380"}, {Via: "direct", Addr: a}}})
		w := f.serve(t, req{from: "100.0.0.1", as: f.ids.stranger, method: "POST", target: "/mesh/pair", body: string(raw)})
		if w.Code != http.StatusUnprocessableEntity || errorCode(t, w) != "bad_address" {
			t.Errorf("addresses with %s: %d %s", a, w.Code, w.Body)
		}
	}
	if fp.calls() != 0 {
		t.Fatal("a bad address reached the pairing store")
	}
	// Any network's address is fine at arrival: a LAN, a MagicDNS name at
	// any port, a public one; an unknown adapter is skipped.
	f.srv.pairLimits = newPairLimits(time.Now)
	raw, _ := json.Marshal(PairJoin{Secret: base64.RawURLEncoding.EncodeToString(secret), Name: "spark", Addresses: []contracts.NetAddress{
		{Via: "direct", Addr: "10.0.0.5:17380", Kind: "lan"}, {Via: "direct", Addr: "spark.tail1.ts.net:9000"}, {Via: "relay", Addr: "r.example:443"}, {Via: "direct", Addr: "8.8.8.8:17380"}}})
	if w := f.serve(t, req{from: "100.0.0.1", as: f.ids.stranger, method: "POST", target: "/mesh/pair", body: string(raw)}); w.Code != http.StatusCreated {
		t.Fatalf("good addresses: %d %s", w.Code, w.Body)
	}
	fp.mu.Lock()
	got := fp.redeemed[len(fp.redeemed)-1]
	fp.mu.Unlock()
	if len(got.Addresses) != 3 || got.Network != "tailscale" {
		t.Fatalf("redeemed %+v", got)
	}
}

// The tailnet policy never caps a level any more: a device granted admin
// under a policy of ["control"] acts as admin, and hello says it isn't
// capped. Hello also says where this host listens and its key expiry.
func TestHelloNeverCaps(t *testing.T) {
	f, _, _ := newPairFixture(t)
	f.dirs.pair(f.ids.mac, "mac", contracts.GrantAdmin, contracts.PeerCeilings{})
	exp := time.Date(2027, 3, 3, 0, 0, 0, 0, time.UTC)
	f.srv.opts.Addresses = func() []contracts.NetAddress {
		return []contracts.NetAddress{{Via: "direct", Addr: "100.64.0.9:17380", Kind: "overlay", Label: "Tailscale"}}
	}
	f.srv.opts.TailscaleKeyExpiry = func() *time.Time { return &exp }
	var h Hello
	_ = json.Unmarshal(f.do(t, "100.0.0.1", "GET", "/mesh/hello", nil).Body.Bytes(), &h)
	if h.Grant != "admin" || h.TailnetCapped || strings.Join(h.Access, ",") != "view,chat,control,admin" {
		t.Fatalf("hello: %+v", h)
	}
	if len(h.Addresses) != 1 || h.Addresses[0].Kind != "overlay" || h.TailscaleKeyExpiry == nil || !h.TailscaleKeyExpiry.Equal(exp) {
		t.Fatalf("hello addresses/expiry: %+v", h)
	}
}

// Pairing answers name the host by its mads name; a device
// already paired here isn't held to strangers' poll limit.
func TestPairNamesAndPollLimits(t *testing.T) {
	f, fp, secret := newPairFixture(t)
	f.srv.opts.Name = "pops-mads"
	w := f.serve(t, req{from: "100.0.0.1", as: f.ids.stranger, method: "POST", target: "/mesh/pair", body: joinBody(secret)})
	var st PairStatus
	_ = json.Unmarshal(w.Body.Bytes(), &st)
	if st.Host.Name != "pops-mads" {
		t.Fatalf("host = %+v", st.Host)
	}
	now := time.Now()
	f.srv.pairLimits.now = func() time.Time { return now }
	poll := func() int {
		return f.serve(t, req{from: "100.0.0.2", as: f.ids.stranger, method: "GET", target: "/mesh/pair/pr_1"}).Code
	}
	for range pairPollPerAddr.burst {
		poll()
	}
	if c := poll(); c != http.StatusTooManyRequests {
		t.Fatalf("a stranger past the poll burst: %d", c)
	}
	f.dirs.pair(f.ids.stranger, "spark", contracts.GrantView, contracts.PeerCeilings{})
	if c := poll(); c != http.StatusOK {
		t.Fatalf("paired, polling its answer: %d", c)
	}
	_ = fp
}

// F6 R2: a key already paired here may ask again; its request is taken
// like any other.
func TestPairJoinFromAPairedKey(t *testing.T) {
	f, fp, secret := newPairFixture(t)
	w := f.serve(t, req{from: "100.0.0.1", as: f.ids.mac, method: "POST", target: "/mesh/pair", body: joinBody(secret)})
	if w.Code != http.StatusCreated || fp.calls() != 1 {
		t.Fatalf("a paired key asking again: %d %s", w.Code, w.Body)
	}
}

// F6 R7: the same key with the same code after the host declined gets
// that request back, and the log says so rather than "waiting".
func TestPairJoinAgainAfterDecline(t *testing.T) {
	var buf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(prev)
	f, fp, secret := newPairFixture(t)
	fp.replay = true
	if w := f.serve(t, req{from: "100.0.0.1", as: f.ids.stranger, method: "POST", target: "/mesh/pair", body: joinBody(secret)}); w.Code != http.StatusCreated {
		t.Fatalf("join: %d %s", w.Code, w.Body)
	}
	r := fp.requests["pr_1"]
	r.State = contracts.PairDeclined
	fp.requests["pr_1"] = r
	buf.Reset()
	w := f.serve(t, req{from: "100.0.0.1", as: f.ids.stranger, method: "POST", target: "/mesh/pair", body: joinBody(secret)})
	var st PairStatus
	_ = json.Unmarshal(w.Body.Bytes(), &st)
	if w.Code != http.StatusCreated || st.State != contracts.PairDeclined || st.RequestID != "pr_1" {
		t.Fatalf("again: %d %s", w.Code, w.Body)
	}
	if l := buf.String(); strings.Contains(l, "waiting for approval") || !strings.Contains(l, "already declined") {
		t.Fatalf("log: %s", l)
	}
}

// F6 R1: /mesh/forget is any paired caller's, at any level (none
// included): the Data Service forgets the key the handshake proved, and
// its live requests end. Not paired: unpaired_device, and the store
// isn't asked.
func TestMeshForget(t *testing.T) {
	f, fp, _ := newPairFixture(t)
	w := f.serve(t, req{from: "100.0.0.1", as: f.ids.idle, method: "POST", target: "/mesh/forget"})
	if w.Code != http.StatusNoContent || len(fp.forgotten) != 1 || fp.forgotten[0] != string(f.ids.idle.Fingerprint()) {
		t.Fatalf("level none: %d %s %v", w.Code, w.Body, fp.forgotten)
	}
	if w := f.serve(t, req{from: "100.0.0.1", as: f.ids.stranger, method: "POST", target: "/mesh/forget"}); w.Code != http.StatusForbidden || errorCode(t, w) != "unpaired_device" {
		t.Fatalf("unpaired: %d %s", w.Code, w.Body)
	}
	if len(fp.forgotten) != 1 {
		t.Fatalf("an unpaired key reached the store: %v", fp.forgotten)
	}
	// In the directory, gone from the store (a race): unpaired_device too.
	fp.paired = map[string]bool{}
	if w := f.serve(t, req{from: "100.0.0.1", as: f.ids.mac, method: "POST", target: "/mesh/forget"}); w.Code != http.StatusForbidden || errorCode(t, w) != "unpaired_device" {
		t.Fatalf("gone from the store: %d %s", w.Code, w.Body)
	}
	fp.paired = nil
	if w := f.serve(t, req{from: "100.0.0.1", as: f.ids.mac, method: "GET", target: "/mesh/forget"}); w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET: %d", w.Code)
	}
	fp.forgetErr = errors.New("down")
	if w := f.serve(t, req{from: "100.0.0.1", as: f.ids.mac, method: "POST", target: "/mesh/forget"}); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("store down: %d", w.Code)
	}
}
