package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/services/data/internal/store"
)

// Half-paired devices (docs/MESH-NETWORK-DESIGN.md F6), the Data
// Service's part.

func (e *pairEnv) approve(t *testing.T, id string, d contracts.PairDecision) contracts.PairRequest {
	t.Helper()
	code, raw := e.call(t, "POST", "/pairing/requests/"+id+"/approve", d, nil)
	if code != http.StatusOK {
		t.Fatalf("approve: %d %s", code, raw)
	}
	var out contracts.PairRequest
	_ = json.Unmarshal(raw, &out)
	return out
}

func (e *pairEnv) deviceEvents(t *testing.T, typ contracts.EventType) []contracts.DeviceChange {
	t.Helper()
	evs, err := e.log.Query(t.Context(), store.Filter{Types: []string{string(typ)}})
	if err != nil {
		t.Fatal(err)
	}
	var out []contracts.DeviceChange
	for _, ev := range evs {
		var c contracts.DeviceChange
		_ = json.Unmarshal(ev.Payload, &c)
		out = append(out, c)
	}
	return out
}

// R2: a key already paired here re-pairs: approval updates its row in
// place (same id and name, viewed flag kept; the level chosen, the
// request's addresses beside the user's own; how from this request),
// device.paired says it's a re-pairing, and declining leaves the row.
func TestRePairUpdatesTheRowInPlace(t *testing.T) {
	e := newPairEnv(t)
	first := e.approve(t, e.pendingRequest(t, 1).ID, contracts.PairDecision{Level: contracts.GrantControl, Name: "phone"})
	row, err := e.peers.Get(t.Context(), first.PeerID)
	if err != nil {
		t.Fatal(err)
	}
	yes := true
	row.Viewed = &yes
	row.Addresses = append(row.Addresses, contracts.MeshAddress{NetAddress: contracts.NetAddress{Via: "direct", Addr: "192.168.1.9:17380"}, Source: contracts.SourceUser})
	row.Grant.Ceilings.MaxTrust = contracts.SessionTrust("limited")
	if err := e.peers.Update(store.WithHostAuthority(t.Context()), row, "test"); err != nil {
		t.Fatal(err)
	}

	// Declined: the row stays as it was.
	declined := e.pendingRequest(t, 1)
	if code, _ := e.call(t, "POST", "/pairing/requests/"+declined.ID+"/decline", nil, nil); code != http.StatusOK {
		t.Fatal(code)
	}
	if got, _ := e.peers.Get(t.Context(), row.ID); got.Level() != contracts.GrantControl {
		t.Fatalf("declined re-pair changed the row: %+v", got.Grant)
	}

	e.clock.Advance(time.Minute)
	if _, err := e.log.SetDiscoverable(t.Context(), time.Now().Add(10*time.Minute), "user", false); err != nil {
		t.Fatal(err)
	}
	status, req := e.discovered(t, 1, "123456")
	if status != http.StatusCreated || req.Repair == nil || req.Repair.PeerID != row.ID || req.Repair.Level != contracts.GrantControl {
		t.Fatalf("discovered re-pair: %d %+v", status, req.Repair)
	}
	done := e.approve(t, req.ID, contracts.PairDecision{Level: contracts.GrantView, CheckCodeConfirmed: true})
	if done.PeerID != row.ID || done.Name != "phone" {
		t.Fatalf("approved = %+v", done)
	}
	list, _ := e.peers.List(t.Context())
	if len(list) != 1 {
		t.Fatalf("rows = %d, want 1", len(list))
	}
	got := list[0]
	addrs := []string{}
	for _, a := range got.Addresses {
		addrs = append(addrs, a.Addr+"/"+string(a.Source))
	}
	if got.ID != row.ID || got.Name != "phone" || !got.IsViewed() || got.Level() != contracts.GrantView ||
		got.Grant.Ceilings.MaxTrust != "limited" || got.Pairing.How != contracts.PairDiscovered || !got.Pairing.PairedAt.After(row.Pairing.PairedAt) ||
		strings.Join(addrs, ",") != "100.70.1.1:17380/discovered,192.168.1.9:17380/user" || got.Remote.Platform != "darwin" {
		t.Fatalf("re-paired row = %+v addrs %v pairing %+v", got, addrs, got.Pairing)
	}
	paired := e.deviceEvents(t, contracts.DevicePaired)
	last := paired[len(paired)-1]
	if len(paired) != 2 || !last.Repair || last.Level != contracts.GrantView || last.PreviousLevel != contracts.GrantControl || last.How != contracts.PairDiscovered {
		t.Fatalf("device.paired = %+v", paired)
	}
	if un := e.deviceEvents(t, contracts.DeviceUnpaired); len(un) != 0 {
		t.Fatalf("a re-pairing unpaired: %+v", un)
	}
}

// R7: a used code is single use per key: the same key presenting it again
// gets its request back (200, as it stands now), for the request's
// lifetime; another key is refused as for any bad code.
func TestRedeemAgainBySameKey(t *testing.T) {
	e := newPairEnv(t)
	_, hash := e.newCode(t)
	code, raw := e.redeem(t, hash, 1)
	if code != http.StatusCreated {
		t.Fatalf("redeem: %d %s", code, raw)
	}
	var req contracts.PairRequest
	_ = json.Unmarshal(raw, &req)
	code, raw = e.redeem(t, hash, 1)
	var again contracts.PairRequest
	_ = json.Unmarshal(raw, &again)
	if code != http.StatusOK || again.ID != req.ID || again.State != contracts.PairPending {
		t.Fatalf("same key again: %d %s", code, raw)
	}
	if code, raw := e.redeem(t, hash, 2); code != http.StatusForbidden || !strings.Contains(string(raw), "pairing_refused") {
		t.Fatalf("another key: %d %s", code, raw)
	}
	e.approve(t, req.ID, contracts.PairDecision{Level: contracts.GrantView})
	code, raw = e.redeem(t, hash, 1)
	_ = json.Unmarshal(raw, &again)
	if code != http.StatusOK || again.State != contracts.PairApproved {
		t.Fatalf("after approval: %d %s", code, raw)
	}
	if n := len(e.deviceEvents(t, contracts.DevicePairRequested)); n != 1 {
		t.Fatalf("device.pair_requested %d times, want 1", n)
	}
	e.clock.Advance(contracts.PairRequestTTL)
	if code, _ := e.redeem(t, hash, 1); code != http.StatusForbidden {
		t.Fatalf("past the request's lifetime: %d", code)
	}
}

// R1: POST /pairing/forget deletes the device's row (both sides), as
// device.unpaired by "itself"; 404 device_not_found when it isn't paired;
// never relayed.
func TestPairingForget(t *testing.T) {
	e := newPairEnv(t)
	done := e.approve(t, e.pendingRequest(t, 1).ID, contracts.PairDecision{Level: contracts.GrantChat})
	in := contracts.PairForget{Fingerprint: joinerFP(1)}
	if code, _ := e.call(t, "POST", "/pairing/forget", in, map[string]string{contracts.MeshRelayHeader: "mac"}); code != http.StatusForbidden {
		t.Fatalf("relayed: %d", code)
	}
	if code, _ := e.call(t, "POST", "/pairing/forget", contracts.PairForget{Fingerprint: "nope"}, nil); code != http.StatusUnprocessableEntity {
		t.Fatalf("bad fingerprint: %d", code)
	}
	if code, raw := e.call(t, "POST", "/pairing/forget", in, nil); code != http.StatusNoContent {
		t.Fatalf("forget: %d %s", code, raw)
	}
	if _, err := e.peers.Get(t.Context(), done.PeerID); err == nil {
		t.Fatal("still paired")
	}
	un := e.deviceEvents(t, contracts.DeviceUnpaired)
	if len(un) != 1 || un[0].By != "itself" || un[0].PreviousLevel != contracts.GrantChat {
		t.Fatalf("device.unpaired = %+v", un)
	}
	if code, raw := e.call(t, "POST", "/pairing/forget", in, nil); code != http.StatusNotFound || !strings.Contains(string(raw), "device_not_found") {
		t.Fatalf("again: %d %s", code, raw)
	}
}

// R4: the Router's reconcile removal (DELETE ?by=reconcile) is on record
// as device.unpaired by "reconcile"; a plain removal says nothing.
func TestReconcileRemovalSaysSo(t *testing.T) {
	e := newPairEnv(t)
	a := e.approve(t, e.pendingRequest(t, 1).ID, contracts.PairDecision{Level: contracts.GrantView})
	b := e.approve(t, e.pendingRequest(t, 2).ID, contracts.PairDecision{Level: contracts.GrantView})
	if code, raw := e.call(t, "DELETE", "/peers/"+a.PeerID+"?by=reconcile", nil, nil); code != http.StatusOK {
		t.Fatalf("%d %s", code, raw)
	}
	if code, raw := e.call(t, "DELETE", "/peers/"+b.PeerID, nil, nil); code != http.StatusOK {
		t.Fatalf("%d %s", code, raw)
	}
	un := e.deviceEvents(t, contracts.DeviceUnpaired)
	if len(un) != 2 || un[0].By != "reconcile" || un[1].By != "" {
		t.Fatalf("device.unpaired = %+v", un)
	}
}

// Review round 1: a waiting re-pair request's repair is read from the rows
// when it's listed, so the prompt never names a row that's gone.
func TestRePairPromptFollowsTheRow(t *testing.T) {
	e := newPairEnv(t)
	first := e.approve(t, e.pendingRequest(t, 1).ID, contracts.PairDecision{Level: contracts.GrantControl, Name: "phone"})
	req := e.pendingRequest(t, 1)
	if req.Repair == nil || req.Repair.PeerID != first.PeerID {
		t.Fatalf("repair at first: %+v", req.Repair)
	}
	if err := e.peers.Delete(t.Context(), first.PeerID, "test"); err != nil {
		t.Fatal(err)
	}
	var list []contracts.PairRequest
	code, raw := e.call(t, "GET", "/pairing/requests", nil, nil)
	if code != http.StatusOK || json.Unmarshal(raw, &list) != nil {
		t.Fatal(code)
	}
	for _, r := range list {
		if r.ID == req.ID && r.Repair != nil {
			t.Fatalf("the prompt still names a removed row: %+v", r.Repair)
		}
	}
	var one contracts.PairRequest
	code, raw = e.call(t, "GET", "/pairing/requests/"+req.ID, nil, nil)
	if code != http.StatusOK || json.Unmarshal(raw, &one) != nil || one.Repair != nil {
		t.Fatalf("GET one: %d %+v", code, one.Repair)
	}
}
