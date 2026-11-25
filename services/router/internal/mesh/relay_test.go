package mesh

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/services/data/datatest"
)

// The loop guard, on every route this layer would otherwise send to a
// peer: a request that arrived through this node's Mesh Service (the relay
// mark present, even empty) is answered here alone. Two nodes that view
// each other never bounce it, and a device paired with this node never
// reaches this node's peers through it (D5: no relaying C through A to B).
func TestRelayedRequestsAreNeverForwardedAgain(t *testing.T) {
	h, p := linksHarness(t)
	for _, mark := range []string{"mac", ""} {
		for _, tc := range []struct {
			method, target string
			status         int    // 0: whatever the local API says
			local          string // the local API must have answered
		}{
			{"GET", "/sessions?scope=all", 0, "here"},
			{"GET", "/sessions/ses_remote", 0, `"served_by":"local"`},
			{"GET", "/sessions/ses_remote/output", 0, `"served_by":"local"`},
			{"POST", "/sessions/ses_remote/messages", 0, `"served_by":"local"`},
			{"GET", "/links?session_ids=ses_local,ses_remote", 0, "lnk_local"},
			{"GET", "/peers", http.StatusForbidden, ""},
			{"GET", "/peers/pop-os/sessions", http.StatusForbidden, ""},
			{"POST", "/peers", http.StatusForbidden, ""},
			{"GET", "/peers/candidates", http.StatusForbidden, ""},
		} {
			before := len(p.forwarded())
			req, _ := http.NewRequest(tc.method, h.srv.URL+tc.target, nil)
			req.Header[contracts.MeshRelayHeader] = []string{mark}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			body, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if tc.status != 0 && resp.StatusCode != tc.status {
				t.Errorf("mark %q: %s %s = %d %s, want %d", mark, tc.method, tc.target, resp.StatusCode, body, tc.status)
			}
			if tc.local != "" && !strings.Contains(string(body), tc.local) {
				t.Errorf("mark %q: %s %s = %s, want the local answer", mark, tc.method, tc.target, body)
			}
			if strings.Contains(string(body), `"host"`) || strings.Contains(string(body), "lnk_remote") || strings.Contains(string(body), `"peer":"pop-os"`) {
				t.Errorf("mark %q: %s %s carried the peer's data: %s", mark, tc.method, tc.target, body)
			}
			if got := p.forwarded()[before:]; len(got) != 0 {
				t.Errorf("mark %q: %s %s reached the peer: %v", mark, tc.method, tc.target, got)
			}
		}
	}
	// Unmarked, the same requests do reach the peer (the guard is the mark).
	before := len(p.forwarded())
	get(t, h.srv.URL+"/sessions/ses_remote")
	if len(p.forwarded()) == before {
		t.Fatal("an unmarked session request wasn't routed: the test proves nothing")
	}
}

// D8: the authenticated device the Mesh Service named (X-Mads-Actor on a
// relayed request) is the actor of every Data Service write made for it —
// the header and appended events — never the Router's own. Without the
// relay mark a caller's X-Mads-Actor changes nothing.
func TestRelayedWritesCarryTheDeviceActor(t *testing.T) {
	g := datatest.StartGuarded(t)
	data := dataclient.New(g.URL, "router", dataclient.WithToken(g.Token))
	// A relayed peer write needs an admin device behind it: the
	// relaying device is pinned at admin on this host first.
	no := false
	admin := &contracts.Peer{Name: "admin-dev", Viewed: &no, Fingerprint: testFingerprint(99), Grant: &contracts.PeerGrant{Level: contracts.GrantAdmin}}
	if err := data.CreatePeer(context.Background(), admin); err != nil {
		t.Fatal(err)
	}
	mgr := NewManager(data, Options{AllowLoopback: true, SelfName: "mac"})
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		no := false
		p := &contracts.Peer{Name: r.URL.Query().Get("name"), Viewed: &no, Fingerprint: testFingerprint(byte(len(r.URL.Query().Get("name")))),
			Pairing: &contracts.PeerPairing{How: contracts.PairIntroduction}, Grant: &contracts.PeerGrant{}}
		if err := data.CreatePeer(r.Context(), p); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		env := &contracts.Envelope{Type: contracts.SessionRenamed, Actor: "user"}
		if err := data.Append(r.Context(), env); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte(env.Actor))
	})
	srv := httptest.NewServer(mgr.Handler(next))
	t.Cleanup(srv.Close)
	for _, tc := range []struct {
		name, mark, actor, want string
		relayed                 bool
	}{
		{"a", "pop-os", "device:pop-os/ABCD-EFGH-IJKL-MNOP", "device:pop-os/ABCD-EFGH-IJKL-MNOP", true},
		{"bb", "", "device:phone/QRST-UVWX-YZ23-4567", "device:phone/QRST-UVWX-YZ23-4567", true},
		{"ccc", "", "device:forged", "router", false}, // no mark: ignored
	} {
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/x?name="+tc.name, nil)
		if tc.relayed {
			req.Header[contracts.MeshRelayHeader] = []string{tc.mark}
			req.Header.Set(contracts.MeshDeviceHeader, admin.Fingerprint)
		}
		req.Header.Set("X-Mads-Actor", tc.actor)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		wantEvent := tc.want
		if !tc.relayed {
			wantEvent = "user" // the handler's own, unchanged
		}
		if resp.StatusCode != 200 || string(body) != wantEvent {
			t.Fatalf("%s: %d event actor %q, want %q", tc.name, resp.StatusCode, body, wantEvent)
		}
		row, err := data.Peer(context.Background(), tc.name)
		if err != nil || row.Pairing == nil || row.Pairing.PairedBy != tc.want {
			t.Fatalf("%s: paired_by = %+v (%v), want %q", tc.name, row.Pairing, err, tc.want)
		}
	}
}
