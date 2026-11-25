package mesh

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/pkg/devicekey"
	"github.com/drewkidwell/mads/pkg/paircode"
	"github.com/drewkidwell/mads/services/data/datatest"
)

// What a paired device lets this node do there is read from its hello —
// viewed or not — kept as of when it was read, and written to its row
// only when it changes: an offline device keeps its last known answer, a
// refusal reads as none.
func TestReverseLevelFromHello(t *testing.T) {
	h := newHarness(t)
	p := newFakePeer(t, "pop-os")
	p.hello.Platform, p.hello.HostsSessions = "linux", ptrBool(true)
	no := false
	// Paired, not viewed, with an address: probed for this alone.
	if err := h.data.CreatePeer(context.Background(), &contracts.Peer{Name: "pop-os", MeshURL: p.meshURL(), Viewed: &no, Fingerprint: p.fp,
		Grant: &contracts.PeerGrant{Level: contracts.GrantView}}); err != nil {
		t.Fatal(err)
	}
	// One granted nothing here isn't dialled either (its address is
	// its own choice, and there's no reason to).
	lurker := newFakePeer(t, "lurker")
	if err := h.data.CreatePeer(context.Background(), &contracts.Peer{Name: "lurker", MeshURL: lurker.meshURL(), Viewed: &no, Fingerprint: lurker.fp,
		Grant: &contracts.PeerGrant{}}); err != nil {
		t.Fatal(err)
	}
	// One that doesn't host sessions is never dialled.
	phone := newFakePeer(t, "phone")
	if err := h.data.CreatePeer(context.Background(), &contracts.Peer{Name: "phone", MeshURL: phone.meshURL(), Viewed: &no, Fingerprint: phone.fp,
		Remote: &contracts.PeerRemote{HostsSessions: &no}}); err != nil {
		t.Fatal(err)
	}
	if err := h.mgr.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	h.mgr.ProbeAll(context.Background())
	if phone.handshakes.Load() != 0 {
		t.Fatal("a device that hosts no sessions was dialled")
	}
	if lurker.handshakes.Load() != 0 {
		t.Fatal("a device granted nothing here was dialled")
	}
	row, _ := h.data.Peer(context.Background(), "pop-os")
	if row.Remote == nil || row.Remote.Level != contracts.GrantControl || row.Remote.Platform != "linux" || row.Remote.AsOf == nil {
		t.Fatalf("remote = %+v", row.Remote)
	}
	written := row.UpdatedAt
	// Unchanged: nothing more is written, but the time it was read moves.
	h.clk.Add(time.Minute)
	h.mgr.ProbeAll(context.Background())
	row, _ = h.data.Peer(context.Background(), "pop-os")
	if !row.UpdatedAt.Equal(written) {
		t.Fatal("an unchanged answer was written again")
	}
	// The in-memory answer is newer than the row's.
	h.mgr.mu.Lock()
	seen := h.mgr.remote[row.ID].remote
	h.mgr.mu.Unlock()
	if !seen.AsOf.Equal(h.clk.Now().UTC()) {
		t.Fatalf("as of %v, want %v", seen.AsOf, h.clk.Now())
	}
	// Offline: the last known answer stands.
	p.Close()
	h.mgr.ProbeAll(context.Background())
	row, _ = h.data.Peer(context.Background(), "pop-os")
	if row.Remote.Level != contracts.GrantControl {
		t.Fatalf("offline: %+v", row.Remote)
	}
	// Refused outright (it unpaired this device): none.
	q := newFakePeer(t, "spark")
	q.status = http.StatusForbidden
	if err := h.data.CreatePeer(context.Background(), &contracts.Peer{Name: "spark", MeshURL: q.meshURL(), Viewed: &no, Fingerprint: q.fp,
		Grant:  &contracts.PeerGrant{Level: contracts.GrantView},
		Remote: &contracts.PeerRemote{Level: contracts.GrantView, AsOf: ptrTime(time.Unix(1, 0))}}); err != nil {
		t.Fatal(err)
	}
	_ = h.mgr.Reload(context.Background())
	h.mgr.ProbeAll(context.Background())
	row, _ = h.data.Peer(context.Background(), "spark")
	if row.Remote == nil || row.Remote.Level != contracts.GrantNone || row.Remote.AsOf == nil || row.Remote.AsOf.Unix() == 1 {
		t.Fatalf("refused: %+v", row.Remote)
	}
}

func ptrBool(b bool) *bool { return &b }

// Remove everywhere reports each host it administers (removed, offline,
// failed, not_admin) and lists the known hosts it can't manage.
func TestRemoveEverywhereResults(t *testing.T) {
	h := newHarness(t)
	devices := h.mgr.Devices()
	srv := newDevicesServer(t, h, devices)
	ok := newFakePeer(t, "ok")
	ok.answer = func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/devices/") {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		writeJSON(w, http.StatusOK, ok.hello)
	}
	gone := newFakePeer(t, "gone")
	gone.Close()
	broken := newFakePeer(t, "broken")
	broken.answer = func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusInternalServerError, "data_unavailable", "its Data Service is down")
	}
	demoted := newFakePeer(t, "demoted")
	demoted.answer = func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusForbidden, "no_access", `DELETE /devices/{fp} needs "admin" here; this device has "control"`)
	}
	viewer := newFakePeer(t, "viewer")
	no := false
	asOf := time.Now()
	for _, tc := range []struct {
		p     *fakePeer
		level contracts.GrantLevel
	}{{ok, contracts.GrantAdmin}, {gone, contracts.GrantAdmin}, {broken, contracts.GrantAdmin}, {demoted, contracts.GrantAdmin}, {viewer, contracts.GrantView}} {
		if err := h.data.CreatePeer(context.Background(), &contracts.Peer{Name: tc.p.hello.Node, MeshURL: tc.p.meshURL(), Viewed: &no, Fingerprint: tc.p.fp,
			Remote: &contracts.PeerRemote{Level: tc.level, AsOf: &asOf}}); err != nil {
			t.Fatal(err)
		}
	}
	target := testFingerprint(77)
	if err := h.data.CreatePeer(context.Background(), &contracts.Peer{Name: "lost-phone", Viewed: &no, Fingerprint: target,
		Grant: &contracts.PeerGrant{Level: contracts.GrantChat}}); err != nil {
		t.Fatal(err)
	}
	var res RemoveResult
	code := postDevices(t, srv+"/devices/"+target+"/remove-everywhere", `{}`, &res)
	if code != http.StatusOK || res.Here != "removed" || res.Device.Name != "lost-phone" {
		t.Fatalf("%d %+v", code, res)
	}
	got := map[string]string{}
	for _, r := range res.Hosts {
		got[r.Name] = r.Result
	}
	want := map[string]string{"ok": "removed", "gone": "offline", "broken": "failed", "demoted": "not_admin"}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s: %q, want %q (all %v)", k, got[k], v, got)
		}
	}
	if len(res.CannotManage) != 1 || res.CannotManage[0].Name != "viewer" || res.CannotManage[0].Level != "view" {
		t.Fatalf("cannot manage = %+v", res.CannotManage)
	}
	if _, err := h.data.Peer(context.Background(), "lost-phone"); err == nil {
		t.Fatal("not removed here")
	}
	// Retry one host: the device is already gone here.
	res = RemoveResult{}
	code = postDevices(t, srv+"/devices/"+target+"/remove-everywhere", `{"hosts":["ok"]}`, &res)
	if code != http.StatusOK || res.Here != "absent" || len(res.Hosts) != 1 || res.Hosts[0].Result != "removed" {
		t.Fatalf("retry: %d %+v", code, res)
	}
	// Unknown device, no hosts named: 404.
	if code := postDevices(t, srv+"/devices/"+testFingerprint(78)+"/remove-everywhere", `{}`, nil); code != http.StatusNotFound {
		t.Fatalf("unknown device: %d", code)
	}
}

// The local-only device routes refuse a relayed request, whatever reached
// them; and the grant route offers C1 allowed or refused only (D4).
func TestDeviceRoutesLocalOnlyAndC1(t *testing.T) {
	h := newHarness(t)
	srv := newDevicesServer(t, h, h.mgr.Devices())
	for _, path := range []string{"/devices/self", "/devices/activity", "/devices/pair-requests"} {
		req, _ := http.NewRequest(http.MethodGet, srv+path, nil)
		req.Header[contracts.MeshRelayHeader] = []string{"mac"}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("relayed %s: %d, want 403", path, resp.StatusCode)
		}
	}
	no := false
	fp := testFingerprint(5)
	if err := h.data.CreatePeer(context.Background(), &contracts.Peer{Name: "phone", Viewed: &no, Fingerprint: fp, Grant: &contracts.PeerGrant{Level: contracts.GrantChat}}); err != nil {
		t.Fatal(err)
	}
	put := func(body string) int {
		req, _ := http.NewRequest(http.MethodPut, srv+"/devices/"+fp+"/grant", strings.NewReader(body))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		return resp.StatusCode
	}
	if code := put(`{"level":"chat","ceilings":{"full_trust_chat":"held"}}`); code != http.StatusUnprocessableEntity {
		t.Fatalf("held: %d, want 422", code)
	}
	if code := put(`{"level":"chat","ceilings":{"full_trust_chat":"refused","max_trust":"limited"}}`); code != http.StatusOK {
		t.Fatalf("refused: %d", code)
	}
	row, _ := h.data.Peer(context.Background(), "phone")
	if row.Grant.Ceilings.FullTrustChat != contracts.FullTrustChatRefused || row.Grant.Ceilings.MaxTrust != contracts.TrustLimited {
		t.Fatalf("ceilings = %+v", row.Grant.Ceilings)
	}
	// A level change keeps the ceilings; none drops the grant.
	if code := put(`{"level":"view"}`); code != http.StatusOK {
		t.Fatal(code)
	}
	row, _ = h.data.Peer(context.Background(), "phone")
	if row.Level() != contracts.GrantView || row.Grant.Ceilings.FullTrustChat != contracts.FullTrustChatRefused {
		t.Fatalf("after level change: %+v", row.Grant)
	}
	// None keeps the grant and its limits: raised again, it's as
	// limited as before.
	if code := put(`{"level":"none"}`); code != http.StatusOK {
		t.Fatal(code)
	}
	if row, _ = h.data.Peer(context.Background(), "phone"); row.Grant == nil || row.Level() != contracts.GrantNone || row.Grant.Ceilings.MaxTrust != contracts.TrustLimited {
		t.Fatalf("none: %+v", row.Grant)
	}
	if code := put(`{"level":"control"}`); code != http.StatusOK {
		t.Fatal(code)
	}
	if row, _ = h.data.Peer(context.Background(), "phone"); row.Level() != contracts.GrantControl || row.Grant.Ceilings.MaxTrust != contracts.TrustLimited || row.Grant.Ceilings.FullTrustChat != contracts.FullTrustChatRefused {
		t.Fatalf("raised again: %+v", row.Grant)
	}
}

// newDevicesServer serves the devices routes behind the mesh layer, as the
// Router does.
func newDevicesServer(t *testing.T, h *harness, devices http.Handler) string {
	t.Helper()
	mux := http.NewServeMux()
	mux.Handle("/devices", devices)
	mux.Handle("/devices/", devices)
	mux.Handle("/", h.local)
	srv := httptest.NewServer(h.mgr.Handler(mux))
	t.Cleanup(srv.Close)
	return srv.URL
}

func postDevices(t *testing.T, url, body string, out any) int {
	t.Helper()
	resp, err := http.Post(url, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if out != nil {
		_ = json.NewDecoder(resp.Body).Decode(out)
	}
	return resp.StatusCode
}

// A remote admin reading GET /devices never sees this host's dial
// errors.
func TestRelayedDeviceListHasNoDialErrors(t *testing.T) {
	h := newHarness(t)
	srv := newDevicesServer(t, h, h.mgr.Devices())
	p := newFakePeer(t, "gone")
	p.Close()
	if err := h.data.CreatePeer(context.Background(), p.row("gone")); err != nil {
		t.Fatal(err)
	}
	_ = h.mgr.Reload(context.Background())
	h.mgr.ProbeAll(context.Background())
	read := func(relayed bool) contracts.DeviceStatus {
		req, _ := http.NewRequest(http.MethodGet, srv+"/devices", nil)
		if relayed {
			req.Header.Set(contracts.MeshRelayHeader, "mac")
			req.Header.Set(contracts.MeshLevelHeader, "admin")
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		var list []contracts.DeviceStatus
		_ = json.NewDecoder(resp.Body).Decode(&list)
		if len(list) != 1 {
			t.Fatalf("list = %+v", list)
		}
		return list[0]
	}
	if d := read(false); d.Reason == "" {
		t.Fatal("locally the reason is shown")
	}
	if d := read(true); d.Reason != "" || d.MeshURL != "" || d.LastGood != nil || len(d.Addresses) != 0 || d.Path != nil {
		t.Fatalf("relayed: reason %q, where it's reached %q %+v %+v", d.Reason, d.MeshURL, d.LastGood, d.Addresses)
	}
	if d := read(false); d.MeshURL == "" {
		t.Fatal("locally mesh_url is shown")
	}
}

// Removing a device reports what it introduced here, and removes
// those too when asked (a compromised admin's introductions).
func TestRemovingADeviceReportsItsIntroductions(t *testing.T) {
	h := newHarness(t)
	srv := newDevicesServer(t, h, h.mgr.Devices())
	no := false
	seed := byte(40)
	addAdmin := func(name string) string {
		seed++
		fp := testFingerprint(seed)
		if err := h.data.CreatePeer(context.Background(), &contracts.Peer{Name: name, Viewed: &no, Fingerprint: fp, Grant: &contracts.PeerGrant{Level: contracts.GrantControl}}); err != nil {
			t.Fatal(err)
		}
		return fp
	}
	introduce := func(by, byFP, name string) {
		seed++
		actor := "device:" + by + "/" + devicekey.Fingerprint(byFP).Short()
		if err := h.data.As(actor).CreatePeer(context.Background(), &contracts.Peer{Name: name, Viewed: &no, Fingerprint: testFingerprint(seed),
			Grant: &contracts.PeerGrant{Level: contracts.GrantView}, Pairing: &contracts.PeerPairing{How: contracts.PairIntroduction}}); err != nil {
			t.Fatal(err)
		}
	}
	del := func(fp, query string) DeviceRemoval {
		req, _ := http.NewRequest(http.MethodDelete, srv+"/devices/"+fp+query, nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		var out DeviceRemoval
		_ = json.NewDecoder(resp.Body).Decode(&out)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("delete: %d", resp.StatusCode)
		}
		return out
	}
	mac := addAdmin("mac")
	introduce("mac", mac, "tablet")
	addAdmin("unrelated")
	out := del(mac, "")
	if len(out.IntroducedByThis) != 1 || out.IntroducedByThis[0].Name != "tablet" || len(out.AlsoRemoved) != 0 {
		t.Fatalf("plain remove: %+v", out)
	}
	if _, err := h.data.Peer(context.Background(), "tablet"); err != nil {
		t.Fatal("removed without being asked")
	}
	laptop := addAdmin("laptop")
	introduce("laptop", laptop, "kiosk")
	out = del(laptop, "?also_introduced=1")
	if len(out.AlsoRemoved) != 1 || out.AlsoRemoved[0].Name != "kiosk" {
		t.Fatalf("also introduced: %+v", out)
	}
	if _, err := h.data.Peer(context.Background(), "kiosk"); err == nil {
		t.Fatal("kiosk still paired")
	}
	if _, err := h.data.Peer(context.Background(), "unrelated"); err != nil {
		t.Fatal("an unrelated device was removed")
	}
	// Remove everywhere, the same.
	spark := addAdmin("spark")
	introduce("spark", spark, "frame")
	var res RemoveResult
	if code := postDevices(t, srv+"/devices/"+spark+"/remove-everywhere", `{"also_introduced":true}`, &res); code != http.StatusOK ||
		len(res.IntroducedByThis) != 1 || len(res.AlsoRemoved) != 1 || res.AlsoRemoved[0].Name != "frame" {
		t.Fatalf("remove everywhere: %d %+v", code, res)
	}
}

// A device's row is written at most once a minute for a change that
// grants this node more; a change to less is written at once.
func TestRemoteWritesAreDebounced(t *testing.T) {
	h := newHarness(t)
	p := newFakePeer(t, "pop-os")
	no := false
	if err := h.data.CreatePeer(context.Background(), &contracts.Peer{Name: "pop-os", MeshURL: p.meshURL(), Viewed: &no, Fingerprint: p.fp,
		Grant: &contracts.PeerGrant{Level: contracts.GrantView}}); err != nil {
		t.Fatal(err)
	}
	_ = h.mgr.Reload(context.Background())
	level := func() contracts.GrantLevel {
		row, _ := h.data.Peer(context.Background(), "pop-os")
		if row.Remote == nil {
			return "unset"
		}
		return row.Remote.Level
	}
	set := func(access ...string) {
		p.mu.Lock()
		p.hello.Access = access
		p.mu.Unlock()
		_ = h.mgr.Reload(context.Background())
		h.mgr.ProbeAll(context.Background())
	}
	set("view", "chat", "control")
	if level() != contracts.GrantControl {
		t.Fatalf("first: %s", level())
	}
	h.clk.Add(10 * time.Second)
	set("view", "chat", "control", "admin")
	if level() != contracts.GrantControl {
		t.Fatalf("a raise within the minute was written: %s", level())
	}
	h.clk.Add(time.Minute)
	set("view", "chat", "control", "admin")
	if level() != contracts.GrantAdmin {
		t.Fatalf("a raise after the minute: %s", level())
	}
	h.clk.Add(time.Second)
	set("view")
	if level() != contracts.GrantView {
		t.Fatalf("a lowering waited: %s", level())
	}
}

// A Data Service error reaches the caller as its own code and
// message: never the internal request line, the Data Service's address
// or its raw JSON.
func TestDataErrorsCarryNoInternals(t *testing.T) {
	// Answering a pairing request needs host authority: the front door.
	g := datatest.StartGuarded(t)
	h := newHarness(t)
	h.data = dataclient.New(g.URL, "router", dataclient.WithToken(g.Token))
	h.mgr = NewManager(h.data, Options{AllowLoopback: true, SelfName: "mac", Identity: h.id})
	srv := newDevicesServer(t, h, h.mgr.Devices())
	secret := bytes.Repeat([]byte{7}, 32)
	if _, err := h.data.CreatePairingCode(context.Background(), paircode.Hash(secret)); err != nil {
		t.Fatal(err)
	}
	req, err := h.data.RedeemPairingCode(context.Background(), contracts.PairRedeem{SecretHash: paircode.Hash(secret), Fingerprint: testFingerprint(60), Name: "x"})
	if err != nil {
		t.Fatal(err)
	}
	call := func(path, body string) (int, string) {
		resp, err := http.Post(srv+path, "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	if code, _ := call("/devices/pair-requests/"+req.ID+"/decline", "{}"); code != http.StatusOK {
		t.Fatal(code)
	}
	for _, tc := range []struct{ path, code string }{
		{"/devices/pair-requests/" + req.ID + "/decline", "already_answered"},
		{"/devices/pair-requests/" + req.ID + "/approve", "already_answered"},
		{"/devices/pair-requests/pr_nope/approve", "pair_request_not_found"},
		{"/devices/pair-requests/pr_nope/decline", "pair_request_not_found"},
	} {
		status, body := call(tc.path, `{"level":"view"}`)
		var e struct{ Code, Error string }
		_ = json.Unmarshal([]byte(body), &e)
		if e.Code != tc.code || strings.Contains(body, "127.0.0.1") || strings.Contains(body, "/pairing/") || strings.Contains(body, `\"`) {
			t.Errorf("%s: %d %s", tc.path, status, body)
		}
	}
}

// What a host's hello says about the tailnet capping this node's
// grant there reaches /peers.
func TestTailnetCapReachesPeers(t *testing.T) {
	h := newHarness(t)
	p := newFakePeer(t, "pop-os")
	p.hello.Grant, p.hello.TailnetCapped = "admin", true
	h.addPeer(t, p)
	h.mgr.ProbeAll(context.Background())
	st := h.mgr.Statuses()
	if len(st) != 1 || st[0].Remote == nil || !st[0].Remote.TailnetCapped || st[0].Remote.Granted != contracts.GrantAdmin || st[0].Remote.Level != contracts.GrantControl {
		t.Fatalf("remote = %+v", st[0].Remote)
	}
}

// Text a host answers with is cleaned before this device shows it: no
// control or bidi characters, at most paircode.MaxShown.
func TestJoinCleansTheHostsAnswer(t *testing.T) {
	h := newHarness(t)
	srv := newDevicesServer(t, h, h.mgr.Devices())
	p := newFakePeer(t, "pop\u202eos")
	p.answer = func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusForbidden, "pairing_refused", "nope\u202e"+strings.Repeat("x", 2000))
	}
	code := paircode.Encode(paircode.Code{Host: "pop\u202eos", MeshURL: p.meshURL(), Fingerprint: devicekey.Fingerprint(p.fp), ExpiresAt: time.Now().Add(time.Minute), Secret: bytes.Repeat([]byte{1}, 32)})
	var e struct{ Code, Error string }
	if status := postDevices(t, srv+"/devices/pair", `{"code":"`+code+`"}`, &e); status != http.StatusForbidden {
		t.Fatalf("%d %+v", status, e)
	}
	if strings.ContainsRune(e.Error, '\u202e') || len([]rune(e.Error)) > paircode.MaxShown+1 {
		t.Fatalf("shown as %q (%d)", e.Error[:40], len(e.Error))
	}
}

// Each host's introductions reach the remove-everywhere
// result, and also_introduced works once the device itself is gone.
func TestRemoveEverywhereIntroductionsAfterTheDeviceIsGone(t *testing.T) {
	h := newHarness(t)
	srv := newDevicesServer(t, h, h.mgr.Devices())
	ctx := context.Background()
	no := false
	gone := testFingerprint(90)
	// Here: a device it introduced, the device itself already removed.
	actor := "device:old/" + devicekey.Fingerprint(gone).Short()
	if err := h.data.As(actor).CreatePeer(ctx, &contracts.Peer{Name: "w", Viewed: &no, Fingerprint: testFingerprint(91),
		Grant: &contracts.PeerGrant{}, Pairing: &contracts.PeerPairing{How: contracts.PairIntroduction}}); err != nil {
		t.Fatal(err)
	}
	// A host it administers, which answers with its own introductions.
	b := newFakePeer(t, "b")
	var asked string
	b.answer = func(w http.ResponseWriter, r *http.Request) {
		asked = r.URL.RequestURI()
		writeJSON(w, http.StatusOK, DeviceRemoval{IntroducedByThis: []PairHost{{Name: "w-on-b", Fingerprint: testFingerprint(92)}}, AlsoRemoved: []PairHost{{Name: "w-on-b"}}, Absent: true})
	}
	asOf := time.Now()
	if err := h.data.CreatePeer(ctx, &contracts.Peer{Name: "b", MeshURL: b.meshURL(), Viewed: &no, Fingerprint: b.fp,
		Remote: &contracts.PeerRemote{Level: contracts.GrantAdmin, AsOf: &asOf}}); err != nil {
		t.Fatal(err)
	}
	var res RemoveResult
	if code := postDevices(t, srv+"/devices/"+gone+"/remove-everywhere", `{"hosts":["b"],"also_introduced":true}`, &res); code != http.StatusOK {
		t.Fatalf("%d %+v", code, res)
	}
	if res.Here != "absent" || len(res.AlsoRemoved) != 1 || res.AlsoRemoved[0].Name != "w" {
		t.Fatalf("here: %+v", res)
	}
	if len(res.Hosts) != 1 || res.Hosts[0].Result != "removed" || len(res.Hosts[0].IntroducedByThis) != 1 || res.Hosts[0].IntroducedByThis[0].Name != "w-on-b" || res.Hosts[0].AlsoRemoved != 1 {
		t.Fatalf("host b: %+v", res.Hosts)
	}
	if !strings.Contains(asked, "also_introduced=1") {
		t.Fatalf("asked b %s", asked)
	}
	// The host side: DELETE of a device already gone, also_introduced,
	// still removes what it introduced.
	if err := h.data.As(actor).CreatePeer(ctx, &contracts.Peer{Name: "w2", Viewed: &no, Fingerprint: testFingerprint(93),
		Grant: &contracts.PeerGrant{}, Pairing: &contracts.PeerPairing{How: contracts.PairIntroduction}}); err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodDelete, srv+"/devices/"+gone+"?also_introduced=1", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var d DeviceRemoval
	_ = json.NewDecoder(resp.Body).Decode(&d)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !d.Absent || len(d.AlsoRemoved) != 1 || d.AlsoRemoved[0].Name != "w2" {
		t.Fatalf("absent delete: %d %+v", resp.StatusCode, d)
	}
}

// A device that said it hosts nothing (it paired before its
// Mesh Service ran) is asked again now and then, and what it says now is
// recorded; it isn't dialled every probe.
func TestHostsNothingIsRechecked(t *testing.T) {
	h := newHarness(t)
	p := newFakePeer(t, "late")
	p.hello.HostsSessions, p.hello.Platform = ptrBool(true), "darwin"
	no := false
	if err := h.data.CreatePeer(context.Background(), &contracts.Peer{Name: "late", MeshURL: p.meshURL(), Viewed: &no, Fingerprint: p.fp,
		Grant: &contracts.PeerGrant{Level: contracts.GrantView}, Remote: &contracts.PeerRemote{HostsSessions: &no}}); err != nil {
		t.Fatal(err)
	}
	_ = h.mgr.Reload(context.Background())
	h.mgr.ProbeAll(context.Background())
	row, _ := h.data.Peer(context.Background(), "late")
	if row.Remote == nil || row.Remote.HostsSessions == nil || !*row.Remote.HostsSessions || row.Remote.Platform != "darwin" {
		t.Fatalf("remote = %+v", row.Remote)
	}
	// Another that still hosts nothing: once, then not again for a while.
	q := newFakePeer(t, "phone")
	q.hello.HostsSessions = ptrBool(false)
	if err := h.data.CreatePeer(context.Background(), &contracts.Peer{Name: "phone", MeshURL: q.meshURL(), Viewed: &no, Fingerprint: q.fp,
		Grant: &contracts.PeerGrant{Level: contracts.GrantView}, Remote: &contracts.PeerRemote{HostsSessions: &no}}); err != nil {
		t.Fatal(err)
	}
	_ = h.mgr.Reload(context.Background())
	h.mgr.ProbeAll(context.Background())
	first := q.handshakes.Load()
	h.clk.Add(time.Minute)
	h.mgr.ProbeAll(context.Background())
	if first == 0 {
		t.Fatal("never asked")
	}
	h.mgr.mu.Lock()
	var checked time.Time
	for _, st := range h.mgr.paired {
		if st.peer.Name == "phone" {
			checked = st.checkedAt
		}
	}
	h.mgr.mu.Unlock()
	if !checked.Equal(time.Unix(1_000_000, 0)) {
		t.Fatalf("asked again within the interval (checked %v)", checked)
	}
	h.clk.Add(rareProbeEvery)
	h.mgr.ProbeAll(context.Background())
	h.mgr.mu.Lock()
	for _, st := range h.mgr.paired {
		if st.peer.Name == "phone" {
			checked = st.checkedAt
		}
	}
	h.mgr.mu.Unlock()
	if checked.Equal(time.Unix(1_000_000, 0)) {
		t.Fatal("not asked again after the interval")
	}
}

// An answer the write debounce held back is written when the
// Router stops, not lost with it.
func TestHeldBackRemoteIsWrittenAtShutdown(t *testing.T) {
	h := newHarness(t)
	h.mgr.opts.ProbeInterval, h.mgr.opts.IndexInterval = time.Hour, time.Hour
	p := newFakePeer(t, "pop-os")
	no := false
	if err := h.data.CreatePeer(context.Background(), &contracts.Peer{Name: "pop-os", MeshURL: p.meshURL(), Viewed: &no, Fingerprint: p.fp,
		Grant: &contracts.PeerGrant{Level: contracts.GrantView}}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { h.mgr.Run(ctx); close(done) }()
	waitFor(t, "the list", func() bool { return len(h.mgr.Statuses()) == 0 && pairedLoaded(h.mgr) })
	h.mgr.ProbeAll(context.Background()) // control, written at once
	p.mu.Lock()
	p.hello.Access = []string{"view", "chat", "control", "admin"}
	p.mu.Unlock()
	h.clk.Add(time.Second)
	h.mgr.ProbeAll(context.Background()) // admin, held back
	if row, _ := h.data.Peer(context.Background(), "pop-os"); row.Remote.Level != contracts.GrantControl {
		t.Fatalf("not held back: %s", row.Remote.Level)
	}
	cancel()
	<-done
	if row, _ := h.data.Peer(context.Background(), "pop-os"); row.Remote.Level != contracts.GrantAdmin {
		t.Fatalf("after shutdown: %s", row.Remote.Level)
	}
}

func pairedLoaded(m *Manager) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.all) > 0
}

// Remove everywhere answers inside the UI's 10 s, a host that
// hangs reading offline.
func TestRemoveEverywhereAnswersInTime(t *testing.T) {
	if removeAllWithin >= 9*time.Second || removeHostWithin >= removeAllWithin {
		t.Fatalf("budgets %s per host, %s overall", removeHostWithin, removeAllWithin)
	}
	host, all := removeHostWithin, removeAllWithin
	removeHostWithin, removeAllWithin = 300*time.Millisecond, 500*time.Millisecond
	t.Cleanup(func() { removeHostWithin, removeAllWithin = host, all })
	h := newHarness(t)
	srv := newDevicesServer(t, h, h.mgr.Devices())
	slow := newFakePeer(t, "slow")
	slow.answer = func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }
	no := false
	asOf := time.Now()
	if err := h.data.CreatePeer(context.Background(), &contracts.Peer{Name: "slow", MeshURL: slow.meshURL(), Viewed: &no, Fingerprint: slow.fp,
		Remote: &contracts.PeerRemote{Level: contracts.GrantAdmin, AsOf: &asOf}}); err != nil {
		t.Fatal(err)
	}
	target := testFingerprint(95)
	if err := h.data.CreatePeer(context.Background(), &contracts.Peer{Name: "lost", Viewed: &no, Fingerprint: target, Grant: &contracts.PeerGrant{}}); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	var res RemoveResult
	if code := postDevices(t, srv+"/devices/"+target+"/remove-everywhere", `{}`, &res); code != http.StatusOK {
		t.Fatal(code)
	}
	if took := time.Since(start); took > 2*time.Second || len(res.Hosts) != 1 || res.Hosts[0].Result != "offline" {
		t.Fatalf("took %s: %+v", took, res.Hosts)
	}
}

// With the cursor unreadable, watchPeers
// never follows from the start of history (replaying every peer.* event),
// and keeps reloading the list on a short backoff.
func TestWatchPeersWithoutACursor(t *testing.T) {
	base := datatest.Start(t)
	writer := dataclient.New(base, "test")
	ctx := context.Background()
	row := &contracts.Peer{Name: "x", MeshURL: "http://x:1"}
	if err := writer.CreatePeer(ctx, row); err != nil {
		t.Fatal(err)
	}
	target, _ := url.Parse(base)
	proxy := httputil.NewSingleHostReverseProxy(target)
	var streams, lists atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/events" && strings.Contains(r.Header.Get("Accept"), "text/event-stream"):
			streams.Add(1)
		case r.URL.Path == "/events":
			http.Error(w, "broken", http.StatusInternalServerError)
			return
		case r.URL.Path == "/peers":
			lists.Add(1)
		}
		proxy.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	oldMin, oldMax := watchBackoffMin, watchCursorRetryMax
	watchBackoffMin, watchCursorRetryMax = 10*time.Millisecond, 20*time.Millisecond
	t.Cleanup(func() { watchBackoffMin, watchCursorRetryMax = oldMin, oldMax })
	m := NewManager(dataclient.New(srv.URL, "router"), Options{SelfName: "mac"})
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { m.watchPeers(runCtx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	waitFor(t, "the list", func() bool { return len(m.Statuses()) == 1 })
	if err := writer.DeletePeer(ctx, row.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the removal, by reload", func() bool { return len(m.Statuses()) == 0 })
	if n := streams.Load(); n != 0 {
		t.Fatalf("%d streams opened without a cursor", n)
	}
}

// A reload whose list read began before a rename can't land
// after the rename's own reload and put the old name back (what made
// TestInstallTokenNeverReachesAPeer's /peers/pop/version 404 under load).
func TestAStaleReloadNeverLandsLast(t *testing.T) {
	base := datatest.Start(t)
	writer := dataclient.New(base, "test")
	ctx := context.Background()
	row := &contracts.Peer{Name: "pop-os", MeshURL: "http://pop-os:17380"}
	if err := writer.CreatePeer(ctx, row); err != nil {
		t.Fatal(err)
	}
	target, _ := url.Parse(base)
	proxy := httputil.NewSingleHostReverseProxy(target)
	var holdNext atomic.Bool
	read, release := make(chan struct{}), make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/peers" && holdNext.CompareAndSwap(true, false) {
			// Read now (the old rows), answer later.
			rec := httptest.NewRecorder()
			proxy.ServeHTTP(rec, r)
			close(read)
			<-release
			for k, v := range rec.Header() {
				w.Header()[k] = v
			}
			w.WriteHeader(rec.Code)
			_, _ = w.Write(rec.Body.Bytes())
			return
		}
		proxy.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	m := NewManager(dataclient.New(srv.URL, "router"), Options{SelfName: "mac"})
	holdNext.Store(true)
	stale := make(chan error, 1)
	go func() { stale <- m.Reload(ctx) }()
	<-read
	row.Name = "pop"
	if err := writer.UpdatePeer(ctx, row); err != nil {
		t.Fatal(err)
	}
	fresh := make(chan error, 1)
	go func() { fresh <- m.Reload(ctx) }()
	time.Sleep(100 * time.Millisecond) // the fresh reload has read, if it can
	close(release)
	if err := <-stale; err != nil {
		t.Fatal(err)
	}
	if err := <-fresh; err != nil {
		t.Fatal(err)
	}
	if _, ok := m.Status("pop"); !ok {
		t.Fatalf("the rename was undone by a stale reload: %+v", m.Statuses())
	}
}

func relayedGet(t *testing.T, url, level string) int {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	req.Header.Set(contracts.MeshRelayHeader, "mac")
	req.Header.Set(contracts.MeshLevelHeader, level)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}

// The Router's /devices routes check their own edges: GET /devices over
// the mesh is admin only, /devices/activity validates its query,
// mesh.listening counts only from the Mesh Service, local writes are the
// user's, and remove-everywhere skips a host that hosts nothing.
func TestDeviceRouteEdges(t *testing.T) {
	h := newHarness(t)
	srv := newDevicesServer(t, h, h.mgr.Devices())
	ctx := context.Background()

	// GET /devices over the mesh is admin only, checked here too.
	if code := relayedGet(t, srv+"/devices", "control"); code != http.StatusForbidden {
		t.Fatalf("relayed control: %d", code)
	}
	if code := relayedGet(t, srv+"/devices", "admin"); code != http.StatusOK {
		t.Fatalf("relayed admin: %d", code)
	}

	// /devices/activity validates its query.
	for _, q := range []string{"?limit=x", "?limit=-1", "?limit=9999", "?before=no"} {
		resp, err := http.Get(srv + "/devices/activity" + q)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: %d", q, resp.StatusCode)
		}
	}

	// mesh.listening counts only from the Mesh Service; its platform is
	// this device's everywhere.
	forged, _ := json.Marshal(contracts.MeshListener{MeshURL: "http://evil:17380", Platform: "evil"})
	real, _ := json.Marshal(contracts.MeshListener{MeshURL: "http://mac.example.ts.net:17380", Platform: "ios"})
	_ = h.data.Append(ctx, &contracts.Envelope{Type: contracts.MeshListening, Actor: "mesh", Payload: real})
	_ = h.data.Append(ctx, &contracts.Envelope{Type: contracts.MeshListening, Actor: "user", Payload: forged})
	self := h.mgr.selfDevice(ctx)
	if self.MeshURL != "http://mac.example.ts.net:17380" || self.Platform != "ios" {
		t.Fatalf("self = %+v", self)
	}

	// Local writes are the user's.
	no := false
	fp := testFingerprint(80)
	if err := h.data.CreatePeer(ctx, &contracts.Peer{Name: "phone", Viewed: &no, Fingerprint: fp, Grant: &contracts.PeerGrant{Level: contracts.GrantView}}); err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodPut, srv+"/devices/"+fp+"/grant", strings.NewReader(`{"level":"chat"}`))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	evs, _ := h.data.Events(ctx, 0, "", []string{string(contracts.DeviceGrantChanged)})
	if len(evs) != 1 || evs[0].Actor != "user" {
		t.Fatalf("grant change by %+v", evs)
	}

	// Remove everywhere: a paired device that hosts nothing is skipped,
	// saying why; a retry keeps the device's name.
	if err := h.data.CreatePeer(ctx, &contracts.Peer{Name: "kiosk", MeshURL: "http://kiosk.example.ts.net:17380", Viewed: &no, Fingerprint: testFingerprint(81),
		Grant: &contracts.PeerGrant{Level: contracts.GrantView}, Remote: &contracts.PeerRemote{HostsSessions: &no}}); err != nil {
		t.Fatal(err)
	}
	var res RemoveResult
	if code := postDevices(t, srv+"/devices/"+fp+"/remove-everywhere", `{}`, &res); code != http.StatusOK || len(res.Hosts) != 1 || res.Hosts[0].Result != "skipped" || res.Hosts[0].Error == "" {
		t.Fatalf("skipped: %d %+v", code, res)
	}
	res = RemoveResult{}
	if code := postDevices(t, srv+"/devices/"+fp+"/remove-everywhere", `{"hosts":["kiosk"]}`, &res); code != http.StatusOK || res.Here != "absent" || res.Device.Name != "phone" {
		t.Fatalf("retry: %d %+v", code, res)
	}
}
