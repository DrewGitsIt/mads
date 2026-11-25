package store

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/devicekey"
)

func TestPeersRegistry(t *testing.T) {
	ctx := context.Background()
	l := openMigratedDB(t)
	peers := NewPeers(l)

	p := &contracts.Peer{Name: "pop-os", DNSName: "pop-os.example.ts.net", MeshURL: "http://pop-os.example.ts.net:17380"}
	if err := peers.Create(ctx, p, "test"); err != nil {
		t.Fatal(err)
	}
	if p.ID == "" || p.ID[:4] != "per_" {
		t.Fatalf("id = %q, want a per_ id", p.ID)
	}
	got, err := peers.GetByName(ctx, "pop-os")
	if err != nil || got.MeshURL != p.MeshURL {
		t.Fatalf("GetByName = %+v, %v", got, err)
	}
	if err := peers.Create(ctx, &contracts.Peer{Name: "pop-os", MeshURL: "http://x:1"}, "test"); err == nil {
		t.Fatal("duplicate name must be refused")
	}

	// Every change is an event, committed with the row (D3: the UI and the
	// Router's peer watcher both learn of changes from the log).
	evs, err := l.Query(ctx, Filter{Types: []string{"peer.*"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 1 || evs[0].Type != contracts.PeerCreated {
		t.Fatalf("events after create = %+v", evs)
	}
	got.Hidden = true
	if err := peers.Update(ctx, got, "test"); err != nil {
		t.Fatal(err)
	}
	if err := peers.Delete(ctx, got.ID, "test"); err != nil {
		t.Fatal(err)
	}
	evs, _ = l.Query(ctx, Filter{Types: []string{"peer.*"}})
	if len(evs) != 3 || evs[1].Type != contracts.PeerUpdated || evs[2].Type != contracts.PeerDeleted {
		t.Fatalf("events = %+v", evs)
	}
}

func TestPeerValidation(t *testing.T) {
	peers := NewPeers(openMigratedDB(t))
	for _, p := range []contracts.Peer{
		{MeshURL: "http://a:1"},
		{Name: "has/slash", MeshURL: "http://a:1"},
		{Name: "has space", MeshURL: "http://a:1"},
		{Name: "ok"},
		{Name: "ok", MeshURL: "https://a:1"},
		{Name: "ok", MeshURL: "http://a:1/some/path"},
		{Name: "ok", MeshURL: "not a url"},
	} {
		p := p
		err := peers.Create(context.Background(), &p, "test")
		var v *contracts.ValidationError
		if !errors.As(err, &v) {
			t.Errorf("Create(%+v) = %v, want a ValidationError", p, err)
		}
	}
}

// testFingerprint is a valid device fingerprint, distinct per seed.
func testFingerprint(seed byte) string {
	return string(devicekey.FingerprintOf(ed25519.PublicKey(bytes.Repeat([]byte{seed}, ed25519.PublicKeySize))))
}

// A row stored before pairing existed — the live pop-os row's shape —
// reads and updates exactly as it did: viewed, unpaired, and no device.*
// events from editing it.
func TestPeerPrePairingRowKeepsWorking(t *testing.T) {
	ctx := context.Background()
	l := openMigratedDB(t)
	if _, err := l.db.Exec(`INSERT INTO mesh_peers (id, name, system, data, created_at, updated_at) VALUES (?, ?, 0, ?, ?, ?)`,
		"per_old", "pop-os",
		`{"id":"per_old","name":"pop-os","dns_name":"pop-os.example.ts.net","mesh_url":"http://pop-os.example.ts.net:17380","created_at":"2026-11-12T21:04:05Z","updated_at":"2026-11-12T21:04:05Z"}`,
		"2026-11-12T21:04:05Z", "2026-11-12T21:04:05Z"); err != nil {
		t.Fatal(err)
	}
	peers := NewPeers(l)
	p, err := peers.GetByName(ctx, "pop-os")
	if err != nil {
		t.Fatal(err)
	}
	if !p.IsViewed() || p.IsPaired() || p.Grant != nil || p.MeshURL != "http://pop-os.example.ts.net:17380" {
		t.Fatalf("old row reads as %+v", p)
	}
	p.Hidden = true
	if err := peers.Update(ctx, p, "test"); err != nil {
		t.Fatalf("updating an old row: %v", err)
	}
	if err := peers.Delete(ctx, p.ID, "test"); err != nil {
		t.Fatal(err)
	}
	evs, _ := l.Query(ctx, Filter{Types: []string{"device.*"}})
	if len(evs) != 0 {
		t.Fatalf("unpaired rows logged device events: %+v", evs)
	}
}

func TestPeerPairingLifecycle(t *testing.T) {
	ctx := context.Background()
	l := openMigratedDB(t)
	peers := NewPeers(l)
	no := false
	fp := testFingerprint(1)

	// A paired-only device (a phone): no mesh URL, not viewed.
	phone := &contracts.Peer{Name: "phone", Viewed: &no, Fingerprint: fp, Grant: &contracts.PeerGrant{Level: contracts.GrantView},
		// A caller's pairing record is ignored but for How.
		Pairing: &contracts.PeerPairing{PairedBy: "someone-else", How: contracts.PairCeremony}}
	if err := peers.Create(ctx, phone, "user"); err != nil {
		t.Fatal(err)
	}
	if phone.Pairing == nil || phone.Pairing.PairedBy != "user" || phone.Pairing.How != contracts.PairCeremony || phone.Pairing.PairedAt.IsZero() {
		t.Fatalf("pairing record = %+v", phone.Pairing)
	}
	pairedAt := phone.Pairing.PairedAt

	// Raise the grant: grant_changed; the pairing record is kept.
	phone.Grant = &contracts.PeerGrant{Level: contracts.GrantChat, Ceilings: contracts.PeerCeilings{FullTrustChat: contracts.FullTrustChatRefused}}
	phone.Pairing = nil
	if err := peers.Update(ctx, phone, "user"); err != nil {
		t.Fatal(err)
	}
	if phone.Pairing == nil || !phone.Pairing.PairedAt.Equal(pairedAt) {
		t.Fatalf("an unchanged key lost its pairing record: %+v", phone.Pairing)
	}
	// A rename alone is no device event.
	phone.Name = "pixel"
	if err := peers.Update(ctx, phone, "user"); err != nil {
		t.Fatal(err)
	}
	// A new key is an unpair plus a pair (D1: rotation is both).
	phone.Fingerprint, phone.Pairing = testFingerprint(2), nil
	if err := peers.Update(ctx, phone, "user"); err != nil {
		t.Fatal(err)
	}
	// Unpairing a device that isn't viewed leaves nothing: delete it.
	if err := peers.Delete(ctx, phone.ID, "user"); err != nil {
		t.Fatal(err)
	}

	evs, err := l.Query(ctx, Filter{Types: []string{"device.*"}})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range evs {
		var c contracts.DeviceChange
		if err := json.Unmarshal(e.Payload, &c); err != nil {
			t.Fatal(err)
		}
		got = append(got, fmt.Sprintf("%s %s %s->%s %s", e.Type, c.Fingerprint[:4], c.PreviousLevel, c.Level, c.How))
		if c.PeerID != phone.ID || e.Actor != "user" {
			t.Errorf("event %s: peer %q actor %q", e.Type, c.PeerID, e.Actor)
		}
	}
	want := []string{
		"device.paired " + fp[:4] + " ->view ceremony",
		"device.grant_changed " + fp[:4] + " view->chat ",
		"device.unpaired " + fp[:4] + " chat-> ",
		"device.paired " + testFingerprint(2)[:4] + " ->chat accept",
		"device.unpaired " + testFingerprint(2)[:4] + " chat-> ",
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("device events:\n got %q\nwant %q", got, want)
	}
	// Each device event commits beside its peer.* event.
	if pe, _ := l.Query(ctx, Filter{Types: []string{"peer.*"}}); len(pe) != 5 {
		t.Fatalf("peer events = %d, want 5", len(pe))
	}
}

// Pairing a device this node already views adds the paired side to its
// row; unpairing it takes the side away and keeps the row.
func TestPeerPairAViewedRow(t *testing.T) {
	ctx := context.Background()
	l := openMigratedDB(t)
	peers := NewPeers(l)
	p := &contracts.Peer{Name: "pop-os", MeshURL: "http://pop-os:17380"}
	if err := peers.Create(ctx, p, "user"); err != nil {
		t.Fatal(err)
	}
	p.Fingerprint = testFingerprint(3)
	p.Grant = &contracts.PeerGrant{Level: contracts.GrantControl}
	if err := peers.Update(ctx, p, "user"); err != nil {
		t.Fatal(err)
	}
	p.Fingerprint, p.Grant = "", nil
	if err := peers.Update(ctx, p, "user"); err != nil {
		t.Fatal(err)
	}
	got, _ := peers.Get(ctx, p.ID)
	if !got.IsViewed() || got.IsPaired() || got.Pairing != nil {
		t.Fatalf("after unpair: %+v", got)
	}
	evs, _ := l.Query(ctx, Filter{Types: []string{"device.*"}})
	if len(evs) != 2 || evs[0].Type != contracts.DevicePaired || evs[1].Type != contracts.DeviceUnpaired {
		t.Fatalf("events = %+v", evs)
	}
}

func TestPeerFingerprintIsUnique(t *testing.T) {
	ctx := context.Background()
	peers := NewPeers(openMigratedDB(t))
	no := false
	fp := testFingerprint(4)
	a := &contracts.Peer{Name: "a", Viewed: &no, Fingerprint: fp}
	if err := peers.Create(ctx, a, "user"); err != nil {
		t.Fatal(err)
	}
	var v *contracts.ValidationError
	if err := peers.Create(ctx, &contracts.Peer{Name: "b", Viewed: &no, Fingerprint: fp}, "user"); !errors.As(err, &v) || !strings.Contains(err.Error(), `already paired as "a"`) {
		t.Fatalf("second create with the same key = %v", err)
	}
	b := &contracts.Peer{Name: "b", MeshURL: "http://b:1"}
	if err := peers.Create(ctx, b, "user"); err != nil {
		t.Fatal(err)
	}
	b.Fingerprint = fp
	if err := peers.Update(ctx, b, "user"); !errors.As(err, &v) {
		t.Fatalf("moving a pinned key to another row = %v", err)
	}
	// Re-saving the row that holds it is fine.
	a.Grant = &contracts.PeerGrant{Level: contracts.GrantView}
	if err := peers.Update(ctx, a, "user"); err != nil {
		t.Fatal(err)
	}
}

func TestPeerPairingValidation(t *testing.T) {
	no := false
	fp := testFingerprint(5)
	pub := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{5}, ed25519.PublicKeySize))
	otherPub := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{6}, ed25519.PublicKeySize))
	peers := NewPeers(openMigratedDB(t))
	for name, p := range map[string]contracts.Peer{
		"neither viewed nor paired": {Name: "x", Viewed: &no},
		"viewed without a url":      {Name: "x", Fingerprint: fp},
		"bad fingerprint":           {Name: "x", Viewed: &no, Fingerprint: "nope"},
		"short fingerprint":         {Name: "x", Viewed: &no, Fingerprint: devicekey.Fingerprint(fp).Short()},
		"grouped fingerprint":       {Name: "x", Viewed: &no, Fingerprint: devicekey.Fingerprint(fp).Grouped()},
		"key doesn't match":         {Name: "x", Viewed: &no, Fingerprint: fp, PublicKey: otherPub},
		"garbled key":               {Name: "x", Viewed: &no, Fingerprint: fp, PublicKey: "AAAA"},
		"grant unpaired":            {Name: "x", MeshURL: "http://x:1", Grant: &contracts.PeerGrant{Level: contracts.GrantView}},
		"unknown level":             {Name: "x", Viewed: &no, Fingerprint: fp, Grant: &contracts.PeerGrant{Level: "root"}},
		"unknown C1":                {Name: "x", Viewed: &no, Fingerprint: fp, Grant: &contracts.PeerGrant{Level: "chat", Ceilings: contracts.PeerCeilings{FullTrustChat: "sometimes"}}},
		"unknown C3":                {Name: "x", Viewed: &no, Fingerprint: fp, Grant: &contracts.PeerGrant{Level: "control", Ceilings: contracts.PeerCeilings{MaxTrust: "godmode"}}},
		"unknown method":            {Name: "x", Viewed: &no, Fingerprint: fp, Pairing: &contracts.PeerPairing{How: "osmosis"}},
	} {
		err := peers.Create(context.Background(), &p, "test")
		var v *contracts.ValidationError
		if !errors.As(err, &v) {
			t.Errorf("%s: Create = %v, want a ValidationError", name, err)
		}
	}
	// An empty level is none, kept with its limits.
	none := contracts.Peer{Name: "none", Viewed: &no, Fingerprint: testFingerprintFor(t), Grant: &contracts.PeerGrant{Ceilings: contracts.PeerCeilings{NoShell: true}}}
	if err := peers.Create(context.Background(), &none, "test"); err != nil || none.Level() != contracts.GrantNone {
		t.Fatalf("a device at none with limits: %v", err)
	}
	ok := contracts.Peer{Name: "ok", Viewed: &no, Fingerprint: fp, PublicKey: pub,
		Grant: &contracts.PeerGrant{Level: "control", Ceilings: contracts.PeerCeilings{NoShell: true, MaxTrust: contracts.TrustLimited}}}
	if err := peers.Create(context.Background(), &ok, "test"); err != nil {
		t.Fatalf("a valid paired device: %v", err)
	}
}

// A conditional write fails, writing nothing, when the row changed since
// it was read: a Router writing back its stale copy can't undo a revoke.
func TestPeerConditionalWrites(t *testing.T) {
	ctx := context.Background()
	l := openMigratedDB(t)
	peers := NewPeers(l)
	p := &contracts.Peer{Name: "pop-os", MeshURL: "http://pop-os:17380", Fingerprint: testFingerprint(7), Grant: &contracts.PeerGrant{Level: contracts.GrantControl}}
	if err := peers.Create(ctx, p, "user"); err != nil {
		t.Fatal(err)
	}
	stale := *p
	// Someone revokes it meanwhile.
	revoked := *p
	revoked.Fingerprint, revoked.Grant, revoked.Pairing = "", nil, nil
	if err := peers.UpdateIfUnchanged(ctx, &revoked, "user", p.UpdatedAt); err != nil {
		t.Fatalf("a fresh conditional update: %v", err)
	}
	stale.Hidden = true
	if err := peers.UpdateIfUnchanged(ctx, &stale, "router", stale.UpdatedAt); !errors.Is(err, ErrStale) {
		t.Fatalf("a stale write-back = %v, want ErrStale", err)
	}
	if err := peers.DeleteIfUnchanged(ctx, p.ID, "router", stale.UpdatedAt); !errors.Is(err, ErrStale) {
		t.Fatalf("a stale delete = %v, want ErrStale", err)
	}
	got, _ := peers.Get(ctx, p.ID)
	if got.IsPaired() || got.Hidden {
		t.Fatalf("a stale write landed: %+v", got)
	}
	if err := peers.DeleteIfUnchanged(ctx, p.ID, "user", got.UpdatedAt); err != nil {
		t.Fatalf("a fresh conditional delete: %v", err)
	}
}

// Q2 in the store, against the stored row: granting admin needs host
// authority. Loosening an admin's ceilings is granting; keeping, renaming,
// tightening or lowering isn't.
func TestPeerAdminGrantNeedsHostAuthority(t *testing.T) {
	ctx := context.Background()
	host := WithHostAuthority(ctx)
	peers := NewPeers(openMigratedDB(t))
	no := false
	var forbidden *ForbiddenError
	admin := func() *contracts.Peer {
		return &contracts.Peer{Name: "laptop", Viewed: &no, Fingerprint: testFingerprint(8),
			Grant: &contracts.PeerGrant{Level: contracts.GrantAdmin, Ceilings: contracts.PeerCeilings{FullTrustChat: contracts.FullTrustChatRefused, NoShell: true, MaxTrust: contracts.TrustLimited}}}
	}
	if err := peers.Create(ctx, admin(), "x"); !errors.As(err, &forbidden) {
		t.Fatalf("admin without host authority = %v", err)
	}
	p := admin()
	if err := peers.Create(host, p, "user"); err != nil {
		t.Fatal(err)
	}
	save := func(c context.Context, edit func(*contracts.Peer)) error {
		cur, err := peers.Get(ctx, p.ID)
		if err != nil {
			t.Fatal(err)
		}
		edit(cur)
		return peers.Update(c, cur, "x")
	}
	for name, edit := range map[string]func(*contracts.Peer){
		"rename":    func(c *contracts.Peer) { c.Name = "work" },
		"tighten":   func(c *contracts.Peer) { c.Grant.Ceilings.MaxTrust = contracts.TrustReadOnly },
		"unchanged": func(*contracts.Peer) {},
	} {
		if err := save(ctx, edit); err != nil {
			t.Errorf("%s an admin without host authority: %v", name, err)
		}
	}
	for name, edit := range map[string]func(*contracts.Peer){
		"C1 refused→held":    func(c *contracts.Peer) { c.Grant.Ceilings.FullTrustChat = contracts.FullTrustChatHeld },
		"C1 refused→allowed": func(c *contracts.Peer) { c.Grant.Ceilings.FullTrustChat = "" },
		"C2 off":             func(c *contracts.Peer) { c.Grant.Ceilings.NoShell = false },
		"C3 raised":          func(c *contracts.Peer) { c.Grant.Ceilings.MaxTrust = contracts.TrustFull },
		"C3 removed":         func(c *contracts.Peer) { c.Grant.Ceilings.MaxTrust = "" },
		"new key":            func(c *contracts.Peer) { c.Fingerprint = testFingerprint(9) },
	} {
		if err := save(ctx, edit); !errors.As(err, &forbidden) {
			t.Errorf("%s on an admin without host authority = %v, want forbidden", name, err)
		}
	}
	if err := save(ctx, func(c *contracts.Peer) { c.Grant.Level = contracts.GrantChat }); err != nil {
		t.Fatalf("lowering an admin: %v", err)
	}
	if err := save(ctx, func(c *contracts.Peer) { c.Grant.Level = contracts.GrantAdmin }); !errors.As(err, &forbidden) {
		t.Fatalf("raising to admin without host authority = %v", err)
	}
	if err := save(host, func(c *contracts.Peer) {
		c.Grant.Level = contracts.GrantAdmin
		c.Grant.Ceilings = contracts.PeerCeilings{}
	}); err != nil {
		t.Fatalf("raising to admin on the host: %v", err)
	}
}

// Names the Router can't route to unambiguously are refused here, so no
// writer (the CLI included) can bypass the Router's own check.
func TestPeerNamesThatCollideWithRoutesOrIDs(t *testing.T) {
	ctx := context.Background()
	peers := NewPeers(openMigratedDB(t))
	first := &contracts.Peer{Name: "pop-os", MeshURL: "http://a:1"}
	if err := peers.Create(ctx, first, "user"); err != nil {
		t.Fatal(err)
	}
	var v *contracts.ValidationError
	for _, name := range []string{"candidates", first.ID, "per_anything"} {
		if err := peers.Create(ctx, &contracts.Peer{Name: name, MeshURL: "http://b:1"}, "user"); !errors.As(err, &v) {
			t.Errorf("name %q: %v, want a ValidationError", name, err)
		}
	}
}

// Create never replaces an existing row by id (the write is an upsert):
// that would skip the update path's checks, its stored-row hook and its
// events. Any registry; peers and agents shown.
func TestCreateRefusesAnExistingID(t *testing.T) {
	ctx := context.Background()
	l := openMigratedDB(t)
	peers := NewPeers(l)
	no := false
	orig := &contracts.Peer{Name: "phone", Viewed: &no, Fingerprint: testFingerprint(11), Grant: &contracts.PeerGrant{Level: contracts.GrantChat}}
	if err := peers.Create(ctx, orig, "user"); err != nil {
		t.Fatal(err)
	}
	var v *contracts.ValidationError
	hijack := &contracts.Peer{ID: orig.ID, Name: "other", Viewed: &no, Fingerprint: testFingerprint(12), Grant: &contracts.PeerGrant{Level: contracts.GrantControl}}
	if err := peers.Create(ctx, hijack, "x"); !errors.As(err, &v) || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("Create over an existing id = %v, want a ValidationError", err)
	}
	got, _ := peers.Get(ctx, orig.ID)
	if got.Name != "phone" || got.Fingerprint != orig.Fingerprint || !got.CreatedAt.Equal(orig.CreatedAt) {
		t.Fatalf("the row was replaced: %+v", got)
	}

	agents := NewAgents(l)
	a := &contracts.Agent{Name: "a1", Prompt: "p"}
	if err := agents.Create(ctx, a, "user"); err != nil {
		t.Fatal(err)
	}
	if err := agents.Create(ctx, &contracts.Agent{ID: a.ID, Name: "a2", Prompt: "q"}, "x"); !errors.As(err, &v) {
		t.Fatalf("agent Create over an existing id = %v", err)
	}
	// A caller-chosen id that's free still works (seeds, imports).
	if err := agents.Create(ctx, &contracts.Agent{ID: "agt_chosen", Name: "a3", Prompt: "r"}, "user"); err != nil {
		t.Fatalf("a free caller-chosen id: %v", err)
	}
}

// updated_at only moves forward per row, even when the clock steps back,
// so a write can't land on a stamp a stale reader holds and let its
// conditional write through.
func TestUpdatedAtIsMonotonicAcrossAClockStepBack(t *testing.T) {
	ctx := context.Background()
	peers := NewPeers(openMigratedDB(t))
	base := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	clock := base
	registryNow = func() time.Time { return clock }
	t.Cleanup(func() { registryNow = time.Now })

	p := &contracts.Peer{Name: "pop-os", MeshURL: "http://a:1"}
	if err := peers.Create(ctx, p, "user"); err != nil { // stamped base
		t.Fatal(err)
	}
	clock = base.Add(time.Second)
	stale := *p
	p.Hidden = true
	if err := peers.Update(ctx, p, "user"); err != nil { // stamped base+1s
		t.Fatal(err)
	}
	// The clock steps back to base: the next write must not be stamped
	// base again (the stale reader's value).
	clock = base
	cur, _ := peers.Get(ctx, p.ID)
	if err := peers.Update(ctx, cur, "user"); err != nil {
		t.Fatal(err)
	}
	if !cur.UpdatedAt.After(base.Add(time.Second)) {
		t.Fatalf("updated_at went to %s after %s", cur.UpdatedAt, base.Add(time.Second))
	}
	stale.Hidden = false
	if err := peers.UpdateIfUnchanged(ctx, &stale, "router", base); !errors.Is(err, ErrStale) {
		t.Fatalf("a stale write matched after a clock step: %v", err)
	}
}

func testFingerprintFor(t *testing.T) string {
	t.Helper()
	return string(testFP(9))
}

// A row's addresses: at most eight, each a direct host:port of a known
// source; mesh_url mirrors last_good (or the first address), so an older
// build reading the row still finds a working address.
func TestPeerAddressesStoreRules(t *testing.T) {
	ctx := context.Background()
	peers := NewPeers(openMigratedDB(t))
	now := time.Now().UTC()
	addr := func(a string, src contracts.AddressSource) contracts.MeshAddress {
		return contracts.MeshAddress{NetAddress: contracts.NetAddress{Via: "direct", Addr: a, Kind: "lan"}, Source: src, AddedAt: now}
	}
	p := &contracts.Peer{Name: "pop-os", MeshURL: "http://old.example:17380", Addresses: []contracts.MeshAddress{
		addr("192.168.1.20:17380", contracts.SourceHello), addr("100.101.4.7:17380", contracts.SourceHello),
	}, LastGood: &contracts.PeerPath{Addr: "100.101.4.7:17380", At: now}}
	if err := peers.Create(ctx, p, "test"); err != nil {
		t.Fatal(err)
	}
	if p.MeshURL != "http://100.101.4.7:17380" {
		t.Fatalf("mesh_url = %q, want last_good's", p.MeshURL)
	}
	p.LastGood = &contracts.PeerPath{Addr: "10.9.9.9:17380", At: now} // not one of them
	if err := peers.Update(ctx, p, "test"); err != nil {
		t.Fatal(err)
	}
	if got, _ := peers.Get(ctx, p.ID); got.MeshURL != "http://192.168.1.20:17380" {
		t.Fatalf("mesh_url = %q, want the first address", got.MeshURL)
	}
	// A viewed row with no addresses still needs a mesh_url.
	if err := peers.Create(ctx, &contracts.Peer{Name: "empty"}, "test"); err == nil {
		t.Fatal("a viewed row with no address")
	}
	var many []contracts.MeshAddress
	for i := 0; i < 9; i++ {
		many = append(many, addr(fmt.Sprintf("192.168.1.%d:17380", i+1), contracts.SourceCode))
	}
	for name, bad := range map[string][]contracts.MeshAddress{
		"nine":       many,
		"bad shape":  {addr("http://x:17380", contracts.SourceCode)},
		"metadata":   {addr("169.254.169.254:17380", contracts.SourceCode)},
		"low port":   {addr("192.168.1.2:22", contracts.SourceCode)},
		"bad source": {addr("192.168.1.2:17380", "made-up")},
		"twice":      {addr("192.168.1.2:17380", contracts.SourceCode), addr("192.168.1.2:17380", contracts.SourceHello)},
		"relay":      {{NetAddress: contracts.NetAddress{Via: "relay", Addr: "r.example:443"}, Source: contracts.SourceCode}},
	} {
		err := peers.Create(ctx, &contracts.Peer{Name: "x-" + strings.ReplaceAll(name, " ", "-"), Addresses: bad}, "test")
		var v *contracts.ValidationError
		if !errors.As(err, &v) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// A row from before addresses reads as one legacy candidate.
	old := contracts.Peer{MeshURL: "http://pop-os.example.ts.net:17380"}
	if c := old.Candidates(); len(c) != 1 || c[0].Source != contracts.SourceLegacy || c[0].Addr != "pop-os.example.ts.net:17380" {
		t.Fatalf("legacy candidates %+v", c)
	}
}
