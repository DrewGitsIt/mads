package mesh

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/services/data/datatest"
)

// "Only accept devices on my tailnet" (PUT /devices/settings): this host's
// own user sets it, the Data Service stores it, GET /devices/self reports
// it. A relayed request can't; an explicit -require-tailnet on the Mesh
// Service locks it.
func TestMeshSettingsRoute(t *testing.T) {
	g := datatest.StartGuarded(t)
	data := dataclient.New(g.URL, "router", dataclient.WithToken(g.Token))
	mgr := NewManager(data, Options{SelfName: "mac"})
	mux := http.NewServeMux()
	mux.Handle("/devices/", mgr.Devices())
	srv := httptest.NewServer(mgr.Handler(mux))
	t.Cleanup(srv.Close)

	self := func() SelfDevice {
		t.Helper()
		resp, err := http.Get(srv.URL + "/devices/self")
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		var d SelfDevice
		_ = json.NewDecoder(resp.Body).Decode(&d)
		return d
	}
	put := func(body string, relayed bool) (int, SelfDevice, string) {
		t.Helper()
		req, _ := http.NewRequest(http.MethodPut, srv.URL+"/devices/settings", strings.NewReader(body))
		if relayed {
			req.Header[contracts.MeshRelayHeader] = []string{"phone"}
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		var raw json.RawMessage
		_ = json.NewDecoder(resp.Body).Decode(&raw)
		var d SelfDevice
		_ = json.Unmarshal(raw, &d)
		return resp.StatusCode, d, string(raw)
	}

	// Nothing set and no Mesh Service yet: the default, on.
	if d := self(); d.RequireTailnet == nil || !*d.RequireTailnet || d.RequireTailnetLocked {
		t.Fatalf("default: %+v", d)
	}
	if code, _, _ := put(`{"require_tailnet":false}`, true); code != http.StatusForbidden {
		t.Fatalf("relayed: %d, want 403", code)
	}
	if code, _, _ := put(`{}`, false); code != http.StatusUnprocessableEntity {
		t.Fatalf("no value: %d, want 422", code)
	}
	code, d, body := put(`{"require_tailnet":false}`, false)
	if code != http.StatusOK || d.RequireTailnet == nil || *d.RequireTailnet {
		t.Fatalf("off: %d %s", code, body)
	}
	if d := self(); *d.RequireTailnet {
		t.Fatalf("self after off: %+v", d)
	}
	stored, err := data.MeshSettings(context.Background())
	if err != nil || stored.RequireTailnet == nil || *stored.RequireTailnet {
		t.Fatalf("stored: %+v %v", stored, err)
	}

	// A Mesh Service started with an explicit -require-tailnet: its value,
	// locked, whatever is stored; and the route says why it can't change.
	l := contracts.MeshListener{Node: "mac", MeshURL: "http://mac.ts.net:17380", RequireTailnet: true, RequireTailnetFlag: true}
	raw, _ := json.Marshal(l)
	// As the Mesh Service writes it (actor "mesh"; anyone else's is ignored).
	meshData := dataclient.New(g.URL, "mesh", dataclient.WithToken(g.Token))
	if err := meshData.Append(context.Background(), &contracts.Envelope{Type: contracts.MeshListening, Actor: "mesh", Payload: raw}); err != nil {
		t.Fatal(err)
	}
	mgr.self.mu.Lock()
	mgr.self.at = mgr.self.at.AddDate(-1, 0, 0) // past the cache
	mgr.self.mu.Unlock()
	if d := self(); !d.RequireTailnetLocked || !*d.RequireTailnet {
		t.Fatalf("flag: %+v", d)
	}
	if code, _, body := put(`{"require_tailnet":false}`, false); code != http.StatusConflict || !strings.Contains(body, "require_tailnet_locked") {
		t.Fatalf("locked: %d %s", code, body)
	}
}

// An introduced device's address is checked against the live setting:
// a Mesh Service that started with the tailnet off, since turned on here,
// refuses an IP (C).
func TestCheckTargetFollowsTheLiveSetting(t *testing.T) {
	g := datatest.StartGuarded(t)
	data := dataclient.New(g.URL, "router", dataclient.WithToken(g.Token))
	meshData := dataclient.New(g.URL, "mesh", dataclient.WithToken(g.Token))
	mgr := NewManager(data, Options{SelfName: "pop-os"})
	raw, _ := json.Marshal(contracts.MeshListener{Node: "pop-os", DNSName: "pop-os.tail1234.ts.net", MeshURL: "http://pop-os.tail1234.ts.net:17380", RequireTailnet: false})
	if err := meshData.Append(context.Background(), &contracts.Envelope{Type: contracts.MeshListening, Actor: "mesh", Payload: raw}); err != nil {
		t.Fatal(err)
	}
	const ip = "http://100.101.102.103:17380"
	if err := mgr.checkTarget(context.Background(), ip); err != nil {
		t.Fatalf("tailnet not required: %v", err)
	}
	on := true
	if _, err := data.SetMeshSettings(context.Background(), contracts.MeshSettings{RequireTailnet: &on}); err != nil {
		t.Fatal(err)
	}
	if err := mgr.checkTarget(context.Background(), ip); err == nil {
		t.Fatal("an IP passed with the tailnet now required")
	}
	if err := mgr.checkTarget(context.Background(), "http://spark.tail1234.ts.net:17380"); err != nil {
		t.Fatalf("a MagicDNS name: %v", err)
	}
}
