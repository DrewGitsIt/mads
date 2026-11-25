package server

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/pkg/devicekey"
	"github.com/drewkidwell/mads/pkg/paircode"
	"github.com/drewkidwell/mads/services/mesh/internal/tailnet"
)

// frontOn serves f's gate on a real TLS listener for network n, with its
// own budget.
func frontOn(t *testing.T, f *fixture, b *Budget, n Net) *httptest.Server {
	t.Helper()
	cfg, err := devicekey.ServerConfig(f.ids.host)
	if err != nil {
		t.Fatal(err)
	}
	front := httptest.NewUnstartedServer(f.srv)
	front.Listener = f.srv.ListenOn(front.Listener, b, n)
	front.Config.ConnContext = f.srv.ConnContext
	front.TLS = f.srv.TLSConfig(cfg)
	front.EnableHTTP2 = true
	front.StartTLS()
	t.Cleanup(front.Close)
	return front
}

var lanNet = Net{ID: "lan:192.168.1.0/24/192.168.1.1", Label: "Wi-Fi"}

// The Tailscale check runs only on the Tailscale network's listener: a
// connection on the home Wi-Fi isn't a tailnet connection, and a paired
// device's key is all it needs there.
func TestTailnetCheckOnlyOnTailscaleListener(t *testing.T) {
	f := newFixture(t)
	f.tn.set("127.0.0.1", stranger) // WhoIs would refuse it
	lan := frontOn(t, f, NewBudget(DefaultListenLimits), lanNet)
	resp, err := meshClient(t, f.ids.mac, f.ids.host).Get(lan.URL + "/sessions")
	if err != nil {
		t.Fatalf("LAN listener: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("LAN listener with the Tailscale check on: %d", resp.StatusCode)
	}
	if f.tn.calls != 0 {
		t.Fatalf("WhoIs asked %d times for a LAN connection", f.tn.calls)
	}
	ts := frontOn(t, f, NewBudget(DefaultListenLimits), Net{ID: "tailscale", Label: "Tailscale", Tailscale: true})
	cfg, _ := devicekey.ClientConfig(f.ids.mac, f.ids.host.Fingerprint())
	if c, err := tls.Dial("tcp", addrOf(ts), cfg); err == nil {
		_ = c.Close()
		t.Fatal("the Tailscale listener let another user's node complete a handshake")
	}
}

// With the check on and tailscaled unreachable (or this node's own
// identity unknown), the Tailscale network fails closed, saying why.
func TestTailnetCheckFailsClosedWithoutTailscaled(t *testing.T) {
	f := newFixture(t)
	f.srv.opts.TailnetSelf = func() (tailnet.Self, error) { return tailnet.Self{}, errors.New("tailscaled's LocalAPI wasn't found") }
	w := f.do(t, "100.0.0.1", "GET", "/sessions", nil)
	if w.Code != http.StatusServiceUnavailable || errorCode(t, w) != "whois_failed" || !strings.Contains(w.Body.String(), "identity check is on") {
		t.Fatalf("no tailscaled: %d %s", w.Code, w.Body)
	}
	if seen := f.seenRequests(); len(seen) != 0 {
		t.Fatal("a request reached the Router without the check")
	}
	f.tn.mu.Lock()
	f.tn.fail = errors.New("LocalAPI gone")
	f.tn.mu.Unlock()
	f.srv.opts.TailnetSelf = nil
	if w := f.do(t, "100.0.0.9", "GET", "/sessions", nil); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("WhoIs failing: %d", w.Code)
	}
	// Turned off, a paired key is enough again.
	f.srv.SetRequireTailnet(false)
	if w := f.do(t, "100.0.0.9", "GET", "/sessions", nil); w.Code != 200 {
		t.Fatalf("check off: %d", w.Code)
	}
}

// Each network has its own connection budget: a flood on the café Wi-Fi
// can't use up the slots the tailnet's devices need.
func TestFloodOnOneNetworkLeavesAnotherFree(t *testing.T) {
	f := newFixture(t)
	f.tn.set("127.0.0.1", full)
	lim := ListenLimits{MaxConns: 2, MaxPerIP: 2, FirstRequest: 5 * time.Second}
	cafe := frontOn(t, f, NewBudget(lim), lanNet)
	ts := frontOn(t, f, NewBudget(lim), Net{ID: "tailscale", Tailscale: true})
	var flood []net.Conn
	for range 2 {
		c, err := net.Dial("tcp", addrOf(cafe))
		if err != nil {
			t.Fatal(err)
		}
		flood = append(flood, c)
	}
	defer func() {
		for _, c := range flood {
			_ = c.Close()
		}
	}()
	time.Sleep(50 * time.Millisecond)
	client := meshClient(t, f.ids.mac, f.ids.host)
	client.Timeout = 2 * time.Second
	resp, err := client.Get(ts.URL + "/sessions")
	if err != nil {
		t.Fatalf("the tailnet listener was starved by the café's flood: %v", err)
	}
	_ = resp.Body.Close()
}

// An address with 10 failed handshakes in a minute is closed at accept for
// 60 s; others are untouched.
func TestFailedHandshakeCooldown(t *testing.T) {
	f := newFixture(t)
	f.tn.set("127.0.0.1", full)
	logs := &syncBuffer{}
	prev := log.Writer()
	log.SetOutput(logs)
	defer log.SetOutput(prev)
	b := NewBudget(ListenLimits{MaxConns: 64, MaxPerIP: 64, FirstRequest: 5 * time.Second, FailedPerMinute: 3, Cooldown: time.Minute})
	var nowMu sync.Mutex
	now := time.Unix(1000, 0)
	b.now = func() time.Time { nowMu.Lock(); defer nowMu.Unlock(); return now }
	front := frontOn(t, f, b, lanNet)
	for range 3 {
		// No client certificate: the handshake fails.
		c, err := tls.Dial("tcp", addrOf(front), &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS13}) //nolint:gosec // test client
		if err == nil {
			_, _ = c.Read(make([]byte, 1)) // the server's refusal arrives on read in TLS 1.3
			_ = c.Close()
		}
	}
	deadline := time.Now().Add(2 * time.Second)
	for !b.coolingDown("127.0.0.1") {
		if time.Now().After(deadline) {
			t.Fatal("no cool-down after three failed handshakes")
		}
		time.Sleep(10 * time.Millisecond)
	}
	c, err := net.Dial("tcp", addrOf(front))
	if err != nil {
		t.Fatal(err)
	}
	if !closedWithin(c, time.Second) {
		t.Fatal("a cooling-down address was accepted")
	}
	if !strings.Contains(logs.String(), "127.0.0.1: 3 connections in a minute closed without a request") {
		t.Fatalf("the cool-down wasn't logged:\n%s", logs.String())
	}
	_ = c.Close()
	nowMu.Lock()
	now = now.Add(61 * time.Second)
	nowMu.Unlock()
	resp, err := meshClient(t, f.ids.mac, f.ids.host).Get(front.URL + "/sessions")
	if err != nil {
		t.Fatalf("after the cool-down: %v", err)
	}
	_ = resp.Body.Close()
}

// --- Discovery (U4, S6) -----------------------------------------------------------

// /mesh/discover exists only inside the window: outside it, a stranger
// gets exactly what any unpaired key gets.
func TestDiscoverOnlyInsideTheWindow(t *testing.T) {
	f := newFixture(t)
	now := time.Unix(1000, 0)
	f.srv.opts.Now = func() time.Time { return now }
	f.srv.pairLimits.now = f.srv.opts.Now
	outside := f.serve(t, req{from: "100.0.0.1", as: f.ids.stranger, method: "GET", target: "/mesh/discover"})
	plain := f.serve(t, req{from: "100.0.0.1", as: f.ids.stranger, method: "GET", target: "/sessions"})
	if outside.Code != 403 || errorCode(t, outside) != "unpaired_device" || outside.Header().Get("Connection") != "close" ||
		plain.Code != outside.Code || errorCode(t, plain) != errorCode(t, outside) {
		t.Fatalf("outside the window: %d %s (vs %d %s)", outside.Code, outside.Body, plain.Code, plain.Body)
	}
	f.srv.opts.Name = "pop-os"
	f.srv.opts.Platform = "linux"
	f.srv.SetDiscoverable(now.Add(time.Hour)) // capped at ten minutes
	if got := f.srv.DiscoverableUntil(); !got.Equal(now.Add(contracts.DiscoverableMax)) {
		t.Fatalf("window until %s", got)
	}
	w := f.serve(t, req{from: "100.0.0.1", as: f.ids.stranger, method: "GET", target: "/mesh/discover"})
	var d Discover
	_ = json.Unmarshal(w.Body.Bytes(), &d)
	if w.Code != 200 || d.Mads != 1 || d.Name != "pop-os" || d.Platform != "linux" || d.Fingerprint != string(f.ids.host.Fingerprint()) || d.MeshAPI != MeshAPI {
		t.Fatalf("inside: %d %+v", w.Code, d)
	}
	// Only what a person needs to recognise it: nothing else in the answer.
	var raw map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &raw)
	if len(raw) != 6 || raw["paired_with_you"] != false {
		t.Fatalf("discover answers %v", raw)
	}
	// paired_with_you is about the asking key only (F6 R5).
	w = f.serve(t, req{from: "100.0.0.1", as: f.ids.mac, method: "GET", target: "/mesh/discover"})
	_ = json.Unmarshal(w.Body.Bytes(), &d)
	if w.Code != 200 || !d.PairedWithYou {
		t.Fatalf("a paired key: %d %s", w.Code, w.Body)
	}
	// The tailnet check still comes first on the Tailscale network.
	if w := f.serve(t, req{from: "100.0.0.4", as: f.ids.stranger, method: "GET", target: "/mesh/discover"}); w.Code != 403 || errorCode(t, w) != "other_user" {
		t.Fatalf("another user's node: %d %s", w.Code, w.Body)
	}
	// Audited, once per address.
	f.serve(t, req{from: "100.0.0.1", as: f.ids.stranger, method: "GET", target: "/mesh/discover"})
	time.Sleep(50 * time.Millisecond)
	var discovered int
	for _, e := range f.audit.envelopes() {
		if e.Type == contracts.DeviceDiscovered {
			discovered++
		}
	}
	if discovered != 1 {
		t.Fatalf("%d device.discovered for two probes from one address", discovered)
	}
	// The window ends by itself.
	now = now.Add(contracts.DiscoverableMax + time.Second)
	if w := f.serve(t, req{from: "100.0.0.1", as: f.ids.stranger, method: "GET", target: "/mesh/discover"}); w.Code != 403 || errorCode(t, w) != "unpaired_device" {
		t.Fatalf("after expiry: %d %s", w.Code, w.Body)
	}
	// And is closed on request.
	f.srv.SetDiscoverable(now.Add(time.Minute))
	f.srv.SetDiscoverable(time.Time{})
	if f.srv.DiscoverableNow() {
		t.Fatal("still open after closing")
	}
}

func TestDiscoverRateLimited(t *testing.T) {
	f := newFixture(t)
	now := time.Unix(1000, 0)
	f.srv.opts.Now = func() time.Time { return now }
	f.srv.pairLimits.now = f.srv.opts.Now
	f.srv.SetDiscoverable(now.Add(time.Minute))
	for range discoverPerAddr.burst {
		if w := f.serve(t, req{from: "100.0.0.1", as: f.ids.stranger, method: "GET", target: "/mesh/discover"}); w.Code != 200 {
			t.Fatalf("within the burst: %d", w.Code)
		}
	}
	if w := f.serve(t, req{from: "100.0.0.1", as: f.ids.stranger, method: "GET", target: "/mesh/discover"}); w.Code != http.StatusTooManyRequests {
		t.Fatalf("past the burst: %d", w.Code)
	}
}

func commitBody(t *testing.T, joiner, host *devicekey.Identity, nonce string, vouch bool) string {
	t.Helper()
	commit, err := paircode.Commit(nonce, joiner.Fingerprint(), host.Fingerprint())
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(PairJoin{Mode: contracts.PairModeDiscovered, Name: "Alexs-MacBook-Air", Platform: "darwin", Commit: commit, TailscaleVouch: vouch,
		Addresses: []contracts.NetAddress{{Via: "direct", Addr: "100.70.1.1:17380", Kind: "overlay"}}})
	return string(raw)
}

func revealBody(id, nonce string) string {
	raw, _ := json.Marshal(PairJoin{Mode: contracts.PairModeDiscovered, CommitID: id, Nonce: nonce})
	return string(raw)
}

// discoveredJoin is both steps from as at from: commit, then reveal. It
// answers the reveal's response and the host nonce.
func discoveredJoin(t *testing.T, f *fixture, from string, as *devicekey.Identity, vouch bool) (*httptest.ResponseRecorder, string, string) {
	t.Helper()
	f.srv.pairLimits = newPairLimits(time.Now) // per-address limits are pairJoin's, tested there
	nonce, _ := paircode.NewNonce()
	w := f.serve(t, req{from: from, as: as, method: "POST", target: "/mesh/pair", body: commitBody(t, as, f.ids.host, nonce, vouch)})
	if w.Code != http.StatusOK {
		return w, "", nonce
	}
	var c PairCommitted
	_ = json.Unmarshal(w.Body.Bytes(), &c)
	if c.HostNonce == "" || c.CommitID == "" {
		t.Fatalf("commit answer %s", w.Body)
	}
	return f.serve(t, req{from: from, as: as, method: "POST", target: "/mesh/pair", body: revealBody(c.CommitID, nonce)}), c.HostNonce, nonce
}

// A discovered pairing request: refused outside the window (like a bad
// code); inside it, a commitment first, answered with the host's nonce,
// then the reveal, both sides computing the same check code; at most
// three pending; Tailscale vouches only when the request came over
// Tailscale and both ends' WhoIs agree.
func TestDiscoveredPairing(t *testing.T) {
	f, fp, _ := newPairFixture(t)
	if w, _, _ := discoveredJoin(t, f, "100.0.0.1", f.ids.stranger, true); w.Code != 403 || errorCode(t, w) != "pairing_refused" {
		t.Fatalf("outside the window: %d %s", w.Code, w.Body)
	}
	f.srv.SetDiscoverable(time.Now().Add(5 * time.Minute))
	// Over Tailscale (this fixture's connections are), from this user's
	// untagged node, the joiner vouching too: vouched.
	w, hostNonce, nonce := discoveredJoin(t, f, "100.0.0.1", f.ids.stranger, true)
	var st PairStatus
	_ = json.Unmarshal(w.Body.Bytes(), &st)
	if w.Code != 201 || !st.Vouched {
		t.Fatalf("vouched: %d %s", w.Code, w.Body)
	}
	want, _ := paircode.CheckCode(f.ids.host.Fingerprint(), f.ids.stranger.Fingerprint(), nonce, hostNonce)
	fp.mu.Lock()
	got := fp.discovered[0]
	fp.mu.Unlock()
	if got.CheckCode != want || !got.Vouched || got.Network != "tailscale" || len(got.Addresses) != 1 || got.Name != "Alexs-MacBook-Air" {
		t.Fatalf("stored %+v, want code %s", got, want)
	}
	// The joiner didn't vouch: the number check stays.
	w, _, _ = discoveredJoin(t, f, "100.0.0.1", f.ids.stranger, false)
	st = PairStatus{}
	_ = json.Unmarshal(w.Body.Bytes(), &st)
	if w.Code != 201 || st.Vouched {
		t.Fatalf("joiner didn't vouch: %d %s", w.Code, w.Body)
	}
	// With the check off, a node of another user may ask, never vouched.
	f.srv.SetRequireTailnet(false)
	w, _, _ = discoveredJoin(t, f, "100.0.0.4", f.ids.stranger, true)
	st = PairStatus{}
	_ = json.Unmarshal(w.Body.Bytes(), &st)
	if w.Code != 201 || st.Vouched {
		t.Fatalf("another user's node: %d %s", w.Code, w.Body)
	}
	// At most three pending: the store's answer comes back as 429.
	fp.mu.Lock()
	fp.pendingCap = len(fp.discovered)
	fp.mu.Unlock()
	if w, _, _ := discoveredJoin(t, f, "100.0.0.1", f.ids.stranger, false); w.Code != http.StatusTooManyRequests {
		t.Fatalf("past the pending cap: %d %s", w.Code, w.Body)
	}
}

// C1: the joiner's nonce is never accepted before the host's is fixed. A
// first message carrying a nonce (which would let the host grind its own
// to steer the code) is refused; a reveal must match the commitment, by
// the key that made it, once, within a minute.
func TestDiscoveredPairingCommitReveal(t *testing.T) {
	f, fp, _ := newPairFixture(t)
	now := time.Now()
	f.srv.opts.Now = func() time.Time { return now }
	f.srv.SetDiscoverable(now.Add(5 * time.Minute))
	post := func(as *devicekey.Identity, body string) *httptest.ResponseRecorder {
		f.srv.pairLimits = newPairLimits(time.Now)
		return f.serve(t, req{from: "100.0.0.1", as: as, method: "POST", target: "/mesh/pair", body: body})
	}
	nonce, _ := paircode.NewNonce()
	early, _ := json.Marshal(PairJoin{Mode: contracts.PairModeDiscovered, Name: "x", Nonce: nonce})
	if w := post(f.ids.stranger, string(early)); w.Code != 422 || errorCode(t, w) != "commit_required" {
		t.Fatalf("nonce first: %d %s", w.Code, w.Body)
	}
	commit := func() PairCommitted {
		// A fresh window each time: three spoiled reveals would close one
		// (TestAbandonedPairingsCloseTheWindow).
		f.srv.SetDiscoverable(now.Add(5 * time.Minute))
		w := post(f.ids.stranger, commitBody(t, f.ids.stranger, f.ids.host, nonce, false))
		var c PairCommitted
		_ = json.Unmarshal(w.Body.Bytes(), &c)
		if w.Code != 200 {
			t.Fatalf("commit: %d %s", w.Code, w.Body)
		}
		return c
	}
	other, _ := paircode.NewNonce()
	c := commit()
	if w := post(f.ids.stranger, revealBody(c.CommitID, other)); w.Code != 422 || errorCode(t, w) != "bad_reveal" {
		t.Fatalf("another nonce revealed: %d %s", w.Code, w.Body)
	}
	if w := post(f.ids.stranger, revealBody(c.CommitID, nonce)); w.Code != 422 {
		t.Fatalf("a commitment revealed twice: %d %s", w.Code, w.Body)
	}
	c = commit()
	if w := post(f.ids.mac, revealBody(c.CommitID, nonce)); w.Code == 201 {
		t.Fatal("another key revealed the commitment")
	}
	c = commit()
	now = now.Add(commitTTL + time.Second)
	if w := post(f.ids.stranger, revealBody(c.CommitID, nonce)); w.Code != 422 {
		t.Fatalf("an expired commitment: %d %s", w.Code, w.Body)
	}
	fp.mu.Lock()
	defer fp.mu.Unlock()
	if len(fp.discovered) != 0 {
		t.Fatalf("a request was made without a good reveal: %+v", fp.discovered)
	}
}

// On a LAN there's no vouching, whatever the joiner says.
func TestDiscoveredPairingNoVouchOffTailscale(t *testing.T) {
	f, fp, _ := newPairFixture(t)
	f.tn.set("127.0.0.1", full)
	f.srv.SetDiscoverable(time.Now().Add(5 * time.Minute))
	lan := frontOn(t, f, NewBudget(DefaultListenLimits), lanNet)
	nonce, _ := paircode.NewNonce()
	client := meshClient(t, f.ids.stranger, f.ids.host)
	resp, err := client.Post(lan.URL+"/mesh/pair", "application/json", strings.NewReader(commitBody(t, f.ids.stranger, f.ids.host, nonce, true)))
	if err != nil {
		t.Fatal(err)
	}
	var c PairCommitted
	_ = json.NewDecoder(resp.Body).Decode(&c)
	_ = resp.Body.Close()
	resp, err = client.Post(lan.URL+"/mesh/pair", "application/json", strings.NewReader(revealBody(c.CommitID, nonce)))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var st PairStatus
	_ = json.NewDecoder(resp.Body).Decode(&st)
	if resp.StatusCode != 201 || st.Vouched {
		t.Fatalf("LAN: %d %+v", resp.StatusCode, st)
	}
	fp.mu.Lock()
	defer fp.mu.Unlock()
	if fp.discovered[0].Network != lanNet.ID || fp.discovered[0].NetworkLabel != "Wi-Fi" {
		t.Fatalf("%+v", fp.discovered[0])
	}
}

// Round-1 review M2: limits count an IPv6 address by its /64, and the
// store's 429 reaches the joiner with the real wait.
func TestLimitsBySourceAndTheRealWait(t *testing.T) {
	if limitKey("[2001:db8:1:2::10]:4000") != limitKey("[2001:db8:1:2:ffff::1]:5") || limitKey("[2001:db8:1:3::1]:1") == limitKey("[2001:db8:1:2::1]:1") {
		t.Fatal("IPv6 isn't keyed by /64")
	}
	if limitKey("192.168.1.2:4000") != "192.168.1.2" || limitKey("[::ffff:192.168.1.2]:1") != "192.168.1.2" {
		t.Fatal("IPv4 keys")
	}
	f, fp, _ := newPairFixture(t)
	f.srv.SetDiscoverable(time.Now().Add(5 * time.Minute))
	fp.mu.Lock()
	fp.pendingCap = -1 // every request answered 429 below
	fp.mu.Unlock()
	f.srv.opts.Pairing = &limitedPairing{fakePairing: fp}
	w, _, _ := discoveredJoin(t, f, "100.0.0.1", f.ids.stranger, false)
	if w.Code != http.StatusTooManyRequests || w.Header().Get("Retry-After") != "240" || !strings.Contains(w.Body.String(), "in 4m0s") {
		t.Fatalf("429: %d %s %v", w.Code, w.Body, w.Header())
	}
}

type limitedPairing struct{ *fakePairing }

func (p *limitedPairing) CreateDiscoveredPairRequest(context.Context, contracts.PairDiscoveredRequest) (contracts.PairRequest, error) {
	return contracts.PairRequest{}, &dataclient.RateLimitedError{Msg: "a request from this address is already waiting on this host; try again after 12:25:07 (in 4m0s)", RetryAfter: 4 * time.Minute}
}

// Round-1 review S5: whatever a window's renewals say, it ends 30 minutes
// after it was opened.
func TestWindowAbsoluteCap(t *testing.T) {
	f := newFixture(t)
	now := time.Unix(10_000, 0)
	f.srv.opts.Now = func() time.Time { return now }
	f.srv.SetWindow(contracts.Discoverable{OpenedAt: now.Add(-25 * time.Minute), Until: now.Add(10 * time.Minute)})
	if got := f.srv.DiscoverableUntil(); !got.Equal(now.Add(5 * time.Minute)) {
		t.Fatalf("until %s", got)
	}
	closed := now
	f.srv.SetWindow(contracts.Discoverable{OpenedAt: now.Add(-time.Minute), Until: now.Add(5 * time.Minute), ClosedAt: &closed})
	if f.srv.DiscoverableNow() {
		t.Fatal("a closed window is open")
	}
}

// Round-1 review S4: the Tailscale listener closes, at accept, anything
// from outside Tailscale's ranges (a packet for the 100.x address that
// came over the LAN), whether or not the check is on.
func TestTailnetOnlyListener(t *testing.T) {
	f := newFixture(t)
	f.srv.SetRequireTailnet(false)
	ts := frontOn(t, f, NewBudget(DefaultListenLimits), Net{ID: "tailscale", Tailscale: true, TailnetOnly: true})
	c, err := net.Dial("tcp", addrOf(ts))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	if !closedWithin(c, time.Second) {
		t.Fatal("a connection from 127.0.0.1 was kept on the Tailscale listener")
	}
	if !inTailnetRanges("100.101.4.7") || !inTailnetRanges("fd7a:115c:a1e0::1") || inTailnetRanges("192.168.1.2") {
		t.Fatal("ranges")
	}
}

// Round-1 review S6: a device whose grant is above what the tailnet
// policy's capability named (or that the policy didn't name at all) is
// noted once: logged and device.level_uncapped.
func TestUncappedDevicesAreNoted(t *testing.T) {
	f := newFixture(t)
	f.dirs.pair(f.ids.mac, "mac", contracts.GrantAdmin, contracts.PeerCeilings{})
	for range 3 {
		f.do(t, "100.0.0.1", "GET", "/mesh/hello", nil) // capability control, grant admin
		f.do(t, "100.0.0.3", "GET", "/mesh/hello", nil) // no capability (the same device's key)
	}
	f.serve(t, req{from: "100.0.0.1", as: f.ids.phone, method: "GET", target: "/mesh/hello"}) // view under control: no rise
	time.Sleep(50 * time.Millisecond)
	var got []contracts.DeviceUncapped
	for _, e := range f.audit.envelopes() {
		if e.Type == contracts.DeviceLevelUncapped {
			var u contracts.DeviceUncapped
			_ = json.Unmarshal(e.Payload, &u)
			got = append(got, u)
		}
	}
	if len(got) != 1 || got[0].Name != "mac" || got[0].Level != contracts.GrantAdmin || got[0].Capability != contracts.GrantControl || !got[0].HadCapability {
		t.Fatalf("notices %+v", got)
	}
}

// The refusal of an unpaired key never suggests accepting it: a host's log
// fills with strangers' probes outside its window.
func TestUnpairedRefusalDoesntSuggestAccepting(t *testing.T) {
	f := newFixture(t)
	w := f.serve(t, req{from: "100.0.0.1", as: f.ids.stranger, method: "GET", target: "/mesh/discover"})
	if strings.Contains(w.Body.String(), "accept") {
		t.Fatalf("%s", w.Body)
	}
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}
func (s *syncBuffer) String() string { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

// Round-2 review R1: someone in the middle posing as a joiner learns this
// host's code for each commitment before revealing, and can abandon until
// it matches. So a window takes at most maxCommitsPerWindow commitments
// (each a one-in-a-million chance: at most ~1e-5 per window), records
// each as device.pair_started, and closes itself, telling its user and the
// Data Service, after maxAbandoned are dropped unrevealed.
func TestCommitmentsCappedPerWindow(t *testing.T) {
	if bound := float64(maxCommitsPerWindow) / 1e6; bound > 1e-5 {
		t.Fatalf("a grinding MITM's chance per window is %g, over 1e-5", bound)
	}
	f, _, _ := newPairFixture(t)
	f.srv.SetRequireTailnet(false)
	opened := time.Now()
	f.srv.SetWindow(contracts.Discoverable{OpenedAt: opened, Until: opened.Add(5 * time.Minute)})
	commit := func(i int) *httptest.ResponseRecorder {
		f.srv.pairLimits = newPairLimits(time.Now)
		nonce, _ := paircode.NewNonce()
		return f.serve(t, req{from: "100.0.1." + strconv.Itoa(i), as: f.ids.stranger, method: "POST", target: "/mesh/pair", body: commitBody(t, f.ids.stranger, f.ids.host, nonce, false)})
	}
	for i := 1; i <= maxCommitsPerWindow; i++ {
		if w := commit(i); w.Code != http.StatusOK {
			t.Fatalf("commitment %d: %d %s", i, w.Code, w.Body)
		}
	}
	if w := commit(99); w.Code != http.StatusTooManyRequests || errorCode(t, w) != "too_many_pairings" {
		t.Fatalf("past the cap: %d %s", w.Code, w.Body)
	}
	time.Sleep(50 * time.Millisecond)
	started := 0
	for _, e := range f.audit.envelopes() {
		if e.Type == contracts.DevicePairStarted {
			started++
		}
	}
	if started != maxCommitsPerWindow {
		t.Fatalf("%d device.pair_started for %d commitments", started, maxCommitsPerWindow)
	}
	// A new window starts afresh.
	f.srv.SetWindow(contracts.Discoverable{OpenedAt: opened.Add(time.Second), Until: opened.Add(5 * time.Minute)})
	if w := commit(1); w.Code != http.StatusOK {
		t.Fatalf("a new window: %d", w.Code)
	}
}

func TestAbandonedPairingsCloseTheWindow(t *testing.T) {
	f, _, _ := newPairFixture(t)
	var closedFor []string
	var mu sync.Mutex
	f.srv.opts.CloseWindow = func(reason string) { mu.Lock(); closedFor = append(closedFor, reason); mu.Unlock() }
	f.srv.SetRequireTailnet(false)
	var clockMu sync.Mutex
	now := time.Now()
	f.srv.opts.Now = func() time.Time { clockMu.Lock(); defer clockMu.Unlock(); return now }
	advance := func(d time.Duration) { clockMu.Lock(); now = now.Add(d); clockMu.Unlock() }
	opened := f.srv.opts.Now()
	win := contracts.Discoverable{OpenedAt: opened, Until: opened.Add(20 * time.Minute)}
	f.srv.SetWindow(win)
	commit := func(as *devicekey.Identity, from string) string {
		t.Helper()
		f.srv.pairLimits = newPairLimits(time.Now)
		nonce, _ := paircode.NewNonce()
		w := f.serve(t, req{from: from, as: as, method: "POST", target: "/mesh/pair", body: commitBody(t, as, f.ids.host, nonce, false)})
		var c PairCommitted
		_ = json.Unmarshal(w.Body.Bytes(), &c)
		return c.CommitID
	}
	reveal := func(as *devicekey.Identity, from, id string) int {
		f.srv.pairLimits = newPairLimits(time.Now)
		nonce, _ := paircode.NewNonce()
		return f.serve(t, req{from: from, as: as, method: "POST", target: "/mesh/pair", body: revealBody(id, nonce)}).Code
	}

	// Final pass 3: a real joiner retrying Add (the same key, the same
	// address) abandons nothing.
	for range maxAbandoned + 1 {
		commit(f.ids.stranger, "100.0.0.1")
	}
	// Final pass 2: reveals of commit_ids this host never issued spend
	// nothing and close nothing.
	for i := range maxAbandoned + 1 {
		if code := reveal(f.ids.stranger, "100.0.0.1", "pc_invented"+strconv.Itoa(i)); code != http.StatusUnprocessableEntity {
			t.Fatalf("an invented reveal: %d", code)
		}
	}
	if !f.srv.DiscoverableNow() {
		t.Fatal("closed by retries or invented reveals")
	}

	// Real commitments revealed wrong count; ones left to expire are
	// counted when they expire (final pass 4), with nothing else arriving.
	fresh := newDevices(t) // unpaired keys
	if id := commit(fresh.mac, "100.0.0.2"); id == "" || reveal(fresh.mac, "100.0.0.2", id) != http.StatusUnprocessableEntity {
		t.Fatalf("a wrong nonce was accepted (commit %q)", id)
	}
	if commit(fresh.phone, "100.0.0.3") == "" || commit(fresh.chatter, "100.0.0.4") == "" {
		t.Fatal("not committed")
	}
	if !f.srv.DiscoverableNow() {
		t.Fatal("closed before the commitments expired")
	}
	advance(commitTTL + time.Second)
	// A window-state read (GET /mesh/discover) sees the expiries.
	f.serve(t, req{from: "100.0.0.9", as: fresh.idle, method: "GET", target: "/mesh/discover"})
	if f.srv.DiscoverableNow() {
		t.Fatal("the window stayed open after abandoned pairings")
	}
	time.Sleep(50 * time.Millisecond)
	mu.Lock()
	if len(closedFor) != 1 || closedFor[0] != "abandoned_pairings" {
		t.Fatalf("closed at the Data Service for %v", closedFor)
	}
	mu.Unlock()
	alerted := false
	for _, e := range f.audit.envelopes() {
		if e.Type == contracts.DevicePairingAbandoned {
			alerted = true
		}
	}
	if !alerted {
		t.Fatal("no device.pairing_abandoned")
	}
	// The same window's renewal doesn't reopen it.
	win.Until = opened.Add(25 * time.Minute)
	f.srv.SetWindow(win)
	if f.srv.DiscoverableNow() {
		t.Fatal("a renewal reopened a window closed for abandoned pairings")
	}
}

// The expiry is counted by itself, a minute on, with no request at all.
func TestAbandonedCountedAtExpiry(t *testing.T) {
	c := &commitStore{}
	now := time.Now()
	for i, fp := range []devicekey.Fingerprint{"A", "B", "C"} {
		if _, _, ok := c.put(commitEntry{fp: fp, source: strconv.Itoa(i), expires: now.Add(commitTTL)}, now); !ok {
			t.Fatal("put")
		}
	}
	if n := c.sweep(now); n != 0 {
		t.Fatalf("%d abandoned before expiry", n)
	}
	if n := c.sweep(now.Add(commitTTL)); n != 3 {
		t.Fatalf("%d abandoned at expiry", n)
	}
	if n := c.sweep(now.Add(2 * commitTTL)); n != 3 {
		t.Fatalf("counted twice: %d", n)
	}
}

// A paired device's key completing the handshake isn't a failed one, even
// when no request follows (a Router built with Go 1.26 spoke HTTP/1.1 over
// a connection that had agreed to HTTP/2, 2026-11-25): its address isn't
// shut out. A stranger's handshakes still count.
func TestPairedHandshakeIsNotAFailedOne(t *testing.T) {
	f := newFixture(t)
	f.tn.set("127.0.0.1", full)
	b := NewBudget(DefaultListenLimits)
	front := frontOn(t, f, b, lanNet)
	cfg, err := devicekey.ClientConfig(f.ids.mac, f.ids.host.Fingerprint())
	if err != nil {
		t.Fatal(err)
	}
	cfg.NextProtos = []string{"h2"}
	for range 2 * DefaultListenLimits.FailedPerMinute {
		c, err := tls.Dial("tcp", addrOf(front), cfg)
		if err != nil {
			t.Fatalf("paired handshake: %v", err)
		}
		if p := c.ConnectionState().NegotiatedProtocol; p != "h2" {
			t.Fatalf("negotiated %q, want h2", p)
		}
		// Not HTTP/2's preface: the server ends the connection.
		_, _ = c.Write([]byte("GET /mesh/hello HTTP/1.1\r\nHost: x\r\n\r\n"))
		_, _ = c.Read(make([]byte, 64))
		_ = c.Close()
	}
	time.Sleep(200 * time.Millisecond)
	if b.coolingDown("127.0.0.1") {
		t.Fatal("a paired device's completed handshakes shut its address out")
	}
}

// Round-2 review T7: with the shipped limits (10 a minute) and the real
// clock, the cool-down starts and is logged.
func TestFailedHandshakeCooldownDefaults(t *testing.T) {
	f := newFixture(t)
	f.tn.set("127.0.0.1", full)
	logs := &syncBuffer{}
	prev := log.Writer()
	log.SetOutput(logs)
	defer log.SetOutput(prev)
	b := NewBudget(DefaultListenLimits)
	front := frontOn(t, f, b, lanNet)
	for range DefaultListenLimits.FailedPerMinute {
		c, err := tls.Dial("tcp", addrOf(front), &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS13}) //nolint:gosec // test client
		if err == nil {
			_, _ = c.Read(make([]byte, 1))
			_ = c.Close()
		}
	}
	deadline := time.Now().Add(3 * time.Second)
	for !b.coolingDown("127.0.0.1") {
		if time.Now().After(deadline) {
			t.Fatalf("no cool-down after %d failed handshakes:\n%s", DefaultListenLimits.FailedPerMinute, logs.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !strings.Contains(logs.String(), "127.0.0.1: 10 connections in a minute closed without a request (failed handshakes): closing its connections at accept for 1m0s") {
		t.Fatalf("the cool-down wasn't logged:\n%s", logs.String())
	}
}
