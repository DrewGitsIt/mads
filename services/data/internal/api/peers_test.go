package api

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/devicekey"
	"github.com/drewkidwell/mads/pkg/localauth"
	"github.com/drewkidwell/mads/services/data/internal/store"
)

// newInstallServer is the Data API as the front door hands it requests
// carrying the install token.
func newInstallServer(t *testing.T) *httptest.Server {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := store.Migrate(db, store.Migrations()); err != nil {
		t.Fatal(err)
	}
	mux := New(db)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mux.ServeHTTP(w, r.WithContext(localauth.WithPrincipal(r.Context(), localauth.Principal{Kind: localauth.Install})))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func sendPeer(t *testing.T, srv *httptest.Server, method, path string, p contracts.Peer, header map[string]string) (int, contracts.Peer) {
	t.Helper()
	body, _ := json.Marshal(p)
	req, err := http.NewRequest(method, srv.URL+path, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var out contracts.Peer
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func adminPeer(seed byte) contracts.Peer {
	no := false
	return contracts.Peer{
		Name: "laptop", Viewed: &no,
		Fingerprint: string(devicekey.FingerprintOf(ed25519.PublicKey(bytes.Repeat([]byte{seed}, ed25519.PublicKeySize)))),
		Grant:       &contracts.PeerGrant{Level: contracts.GrantAdmin},
	}
}

// Q2: admin is granted only on the host itself — with the install token,
// presented directly. Not without it, and not through the mesh.
func TestAdminGrantNeedsTheHostsOwnInstallToken(t *testing.T) {
	// No install principal (the bare mux, as a request the door didn't
	// vouch for would be).
	bare, _ := newTestServer(t)
	if code, _ := sendPeer(t, bare, "POST", "/peers", adminPeer(1), nil); code != http.StatusForbidden {
		t.Fatalf("admin without the install token: %d, want 403", code)
	}

	srv := newInstallServer(t)
	mesh := map[string]string{"X-Mads-Mesh-Peer": "mac.example.ts.net"}
	if code, _ := sendPeer(t, srv, "POST", "/peers", adminPeer(1), mesh); code != http.StatusForbidden {
		t.Fatalf("admin through the mesh: %d, want 403", code)
	}
	// The marker's presence counts, not its value: a caller with no node
	// name is still a mesh caller.
	if code, _ := sendPeer(t, srv, "POST", "/peers", adminPeer(1), map[string]string{"X-Mads-Mesh-Peer": ""}); code != http.StatusForbidden {
		t.Fatalf("admin through the mesh with an empty marker: %d, want 403", code)
	}
	// A relayed write is held to what the relaying device may do: it must
	// be an admin here (D5). An admin device introduces a device
	// at control, may lower an admin and may remove one; it may not raise
	// a device to admin, nor change anything but the level.
	introducer := adminPeer(3)
	introducer.Name = "mac"
	if code, _ := sendPeer(t, srv, "POST", "/peers", introducer, nil); code != http.StatusCreated {
		t.Fatalf("pin the admin introducer on the host: %d", code)
	}
	viaAdmin := map[string]string{"X-Mads-Mesh-Peer": "mac", "X-Mads-Mesh-Device": introducer.Fingerprint}
	ctl := adminPeer(1)
	ctl.Grant.Level = contracts.GrantControl
	if code, _ := sendPeer(t, srv, "POST", "/peers", ctl, mesh); code != http.StatusForbidden {
		t.Fatalf("relayed with no device: %d, want 403", code)
	}
	if code, _ := sendPeer(t, srv, "POST", "/peers", ctl, viaAdmin); code != http.StatusForbidden {
		t.Fatalf("relayed create that isn't an introduction: %d, want 403", code)
	}
	ctl.Pairing = &contracts.PeerPairing{How: contracts.PairIntroduction}
	code, p := sendPeer(t, srv, "POST", "/peers", ctl, viaAdmin)
	if code != http.StatusCreated || p.Pairing == nil || p.Pairing.How != contracts.PairIntroduction {
		t.Fatalf("introduce at control: %d %+v", code, p.Pairing)
	}
	// Logged as an introduction, by the introducer, not as a pairing.
	evResp, err := srv.Client().Get(srv.URL + "/events?types=device.paired,device.introduced")
	if err != nil {
		t.Fatal(err)
	}
	var evs []contracts.Envelope
	_ = json.NewDecoder(evResp.Body).Decode(&evs)
	_ = evResp.Body.Close()
	if last := evs[len(evs)-1]; last.Type != contracts.DeviceIntroduced || !strings.Contains(string(last.Payload), ctl.Fingerprint) {
		t.Fatalf("last device event = %s %s", last.Type, last.Payload)
	}
	// Raising to admin through the mesh is refused; on the host it's fine.
	p.Grant = &contracts.PeerGrant{Level: contracts.GrantAdmin}
	if code, _ := sendPeer(t, srv, "PUT", "/peers/"+p.ID, p, viaAdmin); code != http.StatusForbidden {
		t.Fatalf("raise to admin through the mesh: %d, want 403", code)
	}
	if code, _ := sendPeer(t, srv, "PUT", "/peers/"+p.ID, p, nil); code != http.StatusOK {
		t.Fatalf("raise to admin on the host: %d", code)
	}
	// Remotely only the level changes: a rename is the host's.
	renamed := p
	renamed.Name = "work-laptop"
	if code, _ := sendPeer(t, srv, "PUT", "/peers/"+p.ID, renamed, viaAdmin); code != http.StatusForbidden {
		t.Fatalf("rename through the mesh: %d, want 403", code)
	}
	// Lowering an admin grants nothing: any admin may (Q2).
	p.Grant.Level = contracts.GrantChat
	if code, _ := sendPeer(t, srv, "PUT", "/peers/"+p.ID, p, viaAdmin); code != http.StatusOK {
		t.Fatalf("lower an admin: %d", code)
	}
	// A new key at admin is a new grant, even on a row that was admin.
	p.Grant.Level = contracts.GrantAdmin
	if code, _ := sendPeer(t, srv, "PUT", "/peers/"+p.ID, p, nil); code != http.StatusOK {
		t.Fatal("re-raise on the host")
	}
	p.Fingerprint = adminPeer(2).Fingerprint
	if code, _ := sendPeer(t, srv, "PUT", "/peers/"+p.ID, p, viaAdmin); code != http.StatusForbidden {
		t.Fatalf("swap the key under an admin grant through the mesh: %d, want 403", code)
	}
	// Ceilings are the host's: a relayed write never loosens them. None
	// keeps the grant and its limits; dropping the grant (and with it the
	// limits) is refused, so none and back can't shed them.
	p.Grant = &contracts.PeerGrant{Level: contracts.GrantChat, Ceilings: contracts.PeerCeilings{FullTrustChat: contracts.FullTrustChatRefused}}
	_, p = sendPeer(t, srv, "PUT", "/peers/"+p.ID, p, nil)
	loose := p
	loose.Grant = &contracts.PeerGrant{Level: contracts.GrantChat}
	if code, _ := sendPeer(t, srv, "PUT", "/peers/"+p.ID, loose, viaAdmin); code != http.StatusForbidden {
		t.Fatalf("loosen a ceiling through the mesh: %d, want 403", code)
	}
	dropped := p
	dropped.Grant = nil
	if code, _ := sendPeer(t, srv, "PUT", "/peers/"+p.ID, dropped, viaAdmin); code != http.StatusForbidden {
		t.Fatalf("drop a grant (and its limits) through the mesh: %d, want 403", code)
	}
	p.Grant = &contracts.PeerGrant{Level: contracts.GrantNone, Ceilings: p.Grant.Ceilings}
	if code, np := sendPeer(t, srv, "PUT", "/peers/"+p.ID, p, viaAdmin); code != http.StatusOK || np.Grant == nil || np.Grant.Ceilings.FullTrustChat != contracts.FullTrustChatRefused {
		t.Fatalf("take a device to none through the mesh, keeping its limits: %d %+v", code, np.Grant)
	}
	_, p = sendPeer(t, srv, "GET", "/peers/"+p.ID, contracts.Peer{}, nil)
	back := p
	back.Grant = &contracts.PeerGrant{Level: contracts.GrantControl}
	if code, _ := sendPeer(t, srv, "PUT", "/peers/"+p.ID, back, viaAdmin); code != http.StatusForbidden {
		t.Fatalf("raise from none shedding the limits: %d, want 403", code)
	}
	back.Grant.Ceilings = p.Grant.Ceilings
	if code, np := sendPeer(t, srv, "PUT", "/peers/"+p.ID, back, viaAdmin); code != http.StatusOK || np.Grant.Ceilings.FullTrustChat != contracts.FullTrustChatRefused {
		t.Fatalf("raise from none keeping the limits: %d", code)
	}

	// A capped admin (C2 and C3 here) lends no more than it has: an
	// introduction takes at least its limits, and so does a device it
	// raises (so its own second key can't come in uncapped).
	capped := adminPeer(4)
	capped.Name = "capped"
	capped.Grant.Ceilings = contracts.PeerCeilings{NoShell: true, MaxTrust: contracts.TrustLimited}
	if code, _ := sendPeer(t, srv, "POST", "/peers", capped, nil); code != http.StatusCreated {
		t.Fatal(code)
	}
	viaCapped := map[string]string{"X-Mads-Mesh-Peer": "capped", "X-Mads-Mesh-Device": capped.Fingerprint}
	second := adminPeer(5)
	second.Name = "second-key"
	second.Grant = &contracts.PeerGrant{Level: contracts.GrantControl}
	second.Pairing = &contracts.PeerPairing{How: contracts.PairIntroduction}
	if code, _ := sendPeer(t, srv, "POST", "/peers", second, viaCapped); code != http.StatusForbidden {
		t.Fatalf("a capped admin introduces an uncapped key: %d, want 403", code)
	}
	second.Grant.Ceilings = capped.Grant.Ceilings
	code, sp := sendPeer(t, srv, "POST", "/peers", second, viaCapped)
	if code != http.StatusCreated {
		t.Fatalf("introduce with the introducer's limits: %d", code)
	}
	// A device the host left uncapped, raised by the capped admin, takes
	// its limits.
	plain := adminPeer(6)
	plain.Name = "plain"
	plain.Grant = &contracts.PeerGrant{Level: contracts.GrantView}
	_, plain = sendPeer(t, srv, "POST", "/peers", plain, nil)
	plain.Grant.Level = contracts.GrantControl
	if code, _ := sendPeer(t, srv, "PUT", "/peers/"+plain.ID, plain, viaCapped); code != http.StatusForbidden {
		t.Fatalf("raise without the raiser's limits: %d, want 403", code)
	}
	plain.Grant.Ceilings = capped.Grant.Ceilings
	if code, _ := sendPeer(t, srv, "PUT", "/peers/"+plain.ID, plain, viaCapped); code != http.StatusOK {
		t.Fatalf("raise with the raiser's limits: %d", code)
	}
	_ = sp
	// A device paired at none before grants carried levels
	// (paired, not viewed, no grant) reads as a grant at none: managed
	// remotely like any other.
	legacy := adminPeer(8)
	legacy.Name, legacy.Grant = "legacy", nil
	_, legacy = sendPeer(t, srv, "POST", "/peers", legacy, nil)
	legacy.Grant = &contracts.PeerGrant{Level: contracts.GrantView}
	if code, _ := sendPeer(t, srv, "PUT", "/peers/"+legacy.ID, legacy, viaAdmin); code != http.StatusOK {
		t.Fatalf("raise a device paired at none with no grant row remotely: %d", code)
	}
	if code, _ := sendPeer(t, srv, "DELETE", "/peers/"+legacy.ID, contracts.Peer{}, viaAdmin); code != http.StatusOK {
		t.Fatalf("remove it remotely: %d", code)
	}
	// A device this host only views (no grant) isn't managed remotely.
	viewOnly := adminPeer(7)
	viewOnly.Name, viewOnly.Grant, viewOnly.MeshURL = "viewed", nil, "http://viewed:17380"
	yes := true
	viewOnly.Viewed = &yes
	_, viewOnly = sendPeer(t, srv, "POST", "/peers", viewOnly, nil)
	if code, _ := sendPeer(t, srv, "DELETE", "/peers/"+viewOnly.ID, contracts.Peer{}, viaAdmin); code != http.StatusForbidden {
		t.Fatalf("remove a view-only row through the mesh: %d, want 403", code)
	}
	viewOnly.Grant = &contracts.PeerGrant{Level: contracts.GrantView}
	if code, _ := sendPeer(t, srv, "PUT", "/peers/"+viewOnly.ID, viewOnly, viaAdmin); code != http.StatusForbidden {
		t.Fatalf("grant a view-only row through the mesh: %d, want 403", code)
	}
	// A device that isn't an admin here can't relay a change at all.
	lowered := introducer
	lowered.Grant = &contracts.PeerGrant{Level: contracts.GrantControl}
	var cur contracts.Peer
	_, cur = sendPeer(t, srv, "GET", "/peers/mac", contracts.Peer{}, nil)
	lowered.ID, lowered.UpdatedAt = cur.ID, cur.UpdatedAt
	if code, _ := sendPeer(t, srv, "PUT", "/peers/"+cur.ID, lowered, nil); code != http.StatusOK {
		t.Fatalf("lower the introducer on the host: %d", code)
	}
	if code, _ := sendPeer(t, srv, "DELETE", "/peers/"+p.ID, contracts.Peer{}, viaAdmin); code != http.StatusForbidden {
		t.Fatalf("remove through a device no longer admin: %d, want 403", code)
	}
}

// PUT and DELETE with If-Match are conditional: a row changed since the
// caller's read is 409 stale and untouched.
func TestPeerIfMatch(t *testing.T) {
	srv := newInstallServer(t)
	code, p := sendPeer(t, srv, "POST", "/peers", contracts.Peer{Name: "pop-os", MeshURL: "http://pop-os:17380"}, nil)
	if code != http.StatusCreated {
		t.Fatal(code)
	}
	read := p.UpdatedAt.Format(time.RFC3339Nano)
	p.Hidden = true
	if code, _ := sendPeer(t, srv, "PUT", "/peers/"+p.ID, p, map[string]string{"If-Match": read}); code != http.StatusOK {
		t.Fatalf("fresh conditional PUT: %d", code)
	}
	p.Hidden = false
	if code, _ := sendPeer(t, srv, "PUT", "/peers/"+p.ID, p, map[string]string{"If-Match": read}); code != http.StatusConflict {
		t.Fatalf("stale PUT: %d, want 409", code)
	}
	if code, _ := sendPeer(t, srv, "DELETE", "/peers/"+p.ID, contracts.Peer{}, map[string]string{"If-Match": read}); code != http.StatusConflict {
		t.Fatalf("stale DELETE: %d, want 409", code)
	}
	if code, _ := sendPeer(t, srv, "PUT", "/peers/"+p.ID, p, map[string]string{"If-Match": "yesterday"}); code != http.StatusBadRequest {
		t.Fatalf("malformed If-Match: %d, want 400", code)
	}
}
