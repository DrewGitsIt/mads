package api_test

import (
	"net/http"
	"testing"

	"github.com/drewkidwell/mads/pkg/contracts"
)

// No workspace at all: provider/trust come from Config, model is discovered
// from the resolved provider's Default:true model, isolation falls back to
// the hardcoded builtin.
func TestSessionDefaultsNoWorkspace(t *testing.T) {
	srv := newTestServer(t)
	setDefaultProvider(t, srv.URL, "fake")

	resp, err := http.Get(srv.URL + "/sessions/defaults")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	got := decodeJSON[contracts.SessionDefaults](t, resp)

	want := contracts.SessionDefaults{
		Provider:  contracts.ResolvedSessionField{Value: "fake", Source: contracts.SessionDefaultSourceConfig},
		Model:     contracts.ResolvedSessionField{Value: "fake-fast", Source: contracts.SessionDefaultSourceProviderDefault},
		Isolation: contracts.ResolvedSessionField{Value: contracts.IsolationWorktree, Source: contracts.SessionDefaultSourceBuiltin},
		Trust:     contracts.ResolvedSessionField{Value: string(contracts.TrustLimited), Source: contracts.SessionDefaultSourceConfig},
	}
	if got != want {
		t.Errorf("got = %+v, want %+v", got, want)
	}
}

// A workspace with Defaults set: model/isolation/agent all sourced from the
// workspace, and Workspace names which one was consulted.
func TestSessionDefaultsWorkspaceSet(t *testing.T) {
	srv := newTestServer(t)
	setDefaultProvider(t, srv.URL, "fake")

	wsResp := postJSON(t, srv.URL+"/workspaces", contracts.Workspace{
		Name: "ws-with-defaults",
		Defaults: contracts.WorkspaceDefaults{
			Model:     "fake-smart",
			Isolation: contracts.IsolationCurrentDir,
			Agent:     "some-agent",
		},
	})
	if wsResp.StatusCode != http.StatusCreated {
		t.Fatalf("create workspace status = %d, want 201", wsResp.StatusCode)
	}
	ws := decodeJSON[contracts.Workspace](t, wsResp)

	resp, err := http.Get(srv.URL + "/sessions/defaults?workspace=" + ws.ID)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	got := decodeJSON[contracts.SessionDefaults](t, resp)

	if got.Model != (contracts.ResolvedSessionField{Value: "fake-smart", Source: contracts.SessionDefaultSourceWorkspace}) {
		t.Errorf("model = %+v, want workspace-sourced fake-smart", got.Model)
	}
	if got.Isolation != (contracts.ResolvedSessionField{Value: contracts.IsolationCurrentDir, Source: contracts.SessionDefaultSourceWorkspace}) {
		t.Errorf("isolation = %+v, want workspace-sourced %q", got.Isolation, contracts.IsolationCurrentDir)
	}
	if got.Agent != (contracts.ResolvedSessionField{Value: "some-agent", Source: contracts.SessionDefaultSourceWorkspace}) {
		t.Errorf("agent = %+v, want workspace-sourced some-agent", got.Agent)
	}
	if got.Workspace != ws.Name {
		t.Errorf("workspace = %q, want %q", got.Workspace, ws.Name)
	}
}

// A non-default Config.DefaultTrust is reflected with source "config".
func TestSessionDefaultsConfigSet(t *testing.T) {
	srv := newTestServer(t)
	setDefaultProvider(t, srv.URL, "fake")

	getResp, err := http.Get(srv.URL + "/config")
	if err != nil {
		t.Fatal(err)
	}
	cfg := decodeJSON[contracts.Config](t, getResp)
	cfg.DefaultTrust = contracts.TrustFull
	putResp := putJSON(t, srv.URL+"/config", cfg)
	if putResp.StatusCode != http.StatusOK {
		t.Fatalf("set default trust: status %d", putResp.StatusCode)
	}

	resp, err := http.Get(srv.URL + "/sessions/defaults")
	if err != nil {
		t.Fatal(err)
	}
	got := decodeJSON[contracts.SessionDefaults](t, resp)
	want := contracts.ResolvedSessionField{Value: string(contracts.TrustFull), Source: contracts.SessionDefaultSourceConfig}
	if got.Trust != want {
		t.Errorf("trust = %+v, want %+v", got.Trust, want)
	}
}

// An unknown ?workspace= value degrades gracefully to no workspace defaults,
// not an error — mirrors resolveSessionDefaults' existing stale-workspace
// behavior (sessiondefaults.go).
func TestSessionDefaultsUnknownWorkspaceDegradesGracefully(t *testing.T) {
	srv := newTestServer(t)
	setDefaultProvider(t, srv.URL, "fake")

	resp, err := http.Get(srv.URL + "/sessions/defaults?workspace=does-not-exist")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	got := decodeJSON[contracts.SessionDefaults](t, resp)
	if got.Workspace != "" {
		t.Errorf("workspace = %q, want empty for unknown workspace key", got.Workspace)
	}
}

// No default provider configured at all: nothing to discover a model from,
// so both Provider and Model resolve to entirely empty fields (no source).
func TestSessionDefaultsNoProviderConfigured(t *testing.T) {
	srv := newTestServer(t)

	resp, err := http.Get(srv.URL + "/sessions/defaults")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	got := decodeJSON[contracts.SessionDefaults](t, resp)
	if got.Provider != (contracts.ResolvedSessionField{}) {
		t.Errorf("provider = %+v, want empty", got.Provider)
	}
	if got.Model != (contracts.ResolvedSessionField{}) {
		t.Errorf("model = %+v, want empty", got.Model)
	}
}
