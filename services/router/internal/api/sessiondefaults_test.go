package api_test

import (
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
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
	// Nobody asked for a directory: the preview reports the Router's own,
	// as a builtin. Its git-ness depends on where the tests run.
	wd, _ := os.Getwd()
	if got.Dir != (contracts.ResolvedSessionField{Value: wd, Source: contracts.SessionDefaultSourceBuiltin}) {
		t.Errorf("dir = %+v, want builtin %q", got.Dir, wd)
	}
	got.Dir, got.DirStatus = contracts.ResolvedSessionField{}, ""

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

// updateConfig applies mutate to the live Config and PUTs it back.
func updateConfig(t *testing.T, baseURL string, mutate func(*contracts.Config)) {
	t.Helper()
	getResp, err := http.Get(baseURL + "/config")
	if err != nil {
		t.Fatal(err)
	}
	cfg := decodeJSON[contracts.Config](t, getResp)
	mutate(&cfg)
	if resp := putJSON(t, baseURL+"/config", cfg); resp.StatusCode != http.StatusOK {
		t.Fatalf("put config: status %d", resp.StatusCode)
	}
}

func getDefaults(t *testing.T, baseURL string, query url.Values) contracts.SessionDefaults {
	t.Helper()
	resp, err := http.Get(baseURL + "/sessions/defaults?" + query.Encode())
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	return decodeJSON[contracts.SessionDefaults](t, resp)
}

// Every pill's "Save as my defaults" value resolves from Config with source
// "config" when no workspace says otherwise.
func TestSessionDefaultsSavedConfigDefaults(t *testing.T) {
	srv := newTestServer(t)
	dir := t.TempDir()
	updateConfig(t, srv.URL, func(c *contracts.Config) {
		c.DefaultProvider = "fake"
		c.DefaultModel = "fake-smart"
		c.DefaultAgent = "auto"
		c.DefaultIsolation = contracts.IsolationCurrentDir
		c.DefaultDir = dir
	})

	got := getDefaults(t, srv.URL, nil)
	cfgSrc := contracts.SessionDefaultSourceConfig
	for name, pair := range map[string][2]contracts.ResolvedSessionField{
		"model":     {got.Model, {Value: "fake-smart", Source: cfgSrc}},
		"agent":     {got.Agent, {Value: "auto", Source: cfgSrc}},
		"isolation": {got.Isolation, {Value: contracts.IsolationCurrentDir, Source: cfgSrc}},
		"dir":       {got.Dir, {Value: dir, Source: cfgSrc}},
	} {
		if pair[0] != pair[1] {
			t.Errorf("%s = %+v, want %+v", name, pair[0], pair[1])
		}
	}
	if got.DirStatus != contracts.DirStatusPlain {
		t.Errorf("dir_status = %q, want plain for a non-repo folder", got.DirStatus)
	}
}

// A workspace's defaults — its primary repo included — still beat the
// user's saved ones; a folder picked on the Launchpad beats both.
func TestSessionDefaultsDirPrecedence(t *testing.T) {
	srv := newTestServer(t)
	saved, repo, picked := t.TempDir(), t.TempDir(), t.TempDir()
	updateConfig(t, srv.URL, func(c *contracts.Config) {
		c.DefaultProvider = "fake"
		c.DefaultDir = saved
		c.DefaultIsolation = contracts.IsolationWorktree
	})
	wsResp := postJSON(t, srv.URL+"/workspaces", contracts.Workspace{
		Name:     "ws",
		Repos:    []contracts.WorkspaceRepo{{Path: repo, Primary: true}},
		Defaults: contracts.WorkspaceDefaults{Isolation: contracts.IsolationCurrentDir},
	})
	if wsResp.StatusCode != http.StatusCreated {
		t.Fatalf("create workspace status = %d", wsResp.StatusCode)
	}

	got := getDefaults(t, srv.URL, url.Values{"workspace": {"ws"}})
	if got.Dir != (contracts.ResolvedSessionField{Value: repo, Source: contracts.SessionDefaultSourceWorkspace}) {
		t.Errorf("dir = %+v, want the workspace's primary repo", got.Dir)
	}
	if got.Isolation.Source != contracts.SessionDefaultSourceWorkspace {
		t.Errorf("isolation source = %q, want workspace over config", got.Isolation.Source)
	}

	got = getDefaults(t, srv.URL, url.Values{"workspace": {"ws"}, "dir": {picked}})
	if got.Dir != (contracts.ResolvedSessionField{Value: picked}) {
		t.Errorf("dir = %+v, want the picked folder with no default source", got.Dir)
	}
}

// A saved model belongs to the saved provider; it must not leak onto a
// session that picked a different one.
func TestSessionDefaultsConfigModelOnlyForConfigProvider(t *testing.T) {
	srv := newTestServer(t)
	updateConfig(t, srv.URL, func(c *contracts.Config) {
		c.DefaultProvider = "other"
		c.DefaultModel = "other-model"
	})
	resp := postJSON(t, srv.URL+"/sessions", map[string]string{"name": "x", "provider": "fake"})
	created := decodeJSON[struct {
		Session contracts.Session `json:"session"`
	}](t, resp)
	if created.Session.Model != "fake-fast" {
		t.Errorf("model = %q, want fake's own default, not the saved other-model", created.Session.Model)
	}

	updateConfig(t, srv.URL, func(c *contracts.Config) { c.DefaultProvider = "fake" })
	if got := getDefaults(t, srv.URL, nil); got.Model != (contracts.ResolvedSessionField{Value: "other-model", Source: contracts.SessionDefaultSourceConfig}) {
		t.Errorf("model = %+v, want the saved model while its provider resolves", got.Model)
	}
}

// dir_status lets the Launchpad warn before Start.
func TestSessionDefaultsDirStatus(t *testing.T) {
	srv := newTestServer(t)
	setDefaultProvider(t, srv.URL, "fake")
	repo := t.TempDir()
	if out, err := exec.Command("git", "init", "-q", repo).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	for dir, want := range map[string]contracts.DirStatus{
		repo:                                    contracts.DirStatusGit,
		t.TempDir():                             contracts.DirStatusPlain,
		filepath.Join(t.TempDir(), "not-there"): contracts.DirStatusMissing,
	} {
		if got := getDefaults(t, srv.URL, url.Values{"dir": {dir}}); got.DirStatus != want {
			t.Errorf("dir_status(%s) = %q, want %q", dir, got.DirStatus, want)
		}
	}
}

// Create refuses a folder that isn't there instead of spawning a child that
// dies on chdir, and records one that is.
func TestCreateSessionDir(t *testing.T) {
	srv := newTestServer(t)
	missing := filepath.Join(t.TempDir(), "gone")
	resp := postJSON(t, srv.URL+"/sessions", map[string]string{"name": "x", "provider": "fake", "dir": missing})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("missing dir: status = %d, want 400", resp.StatusCode)
	}
	resp = postJSON(t, srv.URL+"/sessions", map[string]string{"name": "x", "provider": "fake", "dir": "relative/path"})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("relative dir: status = %d, want 400", resp.StatusCode)
	}

	dir := t.TempDir()
	resp = postJSON(t, srv.URL+"/sessions", map[string]string{"name": "x", "provider": "fake", "dir": dir})
	created := decodeJSON[struct {
		Session contracts.Session `json:"session"`
	}](t, resp)
	if created.Session.Dir != dir {
		t.Errorf("recorded dir = %q, want %q", created.Session.Dir, dir)
	}
}
