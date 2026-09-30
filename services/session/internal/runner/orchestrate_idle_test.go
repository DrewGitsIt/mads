package runner

import (
	"context"
	"testing"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/providers/fake"
	"github.com/drewkidwell/mads/providers/provider"
	"github.com/drewkidwell/mads/services/data/datatest"
)

// startOrchestrateLeader starts a runner on a fresh leader session over the
// fake provider, with linked sessions on. Returns the runner, the fake, the
// leader's data client, and the base URL.
func startOrchestrateLeader(t *testing.T) (*Runner, *fake.Fake, *dataclient.Client, contracts.Session, string) {
	t.Helper()
	putMadsOnPath(t)
	ctx := context.Background()
	base := datatest.Start(t)
	enableOrchestrateLinkedSessions(t, dataclient.New(base, "test-bootstrap"))

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
	t.Cleanup(func() { _ = r.MCPRegistry.Stop(context.Background(), "orchestrate-"+leader.ID) })
	return r, fp, leaderData, leader, base
}

func runTurn(t *testing.T, r *Runner, text string) {
	t.Helper()
	ch, unsubscribe := r.Subscribe()
	defer unsubscribe()
	if err := r.Send(context.Background(), text); err != nil {
		t.Fatalf("send: %v", err)
	}
	drainText(t, ch)
}

func mkLeaderLink(t *testing.T, data *dataclient.Client, base, leaderID string) contracts.Link {
	t.Helper()
	subject := newSession(t, base)
	link, err := data.CreateLink(context.Background(), contracts.LinkRequest{
		GrantingSessionID: leaderID, SubjectSessionID: subject.ID, Preset: contracts.PresetLeader,
	})
	if err != nil {
		t.Fatalf("create link: %v", err)
	}
	return link
}

// A refresh refused for lack of a resumable ref (nothing said yet) isn't
// dropped: it re-arms, and the first turn's end mounts the tools.
func TestOrchestrateNoResumeRefActivatesAfterFirstTurn(t *testing.T) {
	r, fp, data, leader, base := startOrchestrateLeader(t)
	link := mkLeaderLink(t, data, base, leader.ID)

	if err := r.RefreshOrchestrate(context.Background()); err != nil {
		t.Fatalf("RefreshOrchestrate: %v", err)
	}
	waitOrchestrateStatus(t, data, link.ID, contracts.OrchestratePending)

	runTurn(t, r, "echo hi")
	waitOrchestrateStatus(t, data, link.ID, contracts.OrchestrateActive)
	if refs := providerMCPServers(t, data, fp, leader.ID); len(refs) != 1 {
		t.Fatalf("MCPServers = %+v, want the orchestrate server", refs)
	}
}

// A pending grant nobody told the runner about (the Router's refresh notify
// was lost) still activates at the next quiet turn boundary, on the same
// conversation.
func TestOrchestratePendingGrantActivatesWhenIdleWithoutNotify(t *testing.T) {
	r, fp, data, leader, base := startOrchestrateLeader(t)
	runTurn(t, r, "echo hi") // ref announced
	link := mkLeaderLink(t, data, base, leader.ID)
	sess, err := data.Session(context.Background(), leader.ID)
	if err != nil {
		t.Fatal(err)
	}
	refBefore := sess.ProviderRef

	// No RefreshOrchestrate call: only the turn boundary can pick it up.
	runTurn(t, r, "echo again")
	waitOrchestrateStatus(t, data, link.ID, contracts.OrchestrateActive)

	starts := fp.StartCalls()
	if got := starts[len(starts)-1].Resume; got == "" || got != refBefore {
		t.Errorf("cycle resumed %q, want the existing conversation %q", got, refBefore)
	}
}

// Several pending links activate in one provider cycle, not one each.
func TestOrchestrateSeveralPendingLinksCycleOnce(t *testing.T) {
	r, fp, data, leader, base := startOrchestrateLeader(t)
	runTurn(t, r, "echo hi")
	a := mkLeaderLink(t, data, base, leader.ID)
	b := mkLeaderLink(t, data, base, leader.ID)
	startsBefore := len(fp.StartCalls())

	runTurn(t, r, "echo again")
	waitOrchestrateStatus(t, data, a.ID, contracts.OrchestrateActive)
	waitOrchestrateStatus(t, data, b.ID, contracts.OrchestrateActive)

	// Another quiet boundary with nothing pending must not cycle again.
	runTurn(t, r, "echo once more")
	if got := len(fp.StartCalls()) - startsBefore; got != 1 {
		t.Fatalf("provider cycles = %d, want exactly 1 for two pending links", got)
	}
}
