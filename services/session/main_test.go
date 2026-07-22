package main

import (
	"encoding/json"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/drewkidwell/mads/pkg/agentenv"
	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/services/session/internal/api"
	"github.com/drewkidwell/mads/services/session/internal/runner"
)

func TestHealth(t *testing.T) {
	// /health never touches the runner (spec §4: always 200 regardless of
	// runner state), so an unstarted Runner is enough for this test.
	r := runner.New("unused", nil, nil)
	srv := httptest.NewServer(api.New(r))
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
	if body["service"] != "session" || body["status"] != "ok" {
		t.Errorf("unexpected health body: %v", body)
	}
}

func TestResolveAgentEnv_InheritOmitsCredEnvToStayByteIdentical(t *testing.T) {
	parent := []string{"PATH=/bin", "MADS_CRED_A=secret-value"}
	cfg := contracts.Config{CredentialBindings: []string{"MADS_CRED_A"}}

	mode, passthrough, credEnv := resolveAgentEnv(cfg, parent)

	if mode == agentenv.ModeIsolated {
		t.Fatalf("mode = %v, want inherit (zero value)", mode)
	}
	if passthrough != nil || credEnv != nil {
		t.Fatalf("inherit mode should resolve no passthrough/credEnv (parentEnv already has it): passthrough=%v credEnv=%v", passthrough, credEnv)
	}
}

func TestResolveAgentEnv_IsolatedSelectsBoundCredentialsFromParentEnv(t *testing.T) {
	parent := []string{"PATH=/bin", "MADS_CRED_A=secret-value", "OTHER=x"}
	cfg := contracts.Config{
		EnvMode:            contracts.EnvModeIsolated,
		EnvPassthrough:     []string{"AWS_PROFILE"},
		CredentialBindings: []string{"MADS_CRED_A"},
	}

	mode, passthrough, credEnv := resolveAgentEnv(cfg, parent)

	if mode != agentenv.ModeIsolated {
		t.Fatalf("mode = %v, want isolated", mode)
	}
	if !reflect.DeepEqual(passthrough, cfg.EnvPassthrough) {
		t.Fatalf("passthrough = %v, want %v", passthrough, cfg.EnvPassthrough)
	}
	want := []string{"MADS_CRED_A=secret-value"}
	if !reflect.DeepEqual(credEnv, want) {
		t.Fatalf("credEnv = %v, want %v", credEnv, want)
	}
}
