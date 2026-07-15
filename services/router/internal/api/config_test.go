package api_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/drewkidwell/mads/pkg/contracts"
)

func TestConfigRoutesProxied(t *testing.T) {
	srv := newTestServer(t)

	getResp, err := http.Get(srv.URL + "/config")
	if err != nil {
		t.Fatal(err)
	}
	cfg := decodeJSON[contracts.Config](t, getResp)
	if cfg != contracts.DefaultConfig() {
		t.Fatalf("fresh config = %+v, want DefaultConfig()", cfg)
	}

	cfg.Palette = "space"
	putResp := putJSON(t, srv.URL+"/config", cfg)
	updated := decodeJSON[contracts.Config](t, putResp)
	if updated.Palette != "space" {
		t.Fatalf("update did not apply: %+v", updated)
	}
}

func TestProfileRoutesProxied(t *testing.T) {
	srv := newTestServer(t)

	resp := postJSON(t, srv.URL+"/profiles", contracts.Profile{
		Name:     "quiet-hours",
		Settings: contracts.ProfileSettings{ConcurrencyLimit: 1},
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create: status %d", resp.StatusCode)
	}
	created := decodeJSON[contracts.Profile](t, resp)

	activateResp := postJSON(t, srv.URL+"/profiles/"+created.ID+"/activate", nil)
	merged := decodeJSON[contracts.Config](t, activateResp)
	if merged.ConcurrencyLimit != 1 {
		t.Fatalf("activate did not merge: %+v", merged)
	}
}

func TestCreateWorkspaceProxied(t *testing.T) {
	srv := newTestServer(t)

	resp := postJSON(t, srv.URL+"/workspaces", contracts.Workspace{Name: "my-workspace"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create: status %d", resp.StatusCode)
	}
	created := decodeJSON[contracts.Workspace](t, resp)
	if created.ID == "" {
		t.Fatal("create workspace: no ID assigned")
	}
}

func TestPreflightStubbed(t *testing.T) {
	srv := newTestServer(t)
	resp, err := http.Get(srv.URL + "/preflight")
	if err != nil {
		t.Fatal(err)
	}
	checks := decodeJSON[[]struct {
		Name    string `json:"name"`
		Healthy bool   `json:"healthy"`
	}](t, resp)
	if len(checks) == 0 {
		t.Fatal("preflight: no checks returned")
	}
	for _, c := range checks {
		if !c.Healthy {
			t.Errorf("stub check %q reported unhealthy", c.Name)
		}
	}
}

func putJSON(t *testing.T, url string, body any) *http.Response {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPut, url, bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req) //nolint:noctx // test helper, url is local httptest server
	if err != nil {
		t.Fatal(err)
	}
	return resp
}
