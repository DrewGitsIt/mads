package mesh

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/pkg/devicekey"
	"github.com/drewkidwell/mads/pkg/paircode"
	"github.com/drewkidwell/mads/services/data/datatest"
	"github.com/drewkidwell/mads/services/mesh/meshtest"
)

// node is one device's stack, in-process: a Data Service (behind its front
// door, so the install token carries host authority), its Router's mesh
// layer serving /devices, and (for a host) a real Mesh Service on a fake
// tailnet.
type node struct {
	name   string
	id     *devicekey.Identity
	data   *dataclient.Client
	mgr    *Manager
	router *httptest.Server
	mesh   *meshtest.Mesh
}

func newNode(t *testing.T, name string, host bool) *node {
	t.Helper()
	g := datatest.StartGuarded(t)
	n := &node{name: name, id: newIdentity(t), data: dataclient.New(g.URL, "router", dataclient.WithToken(g.Token))}
	n.mgr = NewManager(n.data, Options{SelfName: name, Identity: n.id, PairPoll: 20 * time.Millisecond, Platform: "darwin"})
	devices := n.mgr.Devices()
	api := http.NewServeMux()
	api.Handle("/devices", devices)
	api.Handle("/devices/", devices)
	api.HandleFunc("GET /events", func(w http.ResponseWriter, r *http.Request) {
		// A live stream: open until the caller (or its grant) goes.
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: hi\n\n"))
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	})
	api.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, []map[string]any{})
	})
	n.router = httptest.NewServer(n.mgr.Handler(api))
	t.Cleanup(n.router.Close)
	if host {
		// The tailnet capability allows admin, so the grant alone decides.
		n.mesh = meshtest.Start(t, meshtest.Options{Identity: n.id, HostName: name, DataURL: g.URL, Token: g.Token, RouterURL: n.router.URL, Access: []string{"admin"}})
	}
	return n
}

func (n *node) call(t *testing.T, method, path string, in any, out any) int {
	t.Helper()
	var body io.Reader
	if in != nil {
		raw, _ := json.Marshal(in)
		body = bytes.NewReader(raw)
	}
	req, _ := http.NewRequest(method, n.router.URL+path, body)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil && resp.StatusCode < 300 {
			t.Fatalf("%s %s: %d %s", method, path, resp.StatusCode, raw)
		}
	}
	return resp.StatusCode
}

func (n *node) code(t *testing.T) PairingCodeResponse {
	t.Helper()
	var c PairingCodeResponse
	if code := n.call(t, "POST", "/devices/pairing-code", struct{}{}, &c); code != http.StatusCreated {
		t.Fatalf("pairing code: %d", code)
	}
	return c
}

func (n *node) device(t *testing.T, fp string) (contracts.DeviceStatus, bool) {
	t.Helper()
	var list []contracts.DeviceStatus
	n.call(t, "GET", "/devices", nil, &list)
	for _, d := range list {
		if d.Fingerprint == fp {
			return d, true
		}
	}
	return contracts.DeviceStatus{}, false
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

type apiErr struct {
	Code  string `json:"code"`
	Error string `json:"error"`
}

// The whole handshake, two stacks: the host makes a code, the joiner
// presents it over mutual TLS pinned to the code's fingerprint, the host
// approves, the joiner learns the outcome and pins the host.
func TestPairingByCodeEndToEnd(t *testing.T) {
	host, joiner := newNode(t, "pop-os", true), newNode(t, "mac", false)
	c := host.code(t)
	if !strings.HasPrefix(c.Code, "mads-pair-1:") || c.Host.MeshURL != host.mesh.URL || c.Host.Fingerprint != string(host.id.Fingerprint()) {
		t.Fatalf("code = %+v", c)
	}
	// The code's secret is in the answer and nowhere in the host's log.
	parsed, _ := paircode.Parse(c.Code)
	evs, _ := host.data.Events(context.Background(), 0, "", nil)
	for _, ev := range evs {
		if bytes.Contains(ev.Payload, []byte(paircode.Hash(parsed.Secret))) || strings.Contains(string(ev.Payload), c.Code[12:40]) {
			t.Fatalf("%s carries the secret: %s", ev.Type, ev.Payload)
		}
	}

	// The host pastes its own code: refused here, nothing dialled.
	var e apiErr
	if code := host.call(t, "POST", "/devices/pair", map[string]string{"code": c.Code}, &e); code != http.StatusUnprocessableEntity || e.Code != "pair_self" {
		t.Fatalf("own code: %d %+v", code, e)
	}

	var st JoinStatus
	if code := joiner.call(t, "POST", "/devices/pair", map[string]string{"code": c.Code}, &st); code != http.StatusAccepted || st.State != contracts.PairPending ||
		st.Self != joiner.id.Fingerprint().Short() || st.ID != host.id.Fingerprint().Short()+":"+st.RequestID {
		t.Fatalf("join: %d %+v", code, st)
	}
	var reqs []contracts.PairRequest
	host.call(t, "GET", "/devices/pair-requests", nil, &reqs)
	if len(reqs) != 1 || reqs[0].Fingerprint != string(joiner.id.Fingerprint()) || reqs[0].Name != "mac" || reqs[0].Platform != "darwin" || reqs[0].State != contracts.PairPending {
		t.Fatalf("requests = %+v", reqs)
	}
	// Used up: presenting it again is refused, generically.
	other := newNode(t, "spark", false)
	if code := other.call(t, "POST", "/devices/pair", map[string]string{"code": c.Code}, &e); code != http.StatusForbidden || e.Code != "pairing_refused" {
		t.Fatalf("reused code: %d %+v", code, e)
	}
	// Admin is never offered by approval.
	if code := host.call(t, "POST", "/devices/pair-requests/"+reqs[0].ID+"/approve", map[string]string{"level": "admin"}, &e); code != http.StatusUnprocessableEntity {
		t.Fatalf("approve at admin: %d", code)
	}
	if code := host.call(t, "POST", "/devices/pair-requests/"+reqs[0].ID+"/approve", map[string]string{"level": "control"}, nil); code != http.StatusOK {
		t.Fatalf("approve: %d", code)
	}
	waitFor(t, "the joiner to learn it was approved", func() bool {
		joiner.call(t, "GET", "/devices/pair/"+st.ID, nil, &st)
		return st.State != contracts.PairPending
	})
	if st.State != contracts.PairApproved || st.Level != "control" || st.PeerID == "" || st.Error != "" {
		t.Fatalf("joined = %+v", st)
	}

	// The host has the joiner at control, paired by code.
	mac, ok := host.device(t, string(joiner.id.Fingerprint()))
	if !ok || mac.Level() != contracts.GrantControl || mac.Pairing.How != contracts.PairCode || mac.IsViewed() || mac.Remote == nil || mac.Remote.Platform != "darwin" {
		t.Fatalf("host's row = %+v", mac)
	}
	// The joiner pinned the host granting it nothing, viewed it (it may
	// view there), and knows what it's let do there.
	pop, ok := joiner.device(t, string(host.id.Fingerprint()))
	if !ok || pop.Name != "pop-os" || pop.Grant != nil || !pop.IsViewed() || pop.Pairing.How != contracts.PairCode ||
		pop.Remote == nil || pop.Remote.Level != contracts.GrantControl || pop.Remote.AsOf == nil {
		t.Fatalf("joiner's row = %+v remote %+v", pop, pop.Remote)
	}
	// Its hello: platform, hosts sessions; /peers carries the same answer.
	joiner.mgr.ProbeAll(context.Background())
	var peers []contracts.PeerStatus
	joiner.call(t, "GET", "/peers", nil, &peers)
	if len(peers) != 1 || peers[0].Reachability != contracts.PeerConnected || peers[0].Remote == nil ||
		peers[0].Remote.Level != contracts.GrantControl || peers[0].Remote.Platform != "linux" || peers[0].Remote.HostsSessions == nil || !*peers[0].Remote.HostsSessions {
		t.Fatalf("joiner /peers = %+v", peers)
	}

	// Pairing again with the same host is refused on both sides.
	c2 := host.code(t)
	if code := joiner.call(t, "POST", "/devices/pair", map[string]string{"code": c2.Code}, &e); code != http.StatusConflict || e.Code != "already_paired" {
		t.Fatalf("pair again (joiner): %d %+v", code, e)
	}
	twin := newNode(t, "mac", false)
	twin.mgr.opts.Identity = joiner.id // the same key, a fresh install
	if code := twin.call(t, "POST", "/devices/pair", map[string]string{"code": c2.Code}, &e); code != http.StatusConflict || e.Code != "already_paired" {
		t.Fatalf("pair again (host): %d %+v", code, e)
	}

	// The host's activity log has it all, newest first, and by device.
	var act []contracts.Envelope
	host.call(t, "GET", "/devices/activity?device="+string(joiner.id.Fingerprint()), nil, &act)
	types := map[contracts.EventType]bool{}
	for _, ev := range act {
		types[ev.Type] = true
	}
	if !types[contracts.DevicePaired] || !types[contracts.DevicePairRequested] || len(act) < 2 || act[0].Seq < act[len(act)-1].Seq {
		t.Fatalf("activity = %v", types)
	}
}

// A code whose fingerprint isn't the key the host proves is refused in
// the handshake: the secret never reaches whoever answers.
func TestPairingPinsTheHostKey(t *testing.T) {
	host, joiner := newNode(t, "pop-os", true), newNode(t, "mac", false)
	c := host.code(t)
	parsed, _ := paircode.Parse(c.Code)
	impostor := newIdentity(t)
	parsed.Fingerprint = impostor.Fingerprint()
	var e apiErr
	if code := joiner.call(t, "POST", "/devices/pair", map[string]string{"code": paircode.Encode(parsed)}, &e); code != http.StatusBadGateway || e.Code != "host_key_mismatch" {
		t.Fatalf("wrong pin: %d %+v", code, e)
	}
	var reqs []contracts.PairRequest
	host.call(t, "GET", "/devices/pair-requests", nil, &reqs)
	if len(reqs) != 0 {
		t.Fatalf("a request was made through a wrong pin: %+v", reqs)
	}
	// The code wasn't consumed: the real one still works.
	var st JoinStatus
	if code := joiner.call(t, "POST", "/devices/pair", map[string]string{"code": c.Code}, &st); code != http.StatusAccepted {
		t.Fatalf("the real code after a wrong pin: %d", code)
	}
	// Bad input is named.
	if code := joiner.call(t, "POST", "/devices/pair", map[string]string{"code": "pop-os 4TXP"}, &e); code != http.StatusUnprocessableEntity || e.Code != "bad_code" {
		t.Fatalf("junk: %d %+v", code, e)
	}
	v2 := strings.Replace(c.Code, "mads-pair-1:", "mads-pair-2:", 1)
	if code := joiner.call(t, "POST", "/devices/pair", map[string]string{"code": v2}, &e); code != http.StatusUnprocessableEntity || e.Code != "code_version" {
		t.Fatalf("v2: %d %+v", code, e)
	}
}

func TestPairingDeclined(t *testing.T) {
	host, joiner := newNode(t, "pop-os", true), newNode(t, "mac", false)
	c := host.code(t)
	var st JoinStatus
	joiner.call(t, "POST", "/devices/pair", map[string]string{"code": c.Code}, &st)
	if code := host.call(t, "POST", "/devices/pair-requests/"+st.RequestID+"/decline", struct{}{}, nil); code != http.StatusOK {
		t.Fatalf("decline: %d", code)
	}
	waitFor(t, "the joiner to learn it was declined", func() bool {
		joiner.call(t, "GET", "/devices/pair/"+st.ID, nil, &st)
		return st.State != contracts.PairPending
	})
	if st.State != contracts.PairDeclined {
		t.Fatalf("state = %s", st.State)
	}
	if _, ok := joiner.device(t, string(host.id.Fingerprint())); ok {
		t.Fatal("a declined host was pinned")
	}
	if _, ok := host.device(t, string(joiner.id.Fingerprint())); ok {
		t.Fatal("a declined joiner was pinned")
	}
}

// pairNodes pairs joiner with host at level, by code, and has the joiner
// learn it.
func pairNodes(t *testing.T, host, joiner *node, level string) {
	t.Helper()
	c := host.code(t)
	var st JoinStatus
	if code := joiner.call(t, "POST", "/devices/pair", map[string]string{"code": c.Code}, &st); code != http.StatusAccepted {
		t.Fatalf("join: %d", code)
	}
	if code := host.call(t, "POST", "/devices/pair-requests/"+st.RequestID+"/approve", map[string]string{"level": level}, nil); code != http.StatusOK {
		t.Fatalf("approve: %d", code)
	}
	waitFor(t, "approval", func() bool {
		joiner.call(t, "GET", "/devices/pair/"+st.ID, nil, &st)
		return st.State == contracts.PairApproved
	})
}

// Admin over the mesh: an admin device introduces devices (at most its own
// level, never admin), changes levels (never to admin, lowering is fine),
// removes devices, and nothing else; a device below admin can do none of
// it. Then remove everywhere.
func TestAdminDeviceOverTheMesh(t *testing.T) {
	host, mac := newNode(t, "pop-os", true), newNode(t, "mac", false)
	pairNodes(t, host, mac, "control")
	macFP := string(mac.id.Fingerprint())
	hostFP := string(host.id.Fingerprint())

	// A phone paired on the mac, to introduce.
	phone := newIdentity(t)
	no, yes := false, true
	if err := mac.data.CreatePeer(context.Background(), &contracts.Peer{Name: "phone", Viewed: &no, Fingerprint: string(phone.Fingerprint()),
		Grant: &contracts.PeerGrant{Level: contracts.GrantChat}, Remote: &contracts.PeerRemote{Platform: "ios", HostsSessions: &no}}); err != nil {
		t.Fatal(err)
	}
	var e apiErr
	// At control, the mac administers nothing there.
	if code := mac.call(t, "POST", "/devices/"+string(phone.Fingerprint())+"/introduce", IntroduceRequest{Host: "pop-os", Level: "chat"}, &e); code != http.StatusForbidden || e.Code != "not_admin" {
		t.Fatalf("introduce below admin: %d %+v", code, e)
	}
	// The host makes it admin (only the host can).
	// Capped: no shell (what it introduces or raises is at least as
	// capped).
	if code := host.call(t, "PUT", "/devices/"+macFP+"/grant", GrantRequest{Level: "admin", Ceilings: &contracts.PeerCeilings{NoShell: true}}, nil); code != http.StatusOK {
		t.Fatalf("host grants admin: %d", code)
	}
	waitFor(t, "the mac to read that it's admin there", func() bool {
		mac.mgr.ProbeAll(context.Background())
		d, _ := mac.device(t, hostFP)
		return d.Remote != nil && d.Remote.Level == contracts.GrantAdmin
	})
	if code := mac.call(t, "POST", "/devices/"+string(phone.Fingerprint())+"/introduce", IntroduceRequest{Host: "pop-os", Level: "admin"}, &e); code != http.StatusUnprocessableEntity {
		t.Fatalf("introduce at admin: %d %+v", code, e)
	}
	if code := mac.call(t, "POST", "/devices/"+string(phone.Fingerprint())+"/introduce", IntroduceRequest{Host: "pop-os", Level: "chat"}, &e); code != http.StatusCreated {
		t.Fatalf("introduce: %d %+v", code, e)
	}
	ph, ok := host.device(t, string(phone.Fingerprint()))
	if !ok || ph.Level() != contracts.GrantChat || ph.Pairing.How != contracts.PairIntroduction || ph.IsViewed() || ph.Remote == nil || ph.Remote.Platform != "ios" {
		t.Fatalf("introduced = %+v", ph)
	}
	if !ph.Grant.Ceilings.NoShell {
		t.Fatalf("introduced without the introducer's limits: %+v", ph.Grant)
	}
	if !strings.HasPrefix(ph.Pairing.PairedBy, "device:mac/") {
		t.Fatalf("paired_by = %q, want the introducer", ph.Pairing.PairedBy)
	}
	var intro []contracts.Envelope
	intro, _ = host.data.Events(context.Background(), 0, "", []string{string(contracts.DeviceIntroduced)})
	if len(intro) != 1 || !strings.HasPrefix(intro[0].Actor, "device:mac/") {
		t.Fatalf("device.introduced = %+v", intro)
	}
	// An introduced device's address must be on the host's tailnet.
	evil := newIdentity(t)
	if err := mac.data.CreatePeer(context.Background(), &contracts.Peer{Name: "evil", MeshURL: "http://10.1.2.3:22", Viewed: &no, Fingerprint: string(evil.Fingerprint())}); err != nil {
		t.Fatal(err)
	}
	if code := mac.call(t, "POST", "/devices/"+string(evil.Fingerprint())+"/introduce", IntroduceRequest{Host: "pop-os", Level: "view"}, &e); code != http.StatusUnprocessableEntity || e.Code != "bad_mesh_url" {
		t.Fatalf("introduce an address off the tailnet: %d %+v", code, e)
	}
	if ev, err := mac.data.Peer(context.Background(), "evil"); err == nil {
		_ = mac.data.DeletePeer(context.Background(), ev.ID)
	}
	// With the tailnet required the host can't confirm whose an
	// address is (CGNAT space), so only a MagicDNS name; any port.
	for _, tc := range []struct {
		url  string
		want int
	}{{"http://100.100.1.2:17380", http.StatusUnprocessableEntity}, {"http://tab.example.ts.net:9000", http.StatusCreated}} {
		id := newIdentity(t)
		if err := mac.data.CreatePeer(context.Background(), &contracts.Peer{Name: "tab", MeshURL: tc.url, Viewed: &no, Fingerprint: string(id.Fingerprint())}); err != nil {
			t.Fatal(err)
		}
		if code := mac.call(t, "POST", "/devices/"+string(id.Fingerprint())+"/introduce", IntroduceRequest{Host: "pop-os", Level: "view"}, &e); code != tc.want {
			t.Fatalf("introduce %s: %d %+v", tc.url, code, e)
		}
		if row, err := mac.data.Peer(context.Background(), "tab"); err == nil {
			_ = mac.data.DeletePeer(context.Background(), row.ID)
		}
		if row, ok := host.device(t, string(id.Fingerprint())); ok {
			_ = host.data.DeletePeer(context.Background(), row.ID)
		}
	}
	if code := mac.call(t, "POST", "/devices/"+string(phone.Fingerprint())+"/introduce", IntroduceRequest{Host: "pop-os", Level: "chat"}, &e); code != http.StatusConflict || e.Code != "already_paired" {
		t.Fatalf("introduce twice: %d %+v", code, e)
	}

	// Levels over the mesh, straight to the host's Mesh Service.
	hostRow, _ := mac.device(t, hostFP)
	remote := func(method, path string, in any) (int, string) {
		code, body, err := mac.mgr.peerCall(context.Background(), hostRow.Peer, method, path, in)
		if err != nil {
			t.Fatal(err)
		}
		c, _ := errorBody(body)
		return code, c
	}
	phonePath := "/devices/" + string(phone.Fingerprint()) + "/grant"
	if code, c := remote("PUT", phonePath, GrantRequest{Level: "admin"}); code != http.StatusForbidden || c != "admin_host_only" {
		t.Fatalf("raise to admin remotely: %d %s", code, c)
	}
	if code, c := remote("PUT", phonePath, GrantRequest{Level: "chat", Ceilings: &contracts.PeerCeilings{FullTrustChat: contracts.FullTrustChatRefused}}); code != http.StatusForbidden || c != "local_only" {
		t.Fatalf("ceilings remotely: %d %s", code, c)
	}
	if code, c := remote("PUT", phonePath, GrantRequest{Level: "control"}); code != http.StatusOK {
		t.Fatalf("raise to control remotely: %d %s", code, c)
	}
	// None and back keeps the limits.
	if code, c := remote("PUT", phonePath, GrantRequest{Level: "none"}); code != http.StatusOK {
		t.Fatalf("to none remotely: %d %s", code, c)
	}
	if code, c := remote("PUT", phonePath, GrantRequest{Level: "control"}); code != http.StatusOK {
		t.Fatalf("back to control remotely: %d %s", code, c)
	}
	if ph, _ = host.device(t, string(phone.Fingerprint())); ph.Level() != contracts.GrantControl || !ph.Grant.Ceilings.NoShell {
		t.Fatalf("none and back: %+v", ph.Grant)
	}
	// Another admin there may be lowered remotely (Q2's safe direction).
	other := newIdentity(t)
	if err := host.data.CreatePeer(context.Background(), &contracts.Peer{Name: "laptop", Viewed: &no, Fingerprint: string(other.Fingerprint()), Grant: &contracts.PeerGrant{Level: contracts.GrantAdmin}}); err != nil {
		t.Fatal(err)
	}
	if code, c := remote("PUT", "/devices/"+string(other.Fingerprint())+"/grant", GrantRequest{Level: "view"}); code != http.StatusOK {
		t.Fatalf("lower an admin remotely: %d %s", code, c)
	}
	// A device paired at none before grants carried levels (no grant, not
	// viewed) is managed remotely like any other.
	legacy := newIdentity(t)
	if err := host.data.CreatePeer(context.Background(), &contracts.Peer{Name: "legacy", Viewed: &no, Fingerprint: string(legacy.Fingerprint())}); err != nil {
		t.Fatal(err)
	}
	if code, c := remote("PUT", "/devices/"+string(legacy.Fingerprint())+"/grant", GrantRequest{Level: "view"}); code != http.StatusOK {
		t.Fatalf("raise a device paired at none with no grant row remotely: %d %s", code, c)
	}
	// The raw peer CRUD stays shut.
	if code, _ := remote("GET", "/peers", nil); code != http.StatusForbidden {
		t.Fatalf("/peers over the mesh: %d", code)
	}
	// The list, admin only.
	if code, _ := remote("GET", "/devices", nil); code != http.StatusOK {
		t.Fatalf("list as admin: %d", code)
	}
	_ = yes

	// Remove everywhere: here, and on pop-os (administered). A host the mac
	// can't manage is listed.
	spark := newIdentity(t)
	if err := mac.data.CreatePeer(context.Background(), &contracts.Peer{Name: "spark", MeshURL: "http://127.0.0.1:1", Viewed: &no, Fingerprint: string(spark.Fingerprint()),
		Remote: &contracts.PeerRemote{Level: contracts.GrantView, AsOf: ptrTime(time.Now())}}); err != nil {
		t.Fatal(err)
	}
	var res RemoveResult
	if code := mac.call(t, "POST", "/devices/"+string(phone.Fingerprint())+"/remove-everywhere", struct{}{}, &res); code != http.StatusOK {
		t.Fatalf("remove everywhere: %d", code)
	}
	if res.Here != "removed" || len(res.Hosts) != 1 || res.Hosts[0].Name != "pop-os" || res.Hosts[0].Result != "removed" ||
		len(res.CannotManage) != 1 || res.CannotManage[0].Name != "spark" || res.CannotManage[0].Level != "view" {
		t.Fatalf("result = %+v", res)
	}
	if _, ok := host.device(t, string(phone.Fingerprint())); ok {
		t.Fatal("still paired on the host")
	}
	if _, ok := mac.device(t, string(phone.Fingerprint())); ok {
		t.Fatal("still paired here")
	}

	// Lowered to control on the host, the mac's admin routes are refused
	// by the Mesh Service.
	if code := host.call(t, "PUT", "/devices/"+macFP+"/grant", GrantRequest{Level: "control"}, nil); code != http.StatusOK {
		t.Fatal(code)
	}
	waitFor(t, "the host's Mesh Service to see the lower grant", func() bool {
		code, _ := remote("GET", "/devices", nil)
		return code == http.StatusForbidden
	})
	if code, c := remote("DELETE", "/devices/"+string(other.Fingerprint()), nil); code != http.StatusForbidden || c != "no_access" {
		t.Fatalf("remove as control: %d %s", code, c)
	}
}

func ptrTime(t time.Time) *time.Time { return &t }

// A grant changed remotely, by an admin device, ends the changed device's
// open streams on the host, as a change made on the host does.
func TestRemoteGrantChangeEndsStreams(t *testing.T) {
	host, mac, spark := newNode(t, "pop-os", true), newNode(t, "mac", false), newNode(t, "spark", false)
	pairNodes(t, host, mac, "control")
	pairNodes(t, host, spark, "view")
	if code := host.call(t, "PUT", "/devices/"+string(mac.id.Fingerprint())+"/grant", GrantRequest{Level: "admin"}, nil); code != http.StatusOK {
		t.Fatal(code)
	}
	hostRow, ok := spark.device(t, string(host.id.Fingerprint()))
	if !ok {
		t.Fatal("spark didn't pin the host")
	}
	c, err := spark.mgr.conn(hostRow.Peer)
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodGet, "https://"+c.host+"/events", nil)
	resp, err := c.rt.RoundTrip(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("stream: %v %v", err, resp)
	}
	ended := make(chan struct{})
	go func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		close(ended)
	}()
	macRow, _ := mac.device(t, string(host.id.Fingerprint()))
	waitFor(t, "the mac to be admin there", func() bool {
		code, _, err := mac.mgr.peerCall(context.Background(), macRow.Peer, "GET", "/devices", nil)
		return err == nil && code == http.StatusOK
	})
	code, body, err := mac.mgr.peerCall(context.Background(), macRow.Peer, "PUT", "/devices/"+string(spark.id.Fingerprint())+"/grant", GrantRequest{Level: "chat"})
	if err != nil || code != http.StatusOK {
		t.Fatalf("remote grant change: %d %s %v", code, body, err)
	}
	select {
	case <-ended:
	case <-time.After(10 * time.Second):
		t.Fatal("the stream outlived the grant it was opened under")
	}
}
