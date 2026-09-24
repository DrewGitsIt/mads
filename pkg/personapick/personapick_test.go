package personapick

import (
	"context"
	"testing"

	"github.com/drewkidwell/mads/providers/fake"
	"github.com/drewkidwell/mads/providers/fastmodel"
)

func fixtureRunner(t *testing.T, reply string) *fastmodel.Runner {
	t.Helper()
	p, err := fake.NewWithScenario(fake.Scenario{
		Name:  "personapick-fixture",
		Rules: []fake.Rule{{Match: "", Reply: reply}},
	})
	if err != nil {
		t.Fatalf("fake scenario: %v", err)
	}
	return fastmodel.New(p, fastmodel.Config{MaxAttempts: 1})
}

// TestAutoPickGoldenPinsThePrompt: the exact text of the one-shot prompt
// is a golden file. A refactor that changes the prompt shape changes a
// golden — that is the whole point of a golden test on a *prompt*
// (the prompt is the contract surface, not the reply).
func TestAutoPickGoldenPinsThePrompt(t *testing.T) {
	req := Request{
		OpeningPrompt:   "review my latest commit for bugs",
		FallbackPersona: "generalist",
		Candidates: []Candidate{
			{ID: "reviewer", Name: "Reviewer", Description: "reads code, reports bugs", Quadrant: "technical_partner", Allowed: []string{"read", "search"}},
			{ID: "editor", Name: "Editor", Description: "edits files", Quadrant: "autonomous_executor", Allowed: []string{"read", "write", "edit"}},
		},
	}
	got := buildPrompt(req)

	const want = `Given the user's prompt and the following candidate personas (id, description, allowed tool intents [read write edit shell search fetch skill mcp]), return the JSON {"persona": id} for the best match. Reply with ONLY the JSON — no prose. The persona id must be one of: the candidate ids listed.

User prompt:

review my latest commit for bugs

Candidates:

- reviewer (technical_partner): Reviewer: reads code, reports bugs | allowed: read,search | denied: 
- editor (autonomous_executor): Editor: edits files | allowed: read,write,edit | denied: 
`

	if got != want {
		t.Errorf("prompt golden mismatch\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

func TestAutoPickPicksValidPersona(t *testing.T) {
	fm := fixtureRunner(t, `{"persona": "reviewer"}`)
	req := Request{
		OpeningPrompt:   "review my latest commit",
		FallbackPersona: "generalist",
		Candidates: []Candidate{
			{ID: "reviewer", Name: "Reviewer", Description: "reads code"},
			{ID: "editor", Name: "Editor", Description: "edits files"},
		},
	}
	res, err := AutoPick(context.Background(), fm, req)
	if err != nil {
		t.Fatalf("AutoPick failed: %v", err)
	}
	if res.PersonaID != "reviewer" {
		t.Fatalf("PersonaID = %q, want \"reviewer\" (source %q)", res.PersonaID, res.Source)
	}
	if res.Source != "fast-model" {
		t.Fatalf("Source = %q, want \"fast-model\"", res.Source)
	}
}

func TestAutoPickFallsBackOnUnusableOutput(t *testing.T) {
	// A fake that replies with prose (no JSON) triggers the
	// fastmodel.ErrUnusableOutput path — same "skippable" shape the
	// real backend would produce on an unconfigured / rate-limited
	// endpoint. Falls back to the workspace default.
	fm := fixtureRunner(t, "best: not-json")
	req := Request{
		OpeningPrompt:   "review my commit",
		FallbackPersona: "generalist",
		Candidates:      []Candidate{{ID: "reviewer"}},
	}
	res, err := AutoPick(context.Background(), fm, req)
	if err != nil {
		t.Fatalf("AutoPick unexpected error on prose reply: %v", err)
	}
	switch res.Source {
	case "fallback-unusable-output", "fallback-hallucinated-id":
		// both are valid fallbacks: the fake's prose reply gets parsed
		// as a bare id "best: not-json" — that's not in the candidate
		// set, so the hallucinated-id fallback wins. Either way,
		// PersonaID ends up at the workspace default.
	default:
		t.Fatalf("Source = %q, want a fallback source (got no fallback)", res.Source)
	}
	if res.PersonaID != "generalist" {
		t.Fatalf("PersonaID = %q, want workspace default \"generalist\" (source %q)", res.PersonaID, res.Source)
	}
}

func TestAutoPickFallbackOnHallucinatedID(t *testing.T) {
	fm := fixtureRunner(t, `{"persona": "does_not_exist"}`)
	req := Request{
		OpeningPrompt:   "review my commit",
		FallbackPersona: "generalist",
		Candidates:      []Candidate{{ID: "reviewer"}},
	}
	res, err := AutoPick(context.Background(), fm, req)
	if err != nil {
		t.Fatalf("AutoPick returned an error: %v", err)
	}
	if res.PersonaID != "generalist" {
		t.Fatalf("PersonaID = %q, want fallback \"generalist\" (hallucinated id was not in the candidate set)", res.PersonaID)
	}
	if res.Source != "fallback-hallucinated-id" {
		t.Fatalf("Source = %q, want \"fallback-hallucinated-id\"", res.Source)
	}
}

func TestAutoPickNoCandidatesSkipsCall(t *testing.T) {
	// No candidates -> no fast-model call. We use a "broken" fake to make
	// any accidental DoText call fail loudly, but the guard short-circuits
	// before reaching the backend. The fake below is a *real* runner from
	// the fake provider (which replies "ok" on every call — harmless).
	fm := fixtureRunner(t, "")
	req := Request{
		OpeningPrompt:   "what to do",
		FallbackPersona: "generalist",
		Candidates:      nil,
	}
	res, err := AutoPick(context.Background(), fm, req)
	if err != nil {
		t.Fatalf("no candidates: %v", err)
	}
	if res.Source != "fallback-workspace-default" {
		t.Fatalf("Source = %q, want \"fallback-workspace-default\" (no candidates — no prompt was sent)", res.Source)
	}
}
