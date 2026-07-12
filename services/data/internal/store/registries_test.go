package store

import (
	"context"
	"errors"
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

func TestSystemAgentsEditableNotDeletable(t *testing.T) {
	l := openMigratedDB(t)
	agents := NewAgents(l)
	ctx := context.Background()

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
