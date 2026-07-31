package agentselect_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/drewkidwell/mads/pkg/agentselect"
	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/services/data/datatest"
)

// setup starts a real Data Service and a Selector against it (never a mock —
// project convention). It pre-registers every MCP capability name the tests
// in this file reference, since agent create/update now rejects any
// capabilities.mcp_servers entry absent from the capability registry.
func setup(t *testing.T) (*agentselect.Selector, *dataclient.Client) {
	t.Helper()
	base := datatest.Start(t)
	c := dataclient.New(base, "selector")
	for _, name := range []string{"acme-tool", "shared-tool", "unique-tool"} {
		mustCreateCapability(t, c, name)
	}
	return agentselect.New(c), c
}

func mustCreateCapability(t *testing.T, c *dataclient.Client, name string) contracts.Capability {
	t.Helper()
	cap := contracts.Capability{Name: name, Type: contracts.CapabilityMCP, Config: contracts.CapabilityConfig{Command: "true"}}
	if err := c.CreateCapability(context.Background(), &cap); err != nil {
		t.Fatalf("create capability %s: %v", name, err)
	}
	return cap
}

func mustCreateAgent(t *testing.T, c *dataclient.Client, a contracts.Agent) contracts.Agent {
	t.Helper()
	if err := c.CreateAgent(context.Background(), &a); err != nil {
		t.Fatalf("create agent %s: %v", a.Name, err)
	}
	return a
}

// acmeMCP excludes every seeded system agent (none declare any MCP server),
// isolating tests to the custom candidates they create.
var acmeMCP = []string{"acme-tool"}

func TestSelectCapabilityFilter(t *testing.T) {
	sel, c := setup(t)
	ctx := context.Background()

	mustCreateAgent(t, c, contracts.Agent{
		Name: "candidate-a", Prompt: "p", Quadrant: contracts.QuadrantAutonomousExecutor,
		Tags: []string{"payment", "gateway"}, // higher affinity, but lacks the required MCP server
	})
	mustCreateAgent(t, c, contracts.Agent{
		Name: "candidate-b", Prompt: "p", Quadrant: contracts.QuadrantAutonomousExecutor,
		Capabilities: contracts.AgentCapabilities{MCPServers: acmeMCP},
	})

	req := agentselect.Request{
		TaskID: "tsk_1", Title: "Implement the payment gateway integration",
		Labels:             []string{"payment", "gateway"},
		RequiredMCPServers: acmeMCP,
	}

	for i := 0; i < 2; i++ {
		got, err := sel.Select(ctx, req)
		if err != nil {
			t.Fatalf("Select: %v", err)
		}
		if got.Agent != "candidate-b" {
			t.Fatalf("run %d: got %q, want candidate-b", i, got.Agent)
		}
	}

	got, _ := sel.Select(ctx, req)
	for _, cb := range got.Candidates {
		if cb.Agent == "candidate-a" && cb.Eliminated != "capability (missing acme-tool)" {
			t.Fatalf("candidate-a: got eliminated=%q, want capability (missing acme-tool)", cb.Eliminated)
		}
	}
}

// TestSelectExtraneousCapabilities proves the least-extraneous-capabilities
// tier actually decides an outcome — placed ahead of track record, so an
// established kitchen-sink generalist's history can't outrank a leaner,
// equally-relevant candidate (owner's explicit condition for accepting
// additive-only capability composition).
func TestSelectExtraneousCapabilities(t *testing.T) {
	sel, c := setup(t)
	ctx := context.Background()

	// candidate-lean declares only the required capability.
	mustCreateAgent(t, c, contracts.Agent{
		Name: "candidate-lean", Prompt: "p", Quadrant: contracts.QuadrantAutonomousExecutor,
		Tags:         []string{"payment"},
		Capabilities: contracts.AgentCapabilities{MCPServers: acmeMCP},
	})
	// candidate-kitchen-sink declares the required capability plus two extras,
	// and has a perfect track record — which must NOT be enough to win.
	mustCreateAgent(t, c, contracts.Agent{
		Name: "candidate-kitchen-sink", Prompt: "p", Quadrant: contracts.QuadrantAutonomousExecutor,
		Tags:         []string{"payment"},
		Capabilities: contracts.AgentCapabilities{MCPServers: []string{"acme-tool", "shared-tool", "unique-tool"}},
	})

	labels := []string{"payment"}
	for _, ok := range []bool{true, true, true} {
		if err := sel.RecordOutcome(ctx, "tsk_history", "", "s", "candidate-kitchen-sink", labels, ok); err != nil {
			t.Fatalf("seed kitchen-sink: %v", err)
		}
	}

	req := agentselect.Request{
		TaskID: "tsk_new", Title: "Implement the payment retry logic",
		Labels:             labels,
		RequiredMCPServers: acmeMCP,
	}

	got, err := sel.Select(ctx, req)
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if got.Agent != "candidate-lean" {
		t.Fatalf("got %q, want candidate-lean (fewer extraneous capabilities beats a better track record)", got.Agent)
	}

	for _, cb := range got.Candidates {
		switch cb.Agent {
		case "candidate-lean":
			if cb.Extraneous != 0 {
				t.Fatalf("candidate-lean Extraneous = %d, want 0", cb.Extraneous)
			}
		case "candidate-kitchen-sink":
			if cb.Extraneous != 2 {
				t.Fatalf("candidate-kitchen-sink Extraneous = %d, want 2", cb.Extraneous)
			}
		}
	}
}

func TestSelectQuadrantFilter(t *testing.T) {
	sel, c := setup(t)
	ctx := context.Background()

	mustCreateAgent(t, c, contracts.Agent{
		Name: "candidate-exec", Prompt: "p", Quadrant: contracts.QuadrantAutonomousExecutor,
		Tags: []string{"login", "auth", "rate", "limiter"},
	})
	mustCreateAgent(t, c, contracts.Agent{
		Name: "candidate-investigator", Prompt: "p", Quadrant: contracts.QuadrantInvestigator,
		Tags: []string{"login", "flow"},
	})

	directing := agentselect.Request{
		TaskID:      "tsk_directing",
		Title:       "Implement the login rate limiter",
		Description: "in auth/limiter.go exactly as specified",
	}
	exploring := agentselect.Request{
		TaskID:      "tsk_exploring",
		Title:       "Not sure why the login flow intermittently fails",
		Description: "can you look into it?",
	}

	got, err := sel.Select(ctx, directing)
	if err != nil {
		t.Fatalf("Select(directing): %v", err)
	}
	if got.Quadrant != contracts.QuadrantAutonomousExecutor {
		t.Fatalf("directing quadrant = %q, want autonomous_executor", got.Quadrant)
	}
	if got.Agent != "candidate-exec" {
		t.Fatalf("directing agent = %q, want candidate-exec", got.Agent)
	}

	got2, err := sel.Select(ctx, exploring)
	if err != nil {
		t.Fatalf("Select(exploring): %v", err)
	}
	if got2.Quadrant != contracts.QuadrantInvestigator {
		t.Fatalf("exploring quadrant = %q, want investigator", got2.Quadrant)
	}
	if got2.Agent != "candidate-investigator" {
		t.Fatalf("exploring agent = %q, want candidate-investigator", got2.Agent)
	}
}

func TestSelectFailureExclusion(t *testing.T) {
	sel, c := setup(t)
	ctx := context.Background()

	mustCreateAgent(t, c, contracts.Agent{
		Name: "candidate-a", Prompt: "p", Quadrant: contracts.QuadrantAutonomousExecutor,
		Tags:         []string{"payment", "gateway"},
		Capabilities: contracts.AgentCapabilities{MCPServers: acmeMCP},
	})
	mustCreateAgent(t, c, contracts.Agent{
		Name: "candidate-b", Prompt: "p", Quadrant: contracts.QuadrantAutonomousExecutor,
		Capabilities: contracts.AgentCapabilities{MCPServers: acmeMCP},
	})

	req := agentselect.Request{
		TaskID: "tsk_1", SessionID: "ses_1", Title: "Implement the payment gateway integration",
		Labels:             []string{"payment", "gateway"},
		RequiredMCPServers: acmeMCP,
	}

	// Without any failure recorded, the label-affinity winner is candidate-a.
	got, err := sel.Select(ctx, req)
	if err != nil || got.Agent != "candidate-a" {
		t.Fatalf("pre-failure select: %+v, %v", got, err)
	}

	if err := sel.RecordOutcome(ctx, req.TaskID, req.SessionID, "implement", "candidate-a", req.Labels, false); err != nil {
		t.Fatalf("RecordOutcome: %v", err)
	}

	for i := 0; i < 2; i++ {
		got, err := sel.Select(ctx, req)
		if err != nil {
			t.Fatalf("run %d: Select: %v", i, err)
		}
		if got.Agent != "candidate-b" {
			t.Fatalf("run %d: got %q, want candidate-b", i, got.Agent)
		}
		var found bool
		for _, cb := range got.Candidates {
			if cb.Agent == "candidate-a" {
				found = true
				if cb.Eliminated != "failure" {
					t.Fatalf("candidate-a eliminated = %q, want failure", cb.Eliminated)
				}
			}
		}
		if !found {
			t.Fatal("candidate-a missing from candidates")
		}
	}
}

func TestSelectLabelAffinity(t *testing.T) {
	sel, c := setup(t)
	ctx := context.Background()

	mustCreateAgent(t, c, contracts.Agent{
		Name: "candidate-high", Prompt: "p", Quadrant: contracts.QuadrantAutonomousExecutor,
		Tags:         []string{"payment", "gateway", "stripe"},
		Capabilities: contracts.AgentCapabilities{MCPServers: acmeMCP},
	})
	mustCreateAgent(t, c, contracts.Agent{
		Name: "candidate-low", Prompt: "p", Quadrant: contracts.QuadrantAutonomousExecutor,
		Tags:         []string{"payment"},
		Capabilities: contracts.AgentCapabilities{MCPServers: acmeMCP},
	})

	req := agentselect.Request{
		TaskID: "tsk_1", Title: "Implement the payment gateway integration with stripe",
		Labels:             []string{"payment", "gateway", "stripe"},
		RequiredMCPServers: acmeMCP,
	}

	for i := 0; i < 2; i++ {
		got, err := sel.Select(ctx, req)
		if err != nil {
			t.Fatalf("run %d: Select: %v", i, err)
		}
		if got.Agent != "candidate-high" {
			t.Fatalf("run %d: got %q, want candidate-high", i, got.Agent)
		}
	}
}

func TestSelectTrackRecord(t *testing.T) {
	sel, c := setup(t)
	ctx := context.Background()

	mustCreateAgent(t, c, contracts.Agent{
		Name: "candidate-a", Prompt: "p", Quadrant: contracts.QuadrantAutonomousExecutor,
		Tags:         []string{"payment"},
		Capabilities: contracts.AgentCapabilities{MCPServers: acmeMCP},
	})
	mustCreateAgent(t, c, contracts.Agent{
		Name: "candidate-b", Prompt: "p", Quadrant: contracts.QuadrantAutonomousExecutor,
		Tags:         []string{"payment"},
		Capabilities: contracts.AgentCapabilities{MCPServers: acmeMCP},
	})

	labels := []string{"payment"}
	// candidate-a: 1/3 success. candidate-b: 3/3 success.
	for i, ok := range []bool{true, false, false} {
		if err := sel.RecordOutcome(ctx, "tsk_history", "", "s", "candidate-a", labels, ok); err != nil {
			t.Fatalf("seed a #%d: %v", i, err)
		}
	}
	for i, ok := range []bool{true, true, true} {
		if err := sel.RecordOutcome(ctx, "tsk_history", "", "s", "candidate-b", labels, ok); err != nil {
			t.Fatalf("seed b #%d: %v", i, err)
		}
	}

	req := agentselect.Request{
		TaskID: "tsk_new", Title: "Implement the payment retry logic",
		Labels:             labels,
		RequiredMCPServers: acmeMCP,
	}

	for i := 0; i < 2; i++ {
		got, err := sel.Select(ctx, req)
		if err != nil {
			t.Fatalf("run %d: Select: %v", i, err)
		}
		if got.Agent != "candidate-b" {
			t.Fatalf("run %d: got %q, want candidate-b", i, got.Agent)
		}
	}
}

func TestSelectColdStart(t *testing.T) {
	sel, c := setup(t)
	ctx := context.Background()

	mustCreateAgent(t, c, contracts.Agent{
		Name: "candidate-cold", Prompt: "p", Quadrant: contracts.QuadrantAutonomousExecutor,
		Tags:         []string{"payment"},
		Capabilities: contracts.AgentCapabilities{MCPServers: acmeMCP},
	})
	mustCreateAgent(t, c, contracts.Agent{
		Name: "candidate-poor", Prompt: "p", Quadrant: contracts.QuadrantAutonomousExecutor,
		Tags:         []string{"payment"},
		Capabilities: contracts.AgentCapabilities{MCPServers: acmeMCP},
	})

	labels := []string{"payment"}
	// candidate-poor: 1/3 success = 0.33, worse than the 0.5 cold-start default.
	for _, ok := range []bool{true, false, false} {
		if err := sel.RecordOutcome(ctx, "tsk_history", "", "s", "candidate-poor", labels, ok); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	req := agentselect.Request{
		TaskID: "tsk_new", Title: "Implement the payment retry logic",
		Labels:             labels,
		RequiredMCPServers: acmeMCP,
	}

	got, err := sel.Select(ctx, req)
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if got.Agent != "candidate-cold" {
		t.Fatalf("got %q, want candidate-cold (never-recorded agent beats a worse track record)", got.Agent)
	}
}

func TestSelectRecencyTiebreak(t *testing.T) {
	sel, c := setup(t)
	ctx := context.Background()

	// candidate-recent additionally declares a "widget" tag so a throwaway
	// request can single it out via label affinity; both candidates declare
	// the identical capability set ("shared-tool" only), so they also tie on
	// extraneous-capability count — recency is the only tier left to decide.
	mustCreateAgent(t, c, contracts.Agent{
		Name: "candidate-recent", Prompt: "p", Quadrant: contracts.QuadrantAutonomousExecutor,
		Tags:         []string{"widget"},
		Capabilities: contracts.AgentCapabilities{MCPServers: []string{"shared-tool"}},
	})
	mustCreateAgent(t, c, contracts.Agent{
		Name: "candidate-never", Prompt: "p", Quadrant: contracts.QuadrantAutonomousExecutor,
		Capabilities: contracts.AgentCapabilities{MCPServers: []string{"shared-tool"}},
	})

	throwaway := agentselect.Request{
		TaskID: "tsk_throwaway", Title: "Implement the widget feature",
		Labels:             []string{"widget"},
		RequiredMCPServers: []string{"shared-tool"},
	}
	if got, err := sel.Select(ctx, throwaway); err != nil || got.Agent != "candidate-recent" {
		t.Fatalf("throwaway select: %+v, %v", got, err)
	}

	// Head-to-head: tied on label affinity (both zero), extraneous count
	// (both zero), and track record (both cold-start, 0.5) — recency must
	// decide.
	req := agentselect.Request{
		TaskID: "tsk_real", Title: "Implement the reporting export",
		RequiredMCPServers: []string{"shared-tool"},
	}
	got, err := sel.Select(ctx, req)
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if got.Agent != "candidate-recent" {
		t.Fatalf("got %q, want candidate-recent (more recently selected)", got.Agent)
	}
}

func TestSelectAlphabeticalFloor(t *testing.T) {
	sel, c := setup(t)
	ctx := context.Background()

	mustCreateAgent(t, c, contracts.Agent{
		Name: "zeta-agent", Prompt: "p", Quadrant: contracts.QuadrantAutonomousExecutor,
		Capabilities: contracts.AgentCapabilities{MCPServers: acmeMCP},
	})
	mustCreateAgent(t, c, contracts.Agent{
		Name: "alpha-agent", Prompt: "p", Quadrant: contracts.QuadrantAutonomousExecutor,
		Capabilities: contracts.AgentCapabilities{MCPServers: acmeMCP},
	})

	req := agentselect.Request{
		TaskID: "tsk_1", Title: "Implement the widget feature",
		RequiredMCPServers: acmeMCP,
	}

	for i := 0; i < 2; i++ {
		got, err := sel.Select(ctx, req)
		if err != nil {
			t.Fatalf("run %d: Select: %v", i, err)
		}
		if got.Agent != "alpha-agent" {
			t.Fatalf("run %d: got %q, want alpha-agent", i, got.Agent)
		}
	}
}

func TestSelectNoCandidates(t *testing.T) {
	sel, c := setup(t)
	ctx := context.Background()

	req := agentselect.Request{
		TaskID: "tsk_1", SessionID: "ses_1", Title: "Implement the widget feature",
		RequiredMCPServers: []string{"nonexistent-xyz"},
	}

	got, err := sel.Select(ctx, req)
	if !errors.Is(err, agentselect.ErrNoCandidates) {
		t.Fatalf("err = %v, want ErrNoCandidates", err)
	}
	if got.Agent != "" {
		t.Fatalf("Agent = %q, want empty", got.Agent)
	}
	for _, cb := range got.Candidates {
		if cb.Eliminated == "" {
			t.Fatalf("candidate %q not eliminated", cb.Agent)
		}
	}

	evs, err := c.Events(ctx, 0, req.SessionID, []string{"agent.selected"})
	if err != nil || len(evs) != 1 {
		t.Fatalf("events: %d, %v", len(evs), err)
	}
	var logged agentselect.Selection
	if err := json.Unmarshal(evs[0].Payload, &logged); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if logged.Agent != "" {
		t.Fatalf("logged.Agent = %q, want empty", logged.Agent)
	}
	for _, cb := range logged.Candidates {
		if cb.Eliminated == "" {
			t.Fatalf("logged candidate %q not eliminated", cb.Agent)
		}
	}
}

func TestSelectEventLogged(t *testing.T) {
	sel, c := setup(t)
	ctx := context.Background()

	mustCreateAgent(t, c, contracts.Agent{
		Name: "candidate-a", Prompt: "p", Quadrant: contracts.QuadrantAutonomousExecutor,
		Capabilities: contracts.AgentCapabilities{MCPServers: acmeMCP},
	})

	req := agentselect.Request{
		TaskID: "tsk_1", SessionID: "ses_1", Title: "Implement the widget feature",
		RequiredMCPServers: acmeMCP,
	}

	want, err := sel.Select(ctx, req)
	if err != nil {
		t.Fatalf("Select: %v", err)
	}

	evs, err := c.Events(ctx, 0, req.SessionID, []string{"agent.selected"})
	if err != nil || len(evs) != 1 {
		t.Fatalf("events: %d, %v", len(evs), err)
	}
	var got agentselect.Selection
	if err := json.Unmarshal(evs[0].Payload, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	gotJSON, _ := json.Marshal(got)
	wantJSON, _ := json.Marshal(want)
	if string(gotJSON) != string(wantJSON) {
		t.Fatalf("logged selection mismatch:\n got  %s\n want %s", gotJSON, wantJSON)
	}
}
