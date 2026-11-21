package api_test

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/pkg/devicekey"
)

// A device reading this host's event log over the mesh doesn't see the
// device registry — peer.* (device names) and device.* (fingerprints,
// grants, refusals) — unless it is admin here; local callers see
// everything (docs/MESH-AUTH-DESIGN.md, A5). Both the batch and the
// streaming read.
func TestEventsHideTheDeviceRegistryFromMeshDevices(t *testing.T) {
	srv, dataBase := newTestServerWithData(t)
	data := dataclient.New(dataBase, "test")
	// The registry's events written as their owners write them (POST
	// /events refuses hand-written ones): a pairing (device.paired
	// then peer.created), the Mesh Service's refusal, the Router's
	// reachability.
	since := mustAppend(t, data, "", contracts.SessionCreated).Seq - 1
	no := false
	if err := data.CreatePeer(context.Background(), &contracts.Peer{Name: "phone", Viewed: &no, Fingerprint: string(devicekey.FingerprintOf(make(ed25519.PublicKey, ed25519.PublicKeySize)))}); err != nil {
		t.Fatal(err)
	}
	mustAppend(t, data.As("mesh"), "", contracts.DeviceRefused)
	mustAppend(t, data.As("router"), "", contracts.PeerReachabilityChanged)
	mustAppend(t, data, "", contracts.SessionRenamed)
	url := srv.URL + "/events?since=" + strconv.FormatInt(since, 10)
	relayed := func(level string) http.Header {
		h := http.Header{contracts.MeshRelayHeader: {"mac"}}
		if level != "" {
			h.Set(contracts.MeshLevelHeader, level)
		}
		return h
	}
	everything := "session.created device.paired peer.created device.refused peer.reachability_changed session.renamed"
	sessionsOnly := "session.created session.renamed"
	for _, tc := range []struct {
		name   string
		header http.Header
		want   string
	}{
		{"local", nil, everything},
		{"relayed, control", relayed("control"), sessionsOnly},
		{"relayed, view", relayed("view"), sessionsOnly},
		{"relayed, no level", relayed(""), sessionsOnly},
		{"relayed, empty mark", http.Header{contracts.MeshRelayHeader: {""}}, sessionsOnly},
		{"relayed, admin", relayed("admin"), everything},
		// The level means nothing without the relay mark (a local caller).
		{"level without the mark", http.Header{contracts.MeshLevelHeader: {"view"}}, everything},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req, _ := http.NewRequest(http.MethodGet, url, nil)
			for k, v := range tc.header {
				req.Header[k] = v
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			var evs []contracts.Envelope
			_ = json.NewDecoder(resp.Body).Decode(&evs)
			_ = resp.Body.Close()
			if got := types(evs); got != tc.want {
				t.Fatalf("batch: %s, want %s", got, tc.want)
			}

			ch, cancel := openSSEWith(t, context.Background(), url, tc.header)
			defer cancel()
			n := len(splitTypes(tc.want))
			frames := collectFrames(t, ch, n, 5*time.Second)
			assertNoMoreFrames(t, ch)
			var got []contracts.Envelope
			for _, f := range frames {
				got = append(got, contracts.Envelope{Type: contracts.EventType(f.typ)})
			}
			if types(got) != tc.want {
				t.Fatalf("stream: %s, want %s", types(got), tc.want)
			}
		})
	}
}

func types(evs []contracts.Envelope) string {
	out := ""
	for i, e := range evs {
		if i > 0 {
			out += " "
		}
		out += string(e.Type)
	}
	return out
}

func splitTypes(s string) []string {
	var out []string
	start := 0
	for i := 0; i <= len(s); i++ {
		if i == len(s) || s[i] == ' ' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return out
}

// Below admin, a device's actor reads as "device" (no name, no
// fingerprint), and mesh.* (this host's Mesh Service) is left out.
func TestEventsRedactDeviceActorsBelowAdmin(t *testing.T) {
	srv, dataBase := newTestServerWithData(t)
	data := dataclient.New(dataBase, "test")
	first := contracts.Envelope{Type: contracts.SessionDeviceMessage, SessionID: "", Actor: "device:phone/ABCD-EFGH-IJKL-MNOP"}
	if err := data.Append(context.Background(), &first); err != nil {
		t.Fatal(err)
	}
	mustAppend(t, data.As("mesh"), "", contracts.MeshListening)
	url := srv.URL + "/events?since=" + strconv.FormatInt(first.Seq-1, 10)
	read := func(level string) []contracts.Envelope {
		req, _ := http.NewRequest(http.MethodGet, url, nil)
		req.Header.Set(contracts.MeshRelayHeader, "mac")
		req.Header.Set(contracts.MeshLevelHeader, level)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		var evs []contracts.Envelope
		_ = json.NewDecoder(resp.Body).Decode(&evs)
		return evs
	}
	if evs := read("control"); len(evs) != 1 || evs[0].Actor != "device" {
		t.Fatalf("control sees %+v", evs)
	}
	if evs := read("admin"); len(evs) != 2 || evs[0].Actor != first.Actor {
		t.Fatalf("admin sees %+v", evs)
	}
	ch, cancel := openSSEWith(t, context.Background(), url, http.Header{contracts.MeshRelayHeader: {"mac"}, contracts.MeshLevelHeader: {"view"}})
	defer cancel()
	frames := collectFrames(t, ch, 1, 5*time.Second)
	assertNoMoreFrames(t, ch)
	if frames[0].env.Actor != "device" {
		t.Fatalf("stream actor %q", frames[0].env.Actor)
	}
}
