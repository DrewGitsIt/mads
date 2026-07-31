package capinfer_test

import (
	"context"
	"os/exec"
	"testing"

	"github.com/drewkidwell/mads/pkg/capinfer"
	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/providers/fake"
	"github.com/drewkidwell/mads/providers/fastmodel"
	"github.com/drewkidwell/mads/services/data/datatest"
)

func setup(t *testing.T, sc fake.Scenario) (*capinfer.Inferrer, *dataclient.Client) {
	t.Helper()
	base := datatest.Start(t)
	c := dataclient.New(base, "capinfer")
	f, err := fake.NewWithScenario(sc)
	if err != nil {
		t.Fatal(err)
	}
	fm := fastmodel.New(f, fastmodel.Config{Model: "fake-fast"})
	return capinfer.New(c, fm), c
}

func mustCreateCapability(t *testing.T, c *dataclient.Client, cap contracts.Capability) {
	t.Helper()
	if err := c.CreateCapability(context.Background(), &cap); err != nil {
		t.Fatalf("create capability %s: %v", cap.Name, err)
	}
}

// Only registry-known names survive — a model reply naming a capability
// that doesn't exist is dropped, never suggested.
func TestInferDropsHallucinatedNames(t *testing.T) {
	inf, c := setup(t, fake.Scenario{Name: "hallucination", Rules: []fake.Rule{{
		Match: "Wire up the deploy pipeline",
		Reply: `{"capabilities": ["playwright", "made-up-capability", "postgres"]}`,
	}}})
	mustCreateCapability(t, c, contracts.Capability{Name: "playwright", Type: contracts.CapabilitySkill, Config: contracts.CapabilityConfig{Path: "~/skills/playwright"}})
	mustCreateCapability(t, c, contracts.Capability{Name: "postgres", Type: contracts.CapabilityMCP, Config: contracts.CapabilityConfig{Command: "npx postgres-mcp"}})

	got, err := inf.Infer(context.Background(), "Wire up the deploy pipeline against staging", nil)
	if err != nil {
		t.Fatalf("Infer: %v", err)
	}
	want := []string{"playwright", "postgres"}
	if !equal(got, want) {
		t.Errorf("Infer() = %v, want %v (made-up-capability must be dropped)", got, want)
	}
}

// Every name Infer returns must exist in the live registry, regardless of
// what the model emits — the core grounding acceptance criterion.
func TestInferOutputIsSubsetOfRegistry(t *testing.T) {
	inf, c := setup(t, fake.Scenario{Name: "subset", Rules: []fake.Rule{{
		Match: "",
		Reply: `{"capabilities": ["nonexistent-a", "nonexistent-b", "jira"]}`,
	}}})
	mustCreateCapability(t, c, contracts.Capability{Name: "jira", Type: contracts.CapabilityMCP, Config: contracts.CapabilityConfig{Command: "npx jira-mcp"}})

	got, err := inf.Infer(context.Background(), "File a ticket for this bug", nil)
	if err != nil {
		t.Fatalf("Infer: %v", err)
	}
	registry, err := c.Capabilities(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	known := map[string]bool{}
	for _, cap := range registry {
		known[cap.Name] = true
	}
	for _, name := range got {
		if !known[name] {
			t.Errorf("Infer() suggested %q, which is not in the registry", name)
		}
	}
	if !equal(got, []string{"jira"}) {
		t.Errorf("Infer() = %v, want [jira]", got)
	}
}

// A nil/unconfigured Fast Model must never fail inference — an empty
// suggestion set is valid, never a hard error.
func TestInferNoFastModelSkipsSilently(t *testing.T) {
	base := datatest.Start(t)
	c := dataclient.New(base, "capinfer")
	mustCreateCapability(t, c, contracts.Capability{Name: "jira", Type: contracts.CapabilityMCP, Config: contracts.CapabilityConfig{Command: "npx jira-mcp"}})

	inf := capinfer.New(c, nil)
	got, err := inf.Infer(context.Background(), "File a ticket for this bug", nil)
	if err != nil {
		t.Fatalf("Infer with nil FastModel: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("Infer() = %v, want empty (no Fast Model configured)", got)
	}
}

// An empty registry means there's nothing to ground suggestions against —
// Infer must not call the model at all and must return no suggestions.
func TestInferEmptyRegistryYieldsNoSuggestions(t *testing.T) {
	inf, _ := setup(t, fake.Scenario{Name: "empty-registry", Rules: []fake.Rule{{
		Match: "",
		Reply: `{"capabilities": ["anything"]}`,
	}}})
	got, err := inf.Infer(context.Background(), "Do something", nil)
	if err != nil {
		t.Fatalf("Infer: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("Infer() = %v, want empty (empty registry)", got)
	}
}

// RepoSignals augments the model's suggestions with a deterministic,
// non-LLM signal: a repo's git remote host implies a same-named registry
// capability, when one exists.
func TestRepoSignalsFromGitRemote(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	runGit(t, dir, "init")
	runGit(t, dir, "remote", "add", "origin", "git@github.com:acme/widgets.git")

	registry := []contracts.Capability{
		{Name: "github", Type: contracts.CapabilityMCP},
		{Name: "unrelated", Type: contracts.CapabilitySkill},
	}
	got := capinfer.RepoSignals([]contracts.WorkspaceRepo{{Path: dir}}, registry)
	if !equal(got, []string{"github"}) {
		t.Errorf("RepoSignals() = %v, want [github]", got)
	}
}

// A remote host with no matching registry capability contributes nothing —
// deterministic signals are grounded exactly like model output.
func TestRepoSignalsHostNotInRegistry(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	runGit(t, dir, "init")
	runGit(t, dir, "remote", "add", "origin", "https://github.com/acme/widgets.git")

	got := capinfer.RepoSignals([]contracts.WorkspaceRepo{{Path: dir}}, []contracts.Capability{
		{Name: "unrelated", Type: contracts.CapabilitySkill},
	})
	if len(got) != 0 {
		t.Errorf("RepoSignals() = %v, want empty (github capability not in registry)", got)
	}
}

// A repo with no remote at all is a no-op, never an error.
func TestRepoSignalsNoRemote(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	runGit(t, dir, "init")

	got := capinfer.RepoSignals([]contracts.WorkspaceRepo{{Path: dir}}, []contracts.Capability{
		{Name: "github", Type: contracts.CapabilityMCP},
	})
	if len(got) != 0 {
		t.Errorf("RepoSignals() = %v, want empty (no remote)", got)
	}
}

// Infer unions the model's suggestions with RepoSignals, deduplicated.
func TestInferUnionsModelAndRepoSignals(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	runGit(t, dir, "init")
	runGit(t, dir, "remote", "add", "origin", "git@github.com:acme/widgets.git")

	inf, c := setup(t, fake.Scenario{Name: "union", Rules: []fake.Rule{{
		Match: "",
		Reply: `{"capabilities": ["playwright"]}`,
	}}})
	mustCreateCapability(t, c, contracts.Capability{Name: "playwright", Type: contracts.CapabilitySkill, Config: contracts.CapabilityConfig{Path: "~/skills/playwright"}})
	mustCreateCapability(t, c, contracts.Capability{Name: "github", Type: contracts.CapabilityMCP, Config: contracts.CapabilityConfig{Command: "npx github-mcp"}})

	got, err := inf.Infer(context.Background(), "Ship the widget feature", []contracts.WorkspaceRepo{{Path: dir}})
	if err != nil {
		t.Fatalf("Infer: %v", err)
	}
	if !equal(got, []string{"github", "playwright"}) {
		t.Errorf("Infer() = %v, want [github playwright]", got)
	}
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(cmd.Environ(), "GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
