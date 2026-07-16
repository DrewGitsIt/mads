package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/capability"
	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/credstore"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/pkg/vault"
	"github.com/drewkidwell/mads/providers/provider"
	"github.com/drewkidwell/mads/services/data/datatest"
)

// fakeVaultSource is a minimal vault.Source stub for runner tests — no
// subprocess involved, since pkg/vault's own fake-binary mechanics
// (MADS_FAKE_OP/MADS_FAKE_AWS) are already covered in that package; this
// test only needs Resolve to return a pre-set value per ref.
type fakeVaultSource struct {
	name   string
	values map[string]string
}

func (f *fakeVaultSource) Name() string { return f.name }

func (f *fakeVaultSource) Healthy(context.Context) (bool, error) { return true, nil }

func (f *fakeVaultSource) Resolve(_ context.Context, ref string) (string, error) {
	v, ok := f.values[ref]
	if !ok {
		return "", fmt.Errorf("fakeVaultSource: unknown ref %q", ref)
	}
	return v, nil
}

var envdumpBin string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "mads-capability-test")
	if err != nil {
		panic(err)
	}
	defer func() { _ = os.RemoveAll(dir) }()

	envdumpBin = filepath.Join(dir, "envdump")
	build := exec.Command("go", "build", "-o", envdumpBin,
		"github.com/drewkidwell/mads/pkg/capability/testdata/envdump")
	if out, err := build.CombinedOutput(); err != nil {
		panic("build envdump: " + err.Error() + "\n" + string(out))
	}

	os.Exit(m.Run())
}

func newCapabilityTestSession(t *testing.T, base, agentName string) contracts.Session {
	t.Helper()
	bootstrap := dataclient.New(base, "test-bootstrap")
	sess := contracts.Session{Name: "test", Provider: "fake", Agent: agentName}
	if err := bootstrap.CreateSession(context.Background(), &sess); err != nil {
		t.Fatalf("create session: %v", err)
	}
	return sess
}

func newTestAgent(t *testing.T, data *dataclient.Client, name string, caps contracts.AgentCapabilities) contracts.Agent {
	t.Helper()
	a := contracts.Agent{Name: name, Prompt: "test agent", Capabilities: caps}
	if err := data.CreateAgent(context.Background(), &a); err != nil {
		t.Fatalf("create agent %s: %v", name, err)
	}
	return a
}

func newTestCapability(t *testing.T, data *dataclient.Client, c contracts.Capability) contracts.Capability {
	t.Helper()
	if err := data.CreateCapability(context.Background(), &c); err != nil {
		t.Fatalf("create capability %s: %v", c.Name, err)
	}
	return c
}

func newTestRunner(sessionID string, data *dataclient.Client, cs *credstore.Store, mcpDir string) *Runner {
	r := New(sessionID, data, provider.NewRegistry())
	r.CredStore = cs
	r.MCPRegistry = capability.NewRegistry(mcpDir)
	return r
}

// waitForFile polls until path exists (a spawned MCP process's dump file
// lands asynchronously — EnsureStarted returns as soon as the process
// starts, not when it finishes writing).
func waitForFile(t *testing.T, path string) []byte {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(path); err == nil {
			return data
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", path)
	return nil
}

// TestSetupCapabilitiesScopesEnvByDeclaration proves the two acceptance-
// criteria scenarios together: a declared MCP capability's server is really
// started with its own credential injected (observed via a real child
// process's dumped env), and a capability the agent never declared has its
// credential provably absent from both the MCP process's env and the
// agent's own env — the "undeclared token absent" boundary is structural
// (setupMCP's env never crosses into the returned skill env), not filtered.
func TestSetupCapabilitiesScopesEnvByDeclaration(t *testing.T) {
	ctx := context.Background()
	base := datatest.Start(t)
	data := dataclient.New(base, "test-bootstrap")

	credPath := filepath.Join(t.TempDir(), "credentials.json")
	cs, err := credstore.Open(credPath)
	if err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]string{
		"MADS_TEST_FOO_TOKEN": "foo-value", // declared skill credential
		"MADS_TEST_BAR_TOKEN": "bar-value", // declared MCP credential
		"MADS_TEST_BAZ_TOKEN": "baz-value", // never declared by the agent
	} {
		if err := cs.Set(name, value); err != nil {
			t.Fatal(err)
		}
	}

	skillDir := t.TempDir()
	dumpPath := filepath.Join(t.TempDir(), "dump.json")

	newTestCapability(t, data, contracts.Capability{
		Name: "skill-cap", Type: contracts.CapabilitySkill,
		Config:            contracts.CapabilityConfig{Path: skillDir},
		CredentialEnvVars: []string{"MADS_TEST_FOO_TOKEN"},
	})
	newTestCapability(t, data, contracts.Capability{
		Name: "mcp-cap", Type: contracts.CapabilityMCP,
		Config:            contracts.CapabilityConfig{Command: envdumpBin + " -out " + dumpPath},
		CredentialEnvVars: []string{"MADS_TEST_BAR_TOKEN"},
	})
	newTestCapability(t, data, contracts.Capability{
		Name: "undeclared-cap", Type: contracts.CapabilityMCP,
		Config:            contracts.CapabilityConfig{Command: "true"},
		CredentialEnvVars: []string{"MADS_TEST_BAZ_TOKEN"},
	})

	agent := newTestAgent(t, data, "cap-agent", contracts.AgentCapabilities{
		Skills:     []string{"skill-cap"},
		MCPServers: []string{"mcp-cap"},
	})
	sess := newCapabilityTestSession(t, base, agent.Name)

	r := newTestRunner(sess.ID, dataclient.New(base, sess.ID), cs, t.TempDir())
	cwd := t.TempDir()

	env := r.setupCapabilities(ctx, sess, cwd)

	joined := strings.Join(env, "\n")
	if !strings.Contains(joined, "MADS_TEST_FOO_TOKEN=foo-value") {
		t.Errorf("agent env missing declared skill credential MADS_TEST_FOO_TOKEN: %v", env)
	}
	if strings.Contains(joined, "MADS_TEST_BAR_TOKEN") {
		t.Errorf("agent env must never see the MCP-scoped credential MADS_TEST_BAR_TOKEN: %v", env)
	}
	if strings.Contains(joined, "MADS_TEST_BAZ_TOKEN") {
		t.Errorf("agent env must never see an undeclared capability's credential MADS_TEST_BAZ_TOKEN: %v", env)
	}

	dump := waitForFile(t, dumpPath)
	var mcpEnv map[string]string
	if err := json.Unmarshal(dump, &mcpEnv); err != nil {
		t.Fatalf("parse dumped env: %v", err)
	}
	if mcpEnv["MADS_TEST_BAR_TOKEN"] != "bar-value" {
		t.Errorf("MCP server process env = %v, want MADS_TEST_BAR_TOKEN=bar-value (its own declared credential)", mcpEnv)
	}
	if _, ok := mcpEnv["MADS_TEST_FOO_TOKEN"]; ok {
		t.Errorf("MCP server process must never see the skill-scoped credential MADS_TEST_FOO_TOKEN: %v", mcpEnv)
	}
	if _, ok := mcpEnv["MADS_TEST_BAZ_TOKEN"]; ok {
		t.Errorf("MCP server process must never see an undeclared capability's credential MADS_TEST_BAZ_TOKEN: %v", mcpEnv)
	}
}

// TestSetupCapabilitiesMissingCredentialDegradesGracefully proves the
// missing-credential-degrades-without-crash acceptance criterion: the call
// never errors (there is no error return at all — a missing credential
// simply leaves that one capability unavailable), the capability reports
// unhealthy, and no credential value — not even an unrelated, successfully
// resolved one — ever reaches the event log.
func TestSetupCapabilitiesMissingCredentialDegradesGracefully(t *testing.T) {
	ctx := context.Background()
	base := datatest.Start(t)
	data := dataclient.New(base, "test-bootstrap")

	credPath := filepath.Join(t.TempDir(), "credentials.json")
	cs, err := credstore.Open(credPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := cs.Set("GOOD_TOKEN", "good-secret-value"); err != nil {
		t.Fatal(err)
	}
	// MISSING_TOKEN is deliberately never set.

	skillDir := t.TempDir()
	newTestCapability(t, data, contracts.Capability{
		Name: "good-cap", Type: contracts.CapabilitySkill,
		Config:            contracts.CapabilityConfig{Path: skillDir},
		CredentialEnvVars: []string{"GOOD_TOKEN"},
	})
	newTestCapability(t, data, contracts.Capability{
		Name: "missing-cap", Type: contracts.CapabilityMCP,
		Config:            contracts.CapabilityConfig{Command: "true"},
		CredentialEnvVars: []string{"MISSING_TOKEN"},
	})

	agent := newTestAgent(t, data, "degraded-agent", contracts.AgentCapabilities{
		Skills:     []string{"good-cap"},
		MCPServers: []string{"missing-cap"},
	})
	sess := newCapabilityTestSession(t, base, agent.Name)

	r := newTestRunner(sess.ID, dataclient.New(base, sess.ID), cs, t.TempDir())
	cwd := t.TempDir()

	// Must not panic or otherwise abort — the session continues.
	env := r.setupCapabilities(ctx, sess, cwd)
	if !strings.Contains(strings.Join(env, "\n"), "GOOD_TOKEN=good-secret-value") {
		t.Errorf("resolvable skill credential should still be injected: %v", env)
	}

	missing, err := data.Capability(ctx, "missing-cap")
	if err != nil {
		t.Fatal(err)
	}
	if missing.Health.Healthy {
		t.Error("capability with a missing credential should report unhealthy")
	}
	if missing.Health.Reason != "missing credential: MISSING_TOKEN" {
		t.Errorf("health reason = %q, want exactly the env-var name, no value", missing.Health.Reason)
	}

	good, err := data.Capability(ctx, "good-cap")
	if err != nil {
		t.Fatal(err)
	}
	if !good.Health.Healthy {
		t.Errorf("capability with a resolved credential should report healthy: %+v", good.Health)
	}

	events, err := data.Events(ctx, 0, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range events {
		raw, err := json.Marshal(ev)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), "good-secret-value") {
			t.Fatalf("credential value leaked into an event: %s", raw)
		}
	}
}

// TestSetupCapabilitiesHealthChangedFiresOnTransition confirms the runtime
// wiring end to end (not just the store-level transition rule already
// covered elsewhere): a capability whose credential starts out missing
// reports unhealthy without emitting an event (matching the zero-value
// default), and once the credential is added and its long-running MCP
// server actually starts, the genuine flip to healthy emits exactly one
// capability.health_changed event.
func TestSetupCapabilitiesHealthChangedFiresOnTransition(t *testing.T) {
	ctx := context.Background()
	base := datatest.Start(t)
	data := dataclient.New(base, "test-bootstrap")

	credPath := filepath.Join(t.TempDir(), "credentials.json")
	cs, err := credstore.Open(credPath)
	if err != nil {
		t.Fatal(err)
	}

	newTestCapability(t, data, contracts.Capability{
		Name: "runner-cap", Type: contracts.CapabilityMCP,
		Config:            contracts.CapabilityConfig{Command: "sleep 30"},
		CredentialEnvVars: []string{"RUNNING_TOKEN"},
	})
	agent := newTestAgent(t, data, "transition-agent", contracts.AgentCapabilities{
		MCPServers: []string{"runner-cap"},
	})
	sess := newCapabilityTestSession(t, base, agent.Name)

	mcpDir := t.TempDir()
	r := newTestRunner(sess.ID, dataclient.New(base, sess.ID), cs, mcpDir)
	t.Cleanup(func() { _ = r.MCPRegistry.Stop(context.Background(), "runner-cap") })

	// First attempt: credential missing, matches the zero-value default — no event.
	r.setupCapabilities(ctx, sess, t.TempDir())
	events, err := data.Events(ctx, 0, "", []string{"capability.health_changed"})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 0 {
		t.Fatalf("initial unhealthy report: want no events, got %d", len(events))
	}

	// Add the credential and retry: the server actually starts, and the
	// genuine flip to healthy emits exactly one event.
	if err := cs.Set("RUNNING_TOKEN", "running-value"); err != nil {
		t.Fatal(err)
	}
	r.setupCapabilities(ctx, sess, t.TempDir())

	events, err = data.Events(ctx, 0, "", []string{"capability.health_changed"})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("healthy flip: want exactly 1 event, got %d: %+v", len(events), events)
	}
	if !r.MCPRegistry.Running("runner-cap") {
		t.Error("capability's MCP server should be running after a successful start")
	}
}

// newCapabilityTestSessionInWorkspace is like newCapabilityTestSession but
// also sets Workspace, for the cross-workspace isolation test.
func newCapabilityTestSessionInWorkspace(t *testing.T, base, agentName, workspace string) contracts.Session {
	t.Helper()
	bootstrap := dataclient.New(base, "test-bootstrap")
	sess := contracts.Session{Name: "test", Provider: "fake", Agent: agentName, Workspace: workspace}
	if err := bootstrap.CreateSession(context.Background(), &sess); err != nil {
		t.Fatalf("create session: %v", err)
	}
	return sess
}

// TestSetupCapabilitiesResolvesVaultBackedCredential proves a vault-resolved
// credential (spec §13.8) injects into a session whose agent declares the
// capability, following the exact same scoping shape as the credstore-backed
// TestSetupCapabilitiesScopesEnvByDeclaration: a declared capability's
// vault-backed credential is present, and a second, undeclared capability's
// vault-backed credential — belonging to a different agent entirely — never
// appears.
func TestSetupCapabilitiesResolvesVaultBackedCredential(t *testing.T) {
	ctx := context.Background()
	base := datatest.Start(t)
	data := dataclient.New(base, "test-bootstrap")

	credPath := filepath.Join(t.TempDir(), "credentials.json")
	cs, err := credstore.Open(credPath)
	if err != nil {
		t.Fatal(err)
	}

	refs, err := vault.OpenRefStore(filepath.Join(t.TempDir(), "vault-refs.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := refs.Set("MADS_TEST_VAULT_TOKEN", vault.Ref{Source: "fake-vault", Path: "vault-path-declared"}); err != nil {
		t.Fatal(err)
	}
	if err := refs.Set("MADS_TEST_VAULT_TOKEN_OTHER", vault.Ref{Source: "fake-vault", Path: "vault-path-undeclared"}); err != nil {
		t.Fatal(err)
	}
	resolver := &vault.Resolver{
		Refs: refs,
		Sources: map[string]vault.Source{
			"fake-vault": &fakeVaultSource{name: "fake-vault", values: map[string]string{
				"vault-path-declared":   "vault-secret-value",
				"vault-path-undeclared": "other-agent-secret-value",
			}},
		},
	}

	skillDir := t.TempDir()
	newTestCapability(t, data, contracts.Capability{
		Name: "vault-skill-cap", Type: contracts.CapabilitySkill,
		Config:            contracts.CapabilityConfig{Path: skillDir},
		CredentialEnvVars: []string{"MADS_TEST_VAULT_TOKEN"},
	})
	// Never declared by cap-agent below — proves the undeclared boundary
	// holds for vault-backed credentials the same way it does for
	// credstore-backed ones.
	newTestCapability(t, data, contracts.Capability{
		Name: "vault-undeclared-cap", Type: contracts.CapabilityMCP,
		Config:            contracts.CapabilityConfig{Command: "true"},
		CredentialEnvVars: []string{"MADS_TEST_VAULT_TOKEN_OTHER"},
	})

	agent := newTestAgent(t, data, "vault-cap-agent", contracts.AgentCapabilities{
		Skills: []string{"vault-skill-cap"},
	})
	sess := newCapabilityTestSession(t, base, agent.Name)

	r := newTestRunner(sess.ID, dataclient.New(base, sess.ID), cs, t.TempDir())
	r.VaultResolver = resolver

	env := r.setupCapabilities(ctx, sess, t.TempDir())

	joined := strings.Join(env, "\n")
	if !strings.Contains(joined, "MADS_TEST_VAULT_TOKEN=vault-secret-value") {
		t.Errorf("agent env missing declared vault-backed credential MADS_TEST_VAULT_TOKEN: %v", env)
	}
	if strings.Contains(joined, "MADS_TEST_VAULT_TOKEN_OTHER") || strings.Contains(joined, "other-agent-secret-value") {
		t.Errorf("agent env must never see an undeclared capability's vault-backed credential: %v", env)
	}
}

// TestSetupCapabilitiesCrossWorkspaceIsolation is the work item's literal
// done-when: a credential scoped to workspace A never appears in workspace
// B's session environment. Workspace A's capability draws from credstore
// (the env-import shape); workspace B's own capability draws from a vault
// ref, so this also proves the two credential sources compose under the
// same scoping. There is no workspace-keyed credential store — scoping is
// entirely agent -> capability -> env-var-name (contracts.Workspace has no
// credential field), so this exercises setupCapabilities for workspace B's
// session only and asserts workspace A's name/value are absent from both
// the returned skill env and the MCP child's dumped env.
func TestSetupCapabilitiesCrossWorkspaceIsolation(t *testing.T) {
	ctx := context.Background()
	base := datatest.Start(t)
	data := dataclient.New(base, "test-bootstrap")

	credPath := filepath.Join(t.TempDir(), "credentials.json")
	cs, err := credstore.Open(credPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := cs.Set("MADS_TEST_WORKSPACE_A_TOKEN", "workspace-a-secret-value"); err != nil {
		t.Fatal(err)
	}

	refs, err := vault.OpenRefStore(filepath.Join(t.TempDir(), "vault-refs.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := refs.Set("MADS_TEST_WORKSPACE_B_TOKEN", vault.Ref{Source: "fake-vault", Path: "workspace-b-path"}); err != nil {
		t.Fatal(err)
	}
	resolver := &vault.Resolver{
		Refs: refs,
		Sources: map[string]vault.Source{
			"fake-vault": &fakeVaultSource{name: "fake-vault", values: map[string]string{
				"workspace-b-path": "workspace-b-secret-value",
			}},
		},
	}

	// Workspace A's capability/agent exist (and are never invoked below) so
	// this asserts real cross-workspace absence, not merely "never created".
	newTestCapability(t, data, contracts.Capability{
		Name: "workspace-a-skill", Type: contracts.CapabilitySkill,
		Config:            contracts.CapabilityConfig{Path: t.TempDir()},
		CredentialEnvVars: []string{"MADS_TEST_WORKSPACE_A_TOKEN"},
	})
	newTestAgent(t, data, "workspace-a-agent", contracts.AgentCapabilities{
		Skills: []string{"workspace-a-skill"},
	})

	dumpPath := filepath.Join(t.TempDir(), "dump.json")
	newTestCapability(t, data, contracts.Capability{
		Name: "workspace-b-mcp", Type: contracts.CapabilityMCP,
		Config:            contracts.CapabilityConfig{Command: envdumpBin + " -out " + dumpPath},
		CredentialEnvVars: []string{"MADS_TEST_WORKSPACE_B_TOKEN"},
	})
	agentB := newTestAgent(t, data, "workspace-b-agent", contracts.AgentCapabilities{
		MCPServers: []string{"workspace-b-mcp"},
	})
	sessB := newCapabilityTestSessionInWorkspace(t, base, agentB.Name, "workspace-b")

	r := newTestRunner(sessB.ID, dataclient.New(base, sessB.ID), cs, t.TempDir())
	r.VaultResolver = resolver

	env := r.setupCapabilities(ctx, sessB, t.TempDir())
	joined := strings.Join(env, "\n")
	if strings.Contains(joined, "MADS_TEST_WORKSPACE_A_TOKEN") || strings.Contains(joined, "workspace-a-secret-value") {
		t.Errorf("workspace B's session env must never see workspace A's credential: %v", env)
	}

	dump := waitForFile(t, dumpPath)
	var mcpEnv map[string]string
	if err := json.Unmarshal(dump, &mcpEnv); err != nil {
		t.Fatalf("parse dumped env: %v", err)
	}
	if mcpEnv["MADS_TEST_WORKSPACE_B_TOKEN"] != "workspace-b-secret-value" {
		t.Errorf("MCP server process env = %v, want MADS_TEST_WORKSPACE_B_TOKEN=workspace-b-secret-value (its own declared credential)", mcpEnv)
	}
	if _, ok := mcpEnv["MADS_TEST_WORKSPACE_A_TOKEN"]; ok {
		t.Errorf("workspace B's MCP process must never see workspace A's credential: %v", mcpEnv)
	}
	for _, v := range mcpEnv {
		if v == "workspace-a-secret-value" {
			t.Errorf("workspace A's credential value leaked into workspace B's MCP process env: %v", mcpEnv)
		}
	}
}
