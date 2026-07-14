package runner

import (
	"context"
	"encoding/json"
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
	"github.com/drewkidwell/mads/providers/provider"
	"github.com/drewkidwell/mads/services/data/datatest"
)

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
