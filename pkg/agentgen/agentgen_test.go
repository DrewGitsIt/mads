package agentgen_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/drewkidwell/mads/pkg/agentgen"
	"github.com/drewkidwell/mads/pkg/agentselect"
	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/pkg/golden"
	"github.com/drewkidwell/mads/providers/fake"
	"github.com/drewkidwell/mads/providers/fastmodel"
	"github.com/drewkidwell/mads/services/data/datatest"
)

// setup starts a real Data Service and a Generator riding a fake-scripted
// Fast Model — never a mock of either, never a live LLM (project
// convention).
func setup(t *testing.T, sc fake.Scenario) (*agentgen.Generator, *dataclient.Client) {
	t.Helper()
	base := datatest.Start(t)
	c := dataclient.New(base, "agentgen")
	f, err := fake.NewWithScenario(sc)
	if err != nil {
		t.Fatal(err)
	}
	fm := fastmodel.New(f, fastmodel.Config{Model: "fake-fast"})
	return agentgen.New(c, fm), c
}

func mustCreateAgent(t *testing.T, c *dataclient.Client, a contracts.Agent) contracts.Agent {
	t.Helper()
	if err := c.CreateAgent(context.Background(), &a); err != nil {
		t.Fatalf("create agent %s: %v", a.Name, err)
	}
	return a
}

func mustCreateCapability(t *testing.T, c *dataclient.Client, cap contracts.Capability) contracts.Capability {
	t.Helper()
	if err := c.CreateCapability(context.Background(), &cap); err != nil {
		t.Fatalf("create capability %s: %v", cap.Name, err)
	}
	return cap
}

func marshalIndent(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return append(raw, '\n')
}

// The golden task-context generation path: a scripted Fast Model reply keyed
// on a substring of the task title comes back mapped onto a full draft
// contracts.Agent.
func TestGenerateFromTaskGolden(t *testing.T) {
	g, c := setup(t, fake.Scenario{Name: "generate-from-task", Rules: []fake.Rule{{
		// The reply exercises the redesigned draft shape (agent-concept
		// redesign §2): allowlist/denylist over the closed ToolIntent
		// vocabulary, no concrete tool names, no command strings.
		Match: "Fix the flaky login test",
		Reply: `{"name":"login-test-fixer","description":"Fixes flaky login tests",` +
			`"prompt":"You are a QA engineer who stabilizes flaky login tests.",` +
			`"quadrant":"autonomous_executor","tags":["testing","login"],` +
			`"allowlist":["shell","read","write"],"denylist":[],` +
			`"capabilities":{"mcp_servers":[],"skills":["testing"]},` +
			`"permissions":{"allowed_paths":["tests/**"]}}`,
	}}})
	mustCreateCapability(t, c, contracts.Capability{
		Name: "testing", Type: contracts.CapabilitySkill, Config: contracts.CapabilityConfig{Path: "~/skills/testing"},
	})

	result, err := g.GenerateFromTask(context.Background(), agentselect.Request{
		TaskID: "tsk_1", Title: "Fix the flaky login test",
		Description: "It fails intermittently in CI",
	})
	if err != nil {
		t.Fatalf("GenerateFromTask: %v", err)
	}
	if result.Similar != nil {
		t.Fatalf("Similar = %+v, want nil (empty registry)", result.Similar)
	}
	golden.Assert(t, "generate_from_task.golden.json", marshalIndent(t, result.Draft))
}

// The golden NL-description generation path: the Library editor's "describe
// what you want" flow.
func TestGenerateFromDescriptionGolden(t *testing.T) {
	g, c := setup(t, fake.Scenario{Name: "generate-from-description", Rules: []fake.Rule{{
		// Redesigned shape: the deny is now the closed-vocabulary "shell"
		// intent (denylist), not a harness-leaky command string.
		Match: "database migrations and knows PostgreSQL",
		Reply: `{"name":"db-migrator","description":"Specializes in PostgreSQL schema migrations",` +
			`"prompt":"You are a database migration specialist who knows PostgreSQL deeply.",` +
			`"quadrant":"technical_partner","tags":["database","postgres","migrations"],` +
			`"allowlist":["shell","read","write"],"denylist":["shell"],` +
			`"capabilities":{"mcp_servers":["postgres"],"skills":[]},` +
			`"permissions":{"allowed_paths":["migrations/**"]}}`,
	}}})
	mustCreateCapability(t, c, contracts.Capability{
		Name: "postgres", Type: contracts.CapabilityMCP, Config: contracts.CapabilityConfig{Command: "npx @modelcontextprotocol/server-postgres"},
	})

	result, err := g.GenerateFromDescription(context.Background(),
		"I need an agent that specializes in database migrations and knows PostgreSQL")
	if err != nil {
		t.Fatalf("GenerateFromDescription: %v", err)
	}
	if result.Similar != nil {
		t.Fatalf("Similar = %+v, want nil (empty registry)", result.Similar)
	}
	golden.Assert(t, "generate_from_description.golden.json", marshalIndent(t, result.Draft))
}

// The registry-grounding acceptance criteria: given a registry that knows
// some of the model's emitted names but not others, only the known names
// survive into the persisted draft, and the dropped ones are surfaced on
// Result rather than silently disappearing.
func TestGenerateKeepsOnlyRegisteredCapabilities(t *testing.T) {
	g, c := setup(t, fake.Scenario{Name: "known-and-unknown-caps", Rules: []fake.Rule{{
		Match: "Set up the reporting pipeline",
		// Redesigned shape: allowlist is the closed ToolIntent vocabulary
		// (the persona is harness-free), mcp/skills stay registry-refs.
		Reply: `{"name":"reporting-agent","description":"Builds reports","prompt":"You build reports.",` +
			`"quadrant":"autonomous_executor",` +
			`"allowlist":["shell"],"capabilities":{"mcp_servers":["postgres","made-up-mcp"],"skills":["testing","made-up-skill"]},` +
			`"permissions":{}}`,
	}}})
	mustCreateCapability(t, c, contracts.Capability{
		Name: "postgres", Type: contracts.CapabilityMCP, Config: contracts.CapabilityConfig{Command: "npx @modelcontextprotocol/server-postgres"},
	})
	mustCreateCapability(t, c, contracts.Capability{
		Name: "testing", Type: contracts.CapabilitySkill, Config: contracts.CapabilityConfig{Path: "~/skills/testing"},
	})

	result, err := g.GenerateFromTask(context.Background(), agentselect.Request{
		TaskID: "tsk_mixed", Title: "Set up the reporting pipeline",
	})
	if err != nil {
		t.Fatalf("GenerateFromTask: %v", err)
	}

	gotCaps, _ := json.Marshal(result.Draft.Capabilities)
	// Redesigned shape (agent-concept redesign §2): the persona's tool
	// allowance is the closed ToolIntent vocabulary (Allowlist), not a
	// concrete `capabilities.tools` name list — "shell" folds in as intent
	// `shell`. Only registry-known skill/MCP names survive.
	wantCaps, _ := json.Marshal(contracts.AgentCapabilities{
		MCPServers: []string{"postgres"}, Skills: []string{"testing"},
	})
	if string(gotCaps) != string(wantCaps) {
		t.Fatalf("Capabilities = %s, want only registered names kept: %s", gotCaps, wantCaps)
	}
	if len(result.Draft.Allowlist) == 0 {
		t.Fatal("Allowlist = empty, want the persona to declare a tool allowance for its task")
	}

	wantDropped := map[string]bool{"made-up-mcp": true, "made-up-skill": true}
	if len(result.Dropped) != len(wantDropped) {
		t.Fatalf("Dropped = %v, want exactly %v", result.Dropped, wantDropped)
	}
	for _, d := range result.Dropped {
		if !wantDropped[d] {
			t.Errorf("Dropped contains unexpected name %q", d)
		}
	}
}

// A requirement-bearing task's generated draft must declare the effective
// required set even when the Fast Model's own reply omits it entirely — a
// draft that doesn't declare its own task's requirements would fail
// auto-select's capability filter on retry.
func TestGenerateFromTaskDeclaresRequiredCapabilities(t *testing.T) {
	g, c := setup(t, fake.Scenario{Name: "requires-capability", Rules: []fake.Rule{{
		Match: "Scrape the pricing page",
		// Redesigned shape: the persona declares its tool allowance as the
		// closed-vocabulary "shell" intent (an allowlist entry), not a
		// concrete tool name. This test's point (a draft missing the
		// task's effective required capabilities is *still* complete after
		// the required-set merge) survives the redesign: the merge now
		// adds "shell" to Allowlist when the task's RequiredTools name a
		// concrete tool that maps to that intent.
		Reply: `{"name":"scraper-agent","description":"Scrapes pricing pages","prompt":"You scrape pages.",` +
			`"quadrant":"autonomous_executor",` +
			`"capabilities":{"mcp_servers":[],"skills":[]},"permissions":{}}`,
	}}})
	mustCreateCapability(t, c, contracts.Capability{
		Name: "playwright", Type: contracts.CapabilityMCP, Config: contracts.CapabilityConfig{Command: "npx playwright-mcp"},
	})

	result, err := g.GenerateFromTask(context.Background(), agentselect.Request{
		TaskID: "tsk_requires", Title: "Scrape the pricing page",
		RequiredMCPServers: []string{"playwright"},
		RequiredTools:      []string{"shell"}, // folded into the persona's Allowlist by the merge
	})
	if err != nil {
		t.Fatalf("GenerateFromTask: %v", err)
	}

	var found bool
	for _, m := range result.Draft.Capabilities.MCPServers {
		if m == "playwright" {
			found = true
		}
	}
	if !found {
		t.Fatalf("Capabilities.MCPServers = %v, want playwright present despite the model's reply omitting it", result.Draft.Capabilities.MCPServers)
	}
	if len(result.Draft.Allowlist) == 0 {
		t.Fatal("Allowlist = empty, want shell folded in from the task's required tools")
	}
}

// The empty-registry acceptance criteria: with no capabilities registered at
// all, every skill/MCP name the model emits is dropped, regardless of how
// plausible-looking it is. The persona's tool allowance (Allowlist) is a
// closed-vocabulary list, untouched by the registry check — it isn't a
// registry entry, so it isn't subject to the drop.
func TestGenerateWithEmptyRegistryDropsAllSkillsAndMCP(t *testing.T) {
	g, _ := setup(t, fake.Scenario{Name: "empty-registry", Rules: []fake.Rule{{
		Match: "Set up the reporting pipeline",
		// Redesigned shape: the allowance is allowlist/denylist over the
		// closed ToolIntent vocabulary, not a concrete-name capabilities.tools.
		Reply: `{"name":"reporting-agent","description":"Builds reports","prompt":"You build reports.",` +
			`"quadrant":"autonomous_executor",` +
			`"allowlist":["shell","read"],"capabilities":{"mcp_servers":["postgres"],"skills":["testing"]},` +
			`"permissions":{}}`,
	}}})

	result, err := g.GenerateFromTask(context.Background(), agentselect.Request{
		TaskID: "tsk_empty_registry", Title: "Set up the reporting pipeline",
	})
	if err != nil {
		t.Fatalf("GenerateFromTask: %v", err)
	}

	if len(result.Draft.Capabilities.Skills) != 0 {
		t.Errorf("Capabilities.Skills = %v, want none (empty registry)", result.Draft.Capabilities.Skills)
	}
	if len(result.Draft.Capabilities.MCPServers) != 0 {
		t.Errorf("Capabilities.MCPServers = %v, want none (empty registry)", result.Draft.Capabilities.MCPServers)
	}
	wantAllow := []string{"shell", "read"}
	gotAllow, _ := json.Marshal(result.Draft.Allowlist)
	wantAllowJSON, _ := json.Marshal(wantAllow)
	if string(gotAllow) != string(wantAllowJSON) {
		t.Errorf("Allowlist = %s, want untouched %s (allowlist is closed-vocabulary, not a registry entry)", gotAllow, wantAllowJSON)
	}
	if len(result.Dropped) != 2 {
		t.Fatalf("Dropped = %v, want both postgres and testing dropped", result.Dropped)
	}
}

// A near-duplicate generation request must return the similarity warning
// naming the matched agent — the specific proof the task's acceptance
// criteria names.
func TestGenerateFromTaskNearDuplicateWarns(t *testing.T) {
	g, c := setup(t, fake.Scenario{Name: "near-duplicate", Rules: []fake.Rule{{
		Match: "Add another postgres migration helper",
		Reply: `{"name":"pg-migration-helper-2","description":"Handles postgres database migrations",` +
			`"prompt":"You are a database migration specialist.",` +
			`"quadrant":"autonomous_executor","tags":["postgres","migrations"],` +
			`"capabilities":{"tools":["shell","sql"],"mcp_servers":["postgres"],"skills":[]},` +
			`"permissions":{}}`,
	}}})
	mustCreateCapability(t, c, contracts.Capability{
		Name: "postgres", Type: contracts.CapabilityMCP, Config: contracts.CapabilityConfig{Command: "npx @modelcontextprotocol/server-postgres"},
	})

	mustCreateAgent(t, c, contracts.Agent{
		Name: "pg-migration-helper", Prompt: "p", Description: "Handles postgres database migrations",
		Tags: []string{"postgres", "migrations"}, Quadrant: contracts.QuadrantAutonomousExecutor,
		Capabilities: contracts.AgentCapabilities{Tools: []string{"shell", "sql"}, MCPServers: []string{"postgres"}},
	})

	result, err := g.GenerateFromTask(context.Background(), agentselect.Request{
		TaskID: "tsk_dup", Title: "Add another postgres migration helper",
	})
	if err != nil {
		t.Fatalf("GenerateFromTask: %v", err)
	}
	if result.Similar == nil {
		t.Fatal("Similar = nil, want a match on pg-migration-helper")
	}
	if result.Similar.Agent != "pg-migration-helper" {
		t.Fatalf("Similar.Agent = %q, want pg-migration-helper", result.Similar.Agent)
	}
	if result.Similar.Score < 0.6 {
		t.Fatalf("Similar.Score = %v, want >= 0.6", result.Similar.Score)
	}
}

// An unrelated existing agent (different quadrant, disjoint tags and
// capabilities) must not trip the near-duplicate warning.
func TestGenerateFromTaskNoSimilarMatch(t *testing.T) {
	g, c := setup(t, fake.Scenario{Name: "no-duplicate", Rules: []fake.Rule{{
		Match: "Add another postgres migration helper",
		Reply: `{"name":"pg-migration-helper-2","description":"Handles postgres database migrations",` +
			`"prompt":"You are a database migration specialist.",` +
			`"quadrant":"autonomous_executor","tags":["postgres","migrations"],` +
			`"capabilities":{"tools":["shell","sql"],"mcp_servers":["postgres"],"skills":[]},` +
			`"permissions":{}}`,
	}}})

	mustCreateAgent(t, c, contracts.Agent{
		Name: "frontend-builder", Prompt: "p", Description: "Handles frontend react components",
		Tags: []string{"react", "frontend"}, Quadrant: contracts.QuadrantResearchAnalyst,
		Capabilities: contracts.AgentCapabilities{Tools: []string{"browser"}},
	})

	result, err := g.GenerateFromTask(context.Background(), agentselect.Request{
		TaskID: "tsk_nodup", Title: "Add another postgres migration helper",
	})
	if err != nil {
		t.Fatalf("GenerateFromTask: %v", err)
	}
	if result.Similar != nil {
		t.Fatalf("Similar = %+v, want nil", result.Similar)
	}
}

// A reply with a missing/invalid quadrant falls back to agentselect.Classify
// so the draft is immediately usable by auto-select's quadrant filter.
func TestGenerateFromTaskInvalidQuadrantFallsBackToClassify(t *testing.T) {
	g, _ := setup(t, fake.Scenario{Name: "bad-quadrant", Rules: []fake.Rule{{
		Match: "Implement the login rate limiter",
		Reply: `{"name":"limiter-agent","description":"d","prompt":"p","quadrant":"not-a-real-quadrant"}`,
	}}})

	req := agentselect.Request{
		TaskID: "tsk_bad_q", Title: "Implement the login rate limiter in auth/limiter.go exactly as specified",
	}
	result, err := g.GenerateFromTask(context.Background(), req)
	if err != nil {
		t.Fatalf("GenerateFromTask: %v", err)
	}
	want := agentselect.Classify(req)
	if result.Draft.Quadrant != want {
		t.Fatalf("Quadrant = %q, want fallback %q", result.Draft.Quadrant, want)
	}
}

func TestDescribe(t *testing.T) {
	g, _ := setup(t, fake.Scenario{Name: "describe", Rules: []fake.Rule{{
		Match: "You are a database migration specialist",
		Reply: "  Specializes in safe PostgreSQL schema migrations.  ",
	}}})

	got, err := g.Describe(context.Background(), "You are a database migration specialist who knows PostgreSQL.")
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}
	if got != "Specializes in safe PostgreSQL schema migrations." {
		t.Fatalf("Describe: got %q", got)
	}
}

// Suggestions are grounded the same way generation is: only registry names
// survive, invented ones are reported in Dropped, and tool allowance comes
// back as ToolIntents rather than concrete tool names.
func TestSuggestCapabilities(t *testing.T) {
	g, c := setup(t, fake.Scenario{Name: "suggest-capabilities", Rules: []fake.Rule{{
		// The registry descriptions must reach the model for grounding.
		Match: "postgres: Query and migrate PostgreSQL",
		Reply: `{"allowlist":["shell","read","Bash"],"mcp_servers":["postgres","jira"],"skills":["migrations","made-up"]}`,
	}}})
	mustCreateCapability(t, c, contracts.Capability{
		Name: "postgres", Type: contracts.CapabilityMCP, Description: "Query and migrate PostgreSQL",
		Config: contracts.CapabilityConfig{Command: "npx @modelcontextprotocol/server-postgres"},
	})
	mustCreateCapability(t, c, contracts.Capability{
		Name: "migrations", Type: contracts.CapabilitySkill, Config: contracts.CapabilityConfig{Path: "~/skills/migrations"},
	})

	got, err := g.SuggestCapabilities(context.Background(), "a database migration specialist who knows PostgreSQL")
	if err != nil {
		t.Fatalf("SuggestCapabilities: %v", err)
	}
	want := agentgen.Suggestion{
		Allowlist:  []contracts.ToolIntent{"shell", "read"},
		MCPServers: []string{"postgres"},
		Skills:     []string{"migrations"},
		Dropped:    []string{"made-up", "jira", "Bash"},
	}
	gotJSON, _ := json.Marshal(got)
	wantJSON, _ := json.Marshal(want)
	if string(gotJSON) != string(wantJSON) {
		t.Fatalf("SuggestCapabilities: got %s, want %s", gotJSON, wantJSON)
	}
}

// Names the registry doesn't hold never come back as suggestions, however
// plausible they sound.
func TestSuggestCapabilitiesDropsInventedNames(t *testing.T) {
	g, _ := setup(t, fake.Scenario{Name: "suggest-invented", Rules: []fake.Rule{{
		Match: "reviews pull requests",
		Reply: `{"allowlist":["read"],"mcp_servers":["github"],"skills":["code-review"]}`,
	}}})

	got, err := g.SuggestCapabilities(context.Background(), "reviews pull requests")
	if err != nil {
		t.Fatalf("SuggestCapabilities: %v", err)
	}
	if len(got.Skills) != 0 || len(got.MCPServers) != 0 {
		t.Fatalf("got skills=%v mcp=%v, want both empty", got.Skills, got.MCPServers)
	}
	if len(got.Dropped) != 2 {
		t.Fatalf("Dropped = %v, want the two invented names", got.Dropped)
	}
}

// A structured prompt_sections reply is assembled under the scaffold
// headings shown in the Library editor, in scaffold order.
func TestGenerateAssemblesScaffoldedPrompt(t *testing.T) {
	g, _ := setup(t, fake.Scenario{Name: "generate-scaffold", Rules: []fake.Rule{{
		Match: "reviews Go pull requests",
		Reply: `{"name":"go-reviewer","description":"Reviews Go PRs",` +
			`"prompt_sections":{"output":"A list of findings, most severe first.",` +
			`"role":"You review Go pull requests for correctness.",` +
			`"boundaries":"Never push commits.","approach":"Read the diff, then the callers."},` +
			`"quadrant":"investigator"}`,
	}}})

	result, err := g.GenerateFromDescription(context.Background(), "an agent that reviews Go pull requests")
	if err != nil {
		t.Fatalf("GenerateFromDescription: %v", err)
	}
	want := "Role: You review Go pull requests for correctness.\n\n" +
		"Approach: Read the diff, then the callers.\n\n" +
		"Boundaries: Never push commits.\n\n" +
		"Output: A list of findings, most severe first."
	if result.Draft.Prompt != want {
		t.Fatalf("Prompt =\n%s\nwant\n%s", result.Draft.Prompt, want)
	}
}

// A reply with neither prompt_sections nor a flat prompt fails validation
// (and so feeds the Fast Model retry loop).
func TestGenerateRequiresAPrompt(t *testing.T) {
	g, _ := setup(t, fake.Scenario{Name: "generate-no-prompt", Rules: []fake.Rule{{
		Match: "an agent with no prompt",
		Reply: `{"name":"empty","quadrant":"investigator"}`,
	}}})

	if _, err := g.GenerateFromDescription(context.Background(), "an agent with no prompt"); err == nil {
		t.Fatal("GenerateFromDescription: want a validation error, got nil")
	}
}

func TestRefine(t *testing.T) {
	g, _ := setup(t, fake.Scenario{Name: "refine", Rules: []fake.Rule{{
		Match: "make it more concise",
		Reply: "  You stabilize flaky tests.  ",
	}}})

	got, err := g.Refine(context.Background(), "You are a QA engineer who stabilizes flaky login tests.", "make it more concise")
	if err != nil {
		t.Fatalf("Refine: %v", err)
	}
	if got != "You stabilize flaky tests." {
		t.Fatalf("Refine: got %q", got)
	}
}
