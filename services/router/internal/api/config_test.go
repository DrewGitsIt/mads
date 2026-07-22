package api_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"reflect"
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
	if !reflect.DeepEqual(cfg, contracts.DefaultConfig()) {
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

	limitJSON, err := json.Marshal(1)
	if err != nil {
		t.Fatal(err)
	}
	resp := postJSON(t, srv.URL+"/profiles", contracts.Profile{
		Name:     "quiet-hours",
		Snapshot: contracts.ProfileSnapshot{"concurrency_limit": limitJSON},
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

// TestFastModelStatusDefaultsToProviderModeWithFakeCompleter covers the
// zero-config path: no FastModelMode set, no endpoint configured, and
// DefaultProvider pointed at the fake provider (which implements
// provider.Completer, task #1) — the status the "unconfigured mads... now
// has a WORKING Fast Model" acceptance criterion depends on the UI being
// able to observe.
func TestFastModelStatusDefaultsToProviderModeWithFakeCompleter(t *testing.T) {
	srv := newTestServer(t)
	setDefaultProvider(t, srv.URL, "fake")

	resp, err := http.Get(srv.URL + "/fast-model/status")
	if err != nil {
		t.Fatal(err)
	}
	status := decodeJSON[struct {
		Mode              string `json:"mode"`
		ProviderName      string `json:"provider_name"`
		ProviderCompleter bool   `json:"provider_completer"`
	}](t, resp)
	if status.Mode != "provider" {
		t.Errorf("mode = %q, want provider", status.Mode)
	}
	if status.ProviderName != "fake" {
		t.Errorf("provider_name = %q, want fake", status.ProviderName)
	}
	if !status.ProviderCompleter {
		t.Error("provider_completer = false, want true (fake implements Completer)")
	}
}

// TestFastModelStatusProviderModeDefaultsToClaudeCode covers the truly
// unconfigured install: no DefaultProvider set either, so status must report
// the claude-code default name — even though this test registry (testProviders)
// doesn't register claude-code, so ProviderCompleter correctly reports false
// rather than erroring.
func TestFastModelStatusProviderModeDefaultsToClaudeCode(t *testing.T) {
	srv := newTestServer(t)

	resp, err := http.Get(srv.URL + "/fast-model/status")
	if err != nil {
		t.Fatal(err)
	}
	status := decodeJSON[struct {
		Mode              string `json:"mode"`
		ProviderName      string `json:"provider_name"`
		ProviderCompleter bool   `json:"provider_completer"`
	}](t, resp)
	if status.Mode != "provider" {
		t.Errorf("mode = %q, want provider", status.Mode)
	}
	if status.ProviderName != "claude-code" {
		t.Errorf("provider_name = %q, want claude-code", status.ProviderName)
	}
	if status.ProviderCompleter {
		t.Error("provider_completer = true, want false (claude-code unregistered in this test registry)")
	}
}

// TestFastModelStatusOffMode covers the explicit off case: no provider
// resolution happens at all.
func TestFastModelStatusOffMode(t *testing.T) {
	srv := newTestServer(t)

	getResp, err := http.Get(srv.URL + "/config")
	if err != nil {
		t.Fatal(err)
	}
	cfg := decodeJSON[contracts.Config](t, getResp)
	cfg.FastModelMode = contracts.FastModelModeOff
	putJSON(t, srv.URL+"/config", cfg)

	resp, err := http.Get(srv.URL + "/fast-model/status")
	if err != nil {
		t.Fatal(err)
	}
	status := decodeJSON[struct {
		Mode         string `json:"mode"`
		ProviderName string `json:"provider_name"`
	}](t, resp)
	if status.Mode != "off" {
		t.Errorf("mode = %q, want off", status.Mode)
	}
	if status.ProviderName != "" {
		t.Errorf("provider_name = %q, want empty in off mode", status.ProviderName)
	}
}

// TestFastModelStatusEndpointMode covers explicit endpoint mode: no provider
// resolution happens, regardless of DefaultProvider.
func TestFastModelStatusEndpointMode(t *testing.T) {
	srv := newTestServer(t)

	getResp, err := http.Get(srv.URL + "/config")
	if err != nil {
		t.Fatal(err)
	}
	cfg := decodeJSON[contracts.Config](t, getResp)
	cfg.FastModelMode = contracts.FastModelModeEndpoint
	cfg.FastModelBaseURL = "https://api.example.com/v1"
	putJSON(t, srv.URL+"/config", cfg)

	resp, err := http.Get(srv.URL + "/fast-model/status")
	if err != nil {
		t.Fatal(err)
	}
	status := decodeJSON[struct {
		Mode         string `json:"mode"`
		ProviderName string `json:"provider_name"`
	}](t, resp)
	if status.Mode != "endpoint" {
		t.Errorf("mode = %q, want endpoint", status.Mode)
	}
	if status.ProviderName != "" {
		t.Errorf("provider_name = %q, want empty in endpoint mode", status.ProviderName)
	}
}

// setDefaultProvider sets Config.DefaultProvider against a running server,
// so session-create tests that omit "provider" resolve to it instead of
// hitting the unknown-provider pre-check.
func setDefaultProvider(t *testing.T, baseURL, provider string) {
	t.Helper()
	getResp, err := http.Get(baseURL + "/config")
	if err != nil {
		t.Fatal(err)
	}
	cfg := decodeJSON[contracts.Config](t, getResp)
	cfg.DefaultProvider = provider
	putResp := putJSON(t, baseURL+"/config", cfg)
	if putResp.StatusCode != http.StatusOK {
		t.Fatalf("set default provider: status %d", putResp.StatusCode)
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
