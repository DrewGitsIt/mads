package api_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/drewkidwell/mads/pkg/contracts"
)

func TestGetSession(t *testing.T) {
	srv := newTestServer(t)
	created := decodeJSON[struct {
		Session contracts.Session `json:"session"`
	}](t, postJSON(t, srv.URL+"/sessions", map[string]string{"name": "get-me", "provider": "fake"}))

	resp, err := http.Get(srv.URL + "/sessions/" + created.Session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	got := decodeJSON[contracts.Session](t, resp)
	if got.ID != created.Session.ID || got.Name != created.Session.Name {
		t.Errorf("got = %+v, want %+v", got, created.Session)
	}
}

func TestGetSessionNotFound(t *testing.T) {
	srv := newTestServer(t)
	resp, err := http.Get(srv.URL + "/sessions/nonexistent")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
	body := decodeJSON[map[string]string](t, resp)
	if body["error"] == "" {
		t.Errorf("expected non-empty error field, got %+v", body)
	}
}

// All-empty create (no provider/model/agent/isolation/trust) must resolve
// through Config defaults before spawning, and persist the resolved values —
// not just echo them back (spec §10).
func TestCreateSessionResolvesConfigDefaults(t *testing.T) {
	srv := newTestServer(t)
	setDefaultProvider(t, srv.URL, "fake")

	resp := postJSON(t, srv.URL+"/sessions", map[string]string{"name": "defaults-only"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d, want 201", resp.StatusCode)
	}
	created := decodeJSON[struct {
		Session contracts.Session `json:"session"`
		Addr    string            `json:"addr"`
	}](t, resp)

	if created.Session.Provider != "fake" {
		t.Errorf("provider = %q, want %q", created.Session.Provider, "fake")
	}
	if created.Session.Model != "fake-fast" {
		t.Errorf("model = %q, want %q (fake's Default:true model)", created.Session.Model, "fake-fast")
	}
	if created.Session.Isolation != contracts.IsolationWorktree {
		t.Errorf("isolation = %q, want %q", created.Session.Isolation, contracts.IsolationWorktree)
	}
	if created.Session.Trust != contracts.TrustLimited {
		t.Errorf("trust = %q, want %q", created.Session.Trust, contracts.TrustLimited)
	}

	// Persisted, not just echoed — fetch it back independently.
	getResp, err := http.Get(srv.URL + "/sessions")
	if err != nil {
		t.Fatal(err)
	}
	list := decodeJSON[[]contracts.Session](t, getResp)
	var found *contracts.Session
	for i := range list {
		if list[i].ID == created.Session.ID {
			found = &list[i]
		}
	}
	if found == nil {
		t.Fatalf("created session %s not found in GET /sessions", created.Session.ID)
	}
	if found.Provider != "fake" || found.Model != "fake-fast" || found.Isolation != contracts.IsolationWorktree || found.Trust != contracts.TrustLimited {
		t.Errorf("persisted session = %+v, want resolved defaults", found)
	}
}

// Workspace defaults override Config; an explicit session field overrides
// the workspace default (spec §10's full chain).
func TestCreateSessionWorkspaceDefaultsOverrideConfig(t *testing.T) {
	srv := newTestServer(t)
	setDefaultProvider(t, srv.URL, "fake")

	wsResp := postJSON(t, srv.URL+"/workspaces", contracts.Workspace{
		Name: "ws-with-defaults",
		Defaults: contracts.WorkspaceDefaults{
			Model:     "fake-smart",
			Isolation: contracts.IsolationCurrentDir,
		},
	})
	if wsResp.StatusCode != http.StatusCreated {
		t.Fatalf("create workspace status = %d, want 201", wsResp.StatusCode)
	}
	ws := decodeJSON[contracts.Workspace](t, wsResp)

	resp := postJSON(t, srv.URL+"/sessions", map[string]string{
		"name": "ws-defaults", "workspace": ws.ID, "provider": "fake",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d, want 201", resp.StatusCode)
	}
	created := decodeJSON[struct {
		Session contracts.Session `json:"session"`
	}](t, resp)
	if created.Session.Model != "fake-smart" {
		t.Errorf("model = %q, want workspace default %q", created.Session.Model, "fake-smart")
	}
	if created.Session.Isolation != contracts.IsolationCurrentDir {
		t.Errorf("isolation = %q, want workspace default %q", created.Session.Isolation, contracts.IsolationCurrentDir)
	}

	// Explicit session field wins over the workspace default.
	resp2 := postJSON(t, srv.URL+"/sessions", map[string]string{
		"name": "ws-defaults-explicit-model", "workspace": ws.ID, "provider": "fake", "model": "fake-fast",
	})
	if resp2.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d, want 201", resp2.StatusCode)
	}
	created2 := decodeJSON[struct {
		Session contracts.Session `json:"session"`
	}](t, resp2)
	if created2.Session.Model != "fake-fast" {
		t.Errorf("model = %q, want explicit %q to win over workspace default", created2.Session.Model, "fake-fast")
	}
}

// An unknown provider must be rejected before any session record or child
// process is created — a 4xx naming the provider, never a spawned-then-
// crashed 502.
func TestCreateSessionUnknownProviderNoOrphan(t *testing.T) {
	srv := newTestServer(t)

	before, err := http.Get(srv.URL + "/sessions")
	if err != nil {
		t.Fatal(err)
	}
	beforeList := decodeJSON[[]contracts.Session](t, before)

	resp := postJSON(t, srv.URL+"/sessions", map[string]string{"name": "bad-provider", "provider": "nonexistent"})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	body := decodeJSON[map[string]string](t, resp)
	if got := body["error"]; !strings.Contains(got, `unknown provider "nonexistent"`) {
		t.Fatalf("error = %q, want it to name the provider", got)
	}

	after, err := http.Get(srv.URL + "/sessions")
	if err != nil {
		t.Fatal(err)
	}
	afterList := decodeJSON[[]contracts.Session](t, after)
	if len(afterList) != len(beforeList) {
		t.Fatalf("session count changed %d -> %d: unknown-provider create left a record behind", len(beforeList), len(afterList))
	}
}
