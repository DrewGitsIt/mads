package api

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/devicekey"
	"github.com/drewkidwell/mads/pkg/localauth"
	"github.com/drewkidwell/mads/pkg/paircode"
	"github.com/drewkidwell/mads/services/data/internal/store"
)

type pairClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *pairClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *pairClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

type pairEnv struct {
	srv   *httptest.Server
	log   *store.Log
	peers *store.Registry[contracts.Peer]
	clock *pairClock
}

// newPairEnv is the pairing routes, with the peer registry, on a clock
// the test moves, as the front door hands them install-token requests.
func newPairEnv(t *testing.T) *pairEnv {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := store.Migrate(db, store.Migrations()); err != nil {
		t.Fatal(err)
	}
	l := store.NewLog(db)
	peers := store.NewPeers(l)
	clock := &pairClock{now: time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)}
	mux := http.NewServeMux()
	registryRoutes(mux, "peers", peers)
	pairingRoutes(mux, l, peers, clock.Now)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Test-No-Install") == "" {
			r = r.WithContext(localauth.WithPrincipal(r.Context(), localauth.Principal{Kind: localauth.Install}))
		}
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	return &pairEnv{srv: srv, log: l, peers: peers, clock: clock}
}

func (e *pairEnv) call(t *testing.T, method, path string, in any, header map[string]string) (int, []byte) {
	t.Helper()
	var body io.Reader
	if in != nil {
		raw, _ := json.Marshal(in)
		body = bytes.NewReader(raw)
	}
	req, _ := http.NewRequest(method, e.srv.URL+path, body)
	req.Header.Set("X-Mads-Actor", "test")
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := e.srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, raw
}

func (e *pairEnv) newCode(t *testing.T) (contracts.PairingCode, string) {
	t.Helper()
	secret, _ := paircode.NewSecret()
	hash := paircode.Hash(secret)
	code, raw := e.call(t, "POST", "/pairing/codes", contracts.PairingCodeCreate{SecretHash: hash}, nil)
	if code != http.StatusCreated {
		t.Fatalf("create code: %d %s", code, raw)
	}
	var c contracts.PairingCode
	_ = json.Unmarshal(raw, &c)
	return c, hash
}

func joinerFP(seed byte) string {
	return string(devicekey.FingerprintOf(ed25519.PublicKey(bytes.Repeat([]byte{seed}, ed25519.PublicKeySize))))
}

func (e *pairEnv) redeem(t *testing.T, hash string, seed byte) (int, []byte) {
	t.Helper()
	return e.call(t, "POST", "/pairing/redeem", contracts.PairRedeem{
		SecretHash: hash, Fingerprint: joinerFP(seed), Name: "Drew's iPhone", Platform: "iOS", TailnetNode: "drews-iphone.tail4c2e.ts.net",
		MeshURL: "http://mac.example.ts.net:17380", HostsSessions: true, Remote: "100.64.0.7:51234",
	}, nil)
}

// A code works once, within its five minutes; every bad code gets the
// same answer, byte for byte: no oracle.
func TestPairingCodeSingleUseExpiryNoOracle(t *testing.T) {
	e := newPairEnv(t)
	_, hash := e.newCode(t)
	code, raw := e.redeem(t, hash, 1)
	if code != http.StatusCreated {
		t.Fatalf("redeem: %d %s", code, raw)
	}
	var req contracts.PairRequest
	_ = json.Unmarshal(raw, &req)
	if req.State != contracts.PairPending || req.Name != "Drew-s-iPhone" || req.Platform != "ios" || !req.HostsSessions ||
		req.MeshURL != "http://mac.example.ts.net:17380" || !req.ExpiresAt.Equal(e.clock.Now().Add(contracts.PairRequestTTL)) {
		t.Fatalf("request = %+v", req)
	}

	used, usedBody := e.redeem(t, hash, 2)
	wrongSecret, _ := paircode.NewSecret()
	wrong, wrongBody := e.redeem(t, paircode.Hash(wrongSecret), 2)
	junk, junkBody := e.redeem(t, "not-a-hash", 2)
	_, expiring := e.newCode(t)
	e.clock.Advance(contracts.PairingCodeTTL)
	expired, expiredBody := e.redeem(t, expiring, 2)
	for name, got := range map[string]int{"used": used, "wrong": wrong, "junk": junk, "expired": expired} {
		if got != http.StatusForbidden {
			t.Errorf("%s code: %d, want 403", name, got)
		}
	}
	for name, b := range map[string][]byte{"wrong": wrongBody, "junk": junkBody, "expired": expiredBody} {
		if !bytes.Equal(b, usedBody) {
			t.Errorf("%s code's answer differs from a used one's:\n%s\n%s", name, b, usedBody)
		}
	}
	if !strings.Contains(string(usedBody), `"pairing_refused"`) {
		t.Errorf("refusal = %s", usedBody)
	}
}

// At most MaxPairingCodes are outstanding: another retires the oldest.
func TestPairingCodesCapped(t *testing.T) {
	e := newPairEnv(t)
	var hashes []string
	for range contracts.MaxPairingCodes + 1 {
		_, h := e.newCode(t)
		hashes = append(hashes, h)
		e.clock.Advance(time.Second)
	}
	if code, _ := e.redeem(t, hashes[0], 1); code != http.StatusForbidden {
		t.Fatalf("oldest code still works: %d", code)
	}
	if code, _ := e.redeem(t, hashes[len(hashes)-1], 1); code != http.StatusCreated {
		t.Fatalf("newest code: %d", code)
	}
	// A retired code can also be deleted by id while unused.
	c, h := e.newCode(t)
	if code, _ := e.call(t, "DELETE", "/pairing/codes/"+c.ID, nil, nil); code != http.StatusNoContent {
		t.Fatalf("delete: %d", code)
	}
	if code, _ := e.redeem(t, h, 3); code != http.StatusForbidden {
		t.Fatalf("deleted code works: %d", code)
	}
}

// The secret never reaches the Data Service, and its hash isn't in any
// event either.
func TestPairingEventsCarryNoSecret(t *testing.T) {
	e := newPairEnv(t)
	_, hash := e.newCode(t)
	if code, _ := e.redeem(t, hash, 1); code != http.StatusCreated {
		t.Fatal(code)
	}
	evs, err := e.log.Query(t.Context(), store.Filter{Types: []string{"device.*"}})
	if err != nil {
		t.Fatal(err)
	}
	types := map[contracts.EventType]bool{}
	for _, ev := range evs {
		types[ev.Type] = true
		if strings.Contains(string(ev.Payload), hash) {
			t.Errorf("%s carries the secret's hash: %s", ev.Type, ev.Payload)
		}
	}
	if !types[contracts.DeviceCodeCreated] || !types[contracts.DevicePairRequested] {
		t.Fatalf("events = %v", types)
	}
}

func (e *pairEnv) pendingRequest(t *testing.T, seed byte) contracts.PairRequest {
	t.Helper()
	_, hash := e.newCode(t)
	code, raw := e.redeem(t, hash, seed)
	if code != http.StatusCreated {
		t.Fatalf("redeem: %d %s", code, raw)
	}
	var req contracts.PairRequest
	_ = json.Unmarshal(raw, &req)
	return req
}

// Approving pins the device (how "code", not viewed, the level given,
// what it reported), once; admin is never granted this way, and only the
// host's own user answers.
func TestPairApprove(t *testing.T) {
	e := newPairEnv(t)
	req := e.pendingRequest(t, 1)
	path := "/pairing/requests/" + req.ID + "/approve"
	if code, _ := e.call(t, "POST", path, contracts.PairDecision{Level: contracts.GrantAdmin}, nil); code != http.StatusUnprocessableEntity {
		t.Fatalf("approve at admin: %d, want 422", code)
	}
	if code, _ := e.call(t, "POST", path, contracts.PairDecision{Level: contracts.GrantChat}, map[string]string{"X-Mads-Mesh-Peer": "mac"}); code != http.StatusForbidden {
		t.Fatalf("relayed approve: %d, want 403", code)
	}
	if code, _ := e.call(t, "POST", path, contracts.PairDecision{Level: contracts.GrantChat}, map[string]string{"X-Test-No-Install": "1"}); code != http.StatusForbidden {
		t.Fatalf("approve without the install token: %d, want 403", code)
	}
	code, raw := e.call(t, "POST", path, contracts.PairDecision{Level: contracts.GrantChat}, nil)
	if code != http.StatusOK {
		t.Fatalf("approve: %d %s", code, raw)
	}
	var done contracts.PairRequest
	_ = json.Unmarshal(raw, &done)
	if done.State != contracts.PairApproved || done.Level != contracts.GrantChat || done.PeerID == "" {
		t.Fatalf("approved = %+v", done)
	}
	p, err := e.peers.Get(t.Context(), done.PeerID)
	if err != nil {
		t.Fatal(err)
	}
	if p.Fingerprint != req.Fingerprint || p.IsViewed() || p.Level() != contracts.GrantChat || p.Pairing.How != contracts.PairCode ||
		p.Remote == nil || p.Remote.Platform != "ios" || p.Remote.HostsSessions == nil || !*p.Remote.HostsSessions || p.MeshURL != req.MeshURL {
		t.Fatalf("pinned = %+v remote %+v", p, p.Remote)
	}
	if code, _ := e.call(t, "POST", path, contracts.PairDecision{Level: contracts.GrantChat}, nil); code != http.StatusConflict {
		t.Fatalf("approve twice: %d, want 409", code)
	}
	// The joiner's poll reads the outcome.
	code, raw = e.call(t, "GET", "/pairing/requests/"+req.ID, nil, nil)
	if code != http.StatusOK || !strings.Contains(string(raw), `"approved"`) {
		t.Fatalf("poll: %d %s", code, raw)
	}

	// A second device giving the same name is pinned under another, and
	// its prompt says the name is taken; the verified node name is
	// beside the claimed one.
	req2 := e.pendingRequest(t, 2)
	var list []contracts.PairRequest
	_, raw = e.call(t, "GET", "/pairing/requests", nil, nil)
	_ = json.Unmarshal(raw, &list)
	collides := map[string]bool{}
	for _, q := range list {
		collides[q.ID] = q.NameCollides
		if q.TailnetNode != "drews-iphone.tail4c2e.ts.net" {
			t.Fatalf("tailnet node = %q", q.TailnetNode)
		}
	}
	if !collides[req2.ID] || collides[req.ID] {
		t.Fatalf("name_collides = %v", collides)
	}
	code, raw = e.call(t, "POST", "/pairing/requests/"+req2.ID+"/approve", contracts.PairDecision{Level: "none"}, nil)
	if code != http.StatusOK {
		t.Fatalf("approve second: %d %s", code, raw)
	}
	_ = json.Unmarshal(raw, &done)
	p2, _ := e.peers.Get(t.Context(), done.PeerID)
	if p2.Name != "Drew-s-iPhone-2" || p2.Grant == nil || p2.Level() != contracts.GrantNone {
		t.Fatalf("second = %q %+v", p2.Name, p2.Grant)
	}
	// The answer names it as pinned, not as it claimed.
	if done.Name != "Drew-s-iPhone-2" {
		t.Fatalf("approve answered name %q", done.Name)
	}

	// The same device again (it redeemed a second code) re-pairs: its
	// request says so, and approval updates its row in place (F6 R2).
	again := e.pendingRequest(t, 1)
	if again.Repair == nil || again.Repair.PeerID != p.ID || again.Repair.Level != contracts.GrantChat {
		t.Fatalf("repair = %+v", again.Repair)
	}
	code, raw = e.call(t, "POST", "/pairing/requests/"+again.ID+"/approve", contracts.PairDecision{Level: contracts.GrantView}, nil)
	if code != http.StatusOK {
		t.Fatalf("re-pair an already-paired device: %d %s", code, raw)
	}
	_ = json.Unmarshal(raw, &done)
	if done.PeerID != p.ID {
		t.Fatalf("re-pair pinned %s, want the row %s", done.PeerID, p.ID)
	}
}

func TestPairDeclineAndExpiry(t *testing.T) {
	e := newPairEnv(t)
	req := e.pendingRequest(t, 1)
	code, raw := e.call(t, "POST", "/pairing/requests/"+req.ID+"/decline", nil, nil)
	if code != http.StatusOK || !strings.Contains(string(raw), `"declined"`) {
		t.Fatalf("decline: %d %s", code, raw)
	}
	if code, _ := e.call(t, "POST", "/pairing/requests/"+req.ID+"/approve", contracts.PairDecision{Level: contracts.GrantView}, nil); code != http.StatusConflict {
		t.Fatalf("approve after decline: %d, want 409", code)
	}
	if list, _ := e.peers.List(t.Context()); len(list) != 0 {
		t.Fatalf("declined device pinned: %+v", list)
	}

	late := e.pendingRequest(t, 2)
	e.clock.Advance(contracts.PairRequestTTL)
	code, raw = e.call(t, "GET", "/pairing/requests/"+late.ID, nil, nil)
	if code != http.StatusOK || !strings.Contains(string(raw), `"expired"`) {
		t.Fatalf("expired poll: %d %s", code, raw)
	}
	if code, _ := e.call(t, "POST", "/pairing/requests/"+late.ID+"/approve", contracts.PairDecision{Level: contracts.GrantView}, nil); code != http.StatusConflict {
		t.Fatalf("approve after expiry: %d, want 409", code)
	}
	// Answered requests are forgotten after a while.
	e.clock.Advance(decidedMemory + time.Minute)
	if code, _ := e.call(t, "GET", "/pairing/requests/"+late.ID, nil, nil); code != http.StatusNotFound {
		t.Fatalf("old request: %d, want 404", code)
	}
}

// A name the approver types is cleaned like a device's own, and the
// store refuses control or formatting characters and overlong names from
// anywhere.
func TestPairApproveCleansTheName(t *testing.T) {
	e := newPairEnv(t)
	req := e.pendingRequest(t, 1)
	code, raw := e.call(t, "POST", "/pairing/requests/"+req.ID+"/approve", contracts.PairDecision{Level: contracts.GrantView, Name: "\u202eevil/" + strings.Repeat("x", 200)}, nil)
	if code != http.StatusOK {
		t.Fatalf("approve: %d %s", code, raw)
	}
	var done contracts.PairRequest
	_ = json.Unmarshal(raw, &done)
	p, _ := e.peers.Get(t.Context(), done.PeerID)
	if strings.ContainsRune(p.Name, '\u202e') || strings.Contains(p.Name, "/") || len(p.Name) > 64 {
		t.Fatalf("name %q", p.Name)
	}
	no := false
	for _, name := range []string{"ma\u202ec", strings.Repeat("y", 65), "tab\there"} {
		if code, _ := e.call(t, "POST", "/peers", contracts.Peer{Name: name, Viewed: &no, Fingerprint: joinerFP(9)}, nil); code != http.StatusUnprocessableEntity {
			t.Errorf("%q: %d, want 422", name, code)
		}
	}
}
