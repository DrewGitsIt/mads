package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"runtime"
	"testing"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/services/data/datatest"
	"github.com/drewkidwell/mads/services/router/internal/api"
	"github.com/drewkidwell/mads/services/router/internal/supervisor"
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

// GET and PUT /config stamp the Router host's OS (response-only; the
// Settings sleep-inhibit caveat keys off it) without persisting it.
func TestConfigResponsesCarryPlatform(t *testing.T) {
	srv := newTestServer(t)

	getResp, err := http.Get(srv.URL + "/config")
	if err != nil {
		t.Fatal(err)
	}
	got := decodeJSON[map[string]any](t, getResp)
	if got["platform"] != runtime.GOOS {
		t.Fatalf("GET /config platform = %v, want %q", got["platform"], runtime.GOOS)
	}

	// Echoing the response back (as the UI does) must be accepted.
	putResp := putJSON(t, srv.URL+"/config", got)
	if putResp.StatusCode != http.StatusOK {
		t.Fatalf("PUT /config with platform echoed: status %d", putResp.StatusCode)
	}
	updated := decodeJSON[map[string]any](t, putResp)
	if updated["platform"] != runtime.GOOS {
		t.Fatalf("PUT /config platform = %v, want %q", updated["platform"], runtime.GOOS)
	}
}

// GET, PUT and profile-activate /config stamp the folder a session with no
// Dir starts in — the Router's cwd (Options.Root) — without persisting it:
// an echoed PUT is accepted and the Data Service's own copy never has it.
func TestConfigResponsesCarryFallbackDir(t *testing.T) {
	base := datatest.Start(t)
	data := dataclient.New(base, "router")
	sup := supervisor.New(data, []string{testChildBin}, []string{testChildBin}, nil)
	t.Cleanup(func() { sup.Shutdown(context.Background()) })
	opts := testOptions(t, data, sup)
	opts.Root = "/src/checkout"
	srv := httptest.NewServer(api.New(opts))
	t.Cleanup(srv.Close)

	getResp, err := http.Get(srv.URL + "/config")
	if err != nil {
		t.Fatal(err)
	}
	got := decodeJSON[map[string]any](t, getResp)
	if got["fallback_dir"] != "/src/checkout" {
		t.Fatalf("GET /config fallback_dir = %v, want /src/checkout", got["fallback_dir"])
	}

	// Echo it back with a different value, as a stale UI might: dropped.
	got["fallback_dir"] = "/somewhere/else"
	putResp := putJSON(t, srv.URL+"/config", got)
	if putResp.StatusCode != http.StatusOK {
		t.Fatalf("PUT /config with fallback_dir echoed: status %d", putResp.StatusCode)
	}
	updated := decodeJSON[map[string]any](t, putResp)
	if updated["fallback_dir"] != "/src/checkout" {
		t.Fatalf("PUT /config fallback_dir = %v, want /src/checkout", updated["fallback_dir"])
	}

	stored, err := http.Get(base + "/config")
	if err != nil {
		t.Fatal(err)
	}
	raw := decodeJSON[map[string]any](t, stored)
	if _, ok := raw["concurrency_limit"]; !ok {
		t.Fatalf("Data Service GET /config returned no Config: %v", raw)
	}
	if _, ok := raw["fallback_dir"]; ok {
		t.Fatalf("Data Service config persisted fallback_dir: %v", raw)
	}

	resp := postJSON(t, srv.URL+"/profiles", contracts.Profile{Name: "p", Snapshot: contracts.ProfileSnapshot{}})
	created := decodeJSON[contracts.Profile](t, resp)
	activated := decodeJSON[map[string]any](t, postJSON(t, srv.URL+"/profiles/"+created.ID+"/activate", nil))
	if activated["fallback_dir"] != "/src/checkout" {
		t.Fatalf("activate fallback_dir = %v, want /src/checkout", activated["fallback_dir"])
	}
}

// No cwd known (Root unset): the field is absent rather than "".
func TestConfigResponseOmitsUnknownFallbackDir(t *testing.T) {
	srv := newTestServer(t)
	getResp, err := http.Get(srv.URL + "/config")
	if err != nil {
		t.Fatal(err)
	}
	if got := decodeJSON[map[string]any](t, getResp); got["fallback_dir"] != nil {
		t.Fatalf("GET /config fallback_dir = %v, want absent", got["fallback_dir"])
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
