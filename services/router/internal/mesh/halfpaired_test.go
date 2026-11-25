package mesh

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/devicekey"
	"github.com/drewkidwell/mads/pkg/meshnet"
)

// Half-paired devices (docs/MESH-NETWORK-DESIGN.md F6), two stacks.

// pairBoth pairs joiner with host by code, approved at control: the host
// then has the joiner grant-only, the joiner views the host. It answers
// the joiner's row for the host.
func pairBoth(t *testing.T, host, joiner *node) contracts.DeviceStatus {
	t.Helper()
	c := host.code(t)
	var st JoinStatus
	if code := joiner.call(t, "POST", "/devices/pair", map[string]string{"code": c.Code}, &st); code != http.StatusAccepted {
		t.Fatalf("join: %d %+v", code, st)
	}
	approveFrom(t, host, joiner)
	waitFor(t, "the joiner to pin the host", func() bool {
		joiner.call(t, "GET", "/devices/pair/"+st.ID, nil, &st)
		return st.State != contracts.PairPending
	})
	if st.State != contracts.PairApproved || st.Error != "" {
		t.Fatalf("joined: %+v", st)
	}
	row, ok := joiner.device(t, string(host.id.Fingerprint()))
	if !ok {
		t.Fatal("the joiner didn't pin the host")
	}
	return row
}

// approveFrom approves the host's pending request from joiner at control.
func approveFrom(t *testing.T, host, joiner *node) contracts.PairRequest {
	t.Helper()
	var reqs []contracts.PairRequest
	host.call(t, "GET", "/devices/pair-requests", nil, &reqs)
	for _, r := range reqs {
		if r.Fingerprint == string(joiner.id.Fingerprint()) && r.State == contracts.PairPending {
			if code := host.call(t, "POST", "/devices/pair-requests/"+r.ID+"/approve", map[string]any{"level": "control", "check_code_confirmed": true}, nil); code != http.StatusOK {
				t.Fatalf("approve: %d", code)
			}
			return r
		}
	}
	t.Fatalf("no pending request from %s: %+v", joiner.name, reqs)
	return contracts.PairRequest{}
}

// unpairOn deletes on n, behind its Router's back, the row for other (a
// wiped Data Service, a removal on one side only), and waits for n's
// Mesh Service to refuse other's key.
func unpairOn(t *testing.T, n, other *node) {
	t.Helper()
	ctx := context.Background()
	peers, _ := n.data.Peers(ctx)
	for _, p := range peers {
		if p.Fingerprint == string(other.id.Fingerprint()) {
			if err := n.data.DeletePeer(ctx, p.ID); err != nil {
				t.Fatal(err)
			}
		}
	}
	_ = n.mgr.Reload(ctx)
	if n.mesh == nil {
		return
	}
	row, ok := other.device(t, string(n.id.Fingerprint()))
	if !ok {
		return
	}
	waitFor(t, n.name+"'s Mesh Service to forget "+other.name, func() bool {
		p := other.mgr.ProbePeer(ctx, row.Peer)
		return p.Status == http.StatusForbidden && p.Code == "unpaired_device"
	})
}

// later moves n's clock past the guard after a pairing.
func later(n *node) { n.setAhead(2 * pairGuard) }

func (n *node) check(t *testing.T, fp string) contracts.DeviceStatus {
	t.Helper()
	var out struct {
		Devices []contracts.DeviceStatus `json:"devices"`
	}
	if code := n.call(t, "POST", "/devices/check", struct{}{}, &out); code != http.StatusOK {
		t.Fatalf("check: %d", code)
	}
	for _, d := range out.Devices {
		if d.Fingerprint == fp {
			return d
		}
	}
	return contracts.DeviceStatus{}
}

func unpairedEvents(t *testing.T, n *node) []contracts.DeviceChange {
	t.Helper()
	evs, _ := n.data.Events(context.Background(), 0, "", []string{string(contracts.DeviceUnpaired)})
	var out []contracts.DeviceChange
	for _, ev := range evs {
		var c contracts.DeviceChange
		_ = json.Unmarshal(ev.Payload, &c)
		out = append(out, c)
	}
	return out
}

// R1: removing a device asks it to forget this one first, and the
// result says how that went: removed there (device.unpaired by
// "itself"), it didn't have this device (not_paired), or it couldn't be
// reached (offline). The row here goes in every case.
func TestRemoveForgetsOnTheOtherDevice(t *testing.T) {
	host, joiner := newNode(t, "pop-os", true), newNode(t, "mac", true)
	hostFP, joinerFP := string(host.id.Fingerprint()), string(joiner.id.Fingerprint())

	pairBoth(t, host, joiner)
	var rm DeviceRemoval
	if code := joiner.call(t, "DELETE", "/devices/"+hostFP, nil, &rm); code != http.StatusOK || rm.There != "removed" {
		t.Fatalf("remove: %d %+v", code, rm)
	}
	if _, ok := joiner.device(t, hostFP); ok {
		t.Fatal("still paired here")
	}
	if _, ok := host.device(t, joinerFP); ok {
		t.Fatal("still paired there")
	}
	if un := unpairedEvents(t, host); len(un) != 1 || un[0].By != "itself" || un[0].Fingerprint != joinerFP {
		t.Fatalf("host's device.unpaired = %+v", un)
	}

	// It had already dropped this device: not_paired.
	pairBoth(t, host, joiner)
	unpairOn(t, host, joiner)
	if code := joiner.call(t, "DELETE", "/devices/"+hostFP, nil, &rm); code != http.StatusOK || rm.There != "not_paired" {
		t.Fatalf("remove, not paired there: %d %+v", code, rm)
	}

	// Remove everywhere says it too.
	pairBoth(t, host, joiner)
	var res RemoveResult
	if code := joiner.call(t, "POST", "/devices/"+hostFP+"/remove-everywhere", struct{}{}, &res); code != http.StatusOK || res.Here != "removed" || res.There != "removed" {
		t.Fatalf("remove everywhere: %d %+v", code, res)
	}
	if _, ok := host.device(t, joinerFP); ok {
		t.Fatal("remove everywhere: still paired there")
	}

	// A device with no Mesh Service (nowhere to ask): offline, and gone here.
	phone := newNode(t, "phone", false)
	pairBoth(t, host, phone)
	if code := host.call(t, "DELETE", "/devices/"+string(phone.id.Fingerprint()), nil, &rm); code != http.StatusOK || rm.There != "offline" || rm.ThereError == "" {
		t.Fatalf("remove an unreachable device: %d %+v", code, rm)
	}
	if _, ok := host.device(t, string(phone.id.Fingerprint())); ok {
		t.Fatal("an unreachable device wasn't removed here")
	}
}

// R3, R4: POST /devices/check asks every paired device whether it has
// this one paired. A hello is paired; unpaired_device is not_paired, but
// read as unknown within pairGuard of the pairing; a different key is
// other_key. A row this device views is kept; a grant-only row is removed
// (device.unpaired by "reconcile").
func TestCheckPairingThere(t *testing.T) {
	host, joiner := newNode(t, "pop-os", true), newNode(t, "mac", true)
	hostFP, joinerFP := string(host.id.Fingerprint()), string(joiner.id.Fingerprint())
	pairBoth(t, host, joiner)
	if d, _ := joiner.device(t, hostFP); d.PairingThere != "" || d.PairingCheckedAt != nil {
		t.Fatalf("checked before any check: %+v", d)
	}
	if d := joiner.check(t, hostFP); d.PairingThere != contracts.PairingTherePaired || d.PairingCheckedAt == nil {
		t.Fatalf("both paired: %q %q", d.PairingThere, d.PairingThereReason)
	}
	// The host approved moments ago: it doesn't dial the joiner yet.
	if d := host.check(t, joinerFP); d.PairingThere != contracts.PairingThereUnknown || !strings.Contains(d.PairingThereReason, "moments ago") {
		t.Fatalf("just approved (host): %q %q", d.PairingThere, d.PairingThereReason)
	}
	later(host)
	if d := host.check(t, joinerFP); d.PairingThere != contracts.PairingTherePaired {
		t.Fatalf("both paired (host): %q %q", d.PairingThere, d.PairingThereReason)
	}

	// The host loses its row: within the guard it's unknown, then
	// not_paired; the joiner views it, so its row stays.
	unpairOn(t, host, joiner)
	if d := joiner.check(t, hostFP); d.PairingThere != contracts.PairingThereUnknown || !strings.Contains(d.PairingThereReason, "moments ago") {
		t.Fatalf("within the guard: %q %q", d.PairingThere, d.PairingThereReason)
	}
	later(joiner)
	d := joiner.check(t, hostFP)
	if d.PairingThere != contracts.PairingThereNotPaired || d.ID == "" {
		t.Fatalf("not paired there: %+v", d)
	}
	// GET /devices carries it too.
	if d, _ := joiner.device(t, hostFP); d.PairingThere != contracts.PairingThereNotPaired {
		t.Fatalf("GET /devices: %q", d.PairingThere)
	}

	// The other way: the joiner loses its row; the host only granted it
	// access (it doesn't view it), so the host's row goes.
	host2, joiner2 := newNode(t, "pop-os", true), newNode(t, "mac", true)
	pairBoth(t, host2, joiner2)
	joiner2FP := string(joiner2.id.Fingerprint())
	// Only a pairing the other device was seen to have is undone (review
	// round 1): never confirmed (an introduction, a joiner that hasn't
	// pinned yet), a not_paired keeps the row.
	unpairOn(t, joiner2, host2)
	later(host2)
	if d := host2.check(t, joiner2FP); d.ID == "" || d.PairingThere != contracts.PairingThereNotPaired {
		t.Fatalf("a pairing never confirmed there was undone: %+v", d)
	}
	pairBoth(t, host2, joiner2)
	host2.setAhead(4 * pairGuard)
	if d := host2.check(t, joiner2FP); d.PairingThere != contracts.PairingTherePaired {
		t.Fatalf("re-paired: %q %q", d.PairingThere, d.PairingThereReason)
	}
	unpairOn(t, joiner2, host2)
	// Past the guard of the re-pairing too.
	host2.setAhead(6 * pairGuard)
	if d := host2.check(t, joiner2FP); d.ID != "" {
		t.Fatalf("a grant-only row not paired there is kept: %+v", d)
	}
	if un := unpairedEvents(t, host2); len(un) != 1 || un[0].By != "reconcile" {
		t.Fatalf("device.unpaired = %+v", un)
	}

	// A different key at its address: other_key.
	other := newNode(t, "impostor", true)
	host3, joiner3 := newNode(t, "pop-os", true), newNode(t, "mac", true)
	row := pairBoth(t, host3, joiner3)
	if err := joiner3.mgr.editPeer(context.Background(), row.ID, func(p *contracts.Peer) (bool, error) {
		p.Addresses, p.LastGood = []contracts.MeshAddress{maddr(strings.TrimPrefix(other.mesh.URL, "http://"), "lan", contracts.SourceUser)}, nil
		return false, nil
	}); err != nil {
		t.Fatal(err)
	}
	joiner3.mgr.invalidate(row.Fingerprint, "test")
	if d := joiner3.check(t, row.Fingerprint); d.PairingThere != contracts.PairingThereOtherKey {
		t.Fatalf("another key answering: %q %q", d.PairingThere, d.PairingThereReason)
	}
}

// R6: a code from a device paired here that no longer has this one
// paired offers a re-pair ({code, repair: true}), and approval updates
// the row here in place: same id, name, viewed flag and the level this
// device gives it.
func TestRepairOfferedByCode(t *testing.T) {
	host, joiner := newNode(t, "pop-os", true), newNode(t, "mac", true)
	hostFP := string(host.id.Fingerprint())
	row := pairBoth(t, host, joiner)
	if code := joiner.call(t, "PUT", "/devices/"+hostFP+"/grant", GrantRequest{Level: "view"}, nil); code != http.StatusOK {
		t.Fatalf("grant: %d", code)
	}
	unpairOn(t, host, joiner)
	later(joiner)
	c := host.code(t)
	var e struct {
		Code   string `json:"code"`
		PeerID string `json:"peer_id"`
		Name   string `json:"name"`
	}
	if code := joiner.call(t, "POST", "/devices/pair", map[string]string{"code": c.Code}, &e); code != http.StatusConflict || e.Code != "repair_offered" || e.PeerID != row.ID || e.Name != row.Name {
		t.Fatalf("a code from a half-paired host: %d %+v", code, e)
	}
	var st JoinStatus
	if code := joiner.call(t, "POST", "/devices/pair", JoinRequest{Code: c.Code, Repair: true}, &st); code != http.StatusAccepted {
		t.Fatalf("re-pair: %d %+v", code, st)
	}
	approveFrom(t, host, joiner)
	waitFor(t, "the re-pair", func() bool {
		joiner.call(t, "GET", "/devices/pair/"+st.ID, nil, &st)
		return st.State != contracts.PairPending
	})
	if st.State != contracts.PairApproved || st.PeerID != row.ID || st.Error != "" {
		t.Fatalf("re-paired: %+v", st)
	}
	list, _ := joiner.data.Peers(context.Background())
	if len(list) != 1 || list[0].ID != row.ID || list[0].Name != row.Name || !list[0].IsViewed() || list[0].Level() != contracts.GrantView {
		t.Fatalf("joiner's rows after the re-pair: %+v", list)
	}
	if d := joiner.check(t, hostFP); d.PairingThere != contracts.PairingTherePaired {
		t.Fatalf("after the re-pair: %q %q", d.PairingThere, d.PairingThereReason)
	}
	// Both paired again: a code offers to update its addresses, as before.
	c2 := host.code(t)
	if code := joiner.call(t, "POST", "/devices/pair", map[string]string{"code": c2.Code}, &e); code != http.StatusConflict || e.Code != "already_paired_update" {
		t.Fatalf("both paired: %d %+v", code, e)
	}
}

// R5: the found list says who's half-paired, and a found device already
// paired here can be asked again: approved, its row is updated in place.
func TestDiscoveredPairing(t *testing.T) {
	host, joiner := newNode(t, "pop-os", true), newNode(t, "mac", true)
	joiner.mgr.opts.LocalNetworks = func() []meshnet.Network { return []meshnet.Network{testLAN} }
	joiner.mgr.opts.Finders = []Finder{&staticFinder{m: joiner.mgr, addrs: []string{strings.TrimPrefix(host.mesh.URL, "http://")}}}
	row := pairBoth(t, host, joiner)
	if code := host.call(t, "POST", "/devices/discoverable", DiscoverableRequest{On: true}, nil); code != http.StatusOK {
		t.Fatal(code)
	}
	found := func(want string) DiscoveredDevice {
		var d DiscoveredDevice
		waitFor(t, "the host found as "+want, func() bool {
			l := discoveredOf(t, joiner)
			if len(l.Devices) == 1 {
				d = l.Devices[0]
			}
			return d.Pairing == want
		})
		return d
	}
	if d := found("both"); !d.Paired {
		t.Fatalf("both: %+v", d)
	}
	unpairOn(t, host, joiner)
	// Within the guard of the joiner's pinning, it reads as both (the host
	// may not have read a new pairing yet); past it, as here_only.
	found("both")
	later(joiner)
	d := found("here_only")
	var st JoinStatus
	if code := joiner.call(t, "POST", "/devices/discovered/"+d.Fingerprint+"/request", struct{}{}, &st); code != http.StatusAccepted {
		t.Fatalf("request a device paired here: %d %+v", code, st)
	}
	approveFrom(t, host, joiner)
	waitFor(t, "the approval", func() bool {
		joiner.call(t, "GET", "/devices/pair/"+st.ID, nil, &st)
		return st.Confirm == "needed"
	})
	joiner.call(t, "POST", "/devices/pair/"+st.ID+"/confirm", map[string]bool{"match": true}, &st)
	if st.PeerID != row.ID || st.Confirm != "matched" {
		t.Fatalf("re-paired: %+v", st)
	}
	if list, _ := joiner.data.Peers(context.Background()); len(list) != 1 || list[0].ID != row.ID {
		t.Fatalf("joiner's rows: %+v", list)
	}
	found("both")

	// The joiner loses its row: the host still has it (there_only).
	unpairOn(t, joiner, host)
	found("there_only")
}

// Right after a pairing (the UX walk's false alarms): until the joiner
// has had pairGuard to pin the host, the host's found list doesn't read
// it as half-paired, and the host doesn't dial it, so the joiner's Mesh
// Service records no device.refused for a pairing that worked. Past the
// guard, a joiner that still hasn't pinned is refused and shown as before.
func TestJustApprovedIsNoAlarm(t *testing.T) {
	host, joiner := newNode(t, "pop-os", true), newNode(t, "macbook", true)
	hostFP, joinerFP := string(host.id.Fingerprint()), string(joiner.id.Fingerprint())
	for _, n := range []*node{host, joiner} {
		n.mgr.opts.LocalNetworks = func() []meshnet.Network { return []meshnet.Network{testLAN} }
		if code := n.call(t, "POST", "/devices/discoverable", DiscoverableRequest{On: true}, nil); code != http.StatusOK {
			t.Fatal(code)
		}
	}
	host.mgr.opts.Finders = []Finder{&staticFinder{m: host.mgr, addrs: []string{strings.TrimPrefix(joiner.mesh.URL, "http://")}}}
	joiner.mgr.opts.Finders = []Finder{&staticFinder{m: joiner.mgr, addrs: []string{strings.TrimPrefix(host.mesh.URL, "http://")}}}
	foundOn := func(n *node, fp string) (DiscoveredDevice, bool) {
		for _, d := range discoveredOf(t, n).Devices {
			if d.Fingerprint == fp {
				return d, true
			}
		}
		return DiscoveredDevice{}, false
	}
	waitFor(t, "the host to find the joiner", func() bool { _, ok := foundOn(host, joinerFP); return ok })
	waitFor(t, "the joiner to find the host", func() bool { _, ok := foundOn(joiner, hostFP); return ok })
	var st JoinStatus
	if code := joiner.call(t, "POST", "/devices/discovered/"+hostFP+"/request", struct{}{}, &st); code != http.StatusAccepted {
		t.Fatalf("request: %d %+v", code, st)
	}
	approveFrom(t, host, joiner)
	// The joiner hasn't pinned the host: its user hasn't confirmed the
	// check code yet.
	waitFor(t, "the approval to reach the joiner", func() bool {
		joiner.call(t, "GET", "/devices/pair/"+st.ID, nil, &st)
		return st.Confirm == "needed"
	})
	refused := func() int {
		evs, _ := joiner.data.Events(context.Background(), 0, "", []string{string(contracts.DeviceRefused)})
		n := 0
		for _, ev := range evs {
			var r contracts.DeviceRefusal
			_ = json.Unmarshal(ev.Payload, &r)
			if r.Fingerprint == hostFP {
				n++
			}
		}
		return n
	}

	// (a) The host's found list: paired here moments ago reads as both.
	waitFor(t, "the host's list to see its new row", func() bool { d, _ := foundOn(host, joinerFP); return d.Paired })
	if d, _ := foundOn(host, joinerFP); d.Pairing != "both" {
		t.Fatalf("just approved, found as %q", d.Pairing)
	}
	// (b) The probe loop and a check don't dial it yet.
	host.mgr.ProbeAll(context.Background())
	if d := host.check(t, joinerFP); d.PairingThere != contracts.PairingThereUnknown {
		t.Fatalf("just approved, checked: %q %q", d.PairingThere, d.PairingThereReason)
	}
	time.Sleep(300 * time.Millisecond) // device.refused is written off the request path
	if n := refused(); n != 0 {
		t.Fatalf("the joiner recorded %d refusals of the host it is pairing with", n)
	}

	// Past the guard, a joiner that still hasn't pinned the host is what
	// it is: refused there, and here_only here.
	later(host)
	if d := host.check(t, joinerFP); d.PairingThere != contracts.PairingThereNotPaired {
		t.Fatalf("past the guard: %q %q", d.PairingThere, d.PairingThereReason)
	}
	waitFor(t, "the joiner's refusal", func() bool { return refused() == 1 })
	waitFor(t, "here_only past the guard", func() bool { d, _ := foundOn(host, joinerFP); return d.Pairing == "here_only" })
}

func TestPairingOf(t *testing.T) {
	yes, no := true, false
	for _, c := range []struct {
		here    bool
		there   *bool
		checked string
		fresh   bool
		want    string
	}{
		{false, nil, "", false, "none"},
		{false, &no, "", false, "none"},
		{true, &yes, "", false, "both"},
		{true, &no, "", false, "here_only"},
		{false, &yes, "", false, "there_only"},
		// An older build doesn't say: this device's last check stands in.
		{true, nil, "", false, "both"},
		{true, nil, contracts.PairingThereNotPaired, false, "here_only"},
		// Paired here moments ago: the other device may not have read it.
		{true, &no, "", true, "both"},
		{true, nil, contracts.PairingThereNotPaired, true, "both"},
		{false, &no, "", true, "none"},
	} {
		if got := pairingOf(c.here, c.there, c.checked, c.fresh); got != c.want {
			t.Errorf("pairingOf(%v, %v, %q, %v) = %s, want %s", c.here, c.there, c.checked, c.fresh, got, c.want)
		}
	}
}

// R7: transport failures and a host not ready are worth retrying with the
// same code; rate_limited says when; a refusal or another key isn't.
func TestJoinErrorsRetryable(t *testing.T) {
	for _, c := range []struct {
		code      string
		retryable bool
	}{
		{"host_unreachable", true}, {"devices_unavailable", true}, {"pairing_unavailable", true}, {"rate_limited", true},
		{"pairing_refused", false}, {"host_key_mismatch", false}, {"no_shared_network", false}, {"not_tailnet", false},
	} {
		err := retryableJoin(&pairCodeError{status: 502, code: c.code}, 7*time.Second)
		var pe *pairCodeError
		if !errors.As(err, &pe) {
			t.Fatal(err)
		}
		if got := pe.extra["retryable"] == true; got != c.retryable {
			t.Errorf("%s: retryable %v", c.code, got)
		}
		if _, ok := pe.extra["retry_after"]; ok != (c.code == "rate_limited") {
			t.Errorf("%s: retry_after %v", c.code, pe.extra["retry_after"])
		}
	}

	// Through the stack: the host's Mesh Service gone, the same code can
	// be tried again.
	host, joiner := newNode(t, "pop-os", true), newNode(t, "mac", false)
	c := host.code(t)
	host.mesh.Drop(0)
	var e map[string]any
	if code := joiner.call(t, "POST", "/devices/pair", map[string]string{"code": c.Code}, &e); code != http.StatusBadGateway || e["code"] != "host_unreachable" || e["retryable"] != true {
		t.Fatalf("host gone: %d %+v", code, e)
	}
}

// R7: the same code presented again after the host declined answers that
// at once, not pending.
func TestJoinAgainAfterDecline(t *testing.T) {
	host, joiner := newNode(t, "pop-os", true), newNode(t, "mac", false)
	c := host.code(t)
	var st JoinStatus
	if code := joiner.call(t, "POST", "/devices/pair", map[string]string{"code": c.Code}, &st); code != http.StatusAccepted || st.State != contracts.PairPending {
		t.Fatalf("join: %d %+v", code, st)
	}
	var reqs []contracts.PairRequest
	host.call(t, "GET", "/devices/pair-requests", nil, &reqs)
	if len(reqs) != 1 {
		t.Fatalf("requests: %+v", reqs)
	}
	if code := host.call(t, "POST", "/devices/pair-requests/"+reqs[0].ID+"/decline", nil, nil); code != http.StatusOK {
		t.Fatalf("decline: %d", code)
	}
	var again JoinStatus
	if code := joiner.call(t, "POST", "/devices/pair", map[string]string{"code": c.Code}, &again); code != http.StatusAccepted || again.State != contracts.PairDeclined || again.RequestID != st.RequestID {
		t.Fatalf("again: %d %+v", code, again)
	}
	if code := joiner.call(t, "GET", "/devices/pair/"+again.ID, nil, &again); code != http.StatusOK || again.State != contracts.PairDeclined {
		t.Fatalf("read back: %d %+v", code, again)
	}
}

// isPinMismatch is a different key answering, on the path or at every
// address tried.
func TestIsPinMismatch(t *testing.T) {
	pin := &devicekey.PinError{}
	if !isPinMismatch(pin) || isPinMismatch(errors.New("refused")) {
		t.Fatal("pin")
	}
	race := &meshnet.RaceError{Attempts: []meshnet.Attempt{{Started: true, Err: pin}, {Started: true, Err: errors.New("refused")}}}
	if isPinMismatch(race) {
		t.Fatal("one address answered by another key isn't other_key")
	}
	race.Attempts[1].Err = pin
	if !isPinMismatch(race) {
		t.Fatal("every address another key")
	}
}
