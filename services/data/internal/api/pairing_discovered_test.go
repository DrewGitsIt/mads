package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
)

func (e *pairEnv) discovered(t *testing.T, seed byte, code string) (int, contracts.PairRequest) {
	t.Helper()
	return e.discoveredFrom(t, seed, code, "100.70.1."+strconv.Itoa(int(seed))+":51234")
}

func (e *pairEnv) discoveredFrom(t *testing.T, seed byte, code, remote string) (int, contracts.PairRequest) {
	t.Helper()
	status, raw := e.call(t, "POST", "/pairing/discovered", contracts.PairDiscoveredRequest{
		Fingerprint: joinerFP(seed), Name: "Alexs-MacBook-Air", Platform: "darwin", CheckCode: code,
		Addresses: []contracts.NetAddress{{Via: "direct", Addr: "100.70.1.1:17380", Kind: "overlay", Label: "Tailscale"}, {Via: "relay", Addr: "r:443"}},
		Network:   "tailscale", NetworkLabel: "Tailscale", Remote: remote,
	}, nil)
	var out contracts.PairRequest
	_ = json.Unmarshal(raw, &out)
	return status, out
}

// A discovered pairing request (no secret) is accepted only while this
// host's setup window is open, at most three wait at once, and approving
// one pins the device as discovered, with its addresses.
func TestDiscoveredPairRequests(t *testing.T) {
	e := newPairEnv(t)
	ctx := t.Context()
	// Window closed: refused.
	if code, _ := e.discovered(t, 1, "482913"); code != http.StatusForbidden {
		t.Fatalf("closed window: %d", code)
	}
	if _, err := e.log.SetDiscoverable(ctx, time.Now().Add(10*time.Minute), "user", false); err != nil {
		t.Fatal(err)
	}
	if code, _ := e.discovered(t, 1, "48291"); code != http.StatusUnprocessableEntity {
		t.Fatalf("bad check code: %d", code)
	}
	code, req := e.discovered(t, 1, "482913")
	if code != http.StatusCreated || req.Mode != contracts.PairModeDiscovered || req.CheckCode != "482913" || len(req.Addresses) != 1 || req.Network != "tailscale" {
		t.Fatalf("first: %d %+v", code, req)
	}
	// Asking again replaces its own pending request.
	_, again := e.discovered(t, 1, "111111")
	if got, _ := e.call(t, "GET", "/pairing/requests/"+req.ID, nil, nil); got != http.StatusOK {
		t.Fatal(got)
	}
	_, raw := e.call(t, "GET", "/pairing/requests/"+req.ID, nil, nil)
	var old contracts.PairRequest
	_ = json.Unmarshal(raw, &old)
	if old.State != contracts.PairExpired {
		t.Fatalf("superseded request %+v", old)
	}
	// Three pending at most.
	for seed := byte(2); seed <= 3; seed++ {
		if code, _ := e.discovered(t, seed, "222222"); code != http.StatusCreated {
			t.Fatalf("seed %d: %d", seed, code)
		}
	}
	if code, _ := e.discovered(t, 4, "333333"); code != http.StatusTooManyRequests {
		t.Fatalf("fourth pending: %d", code)
	}
	// Approving pins it as discovered, with its addresses.
	code2, raw2 := e.call(t, "POST", "/pairing/requests/"+again.ID+"/approve", contracts.PairDecision{Level: contracts.GrantControl, CheckCodeConfirmed: true}, nil)
	if code2 != http.StatusOK {
		t.Fatalf("approve: %d %s", code2, raw2)
	}
	row, err := e.peers.GetByName(ctx, "Alexs-MacBook-Air")
	if err != nil {
		t.Fatal(err)
	}
	if row.Pairing == nil || row.Pairing.How != contracts.PairDiscovered || len(row.Addresses) != 1 || row.Addresses[0].Source != contracts.SourceDiscovered ||
		row.MeshURL != "http://100.70.1.1:17380" || row.Level() != contracts.GrantControl {
		t.Fatalf("pinned %+v", row)
	}
	// Closed again: refused.
	if _, err := e.log.SetDiscoverable(ctx, time.Time{}, "user", false); err != nil {
		t.Fatal(err)
	}
	if code, _ := e.discovered(t, 5, "444444"); code != http.StatusForbidden {
		t.Fatalf("after closing: %d", code)
	}
	// A relayed caller never makes one.
	if code, _ := e.call(t, "POST", "/pairing/discovered", contracts.PairDiscoveredRequest{Fingerprint: joinerFP(6), CheckCode: "123456"}, map[string]string{contracts.MeshRelayHeader: "x"}); code != http.StatusForbidden {
		t.Fatalf("relayed: %d", code)
	}
}

// A code redeemed with the joiner's addresses carries them to the pin.
func TestRedeemCarriesAddresses(t *testing.T) {
	e := newPairEnv(t)
	_, secretHash := e.newCode(t)
	status, raw := e.call(t, "POST", "/pairing/redeem", contracts.PairRedeem{SecretHash: secretHash, Fingerprint: joinerFP(9), Name: "spark",
		Addresses: []contracts.NetAddress{{Via: "direct", Addr: "192.168.1.30:17380", Kind: "lan"}, {Via: "direct", Addr: "169.254.169.254:17380"}},
		Network:   "lan:192.168.1.0/24/192.168.1.1", NetworkLabel: "Wired network"}, nil)
	if status != http.StatusCreated {
		t.Fatalf("%d %s", status, raw)
	}
	var req contracts.PairRequest
	_ = json.Unmarshal(raw, &req)
	if len(req.Addresses) != 1 || req.Mode != contracts.PairModeCode || req.NetworkLabel != "Wired network" {
		t.Fatalf("%+v", req)
	}
	if code, _ := e.call(t, "POST", "/pairing/requests/"+req.ID+"/approve", contracts.PairDecision{Level: contracts.GrantView}, nil); code != http.StatusOK {
		t.Fatal(code)
	}
	row, err := e.peers.GetByName(t.Context(), "spark")
	if err != nil || len(row.Addresses) != 1 || row.Addresses[0].Source != contracts.SourceJoiner || row.Pairing.How != contracts.PairCode {
		t.Fatalf("%+v %v", row, err)
	}
}

// Round-1 review M2: throwaway keys from one address hold one pending slot,
// never all three; an IPv6 address counts by its /64; the 429 says when a
// slot frees.
func TestDiscoveredOneSlotPerSource(t *testing.T) {
	e := newPairEnv(t)
	if _, err := e.log.SetDiscoverable(t.Context(), time.Now().Add(10*time.Minute), "user", false); err != nil {
		t.Fatal(err)
	}
	if code, _ := e.discoveredFrom(t, 1, "111111", "192.168.1.66:4000"); code != http.StatusCreated {
		t.Fatalf("first: %d", code)
	}
	for seed := byte(2); seed <= 4; seed++ {
		status, raw := e.call(t, "POST", "/pairing/discovered", contracts.PairDiscoveredRequest{Fingerprint: joinerFP(seed), CheckCode: "222222", Remote: "192.168.1.66:4001"}, nil)
		if status != http.StatusTooManyRequests || !strings.Contains(string(raw), "already waiting") || !strings.Contains(string(raw), "retry_after") {
			t.Fatalf("throwaway key %d from the same address: %d %s", seed, status, raw)
		}
	}
	// Someone else can still ask.
	if code, _ := e.discoveredFrom(t, 5, "333333", "192.168.1.30:4000"); code != http.StatusCreated {
		t.Fatalf("another address: %d", code)
	}
	// IPv6: the same /64 is one source.
	if code, _ := e.discoveredFrom(t, 6, "444444", "[2001:db8:1:2::10]:4000"); code != http.StatusCreated {
		t.Fatalf("v6: %d", code)
	}
	if code, _ := e.discoveredFrom(t, 7, "555555", "[2001:db8:1:2::99]:4000"); code != http.StatusTooManyRequests {
		t.Fatalf("v6 same /64: %d", code)
	}
}

// Round-1 tester T2: closing the window early ends its pending discovered
// requests within a minute; past that they can't be approved.
func TestClosingTheWindowExpiresDiscoveredRequests(t *testing.T) {
	e := newPairEnv(t)
	e.clock.Advance(time.Until(e.clock.Now()) * -1) // the fake clock at real time
	if _, err := e.log.SetDiscoverable(t.Context(), time.Now().Add(10*time.Minute), "user", false); err != nil {
		t.Fatal(err)
	}
	code, req := e.discovered(t, 1, "111111")
	if code != http.StatusCreated {
		t.Fatal(code)
	}
	if _, err := e.log.SetDiscoverable(t.Context(), time.Time{}, "user", false); err != nil {
		t.Fatal(err)
	}
	_, raw := e.call(t, "GET", "/pairing/requests/"+req.ID, nil, nil)
	var got contracts.PairRequest
	_ = json.Unmarshal(raw, &got)
	if got.ExpiresAt.After(time.Now().Add(windowGrace + time.Second)) {
		t.Fatalf("still pending until %s after the window closed", got.ExpiresAt)
	}
	e.clock.Advance(2 * time.Minute)
	if status, raw := e.call(t, "POST", "/pairing/requests/"+req.ID+"/approve", contracts.PairDecision{Level: contracts.GrantView}, nil); status != http.StatusConflict || !strings.Contains(string(raw), "expired") {
		t.Fatalf("approve after the window closed: %d %s", status, raw)
	}
}

// Round-1 review M3: Tailscale's vouch says "one of this user's nodes", not
// which one: with two discovered requests waiting, or a name that's
// another device's, both need their check codes, and an approval then
// records the vouch as withheld (the joiner learns it).
func TestVouchWithheld(t *testing.T) {
	e := newPairEnv(t)
	if _, err := e.log.SetDiscoverable(t.Context(), time.Now().Add(10*time.Minute), "user", false); err != nil {
		t.Fatal(err)
	}
	vouched := func(seed byte, name, remote string) contracts.PairRequest {
		status, raw := e.call(t, "POST", "/pairing/discovered", contracts.PairDiscoveredRequest{Fingerprint: joinerFP(seed), Name: name, CheckCode: "111111", Vouched: true, Remote: remote}, nil)
		if status != http.StatusCreated {
			t.Fatalf("%d %s", status, raw)
		}
		var r contracts.PairRequest
		_ = json.Unmarshal(raw, &r)
		return r
	}
	list := func() map[string]contracts.PairRequest {
		_, raw := e.call(t, "GET", "/pairing/requests", nil, nil)
		var l []contracts.PairRequest
		_ = json.Unmarshal(raw, &l)
		out := map[string]contracts.PairRequest{}
		for _, r := range l {
			out[r.ID] = r
		}
		return out
	}
	a := vouched(1, "mac", "100.70.1.1:1")
	if got := list()[a.ID]; !got.Vouched {
		t.Fatalf("alone: %+v", got)
	}
	b := vouched(2, "phone", "100.70.1.2:1")
	for _, id := range []string{a.ID, b.ID} {
		if got := list()[id]; got.Vouched || got.VouchWithheld != "another_request" {
			t.Fatalf("two waiting: %+v", got)
		}
	}
	// R5: approving on the vouch it showed, now withheld, is refused.
	if code, raw := e.call(t, "POST", "/pairing/requests/"+a.ID+"/approve", contracts.PairDecision{Level: contracts.GrantView, ShownVouched: true}, nil); code != http.StatusConflict || !strings.Contains(string(raw), "vouch_withheld") {
		t.Fatalf("approve on a withheld vouch: %d %s", code, raw)
	}
	_, raw := e.call(t, "POST", "/pairing/requests/"+a.ID+"/approve", contracts.PairDecision{Level: contracts.GrantView, CheckCodeConfirmed: true}, nil)
	var approved contracts.PairRequest
	_ = json.Unmarshal(raw, &approved)
	if approved.Vouched || approved.VouchWithheld != "another_request" {
		t.Fatalf("approved while another waited: %+v", approved)
	}
	// A name that's another device's here.
	yes := true
	if err := e.peers.Create(t.Context(), &contracts.Peer{Name: "spark", MeshURL: "http://x.example:17380", Viewed: &yes}, "test"); err != nil {
		t.Fatal(err)
	}
	e.call(t, "POST", "/pairing/requests/"+b.ID+"/decline", struct{}{}, nil)
	c := vouched(3, "spark", "100.70.1.3:1")
	if got := list()[c.ID]; got.Vouched || got.VouchWithheld != "name_collides" {
		t.Fatalf("name collides: %+v", got)
	}
}

// The joiner saying its code didn't match undoes the host's side: a
// pending request is declined, an approved one unpinned; only the joiner
// (by its fingerprint) can say so.
func TestJoinerMismatchUndoesThePairing(t *testing.T) {
	e := newPairEnv(t)
	if _, err := e.log.SetDiscoverable(t.Context(), time.Now().Add(10*time.Minute), "user", false); err != nil {
		t.Fatal(err)
	}
	_, req := e.discovered(t, 1, "111111")
	if code, _ := e.call(t, "POST", "/pairing/requests/"+req.ID+"/mismatch", map[string]string{"fingerprint": joinerFP(2)}, nil); code != http.StatusNotFound {
		t.Fatalf("another key: %d", code)
	}
	if code, _ := e.call(t, "POST", "/pairing/requests/"+req.ID+"/approve", contracts.PairDecision{Level: contracts.GrantControl, CheckCodeConfirmed: true}, nil); code != http.StatusOK {
		t.Fatal(code)
	}
	if _, err := e.peers.GetByName(t.Context(), "Alexs-MacBook-Air"); err != nil {
		t.Fatal(err)
	}
	code, raw := e.call(t, "POST", "/pairing/requests/"+req.ID+"/mismatch", map[string]string{"fingerprint": joinerFP(1)}, nil)
	var out contracts.PairRequest
	_ = json.Unmarshal(raw, &out)
	if code != http.StatusOK || !out.JoinerMismatch || out.State != contracts.PairDeclined {
		t.Fatalf("%d %s", code, raw)
	}
	if _, err := e.peers.GetByName(t.Context(), "Alexs-MacBook-Air"); err == nil {
		t.Fatal("still pinned after the joiner's mismatch")
	}
}

// R5: a discovered request is approved only on a confirmed check code, or
// on a vouch that still stands and was the one shown.
func TestDiscoveredApprovalNeedsWhatWasShown(t *testing.T) {
	e := newPairEnv(t)
	if _, err := e.log.SetDiscoverable(t.Context(), time.Now().Add(10*time.Minute), "user", false); err != nil {
		t.Fatal(err)
	}
	_, unvouched := e.discovered(t, 1, "111111")
	if code, raw := e.call(t, "POST", "/pairing/requests/"+unvouched.ID+"/approve", contracts.PairDecision{Level: contracts.GrantView}, nil); code != http.StatusConflict || !strings.Contains(string(raw), "check_code_required") {
		t.Fatalf("unconfirmed: %d %s", code, raw)
	}
	if code, _ := e.call(t, "POST", "/pairing/requests/"+unvouched.ID+"/approve", contracts.PairDecision{Level: contracts.GrantView, ShownVouched: true}, nil); code != http.StatusConflict {
		t.Fatalf("claimed a vouch it doesn't have: %d", code)
	}
	if code, _ := e.call(t, "POST", "/pairing/requests/"+unvouched.ID+"/approve", contracts.PairDecision{Level: contracts.GrantView, CheckCodeConfirmed: true}, nil); code != http.StatusOK {
		t.Fatalf("confirmed: %d", code)
	}
	status, raw := e.call(t, "POST", "/pairing/discovered", contracts.PairDiscoveredRequest{Fingerprint: joinerFP(2), Name: "mac", CheckCode: "222222", Vouched: true, Remote: "100.70.1.2:1"}, nil)
	var vouched contracts.PairRequest
	_ = json.Unmarshal(raw, &vouched)
	if status != http.StatusCreated || !vouched.Vouched {
		t.Fatalf("%d %s", status, raw)
	}
	if code, _ := e.call(t, "POST", "/pairing/requests/"+vouched.ID+"/approve", contracts.PairDecision{Level: contracts.GrantView, ShownVouched: true}, nil); code != http.StatusOK {
		t.Fatalf("vouched as shown: %d", code)
	}
}

// T6: a request made while another waits is answered withheld from the
// start, so the joiner's screen agrees with the host's.
func TestWithheldFromTheFirstAnswer(t *testing.T) {
	e := newPairEnv(t)
	if _, err := e.log.SetDiscoverable(t.Context(), time.Now().Add(10*time.Minute), "user", false); err != nil {
		t.Fatal(err)
	}
	mk := func(seed byte) contracts.PairRequest {
		_, raw := e.call(t, "POST", "/pairing/discovered", contracts.PairDiscoveredRequest{Fingerprint: joinerFP(seed), Name: "d" + strconv.Itoa(int(seed)), CheckCode: "111111", Vouched: true, Remote: "100.70.1." + strconv.Itoa(int(seed)) + ":1"}, nil)
		var r contracts.PairRequest
		_ = json.Unmarshal(raw, &r)
		return r
	}
	if a := mk(1); !a.Vouched {
		t.Fatalf("alone: %+v", a)
	}
	if b := mk(2); b.Vouched || b.VouchWithheld != "another_request" {
		t.Fatalf("second: %+v", b)
	}
}

// R6: a source the host declined gets no more discovered requests in that
// window, whatever key it uses; another source still can.
func TestDeclinedSourceBlockedForTheWindow(t *testing.T) {
	e := newPairEnv(t)
	e.clock.Advance(time.Until(e.clock.Now()) * -1)
	if _, err := e.log.SetDiscoverable(t.Context(), time.Now().Add(10*time.Minute), "user", false); err != nil {
		t.Fatal(err)
	}
	_, req := e.discoveredFrom(t, 1, "111111", "192.168.1.66:4000")
	e.call(t, "POST", "/pairing/requests/"+req.ID+"/decline", struct{}{}, nil)
	if code, raw := e.call(t, "POST", "/pairing/discovered", contracts.PairDiscoveredRequest{Fingerprint: joinerFP(2), CheckCode: "222222", Remote: "192.168.1.66:5000"}, nil); code != http.StatusForbidden || !strings.Contains(string(raw), "declined_source") {
		t.Fatalf("a declined source again: %d %s", code, raw)
	}
	if code, _ := e.discoveredFrom(t, 3, "333333", "192.168.1.30:4000"); code != http.StatusCreated {
		t.Fatalf("another source: %d", code)
	}
	// A new window forgives it.
	if _, err := e.log.SetDiscoverable(t.Context(), time.Time{}, "user", false); err != nil {
		t.Fatal(err)
	}
	e.clock.Advance(time.Second)
	if _, err := e.log.SetDiscoverable(t.Context(), time.Now().Add(10*time.Minute), "user", false); err != nil {
		t.Fatal(err)
	}
	if code, _ := e.discoveredFrom(t, 4, "444444", "192.168.1.66:6000"); code != http.StatusCreated {
		t.Fatalf("next window: %d", code)
	}
}

// R7: the mismatch undoes only the joiner's own row: one re-pinned to
// another key meanwhile stays.
func TestMismatchKeepsARowRePinnedSince(t *testing.T) {
	e := newPairEnv(t)
	if _, err := e.log.SetDiscoverable(t.Context(), time.Now().Add(10*time.Minute), "user", false); err != nil {
		t.Fatal(err)
	}
	_, req := e.discovered(t, 1, "111111")
	_, raw := e.call(t, "POST", "/pairing/requests/"+req.ID+"/approve", contracts.PairDecision{Level: contracts.GrantView, CheckCodeConfirmed: true}, nil)
	var approved contracts.PairRequest
	_ = json.Unmarshal(raw, &approved)
	row, err := e.peers.Get(t.Context(), approved.PeerID)
	if err != nil {
		t.Fatal(err)
	}
	row.Fingerprint = joinerFP(7) // re-pinned: someone else's now
	if err := e.peers.Update(t.Context(), row, "test"); err != nil {
		t.Fatal(err)
	}
	if code, _ := e.call(t, "POST", "/pairing/requests/"+req.ID+"/mismatch", map[string]string{"fingerprint": joinerFP(1)}, nil); code != http.StatusOK {
		t.Fatal(code)
	}
	if _, err := e.peers.Get(t.Context(), approved.PeerID); err != nil {
		t.Fatal("the re-pinned row was deleted")
	}
}
