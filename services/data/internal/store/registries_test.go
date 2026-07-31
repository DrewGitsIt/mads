package store

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/drewkidwell/mads/pkg/contracts"
)

func TestSystemAgentsSeeded(t *testing.T) {
	l := openMigratedDB(t)
	agents := NewAgents(l)
	ctx := context.Background()

	list, err := agents.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 6 {
		t.Fatalf("want the 6 system agents after migration (spec §12), got %d", len(list))
	}
	for _, name := range []string{"synthesizer", "agent-builder", "architect", "executor", "code-reviewer", "verifier"} {
		a, err := agents.GetByName(ctx, name)
		if err != nil {
			t.Errorf("system agent %q missing: %v", name, err)
			continue
		}
		if !a.System || a.Prompt == "" {
			t.Errorf("system agent %q malformed: system=%v prompt empty=%v", name, a.System, a.Prompt == "")
		}
	}
}

// TestSystemAgentsNoPhantomCapabilities is the cleanup migration's done-when
// (0005 seeded skill names with no capability registry record, spamming
// "runner: load capability X: not found" at every system-agent session
// start): after migration, every capability a system agent declares must
// resolve against the live capabilities table — zero failing lookups.
func TestSystemAgentsNoPhantomCapabilities(t *testing.T) {
	l := openMigratedDB(t)
	agents := NewAgents(l)
	caps := NewCapabilities(l)
	ctx := context.Background()
	if err := SeedShippedCapabilities(ctx, caps); err != nil {
		t.Fatal(err)
	}

	known := map[string]bool{}
	list, err := caps.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range list {
		known[c.Name] = true
	}

	agentList, err := agents.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range agentList {
		if !a.System {
			continue
		}
		for _, name := range append(append([]string{}, a.Capabilities.Skills...), a.Capabilities.MCPServers...) {
			if !known[name] {
				t.Errorf("system agent %q references capability %q, absent from the registry", a.Name, name)
			}
		}
	}
}

func TestSystemAgentsEditableNotDeletable(t *testing.T) {
	l := openMigratedDB(t)
	agents := NewAgents(l)
	ctx := context.Background()
	// Migration 0020 attaches playwright-ui-testing to the executor's
	// declared skills; the Update below re-validates that reference against
	// the live capabilities table (checkAgentCapabilityRefs), so the shipped
	// capability must be seeded first — the same sequencing SeedShippedCapabilities'
	// doc comment describes for real startups (store.Bootstrap).
	if err := SeedShippedCapabilities(ctx, NewCapabilities(l)); err != nil {
		t.Fatal(err)
	}

	exec, err := agents.GetByName(ctx, "executor")
	if err != nil {
		t.Fatal(err)
	}

	// Editable (spec §12).
	exec.Prompt = exec.Prompt + "\nAlways run gofmt before committing."
	if err := agents.Update(ctx, exec, "user"); err != nil {
		t.Fatalf("editing a system agent should work: %v", err)
	}

	// Not deletable.
	var verr *contracts.ValidationError
	if err := agents.Delete(ctx, exec.ID, "user"); !errors.As(err, &verr) {
		t.Fatalf("deleting a system agent: want ValidationError, got %v", err)
	}
	if _, err := agents.GetByName(ctx, "executor"); err != nil {
		t.Error("executor vanished after refused delete")
	}
}

func TestAgentCRUDAndEvents(t *testing.T) {
	l := openMigratedDB(t)
	agents := NewAgents(l)
	ctx := context.Background()

	a := contracts.Agent{Name: "docs-writer", Prompt: "You write documentation.", Quadrant: contracts.QuadrantTechnicalPartner}
	if err := agents.Create(ctx, &a, "user"); err != nil {
		t.Fatal(err)
	}
	if a.ID == "" || a.CreatedAt.IsZero() {
		t.Fatalf("create did not fill metadata: %+v", a)
	}

	// Duplicate name refused.
	dup := contracts.Agent{Name: "docs-writer", Prompt: "x"}
	var verr *contracts.ValidationError
	if err := agents.Create(ctx, &dup, "user"); !errors.As(err, &verr) {
		t.Fatalf("duplicate name: want ValidationError, got %v", err)
	}

	// Validation reports everything at once.
	bad := contracts.Agent{Quadrant: "chaotic_neutral"}
	if err := agents.Create(ctx, &bad, "user"); !errors.As(err, &verr) {
		t.Fatal("invalid agent accepted")
	} else if len(verr.Errors) != 3 { // name, prompt, quadrant
		t.Errorf("want 3 errors together, got %+v", verr.Errors)
	}

	a.Description = "Writes the docs"
	if err := agents.Update(ctx, &a, "user"); err != nil {
		t.Fatal(err)
	}
	if err := agents.Delete(ctx, a.ID, "user"); err != nil {
		t.Fatal(err)
	}
	if _, err := agents.Get(ctx, a.ID); !errors.Is(err, ErrNotFound) {
		t.Error("deleted agent still present")
	}

	types := eventTypes(t, l, Filter{Types: []string{"agent.*"}})
	if len(types) != 3 { // created, updated, deleted for docs-writer
		t.Errorf("want created+updated+deleted events, got %v", types)
	}
}

func TestAgentCapabilityRefsValidated(t *testing.T) {
	l := openMigratedDB(t)
	agents := NewAgents(l)
	caps := NewCapabilities(l)
	ctx := context.Background()

	// Unknown skill and MCP refs together: rejected, naming both.
	bad := contracts.Agent{
		Name: "bad-agent", Prompt: "p",
		Capabilities: contracts.AgentCapabilities{Skills: []string{"ghost-skill"}, MCPServers: []string{"ghost-mcp"}},
	}
	var verr *contracts.ValidationError
	err := agents.Create(ctx, &bad, "user")
	if !errors.As(err, &verr) {
		t.Fatalf("unknown capability refs: want ValidationError, got %v", err)
	}
	if !strings.Contains(verr.Error(), "ghost-skill") || !strings.Contains(verr.Error(), "ghost-mcp") {
		t.Errorf("error doesn't name both offending refs: %v", verr)
	}

	// Tools are provider tool names, never registry entries — never rejected.
	toolsOnly := contracts.Agent{
		Name: "tools-only-agent", Prompt: "p",
		Capabilities: contracts.AgentCapabilities{Tools: []string{"shell", "read"}},
	}
	if err := agents.Create(ctx, &toolsOnly, "user"); err != nil {
		t.Fatalf("tools-only capabilities rejected: %v", err)
	}

	// Register the capabilities, then both create and update succeed.
	if err := caps.Create(ctx, &contracts.Capability{
		Name: "real-skill", Type: contracts.CapabilitySkill, Config: contracts.CapabilityConfig{Path: "~/skills/real"},
	}, "user"); err != nil {
		t.Fatal(err)
	}
	if err := caps.Create(ctx, &contracts.Capability{
		Name: "real-mcp", Type: contracts.CapabilityMCP, Config: contracts.CapabilityConfig{Command: "true"},
	}, "user"); err != nil {
		t.Fatal(err)
	}

	good := contracts.Agent{
		Name: "good-agent", Prompt: "p",
		Capabilities: contracts.AgentCapabilities{Skills: []string{"real-skill"}, MCPServers: []string{"real-mcp"}},
	}
	if err := agents.Create(ctx, &good, "user"); err != nil {
		t.Fatalf("known capability refs rejected: %v", err)
	}

	// Update path: flipping a known agent to reference an unknown skill is
	// caught the same way manual edits shouldn't reintroduce fiction.
	good.Capabilities.Skills = []string{"ghost-skill"}
	if err := agents.Update(ctx, &good, "user"); !errors.As(err, &verr) {
		t.Fatalf("update with unknown ref: want ValidationError, got %v", err)
	}
}

func TestCapabilityValidationByType(t *testing.T) {
	l := openMigratedDB(t)
	caps := NewCapabilities(l)
	ctx := context.Background()

	var verr *contracts.ValidationError
	if err := caps.Create(ctx, &contracts.Capability{Name: "x", Type: "telepathy"}, "user"); !errors.As(err, &verr) {
		t.Error("unknown capability type accepted")
	}
	if err := caps.Create(ctx, &contracts.Capability{Name: "arch-skill", Type: contracts.CapabilitySkill}, "user"); !errors.As(err, &verr) {
		t.Error("skill without path accepted")
	}
	if err := caps.Create(ctx, &contracts.Capability{Name: "jira", Type: contracts.CapabilityMCP}, "user"); !errors.As(err, &verr) {
		t.Error("mcp without command or url accepted")
	}

	ok := contracts.Capability{
		Name: "github", Type: contracts.CapabilityMCP,
		Config:            contracts.CapabilityConfig{Command: "npx @modelcontextprotocol/server-github"},
		CredentialEnvVars: []string{"GITHUB_TOKEN"},
	}
	if err := caps.Create(ctx, &ok, "user"); err != nil {
		t.Fatal(err)
	}
	got, err := caps.GetByName(ctx, "github")
	if err != nil {
		t.Fatal(err)
	}
	if got.Config.Command == "" || len(got.CredentialEnvVars) != 1 {
		t.Errorf("capability round trip lost data: %+v", got)
	}
}

func TestWorkspacePrimaryRepoRule(t *testing.T) {
	l := openMigratedDB(t)
	ws := NewWorkspaces(l)
	ctx := context.Background()

	var verr *contracts.ValidationError
	none := contracts.Workspace{Name: "w1", Repos: []contracts.WorkspaceRepo{{Path: "/a"}, {Path: "/b"}}}
	if err := ws.Create(ctx, &none, "user"); !errors.As(err, &verr) {
		t.Error("workspace with repos but no primary accepted")
	}
	two := contracts.Workspace{Name: "w2", Repos: []contracts.WorkspaceRepo{
		{Path: "/a", Primary: true}, {Path: "/b", Primary: true}}}
	if err := ws.Create(ctx, &two, "user"); !errors.As(err, &verr) {
		t.Error("workspace with two primaries accepted")
	}

	ok := contracts.Workspace{
		Name:  "mads-core",
		Repos: []contracts.WorkspaceRepo{{Path: "~/repo/mads", Primary: true}, {Path: "~/repo/mads-skills"}},
		Defaults: contracts.WorkspaceDefaults{
			Agent: "executor", Isolation: "worktree", Template: "default-implementation",
		},
	}
	if err := ws.Create(ctx, &ok, "user"); err != nil {
		t.Fatal(err)
	}
	// No repos at all is fine — non-git workspaces exist (spec §17).
	bare := contracts.Workspace{Name: "notes"}
	if err := ws.Create(ctx, &bare, "user"); err != nil {
		t.Errorf("repo-less workspace refused: %v", err)
	}
}

// TestWorkspaceCapabilityRefsValidated covers write-time rejection of
// Capabilities names absent from the registry, on both Create and Update
// (owner decision: workspace-level ambient capabilities must resolve against
// the live registry, same invariant as task-local requirements).
func TestWorkspaceCapabilityRefsValidated(t *testing.T) {
	l := openMigratedDB(t)
	ws := NewWorkspaces(l)
	caps := NewCapabilities(l)
	ctx := context.Background()

	var verr *contracts.ValidationError
	bad := contracts.Workspace{Name: "bad-workspace", Capabilities: []string{"ghost-capability"}}
	if err := ws.Create(ctx, &bad, "user"); !errors.As(err, &verr) {
		t.Fatalf("unknown capability ref: want ValidationError, got %v", err)
	}
	if !strings.Contains(verr.Error(), "ghost-capability") {
		t.Errorf("error doesn't name the offending ref: %v", verr)
	}

	// Empty is the default — no constraint — and is always valid.
	bare := contracts.Workspace{Name: "no-constraints"}
	if err := ws.Create(ctx, &bare, "user"); err != nil {
		t.Fatalf("empty Capabilities rejected: %v", err)
	}

	if err := caps.Create(ctx, &contracts.Capability{
		Name: "gitlab-access", Type: contracts.CapabilityMCP, Config: contracts.CapabilityConfig{Command: "true"},
	}, "user"); err != nil {
		t.Fatal(err)
	}
	good := contracts.Workspace{Name: "good-workspace", Capabilities: []string{"gitlab-access"}}
	if err := ws.Create(ctx, &good, "user"); err != nil {
		t.Fatalf("known capability ref rejected: %v", err)
	}

	// Update path: flipping to an unknown ref is caught too.
	good.Capabilities = []string{"ghost-capability"}
	if err := ws.Update(ctx, &good, "user"); !errors.As(err, &verr) {
		t.Fatalf("update with unknown ref: want ValidationError, got %v", err)
	}
}
