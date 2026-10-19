package runner

import (
	"context"
	"slices"
	"testing"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/providers/fake"
	"github.com/drewkidwell/mads/providers/provider"
	"github.com/drewkidwell/mads/services/data/datatest"
)

// With a provider session that can change MCP servers live
// (provider.MCPHotSetter, as claude-code's structured transport can), a
// grant activates in place: no provider cycle at all, and removal tears
// down the same way.
func TestRefreshOrchestrateHotSetsWithoutCycling(t *testing.T) {
	putMadsOnPath(t)
	ctx := context.Background()
	base := datatest.Start(t)
	enableOrchestrateLinkedSessions(t, dataclient.New(base, "test-bootstrap"))

	leader := newSession(t, base)
	subject := newSession(t, base)
	data := dataclient.New(base, leader.ID)
	reg := provider.NewRegistry()
	fp := fake.New(fake.WithMCPHotSet())
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
	t.Cleanup(func() { _ = r.MCPRegistry.Stop(context.Background(), orchestrateCapabilityName(leader.ID)) })
	runTurn(t, r, "echo hi")

	link, err := data.CreateLink(ctx, contracts.LinkRequest{
		GrantingSessionID: leader.ID, SubjectSessionID: subject.ID, Preset: contracts.PresetLeader,
	})
	if err != nil {
		t.Fatalf("create link: %v", err)
	}
	startsBefore := len(fp.StartCalls())

	if err := r.RefreshOrchestrate(ctx); err != nil {
		t.Fatalf("RefreshOrchestrate: %v", err)
	}
	waitOrchestrateStatus(t, data, link.ID, contracts.OrchestrateActive)
	if got := len(fp.StartCalls()) - startsBefore; got != 0 {
		t.Fatalf("provider restarts = %d, want 0 (hot set, no cycle)", got)
	}
	if fp.HotSetCalls() != 1 {
		t.Fatalf("hot sets = %d, want 1", fp.HotSetCalls())
	}
	refs := providerMCPServers(t, data, fp, leader.ID)
	if len(refs) != 1 || refs[0].Name != linkToolsServerName {
		t.Fatalf("live MCP servers = %+v, want the orchestrate server", refs)
	}

	// The conversation carries on in the same process.
	runTurn(t, r, "echo still here")

	if err := data.DeleteLink(ctx, link.ID); err != nil {
		t.Fatalf("delete link: %v", err)
	}
	if err := r.RefreshOrchestrate(ctx); err != nil {
		t.Fatalf("RefreshOrchestrate after delete: %v", err)
	}
	if refs := providerMCPServers(t, data, fp, leader.ID); len(refs) != 0 {
		t.Fatalf("live MCP servers after teardown = %+v, want none", refs)
	}
	if got := len(fp.StartCalls()) - startsBefore; got != 0 {
		t.Fatalf("provider restarts after teardown = %d, want 0", got)
	}
}

// A leader started with only read links runs the read-only orchestrate
// server. Upgrading a link to Leader keeps the server's name and drops
// MADS_ORCHESTRATE_READONLY, which a live change can't apply to a
// launch-time server, so the refresh must cycle the provider to mount
// message_session — not report the link active with the read-only tools still
// live (2026-10-19).
func TestRefreshOrchestrateUpgradeFromReadOnlyCycles(t *testing.T) {
	putMadsOnPath(t)
	ctx := context.Background()
	base := datatest.Start(t)
	enableOrchestrateLinkedSessions(t, dataclient.New(base, "test-bootstrap"))

	leader := newSession(t, base)
	subject := newSession(t, base)
	data := dataclient.New(base, leader.ID)
	link, err := data.CreateLink(ctx, contracts.LinkRequest{
		GrantingSessionID: leader.ID, SubjectSessionID: subject.ID, Preset: contracts.PresetMonitor,
	})
	if err != nil {
		t.Fatalf("create link: %v", err)
	}
	reg := provider.NewRegistry()
	fp := fake.New(fake.WithMCPHotSet())
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
	t.Cleanup(func() { _ = r.MCPRegistry.Stop(context.Background(), orchestrateCapabilityName(leader.ID)) })
	runTurn(t, r, "echo hi")
	if refs := providerMCPServers(t, data, fp, leader.ID); len(refs) != 1 || !slices.Contains(refs[0].Env, "MADS_ORCHESTRATE_READONLY=1") {
		t.Fatalf("launch MCP servers = %+v, want the read-only orchestrate server", refs)
	}

	if _, err := data.UpdateLink(ctx, link.ID, contracts.LinkRequest{
		GrantingSessionID: leader.ID, SubjectSessionID: subject.ID, Preset: contracts.PresetLeader,
	}); err != nil {
		t.Fatalf("upgrade link: %v", err)
	}
	startsBefore := len(fp.StartCalls())
	if err := r.RefreshOrchestrate(ctx); err != nil {
		t.Fatalf("RefreshOrchestrate: %v", err)
	}
	waitOrchestrateStatus(t, data, link.ID, contracts.OrchestrateActive)
	if got := len(fp.StartCalls()) - startsBefore; got != 1 {
		t.Fatalf("provider restarts = %d, want 1 (a launch server can't change live)", got)
	}
	refs := providerMCPServers(t, data, fp, leader.ID)
	if len(refs) != 1 || slices.Contains(refs[0].Env, "MADS_ORCHESTRATE_READONLY=1") {
		t.Fatalf("live MCP servers = %+v, want the orchestrate server without READONLY", refs)
	}
	runTurn(t, r, "echo still here")
}
