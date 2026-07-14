package api

import (
	"net/http"
	"testing"

	"github.com/drewkidwell/mads/pkg/contracts"
)

func TestAgentRegistryOverHTTP(t *testing.T) {
	srv, _ := newTestServer(t)

	// The six system agents are there on a fresh database.
	var list []contracts.Agent
	doJSON(t, srv, "GET", "/agents", nil, &list)
	if len(list) != 6 {
		t.Fatalf("fresh install: want 6 system agents, got %d", len(list))
	}

	// Name resolution — how pipeline `agent:` references resolve.
	var exec contracts.Agent
	if resp := doJSON(t, srv, "GET", "/agents/executor", nil, &exec); resp.StatusCode != 200 {
		t.Fatalf("get by name: status %d", resp.StatusCode)
	}
	if !exec.System || exec.Name != "executor" {
		t.Errorf("resolved wrong agent: %+v", exec)
	}

	// Create, update via name, refuse system delete.
	var created contracts.Agent
	resp := doJSON(t, srv, "POST", "/agents", contracts.Agent{
		Name: "release-notes", Prompt: "You write release notes.",
	}, &created)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create: status %d", resp.StatusCode)
	}
	created.Tags = []string{"docs"}
	if resp := doJSON(t, srv, "PUT", "/agents/release-notes", created, nil); resp.StatusCode != 200 {
		t.Errorf("update by name: status %d", resp.StatusCode)
	}
	if resp := doJSON(t, srv, "DELETE", "/agents/executor", nil, nil); resp.StatusCode != 422 {
		t.Errorf("system delete: status %d, want 422", resp.StatusCode)
	}
	if resp := doJSON(t, srv, "DELETE", "/agents/release-notes", nil, nil); resp.StatusCode != 200 {
		t.Errorf("delete: status %d", resp.StatusCode)
	}
}

func TestCapabilityAndWorkspaceRoutes(t *testing.T) {
	srv, _ := newTestServer(t)

	var cap contracts.Capability
	resp := doJSON(t, srv, "POST", "/capabilities", contracts.Capability{
		Name: "beads", Type: contracts.CapabilitySkill,
		Config: contracts.CapabilityConfig{Path: "~/skills/beads"},
	}, &cap)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("capability create: status %d", resp.StatusCode)
	}

	var verr contracts.ValidationError
	resp = doJSON(t, srv, "POST", "/workspaces", contracts.Workspace{
		Name: "broken", Repos: []contracts.WorkspaceRepo{{Path: "/a"}},
	}, &verr)
	if resp.StatusCode != 422 || len(verr.Errors) == 0 {
		t.Errorf("primary-repo rule not enforced over HTTP: status %d, %+v", resp.StatusCode, verr)
	}

	// Registry mutations land on the event log.
	var events []contracts.Envelope
	doJSON(t, srv, "GET", "/events?types=capability.*", nil, &events)
	if len(events) != 1 || events[0].Type != contracts.CapabilityCreated {
		t.Errorf("want capability.created on the log, got %+v", events)
	}
}

func TestCapabilityHealthRouteOverHTTP(t *testing.T) {
	srv, _ := newTestServer(t)

	var cap contracts.Capability
	resp := doJSON(t, srv, "POST", "/capabilities", contracts.Capability{
		Name: "jira", Type: contracts.CapabilityMCP,
		Config: contracts.CapabilityConfig{Command: "npx @modelcontextprotocol/server-jira"},
	}, &cap)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("capability create: status %d", resp.StatusCode)
	}

	// First report, unhealthy, matches the zero-value default: no event.
	var reported contracts.Capability
	resp = doJSON(t, srv, "POST", "/capabilities/"+cap.Name+"/health",
		map[string]any{"healthy": false, "reason": "missing credential: JIRA_TOKEN"}, &reported)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("set health: status %d", resp.StatusCode)
	}
	if reported.Health.Healthy || reported.Health.Reason != "missing credential: JIRA_TOKEN" {
		t.Errorf("health not applied: %+v", reported.Health)
	}

	// Flip to healthy (resolved by id this time): exactly one transition event.
	resp = doJSON(t, srv, "POST", "/capabilities/"+cap.ID+"/health",
		map[string]any{"healthy": true}, &reported)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("set health: status %d", resp.StatusCode)
	}
	if !reported.Health.Healthy {
		t.Errorf("health not flipped: %+v", reported.Health)
	}

	var events []contracts.Envelope
	doJSON(t, srv, "GET", "/events?types=capability.health_changed", nil, &events)
	if len(events) != 1 {
		t.Fatalf("want exactly 1 capability.health_changed event, got %d: %+v", len(events), events)
	}

	// Same-status repeat: no additional event.
	doJSON(t, srv, "POST", "/capabilities/"+cap.ID+"/health", map[string]any{"healthy": true}, &reported)
	doJSON(t, srv, "GET", "/events?types=capability.health_changed", nil, &events)
	if len(events) != 1 {
		t.Fatalf("same-status repeat: want still 1 event, got %d", len(events))
	}

	// Unknown capability: 404.
	resp = doJSON(t, srv, "POST", "/capabilities/does-not-exist/health", map[string]any{"healthy": true}, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown capability: status %d, want 404", resp.StatusCode)
	}
}
