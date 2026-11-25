package contracts_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/golden"
)

func samplePairedPeer() contracts.Peer {
	ts := time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC)
	viewed := true
	return contracts.Peer{
		ID:          "per_01hq3z",
		Name:        "pop-os",
		DNSName:     "pop-os.example.ts.net",
		MeshURL:     "http://pop-os.example.ts.net:17380",
		Viewed:      &viewed,
		Fingerprint: "JOYG7DSOHJ3RLUQB2VZ5BKSCG5ROKXNL2YNCYARHR6SWZRWSSTQA",
		PublicKey:   "BwcHBwcHBwcHBwcHBwcHBwcHBwcHBwcHBwcHBwcHBwc=",
		Grant: &contracts.PeerGrant{
			Level:    contracts.GrantChat,
			Ceilings: contracts.PeerCeilings{FullTrustChat: contracts.FullTrustChatHeld, MaxTrust: contracts.TrustLimited},
		},
		Pairing:   &contracts.PeerPairing{PairedAt: ts, PairedBy: "user", How: contracts.PairAccept},
		CreatedAt: ts,
		UpdatedAt: ts.Add(5 * time.Minute),
	}
}

func TestPeerProducerMatchesGolden(t *testing.T) {
	got, err := json.MarshalIndent(samplePairedPeer(), "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	golden.Assert(t, "peer.json", append(got, '\n'))
}

func TestPeerConsumerParsesGolden(t *testing.T) {
	var got contracts.Peer
	if err := json.Unmarshal(golden.Read(t, "peer.json"), &got); err != nil {
		t.Fatalf("consumer failed to parse golden: %v", err)
	}
	gotJSON, _ := json.Marshal(got)
	wantJSON, _ := json.Marshal(samplePairedPeer())
	if string(gotJSON) != string(wantJSON) {
		t.Errorf("golden round-trip mismatch:\ngot:  %s\nwant: %s", gotJSON, wantJSON)
	}
}

// peer_v0.json is a row as mesh mode stored it before pairing existed (the
// live pop-os row's shape). It must read as viewed, unpaired, no grant,
// and write back byte-identical, so the rows already in mesh_peers keep
// meaning exactly what they meant.
func TestPeerPrePairingRowStillMeansViewed(t *testing.T) {
	raw := golden.Read(t, "peer_v0.json")
	var p contracts.Peer
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatal(err)
	}
	if p.Name != "pop-os" || p.MeshURL != "http://pop-os.example.ts.net:17380" || p.DNSName != "pop-os.example.ts.net" {
		t.Fatalf("decoded %+v", p)
	}
	if !p.IsViewed() || p.IsPaired() || p.Level() != contracts.GrantNone || p.Grant != nil || p.Pairing != nil {
		t.Fatalf("a pre-pairing row reads as viewed=%v paired=%v level=%q", p.IsViewed(), p.IsPaired(), p.Level())
	}
	out, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	golden.Assert(t, "peer_v0.json", append(out, '\n'))
}

func TestPeerViewedFlag(t *testing.T) {
	no := false
	if (contracts.Peer{Viewed: &no}).IsViewed() {
		t.Error("viewed=false reads as viewed")
	}
	var p contracts.Peer
	if err := json.Unmarshal([]byte(`{"name":"phone","viewed":false,"fingerprint":"X"}`), &p); err != nil || p.IsViewed() || !p.IsPaired() {
		t.Errorf("viewed=false decodes as %+v, %v", p, err)
	}
}

func TestGrantLevelsAndCeilings(t *testing.T) {
	for _, l := range []contracts.GrantLevel{"view", "chat", "control", "admin"} {
		if !l.Valid() {
			t.Errorf("%q invalid", l)
		}
	}
	for _, l := range []contracts.GrantLevel{"", "none", "create", "VIEW"} {
		if l.Valid() {
			t.Errorf("%q valid", l)
		}
	}
	for _, c := range []contracts.FullTrustChat{"", "allowed", "refused", "held"} {
		if !c.Valid() {
			t.Errorf("C1 %q invalid", c)
		}
	}
	if contracts.FullTrustChat("maybe").Valid() {
		t.Error("an unknown C1 setting is valid")
	}
	// Default ceilings are omitted entirely, so a plain grant stays small.
	raw, _ := json.Marshal(contracts.PeerGrant{Level: contracts.GrantView})
	if string(raw) != `{"level":"view"}` {
		t.Errorf("default grant = %s", raw)
	}
}
