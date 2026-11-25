package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/localauth"
	"github.com/drewkidwell/mads/services/data/internal/store"
)

// This host's mesh settings are the mesh_settings row; only this host's
// own user (the install token, not relayed) writes it, and each change is
// on record by its actor (device.settings_changed).
func TestMeshSettings(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := store.Migrate(db, store.Migrations()); err != nil {
		t.Fatal(err)
	}
	l := store.NewLog(db)
	mux := http.NewServeMux()
	deviceActivityRoutes(mux, l)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Test-No-Install") == "" {
			r = r.WithContext(localauth.WithPrincipal(r.Context(), localauth.Principal{Kind: localauth.Install}))
		}
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	call := func(method, body string, header map[string]string) (int, contracts.MeshSettings) {
		t.Helper()
		req, _ := http.NewRequest(method, srv.URL+"/devices/settings", bytes.NewReader([]byte(body)))
		req.Header.Set("X-Mads-Actor", "user")
		for k, v := range header {
			req.Header.Set(k, v)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = res.Body.Close() }()
		var out contracts.MeshSettings
		_ = json.NewDecoder(res.Body).Decode(&out)
		return res.StatusCode, out
	}

	// A fresh install: no mesh network.
	if code, s := call("GET", "", nil); code != 200 || len(s.Networks) != 0 || s.Networks == nil {
		t.Fatalf("unset: %d %+v", code, s)
	}
	ts := `{"networks":[{"id":"tailscale","listen":true,"tailnet_check":true,"decided_at":"2026-11-23T00:00:00Z"}]}`
	// A relayed device, or a caller without the install token, can't.
	if code, _ := call("PUT", ts, map[string]string{contracts.MeshRelayHeader: "mac"}); code != http.StatusForbidden {
		t.Fatalf("relayed write: %d, want 403", code)
	}
	if code, _ := call("PUT", ts, map[string]string{"X-Test-No-Install": "1"}); code != http.StatusForbidden {
		t.Fatalf("write without the install token: %d, want 403", code)
	}
	if code, _ := call("PUT", `{}`, nil); code != http.StatusUnprocessableEntity {
		t.Fatalf("no value: %d, want 422", code)
	}
	if code, _ := call("PUT", `{"networks":[{"id":"lan:x","listen":true,"tailnet_check":true}]}`, nil); code != http.StatusUnprocessableEntity {
		t.Fatalf("a check off Tailscale: %d, want 422", code)
	}
	if code, s := call("GET", "", nil); code != 200 || len(s.Networks) != 0 {
		t.Fatalf("refused writes stored something: %d %+v", code, s)
	}

	if code, s := call("PUT", ts, nil); code != 200 || !s.TailnetCheck() || s.RequireTailnet == nil || !*s.RequireTailnet {
		t.Fatalf("tailscale: %d %+v", code, s)
	}
	// The earlier form sets the Tailscale network's check only.
	if code, s := call("PUT", `{"require_tailnet":false}`, nil); code != 200 || s.TailnetCheck() || len(s.Chosen()) != 1 || *s.RequireTailnet {
		t.Fatalf("legacy off: %d %+v", code, s)
	}
	if code, s := call("GET", "", nil); code != 200 || s.TailnetCheck() || len(s.Chosen()) != 1 {
		t.Fatalf("read back off: %d %+v", code, s)
	}

	// Each change is in the device activity log, by its actor.
	evs, err := l.DeviceActivity(t.Context(), store.DeviceActivityFilter{})
	if err != nil {
		t.Fatal(err)
	}
	var changes int
	for _, e := range evs {
		if e.Type == contracts.DeviceSettingsChanged {
			changes++
			var c contracts.MeshSettingsChange
			_ = json.Unmarshal(e.Payload, &c)
			if e.Actor != store.MeshSettingsActor || c.By != "user" {
				t.Errorf("actor %q by %q, want the store's actor, by user", e.Actor, c.By)
			}
		}
	}
	if changes != 2 {
		t.Fatalf("%d device.settings_changed in the activity log, want 2", changes)
	}
}

// device.settings_changed can't be hand-written through POST /events, by
// any actor: the setting is the store's alone (B).
func TestMeshSettingsCantBePostedAsAnEvent(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := store.Migrate(db, store.Migrations()); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r = r.WithContext(localauth.WithPrincipal(r.Context(), localauth.Principal{Kind: localauth.Install}))
		New(db).ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	post := func(typ, actor, payload string) int {
		body := `{"type":"` + typ + `","actor":"` + actor + `","payload":` + payload + `}`
		res, err := http.Post(srv.URL+"/events", "application/json", bytes.NewReader([]byte(body)))
		if err != nil {
			t.Fatal(err)
		}
		_ = res.Body.Close()
		return res.StatusCode
	}
	for _, actor := range []string{store.MeshSettingsActor, "user", "mesh", "router"} {
		if code := post("device.settings_changed", actor, `{"require_tailnet":false}`); code < 400 {
			t.Errorf("POST /events device.settings_changed as %q: %d, want refused", actor, code)
		}
		// Nor the setup window: it's opened at the device, never remotely.
		if code := post("device.discoverable", actor, `{"until":"2099-01-01T00:00:00Z"}`); code < 400 {
			t.Errorf("POST /events device.discoverable as %q: %d, want refused", actor, code)
		}
	}
	if s, err := store.NewLog(db).MeshSettings(t.Context()); err != nil || s.RequireTailnet != nil || len(s.Networks) != 0 {
		t.Fatalf("after refused posts: %+v %v", s, err)
	}
	if d, _ := store.NewLog(db).Discoverable(t.Context()); !d.Until.IsZero() {
		t.Fatalf("window opened by a post: %+v", d)
	}
	// device.discovered is the Mesh Service's own audit record.
	if code := post("device.discovered", "mesh", `{"kind":"probe","remote":"1.2.3.4:5"}`); code >= 400 {
		t.Errorf("mesh's device.discovered: %d", code)
	}
	if code := post("device.discovered", "router", `{"kind":"probe"}`); code < 400 {
		t.Errorf("router's device.discovered: %d", code)
	}
}
