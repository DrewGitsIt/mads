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

// "Only accept devices on my tailnet" is stored as the newest
// device.settings_changed; only this host's own user (the install token,
// not relayed) writes it, and the change is on record by its actor.
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

	// Never set: nothing stored, the Mesh Service's default holds.
	if code, s := call("GET", "", nil); code != 200 || s.RequireTailnet != nil {
		t.Fatalf("unset: %d %+v", code, s)
	}
	// A relayed device, or a caller without the install token, can't.
	if code, _ := call("PUT", `{"require_tailnet":false}`, map[string]string{contracts.MeshRelayHeader: "mac"}); code != http.StatusForbidden {
		t.Fatalf("relayed write: %d, want 403", code)
	}
	if code, _ := call("PUT", `{"require_tailnet":false}`, map[string]string{"X-Test-No-Install": "1"}); code != http.StatusForbidden {
		t.Fatalf("write without the install token: %d, want 403", code)
	}
	if code, _ := call("PUT", `{}`, nil); code != http.StatusUnprocessableEntity {
		t.Fatalf("no value: %d, want 422", code)
	}
	if code, s := call("GET", "", nil); code != 200 || s.RequireTailnet != nil {
		t.Fatalf("refused writes stored something: %d %+v", code, s)
	}

	if code, s := call("PUT", `{"require_tailnet":false}`, nil); code != 200 || s.RequireTailnet == nil || *s.RequireTailnet {
		t.Fatalf("off: %d %+v", code, s)
	}
	if code, s := call("GET", "", nil); code != 200 || s.RequireTailnet == nil || *s.RequireTailnet {
		t.Fatalf("read back off: %d %+v", code, s)
	}
	if code, _ := call("PUT", `{"require_tailnet":true}`, nil); code != 200 {
		t.Fatalf("on: %d", code)
	}
	if _, s := call("GET", "", nil); s.RequireTailnet == nil || !*s.RequireTailnet {
		t.Fatalf("newest wins: %+v", s)
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
	for _, actor := range []string{store.MeshSettingsActor, "user", "mesh"} {
		body := `{"type":"device.settings_changed","actor":"` + actor + `","payload":{"require_tailnet":false}}`
		res, err := http.Post(srv.URL+"/events", "application/json", bytes.NewReader([]byte(body)))
		if err != nil {
			t.Fatal(err)
		}
		_ = res.Body.Close()
		if res.StatusCode < 400 {
			t.Errorf("POST /events device.settings_changed as %q: %d, want refused", actor, res.StatusCode)
		}
	}
	if s, err := store.NewLog(db).MeshSettings(t.Context()); err != nil || s.RequireTailnet != nil {
		t.Fatalf("after refused posts: %+v %v", s, err)
	}
}
