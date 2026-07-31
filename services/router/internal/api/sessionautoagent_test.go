package api_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/drewkidwell/mads/pkg/agentselect"
	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
)

// Session-start's agent="auto" path must staff against the workspace's
// capability baseline, not just capinfer's inferred suggestions — the same
// union pipeline agent:auto stages get via engine.effectiveCapabilities
// (agentselect.EffectiveCapabilities). "gitlab" here is never suggested by
// inference (the opening prompt names nothing repo/host-related and no Fast
// Model scenario mocks a capability suggestion), so only the workspace's
// Capabilities baseline can be the reason an under-declared agent loses to
// a fully-declared one.
func TestCreateSessionAgentAutoAppliesWorkspaceCapabilityBaseline(t *testing.T) {
	srv, base := newTestServerWithData(t)
	data := dataclient.New(base, "test-setup")
	ctx := context.Background()

	cap := contracts.Capability{
		Name: "gitlab", Type: contracts.CapabilityMCP,
		Config: contracts.CapabilityConfig{Command: "true"},
	}
	if err := data.CreateCapability(ctx, &cap); err != nil {
		t.Fatalf("CreateCapability: %v", err)
	}

	ws := contracts.Workspace{Name: "gitlab-workspace", Capabilities: []string{"gitlab"}}
	if err := data.CreateWorkspace(ctx, &ws); err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}

	// under-declares: Investigator quadrant, but lacks the workspace baseline.
	if err := data.CreateAgent(ctx, &contracts.Agent{
		Name: "under-declared", Prompt: "p", Quadrant: contracts.QuadrantInvestigator,
	}); err != nil {
		t.Fatalf("CreateAgent under-declared: %v", err)
	}
	if err := data.CreateAgent(ctx, &contracts.Agent{
		Name: "fully-declared", Prompt: "p", Quadrant: contracts.QuadrantInvestigator,
		Capabilities: contracts.AgentCapabilities{MCPServers: []string{"gitlab"}},
	}); err != nil {
		t.Fatalf("CreateAgent fully-declared: %v", err)
	}

	resp := postJSON(t, srv.URL+"/sessions", map[string]string{
		"name": "auto-agent-workspace-baseline", "provider": "fake", "agent": "auto",
		"workspace":      ws.ID,
		"opening_prompt": "investigate something",
	})
	if resp.StatusCode != 201 {
		t.Fatalf("create status = %d, want 201", resp.StatusCode)
	}
	created := decodeJSON[struct {
		Session contracts.Session `json:"session"`
	}](t, resp)
	if created.Session.Agent != "fully-declared" {
		t.Fatalf("agent = %q, want %q (workspace baseline must eliminate under-declared)", created.Session.Agent, "fully-declared")
	}

	events, err := data.Events(ctx, 0, "", []string{string(contracts.AgentSelected)})
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("agent.selected events = %d, want 1: %+v", len(events), events)
	}
	var sel agentselect.Selection
	if err := json.Unmarshal(events[0].Payload, &sel); err != nil {
		t.Fatalf("decode agent.selected payload: %v", err)
	}
	if sel.Agent != "fully-declared" {
		t.Errorf("selected = %q, want fully-declared", sel.Agent)
	}
	var underEliminated string
	for _, cb := range sel.Candidates {
		if cb.Agent == "under-declared" {
			underEliminated = cb.Eliminated
		}
	}
	if underEliminated != "capability (missing gitlab)" {
		t.Errorf("under-declared eliminated = %q, want %q", underEliminated, "capability (missing gitlab)")
	}
}
