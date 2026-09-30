package runner

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

// TestSetupOrchestrateWithReadGrantOnlyIsReadOnly proves a session holding
// only the read grant (a monitor) still gets the orchestrate MCP server
// materialized (item 1, "pull tools for read grants"), but signaled into
// read-only mode via MADS_ORCHESTRATE_READONLY — pkg/orchestratemcp's own
// tests (TestReadOnlyToolsListExcludesSendMessage,
// TestReadOnlySendMessageRejected) prove that env var actually excludes/
// rejects send_message; this proves the Runner wires it for the right grant
// shape and not for an orchestrate holder (TestSetupOrchestrateWithGrant's
// 2-entry Env has no such marker).
func TestSetupOrchestrateWithReadGrantOnlyIsReadOnly(t *testing.T) {
	putMadsOnPath(t)
	ctx := context.Background()
	base := datatest.Start(t)
	data := dataclient.New(base, "test-bootstrap")
	enableOrchestrateLinkedSessions(t, data)

	monitor := newSession(t, base)
	subject := newSession(t, base)
	monitorData := dataclient.New(base, monitor.ID)
	if _, err := monitorData.CreateLink(ctx, contracts.LinkRequest{
		GrantingSessionID: monitor.ID,
		SubjectSessionID:  subject.ID,
		Preset:            contracts.PresetMonitor,
	}); err != nil {
		t.Fatalf("create link: %v", err)
	}

	reg := provider.NewRegistry()
	fp := fake.New()
	if err := reg.Register(fp); err != nil {
		t.Fatalf("register fake: %v", err)
	}

	r := New(monitor.ID, monitorData, reg)
	r.MCPRegistry = testMCPRegistry(t)
	r.RouterURL = "http://localhost:9999"
	if err := r.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = r.Shutdown(ctx) })
	t.Cleanup(func() { _ = r.MCPRegistry.Stop(context.Background(), "orchestrate-"+monitor.ID) })

	ch, unsubscribe := r.Subscribe()
	if err := r.Send(ctx, "echo hi"); err != nil {
		t.Fatalf("send: %v", err)
	}
	drainText(t, ch)
	unsubscribe()

	refs := providerMCPServers(t, monitorData, fp, monitor.ID)
	if len(refs) != 1 {
		t.Fatalf("MCPServers = %+v, want 1 entry", refs)
	}
	got := refs[0]
	wantEnv := map[string]bool{
		"MADS_ROUTER_URL=http://localhost:9999": true,
		"MADS_SESSION_ID=" + monitor.ID:         true,
		"MADS_ORCHESTRATE_READONLY=1":           true,
	}
	if len(got.Env) != 3 {
		t.Fatalf("Env = %v, want 3 entries %v", got.Env, wantEnv)
	}
	for _, e := range got.Env {
		if !wantEnv[e] {
			t.Errorf("Env has unexpected entry %q", e)
		}
	}
}

func testMCPRegistry(t *testing.T) *capability.Registry {
	t.Helper()
	return capability.NewRegistry(t.TempDir())
}

// noMCPProvider wraps a fake.Fake but reports BehaviorMCPServers as
// Unsupported, modeling kirocli's declared inability to pass MCP config at
// launch (docs/LINKED-SESSIONS-DESIGN.md: providers that can't do it live
// only ever get the link record's status updated, never a process cycle).
type noMCPProvider struct{ *fake.Fake }

func (noMCPProvider) Matrix() provider.Matrix {
	m := fake.New().Matrix()
	m[provider.BehaviorMCPServers] = provider.Unsupported
	return m
}

// waitOrchestrateStatus polls until the link's OrchestrateStatus matches
// want or the deadline expires — RefreshOrchestrate's swapTransport cycle
// and markOrchestrateLinks both complete asynchronously to the caller of
// RefreshOrchestrate in the mid-turn deferred-apply path.
func waitOrchestrateStatus(t *testing.T, data *dataclient.Client, linkID string, want contracts.OrchestrateStatus) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		link, err := data.Link(context.Background(), linkID)
		if err != nil {
			t.Fatalf("load link: %v", err)
		}
		if link.OrchestrateStatus == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("link %s OrchestrateStatus = %q, want %q", linkID, link.OrchestrateStatus, want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestRefreshOrchestrateMaterializesLive proves the acceptance criterion's
// live-materialization half: a leader link created onto an already-running
// session (with an announced, resumable ref) is picked up by
// RefreshOrchestrate without a user-visible restart — the orchestrate MCP
// server appears in the very next Start call's MCPServers, and the link is
// marked active.
func TestRefreshOrchestrateMaterializesLive(t *testing.T) {
	putMadsOnPath(t)
	ctx := context.Background()
	base := datatest.Start(t)
	data := dataclient.New(base, "test-bootstrap")
	enableOrchestrateLinkedSessions(t, data)

	leader := newSession(t, base)
	subject := newSession(t, base)
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
	t.Cleanup(func() { _ = r.MCPRegistry.Stop(context.Background(), "orchestrate-"+leader.ID) })

	// No link yet at boot: no MCP server should have materialized.
	assertNoOrchestrateMCPServer(t, leaderData, fp, leader.ID)

	// Drive one turn so the fake announces its ref (fakeSession.Ref() reads
	// "" until announced) — this is the "already-running session" precondition:
	// a ref only swapTransport can resume.
	ch, unsubscribe := r.Subscribe()
	if err := r.Send(ctx, "echo hi"); err != nil {
		t.Fatalf("send: %v", err)
	}
	drainText(t, ch)
	unsubscribe()

	link, err := leaderData.CreateLink(ctx, contracts.LinkRequest{
		GrantingSessionID: leader.ID,
		SubjectSessionID:  subject.ID,
		Preset:            contracts.PresetLeader,
	})
	if err != nil {
		t.Fatalf("create link: %v", err)
	}
	if link.OrchestrateStatus != contracts.OrchestratePending {
		t.Fatalf("OrchestrateStatus after create = %q, want %q", link.OrchestrateStatus, contracts.OrchestratePending)
	}

	if err := r.RefreshOrchestrate(ctx); err != nil {
		t.Fatalf("RefreshOrchestrate: %v", err)
	}

	refs := providerMCPServers(t, leaderData, fp, leader.ID)
	if len(refs) != 1 || refs[0].Name != "orchestrate-"+leader.ID {
		t.Fatalf("MCPServers = %+v, want one orchestrate-%s entry", refs, leader.ID)
	}

	got, err := leaderData.Link(ctx, link.ID)
	if err != nil {
		t.Fatalf("load link: %v", err)
	}
	if got.OrchestrateStatus != contracts.OrchestrateActive {
		t.Fatalf("OrchestrateStatus after refresh = %q, want %q", got.OrchestrateStatus, contracts.OrchestrateActive)
	}
}

// TestRefreshOrchestrateNoResumeRefLeavesPendingRestart covers the guard
// that stops RefreshOrchestrate from ever cycling into a brand-new,
// unrelated provider session: a session whose first turn hasn't begun yet
// has no announced ref for swapTransport to resume, so the refresh must
// leave the link's status exactly as the store defaulted it
// (pending_restart) rather than erroring or silently discarding history.
func TestRefreshOrchestrateNoResumeRefLeavesPendingRestart(t *testing.T) {
	putMadsOnPath(t)
	ctx := context.Background()
	base := datatest.Start(t)
	data := dataclient.New(base, "test-bootstrap")
	enableOrchestrateLinkedSessions(t, data)

	leader := newSession(t, base)
	subject := newSession(t, base)
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
	t.Cleanup(func() { _ = r.MCPRegistry.Stop(context.Background(), "orchestrate-"+leader.ID) })

	link, err := leaderData.CreateLink(ctx, contracts.LinkRequest{
		GrantingSessionID: leader.ID,
		SubjectSessionID:  subject.ID,
		Preset:            contracts.PresetLeader,
	})
	if err != nil {
		t.Fatalf("create link: %v", err)
	}

	startsBefore := len(fp.StartCalls())
	if err := r.RefreshOrchestrate(ctx); err != nil {
		t.Fatalf("RefreshOrchestrate: %v", err)
	}
	if got := len(fp.StartCalls()); got != startsBefore {
		t.Fatalf("Start calls = %d, want %d (no cycle should have happened)", got, startsBefore)
	}

	got, err := leaderData.Link(ctx, link.ID)
	if err != nil {
		t.Fatalf("load link: %v", err)
	}
	if got.OrchestrateStatus != contracts.OrchestratePending {
		t.Fatalf("OrchestrateStatus = %q, want %q", got.OrchestrateStatus, contracts.OrchestratePending)
	}
}

// TestRefreshOrchestrateDefersWhileBusy proves a refresh that arrives
// mid-turn doesn't refuse outright (unlike SwitchTransport's ErrSwitchBusy)
// — it queues via orchestrateRefreshPending and onTurnEnd's deferred-apply
// picks it up once the turn actually ends, without the caller (the Router's
// fire-and-forget notification) needing to retry.
func TestRefreshOrchestrateDefersWhileBusy(t *testing.T) {
	putMadsOnPath(t)
	ctx := context.Background()
	base := datatest.Start(t)
	data := dataclient.New(base, "test-bootstrap")
	enableOrchestrateLinkedSessions(t, data)

	leader := newSession(t, base)
	subject := newSession(t, base)
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
	t.Cleanup(func() { _ = r.MCPRegistry.Stop(context.Background(), "orchestrate-"+leader.ID) })

	// Drive one full turn first so the ref is announced (see
	// TestRefreshOrchestrateMaterializesLive) before starting the slow one.
	ch, unsubscribe := r.Subscribe()
	if err := r.Send(ctx, "echo hi"); err != nil {
		t.Fatalf("send: %v", err)
	}
	drainText(t, ch)

	link, err := leaderData.CreateLink(ctx, contracts.LinkRequest{
		GrantingSessionID: leader.ID,
		SubjectSessionID:  subject.ID,
		Preset:            contracts.PresetLeader,
	})
	if err != nil {
		t.Fatalf("create link: %v", err)
	}

	if err := r.Send(ctx, "slow"); err != nil {
		t.Fatalf("send slow: %v", err)
	}

	if err := r.RefreshOrchestrate(ctx); err != nil {
		t.Fatalf("RefreshOrchestrate while busy: %v", err)
	}
	// Deferred, not applied yet: no swap should have happened while busy.
	refs := providerMCPServers(t, leaderData, fp, leader.ID)
	if len(refs) != 0 {
		t.Fatalf("MCPServers while busy = %+v, want none (refresh must defer, not apply mid-turn)", refs)
	}

	if err := r.Interrupt(ctx); err != nil {
		t.Fatalf("interrupt: %v", err)
	}
	// Drain until the interrupted "slow" turn's own turn_end so onTurnEnd's
	// deferred-apply runs.
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				t.Fatal("event channel closed before turn_end")
			}
			if ev.Kind == provider.EventTurnEnd {
				goto drained
			}
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for turn_end after interrupt")
		}
	}
drained:
	unsubscribe()

	waitOrchestrateStatus(t, leaderData, link.ID, contracts.OrchestrateActive)
	refs = providerMCPServers(t, leaderData, fp, leader.ID)
	if len(refs) != 1 || refs[0].Name != "orchestrate-"+leader.ID {
		t.Fatalf("MCPServers after deferred apply = %+v, want one orchestrate-%s entry", refs, leader.ID)
	}
}

// TestRefreshOrchestrateUnsupportedProviderDegradesLinkStatus covers the
// task's "otherwise an explicit takes-effect-on-restart state" half for a
// provider that can never do this live (kirocli declares MCP unsupported):
// the link is marked unsupported (terminal — restarting never helps) and no
// process cycle is attempted at all.
func TestRefreshOrchestrateUnsupportedProviderDegradesLinkStatus(t *testing.T) {
	putMadsOnPath(t)
	ctx := context.Background()
	base := datatest.Start(t)
	data := dataclient.New(base, "test-bootstrap")
	enableOrchestrateLinkedSessions(t, data)

	leader := newSession(t, base)
	subject := newSession(t, base)
	leaderData := dataclient.New(base, leader.ID)

	reg := provider.NewRegistry()
	fp := noMCPProvider{fake.New()}
	if err := reg.Register(fp); err != nil {
		t.Fatalf("register no-mcp fake: %v", err)
	}

	r := New(leader.ID, leaderData, reg)
	r.MCPRegistry = testMCPRegistry(t)
	r.RouterURL = "http://localhost:0"
	if err := r.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = r.Shutdown(ctx) })
	t.Cleanup(func() { _ = r.MCPRegistry.Stop(context.Background(), "orchestrate-"+leader.ID) })

	ch, unsubscribe := r.Subscribe()
	if err := r.Send(ctx, "echo hi"); err != nil {
		t.Fatalf("send: %v", err)
	}
	drainText(t, ch)
	unsubscribe()

	link, err := leaderData.CreateLink(ctx, contracts.LinkRequest{
		GrantingSessionID: leader.ID,
		SubjectSessionID:  subject.ID,
		Preset:            contracts.PresetLeader,
	})
	if err != nil {
		t.Fatalf("create link: %v", err)
	}

	startsBefore := len(fp.StartCalls())
	if err := r.RefreshOrchestrate(ctx); err != nil {
		t.Fatalf("RefreshOrchestrate: %v", err)
	}
	if got := len(fp.StartCalls()); got != startsBefore {
		t.Fatalf("Start calls = %d, want %d (unsupported provider must never cycle)", got, startsBefore)
	}

	got, err := leaderData.Link(ctx, link.ID)
	if err != nil {
		t.Fatalf("load link: %v", err)
	}
	if got.OrchestrateStatus != contracts.OrchestrateUnsupported {
		t.Fatalf("OrchestrateStatus = %q, want %q", got.OrchestrateStatus, contracts.OrchestrateUnsupported)
	}
}

// TestRefreshOrchestrateDowngradesToReadOnlyOnGrantRemoval covers a leader
// link downgraded to a monitor link (orchestrate grant dropped, read grant
// kept): since item 1 ("pull tools for read grants") now materializes the
// orchestrate MCP server for a read grant too, this no longer tears the
// server down — it cycles it into read-only mode (MADS_ORCHESTRATE_READONLY)
// live, without needing a restart.
func TestRefreshOrchestrateDowngradesToReadOnlyOnGrantRemoval(t *testing.T) {
	putMadsOnPath(t)
	ctx := context.Background()
	base := datatest.Start(t)
	data := dataclient.New(base, "test-bootstrap")
	enableOrchestrateLinkedSessions(t, data)

	leader := newSession(t, base)
	subject := newSession(t, base)
	leaderData := dataclient.New(base, leader.ID)

	reg := provider.NewRegistry()
	fp := fake.New()
	if err := reg.Register(fp); err != nil {
		t.Fatalf("register fake: %v", err)
	}

	link, err := leaderData.CreateLink(ctx, contracts.LinkRequest{
		GrantingSessionID: leader.ID,
		SubjectSessionID:  subject.ID,
		Preset:            contracts.PresetLeader,
	})
	if err != nil {
		t.Fatalf("create link: %v", err)
	}

	r := New(leader.ID, leaderData, reg)
	r.MCPRegistry = testMCPRegistry(t)
	r.RouterURL = "http://localhost:0"
	if err := r.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = r.Shutdown(ctx) })
	t.Cleanup(func() { _ = r.MCPRegistry.Stop(context.Background(), "orchestrate-"+leader.ID) })

	// Grant held at boot: materialized immediately (TestSetupOrchestrateWithGrant).
	ch, unsubscribe := r.Subscribe()
	if err := r.Send(ctx, "echo hi"); err != nil {
		t.Fatalf("send: %v", err)
	}
	drainText(t, ch)
	unsubscribe()
	if refs := providerMCPServers(t, leaderData, fp, leader.ID); len(refs) != 1 {
		t.Fatalf("MCPServers before downgrade = %+v, want 1 entry", refs)
	}

	if _, err := leaderData.UpdateLink(ctx, link.ID, contracts.LinkRequest{
		GrantingSessionID: leader.ID,
		SubjectSessionID:  subject.ID,
		Preset:            contracts.PresetMonitor,
	}); err != nil {
		t.Fatalf("update link: %v", err)
	}

	if err := r.RefreshOrchestrate(ctx); err != nil {
		t.Fatalf("RefreshOrchestrate: %v", err)
	}

	refs := providerMCPServers(t, leaderData, fp, leader.ID)
	if len(refs) != 1 {
		t.Fatalf("MCPServers after downgrade = %+v, want 1 entry (still materialized, now read-only)", refs)
	}
	foundReadOnly := false
	for _, e := range refs[0].Env {
		if e == "MADS_ORCHESTRATE_READONLY=1" {
			foundReadOnly = true
		}
	}
	if !foundReadOnly {
		t.Fatalf("Env = %v, want MADS_ORCHESTRATE_READONLY=1 after downgrade to monitor", refs[0].Env)
	}
	if !r.MCPRegistry.Running("orchestrate-" + leader.ID) {
		t.Fatal("orchestrate-mcp capability process not running after downgrade to read-only")
	}
}

// TestRefreshOrchestrateTeardownOnLinkDeletion covers the task's "wire
// teardown symmetrically for link removal" requirement: deleting the only
// link (dropping every grant, not just downgrading) cycles the live MCP set
// back down to empty and stops the local orchestrate-mcp capability
// process, without needing a restart.
func TestRefreshOrchestrateTeardownOnLinkDeletion(t *testing.T) {
	putMadsOnPath(t)
	ctx := context.Background()
	base := datatest.Start(t)
	data := dataclient.New(base, "test-bootstrap")
	enableOrchestrateLinkedSessions(t, data)

	leader := newSession(t, base)
	subject := newSession(t, base)
	leaderData := dataclient.New(base, leader.ID)

	reg := provider.NewRegistry()
	fp := fake.New()
	if err := reg.Register(fp); err != nil {
		t.Fatalf("register fake: %v", err)
	}

	link, err := leaderData.CreateLink(ctx, contracts.LinkRequest{
		GrantingSessionID: leader.ID,
		SubjectSessionID:  subject.ID,
		Preset:            contracts.PresetLeader,
	})
	if err != nil {
		t.Fatalf("create link: %v", err)
	}

	r := New(leader.ID, leaderData, reg)
	r.MCPRegistry = testMCPRegistry(t)
	r.RouterURL = "http://localhost:0"
	if err := r.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = r.Shutdown(ctx) })
	t.Cleanup(func() { _ = r.MCPRegistry.Stop(context.Background(), "orchestrate-"+leader.ID) })

	// Grant held at boot: materialized immediately (TestSetupOrchestrateWithGrant).
	ch, unsubscribe := r.Subscribe()
	if err := r.Send(ctx, "echo hi"); err != nil {
		t.Fatalf("send: %v", err)
	}
	drainText(t, ch)
	unsubscribe()
	if refs := providerMCPServers(t, leaderData, fp, leader.ID); len(refs) != 1 {
		t.Fatalf("MCPServers before removal = %+v, want 1 entry", refs)
	}

	if err := leaderData.DeleteLink(ctx, link.ID); err != nil {
		t.Fatalf("delete link: %v", err)
	}

	if err := r.RefreshOrchestrate(ctx); err != nil {
		t.Fatalf("RefreshOrchestrate: %v", err)
	}

	assertNoOrchestrateMCPServer(t, leaderData, fp, leader.ID)
	if r.MCPRegistry.Running("orchestrate-" + leader.ID) {
		t.Fatal("orchestrate-mcp capability process still running after link deletion")
	}
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

// startLeaderWithTurn starts a leader runner with the fake provider and
// drives one turn so it has a ref a live refresh could resume.
func startLeaderWithTurn(t *testing.T, madsCmd string) (r *Runner, fp *fake.Fake, leaderData *dataclient.Client, leaderID, subjectID string) {
	t.Helper()
	ctx := context.Background()
	base := datatest.Start(t)
	enableOrchestrateLinkedSessions(t, dataclient.New(base, "test-bootstrap"))
	leader := newSession(t, base)
	subject := newSession(t, base)
	leaderData = dataclient.New(base, leader.ID)

	reg := provider.NewRegistry()
	fp = fake.New()
	if err := reg.Register(fp); err != nil {
		t.Fatalf("register fake: %v", err)
	}
	r = New(leader.ID, leaderData, reg)
	r.MCPRegistry = testMCPRegistry(t)
	r.RouterURL = "http://localhost:0"
	r.MadsCmd = madsCmd
	if err := r.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = r.Shutdown(ctx) })
	t.Cleanup(func() { _ = r.MCPRegistry.Stop(context.Background(), "orchestrate-"+leader.ID) })

	ch, unsubscribe := r.Subscribe()
	if err := r.Send(ctx, "echo hi"); err != nil {
		t.Fatalf("send: %v", err)
	}
	drainText(t, ch)
	unsubscribe()
	return r, fp, leaderData, leader.ID, subject.ID
}

// TestRefreshOrchestrateUsesMadsCmdWithoutPath: with mads absent from PATH
// (the repo-launched Router's situation), an explicit MadsCmd still starts
// the tools and is what the provider is handed.
func TestRefreshOrchestrateUsesMadsCmdWithoutPath(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // nothing named mads on it
	madsPath := filepath.Join(madsBinDir, "mads")
	ctx := context.Background()
	r, fp, leaderData, leaderID, subjectID := startLeaderWithTurn(t, madsPath)

	link, err := leaderData.CreateLink(ctx, contracts.LinkRequest{
		GrantingSessionID: leaderID, SubjectSessionID: subjectID, Preset: contracts.PresetLeader,
	})
	if err != nil {
		t.Fatalf("create link: %v", err)
	}
	if err := r.RefreshOrchestrate(ctx); err != nil {
		t.Fatalf("RefreshOrchestrate: %v", err)
	}
	refs := providerMCPServers(t, leaderData, fp, leaderID)
	if len(refs) != 1 || refs[0].Command != madsPath+" internal orchestrate-mcp" {
		t.Fatalf("MCPServers = %+v, want the orchestrate server launched via %s", refs, madsPath)
	}
	if got, _ := leaderData.Link(ctx, link.ID); got.OrchestrateStatus != contracts.OrchestrateActive {
		t.Fatalf("status = %q, want active", got.OrchestrateStatus)
	}
}

// TestRefreshOrchestrateFailedStartMarksLink: the bug on a real machine —
// the tools can't start, and the link used to sit in pending_restart
// forever. It now reads failed with the reason, and the provider process is
// left alone rather than cycled into the same missing tools.
func TestRefreshOrchestrateFailedStartMarksLink(t *testing.T) {
	ctx := context.Background()
	missing := filepath.Join(t.TempDir(), "no-such-mads")
	r, fp, leaderData, leaderID, subjectID := startLeaderWithTurn(t, missing)
	startsBefore := len(fp.StartCalls())

	link, err := leaderData.CreateLink(ctx, contracts.LinkRequest{
		GrantingSessionID: leaderID, SubjectSessionID: subjectID, Preset: contracts.PresetLeader,
	})
	if err != nil {
		t.Fatalf("create link: %v", err)
	}
	if err := r.RefreshOrchestrate(ctx); err != nil {
		t.Fatalf("RefreshOrchestrate: %v", err)
	}

	got, err := leaderData.Link(ctx, link.ID)
	if err != nil {
		t.Fatalf("load link: %v", err)
	}
	if got.OrchestrateStatus != contracts.OrchestrateFailed {
		t.Fatalf("status = %q, want failed", got.OrchestrateStatus)
	}
	if !strings.Contains(got.OrchestrateError, missing) {
		t.Errorf("error = %q, want it to name the command that failed", got.OrchestrateError)
	}
	if n := len(fp.StartCalls()); n != startsBefore {
		t.Errorf("provider started %d more time(s); a failed tool start must not cycle it", n-startsBefore)
	}
	assertNoOrchestrateMCPServer(t, leaderData, fp, leaderID)
}
