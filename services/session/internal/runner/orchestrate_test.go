package runner

import (
	"context"
	"os"
	"testing"

	"github.com/drewkidwell/mads/pkg/capability"
	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/providers/fake"
	"github.com/drewkidwell/mads/providers/provider"
	"github.com/drewkidwell/mads/services/data/datatest"
)

func enableOrchestrateLinkedSessions(t *testing.T, data *dataclient.Client) {
	t.Helper()
	cfg, err := data.Config(context.Background())
	if err != nil {
		t.Fatalf("Config: %v", err)
	}
	cfg.FeatureFlags.LinkedSessions = true
	if err := data.UpdateConfig(context.Background(), &cfg); err != nil {
		t.Fatalf("UpdateConfig: %v", err)
	}
}

// putMadsOnPath prepends madsBinDir (a real, freshly-built `mads` binary) to
// PATH — setupOrchestrate's Config.Command is the fixed literal
// "mads internal orchestrate-mcp", resolved via exec.Command's own PATH
// lookup, exactly like a real installation.
func putMadsOnPath(t *testing.T) {
	t.Helper()
	t.Setenv("PATH", madsBinDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// TestSetupOrchestrateFlagOff proves no orchestrate MCP server is spawned or
// advertised when the linked-sessions flag is off, even with a qualifying
// orchestrate link present.
func TestSetupOrchestrateFlagOff(t *testing.T) {
	putMadsOnPath(t)
	ctx := context.Background()
	base := datatest.Start(t)

	leader := newSession(t, base)
	subject := newSession(t, base)
	data := dataclient.New(base, leader.ID)
	if _, err := data.CreateLink(ctx, contracts.LinkRequest{
		GrantingSessionID: leader.ID,
		SubjectSessionID:  subject.ID,
		Preset:            contracts.PresetLeader,
	}); err != nil {
		t.Fatalf("create link: %v", err)
	}

	reg := provider.NewRegistry()
	fp := fake.New()
	if err := reg.Register(fp); err != nil {
		t.Fatalf("register fake: %v", err)
	}

	r := New(leader.ID, data, reg)
	r.MCPRegistry = testMCPRegistry(t)
	r.RouterURL = "http://localhost:0"
	if err := r.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = r.Shutdown(ctx) })

	assertNoOrchestrateMCPServer(t, data, fp, leader.ID)
}

// TestSetupOrchestrateNoGrant proves the flag alone isn't enough — a
// session with the flag on but no orchestrate grant gets no capability.
func TestSetupOrchestrateNoGrant(t *testing.T) {
	putMadsOnPath(t)
	ctx := context.Background()
	base := datatest.Start(t)
	data := dataclient.New(base, "test-bootstrap")
	enableOrchestrateLinkedSessions(t, data)

	leader := newSession(t, base)
	leaderData := dataclient.New(base, leader.ID)

	reg := provider.NewRegistry()
	fp := fake.New()
	if err := reg.Register(fp); err != nil {
		t.Fatalf("register fake: %v", err)
	}

	r := New(leader.ID, leaderData, reg)
	r.MCPRegistry = testMCPRegistry(t)
	r.RouterURL = "http://localhost:0"
	if err := r.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = r.Shutdown(ctx) })

	assertNoOrchestrateMCPServer(t, leaderData, fp, leader.ID)
}

// TestSetupOrchestrateWithGrant proves flag-on plus a held orchestrate grant
// spawns the real orchestrate-mcp process and advertises it in the
// StartOptions the provider received — the shape a real provider would
// eventually consume (docs/LINKED-SESSIONS-DESIGN.md decomposition item 5).
func TestSetupOrchestrateWithGrant(t *testing.T) {
	putMadsOnPath(t)
	ctx := context.Background()
	base := datatest.Start(t)
	data := dataclient.New(base, "test-bootstrap")
	enableOrchestrateLinkedSessions(t, data)

	leader := newSession(t, base)
	subject := newSession(t, base)
	leaderData := dataclient.New(base, leader.ID)
	if _, err := leaderData.CreateLink(ctx, contracts.LinkRequest{
		GrantingSessionID: leader.ID,
		SubjectSessionID:  subject.ID,
		Preset:            contracts.PresetLeader,
	}); err != nil {
		t.Fatalf("create link: %v", err)
	}

	reg := provider.NewRegistry()
	fp := fake.New()
	if err := reg.Register(fp); err != nil {
		t.Fatalf("register fake: %v", err)
	}

	r := New(leader.ID, leaderData, reg)
	r.MCPRegistry = testMCPRegistry(t)
	r.RouterURL = "http://localhost:9999"
	if err := r.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = r.Shutdown(ctx) })
	t.Cleanup(func() { _ = r.MCPRegistry.Stop(context.Background(), "orchestrate-"+leader.ID) })

	// MCPServers is keyed by the fake's internal ref, but ProviderRef isn't
	// persisted until the provider announces it (fresh session — see
	// syncProviderRef); drive one turn so providerMCPServers' ProviderRef
	// lookup resolves to the ref Start actually used.
	ch, unsubscribe := r.Subscribe()
	if err := r.Send(ctx, "echo hi"); err != nil {
		t.Fatalf("send: %v", err)
	}
	drainText(t, ch)
	unsubscribe()

	refs := providerMCPServers(t, leaderData, fp, leader.ID)
	if len(refs) != 1 {
		t.Fatalf("MCPServers = %+v, want 1 entry", refs)
	}
	got := refs[0]
	if got.Name != "orchestrate-"+leader.ID {
		t.Errorf("Name = %q, want orchestrate-%s", got.Name, leader.ID)
	}
	if got.Command != "mads internal orchestrate-mcp" {
		t.Errorf("Command = %q", got.Command)
	}
	wantEnv := map[string]bool{"MADS_ROUTER_URL=http://localhost:9999": true, "MADS_SESSION_ID=" + leader.ID: true}
	if len(got.Env) != 2 || !wantEnv[got.Env[0]] || !wantEnv[got.Env[1]] {
		t.Errorf("Env = %v, want %v", got.Env, wantEnv)
	}
}

func testMCPRegistry(t *testing.T) *capability.Registry {
	t.Helper()
	return capability.NewRegistry(t.TempDir())
}

func providerMCPServers(t *testing.T, data *dataclient.Client, fp *fake.Fake, sessionID string) []provider.MCPServerRef {
	t.Helper()
	sess, err := data.Session(context.Background(), sessionID)
	if err != nil {
		t.Fatalf("load session: %v", err)
	}
	return fp.MCPServers(sess.ProviderRef)
}

func assertNoOrchestrateMCPServer(t *testing.T, data *dataclient.Client, fp *fake.Fake, sessionID string) {
	t.Helper()
	refs := providerMCPServers(t, data, fp, sessionID)
	if len(refs) != 0 {
		t.Fatalf("MCPServers = %+v, want none", refs)
	}
}
