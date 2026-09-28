package main

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/credstore"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/pkg/registryclient"
	"github.com/drewkidwell/mads/providers/claudecode"
	"github.com/drewkidwell/mads/providers/fake"
	"github.com/drewkidwell/mads/providers/fastmodel"
	"github.com/drewkidwell/mads/providers/kirocli"
	"github.com/drewkidwell/mads/providers/provider"
	"github.com/drewkidwell/mads/services/router/internal/job"
	"github.com/drewkidwell/mads/services/router/internal/supervisor"
)

func TestHealth(t *testing.T) {
	data := dataclient.New("http://localhost:0", "router")
	sup := supervisor.New(data, nil, nil, nil)
	reg := registryclient.New("")
	jobs := job.New(data, fastmodel.New(fake.New(), fastmodel.Config{}), reg)
	providers := provider.NewRegistry()
	if err := providers.Register(fake.New()); err != nil {
		t.Fatal(err)
	}
	creds, err := credstore.Open(t.TempDir() + "/credentials.json")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(newMux(data, sup, jobs, providers, creds, nil, nil, nil, nil, reg, t.TempDir(), "", nil))
	defer srv.Close()

	resp, err := srv.Client().Get(srv.URL + "/health")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 200 {
		t.Fatalf("GET /health = %d, want 200", resp.StatusCode)
	}
	var body map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body["service"] != "router" || body["status"] != "ok" {
		t.Errorf("unexpected health body: %v", body)
	}
}

// TestBuildProviderRegistryIncludesGoose pins the registration set main()
// boots with: goose alongside the pre-existing three, proving the
// extraction out of main() was lossless.
func TestBuildProviderRegistryIncludesGoose(t *testing.T) {
	reg, err := buildProviderRegistry(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"claude-code", "kiro-cli", "goose", "fake"} {
		p, err := reg.Get(name)
		if err != nil {
			t.Fatalf("Get(%q): %v", name, err)
		}
		if p.Name() != name {
			t.Errorf("Get(%q).Name() = %q, want %q", name, p.Name(), name)
		}
	}
}

// TestProvidersEndpointListsGoose confirms GET /providers serves a goose
// row with its behavior matrix straight from the registry — the goose
// registration needed no handler changes (reads.go iterates the registry).
func TestProvidersEndpointListsGoose(t *testing.T) {
	providers, err := buildProviderRegistry(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	data := dataclient.New("http://localhost:0", "router")
	sup := supervisor.New(data, nil, nil, nil)
	reg := registryclient.New("")
	jobs := job.New(data, fastmodel.New(fake.New(), fastmodel.Config{}), reg)
	creds, err := credstore.Open(t.TempDir() + "/credentials.json")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(newMux(data, sup, jobs, providers, creds, nil, nil, nil, nil, reg, t.TempDir(), "", nil))
	defer srv.Close()

	resp, err := srv.Client().Get(srv.URL + "/providers")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 200 {
		t.Fatalf("GET /providers = %d, want 200", resp.StatusCode)
	}
	var rows []struct {
		Name   string          `json:"name"`
		Matrix json.RawMessage `json:"matrix"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&rows); err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.Name == "goose" {
			if len(row.Matrix) == 0 || string(row.Matrix) == "null" {
				t.Fatalf("goose row has no behavior matrix: %s", row.Matrix)
			}
			return
		}
	}
	t.Fatalf("GET /providers has no goose row: %+v", rows)
}

func testRegistry(t *testing.T) *provider.Registry {
	t.Helper()
	reg := provider.NewRegistry()
	if err := reg.Register(fake.New()); err != nil {
		t.Fatal(err)
	}
	if err := reg.Register(kirocli.New(kirocli.Config{})); err != nil {
		t.Fatal(err)
	}
	return reg
}

// An explicit Config field always wins over the -fastmodel-* flags — no
// ambiguity when both are present.
func TestFastModelDynamicConfigEndpointModeConfigWinsOverFlags(t *testing.T) {
	cfg := contracts.Config{
		FastModelMode:      contracts.FastModelModeEndpoint,
		FastModelBaseURL:   "https://config.example.com",
		FastModelAPIKeyEnv: "CONFIG_KEY",
		FastModel:          "config-model",
	}
	got := fastModelDynamicConfig(cfg, "https://flag.example.com", "FLAG_KEY", "flag-model", testRegistry(t))
	want := fastmodel.DynamicConfig{Model: "config-model", BaseURL: "https://config.example.com", APIKeyEnv: "CONFIG_KEY"}
	if got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

// Endpoint mode with no Config endpoint falls back to the -fastmodel-*
// flags — the pre-mode-selector zero-config path some deployments still
// rely on (e.g. a flag-only docker-compose setup).
func TestFastModelDynamicConfigEndpointModeFallsBackToFlags(t *testing.T) {
	cfg := contracts.Config{FastModelMode: contracts.FastModelModeEndpoint}
	got := fastModelDynamicConfig(cfg, "https://flag.example.com", "FLAG_KEY", "flag-model", testRegistry(t))
	want := fastmodel.DynamicConfig{Model: "flag-model", BaseURL: "https://flag.example.com", APIKeyEnv: "FLAG_KEY"}
	if got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

// Provider mode resolves cfg.DefaultProvider from the registry and passes
// it through for dynamicBackend to use via its Completer capability.
func TestFastModelDynamicConfigProviderMode(t *testing.T) {
	cfg := contracts.Config{FastModelMode: contracts.FastModelModeProvider, DefaultProvider: "fake", FastModel: "fake-model"}
	reg := testRegistry(t)
	got := fastModelDynamicConfig(cfg, "", "", "", reg)
	want, err := reg.Get("fake")
	if err != nil {
		t.Fatal(err)
	}
	if got.Provider != want || got.Model != "fake-model" || got.BaseURL != "" {
		t.Errorf("got %+v, want Provider=%v Model=fake-model BaseURL=\"\"", got, want)
	}
}

// Provider mode with an unregistered DefaultProvider must go dormant
// (BaseURL and Provider both unset), never a hard failure.
func TestFastModelDynamicConfigProviderModeUnknownProviderIsDormant(t *testing.T) {
	cfg := contracts.Config{FastModelMode: contracts.FastModelModeProvider, DefaultProvider: "no-such-provider"}
	got := fastModelDynamicConfig(cfg, "", "", "", testRegistry(t))
	if got.Provider != nil || got.BaseURL != "" {
		t.Errorf("got %+v, want dormant (no Provider, no BaseURL)", got)
	}
}

// Provider mode with no DefaultProvider set defaults to claude-code — the
// "unconfigured mads with claude-code default" acceptance criterion.
func TestFastModelDynamicConfigProviderModeDefaultsToClaudeCode(t *testing.T) {
	reg := provider.NewRegistry()
	if err := reg.Register(claudecode.New(claudecode.Config{})); err != nil {
		t.Fatal(err)
	}
	cfg := contracts.Config{FastModelMode: contracts.FastModelModeProvider}
	got := fastModelDynamicConfig(cfg, "", "", "", reg)
	want, err := reg.Get("claude-code")
	if err != nil {
		t.Fatal(err)
	}
	if got.Provider != want {
		t.Errorf("Provider = %v, want the registered claude-code provider", got.Provider)
	}
}

// Off mode leaves the backend unconfigured regardless of what Config or the
// flags otherwise contain.
func TestFastModelDynamicConfigOffMode(t *testing.T) {
	cfg := contracts.Config{
		FastModelMode:    contracts.FastModelModeOff,
		FastModelBaseURL: "https://config.example.com",
		DefaultProvider:  "fake",
	}
	got := fastModelDynamicConfig(cfg, "https://flag.example.com", "", "", testRegistry(t))
	if got != (fastmodel.DynamicConfig{}) {
		t.Errorf("got %+v, want zero value (off mode)", got)
	}
}

// An unset FastModelMode (pre-mode-selector install) with no endpoint
// configured anywhere resolves to provider mode — the zero-config default
// flips from dormant to working (spec).
func TestFastModelDynamicConfigUnsetModeDefaultsToProvider(t *testing.T) {
	reg := testRegistry(t)
	cfg := contracts.Config{DefaultProvider: "fake"}
	got := fastModelDynamicConfig(cfg, "", "", "", reg)
	want, err := reg.Get("fake")
	if err != nil {
		t.Fatal(err)
	}
	if got.Provider != want || got.BaseURL != "" {
		t.Errorf("got %+v, want the fake provider, no BaseURL", got)
	}
}

// An unset FastModelMode with a flag-only endpoint (Config.FastModelBaseURL
// empty) must still resolve to endpoint mode — the regression this test
// guards: a pre-mode-selector install configured entirely via
// -fastmodel-base-url must keep working after this field is introduced.
func TestFastModelDynamicConfigUnsetModePreservesFlagOnlyEndpoint(t *testing.T) {
	cfg := contracts.Config{DefaultProvider: "fake"}
	got := fastModelDynamicConfig(cfg, "https://flag.example.com", "FLAG_KEY", "flag-model", testRegistry(t))
	want := fastmodel.DynamicConfig{Model: "flag-model", BaseURL: "https://flag.example.com", APIKeyEnv: "FLAG_KEY"}
	if got != want {
		t.Errorf("got %+v, want %+v (flag-only endpoint must still win over the provider default)", got, want)
	}
}
