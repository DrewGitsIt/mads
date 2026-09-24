// Package personapick implements the agent-concept redesign's persona
// auto-pick (docs/AGENT-CONCEPT-REDESIGN.md §4): at session start, a
// *thinking-disabled one-shot* against the Fast Model picks the best
// persona for the user's opening prompt. The failure path falls back to
// the workspace's default persona, then to no persona (the chip shows
// "none" and the harness's native persona is used, which is a legal
// outcome).
//
// It deliberately does NOT generate a persona when no candidate fits —
// that is `pkg/autostaff`'s job (agent selection *with generation*), and
// persona generation is a *user* concern (the Library editor), not a
// session-start concern. The two are kept distinct by design.
package personapick

import (
	"context"
	"fmt"
	"strings"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/providers/fastmodel"
)

// Candidate is the minimal persona summary the Fast Model prompt needs.
// It is a strict subset of contracts.Agent — deliberately NOT the whole
// struct — so a Fast Model misbehaving on a complex registry record
// (timestamps, tags, etc.) can't break the selection and the prompt is
// stable and testable (golden fixtures in testdata/).
type Candidate struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Quadrant    string   `json:"quadrant,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	Allowed     []string `json:"allowed_intents,omitempty"` // contracts.ToolIntent values (closed set)
	Denied      []string `json:"denied_intents,omitempty"`
}

// Request is one auto-pick call.
type Request struct {
	// OpeningPrompt is the user's opening prompt (the "creative" UX
	// surface from the design doc — the *text* the user typed into the
	// Launchpad's composer). Empty == no fast-model call, fallback only.
	OpeningPrompt string
	// Candidates is the full persona set (workspace default included —
	// the caller is responsible for including it if present).
	Candidates []Candidate
	// FallbackPersona is the persona id to pick when the Fast Model
	// call fails (transport error, timeout, rate-limit, or unusable
	// output — see fastmodel.IsSkippable). Empty means "no persona" —
	// the harness's native persona is what runs.
	FallbackPersona string
}

// Result is the outcome of AutoPick.
type Result struct {
	PersonaID string
	// Source records where the id came from: "fast-model" or
	// "fallback-workspace-default" or "none".
	Source string
}

// Prompt is the *system* prompt for the one-shot. Pinned in testdata/
// (golden) so a refactor that changes the prompt visibly changes a
// golden file.
const Prompt = `Given the user's prompt and the following candidate personas (id, description, allowed tool intents [read write edit shell search fetch skill mcp]), return the JSON {"persona": id} for the best match. Reply with ONLY the JSON — no prose. The persona id must be one of: the candidate ids listed.

User prompt:

%s

Candidates:

%s
`

// buildPrompt renders the system prompt for one auto-pick call.
func buildPrompt(req Request) string {
	var lines []string
	for _, c := range req.Candidates {
		allow, deny := strings.Join(c.Allowed, ","), strings.Join(c.Denied, ",")
		line := fmt.Sprintf("- %s (%s): %s", c.ID, c.Quadrant, oneLine(c.Name, c.Description))
		line += " | allowed: " + allow + " | denied: " + deny
		lines = append(lines, line)
	}
	return fmt.Sprintf(Prompt, strings.TrimSpace(req.OpeningPrompt), strings.Join(lines, "\n"))
}

func oneLine(name, desc string) string {
	if desc == "" {
		return name
	}
	// single line, max 120 chars (the prompt is a one-shot, no multi-
	// line formatting needed from the model side).
	if len(desc) > 120 {
		desc = desc[:117] + "..."
	}
	return name + ": " + desc
}

// AutoPick runs the Fast Model one-shot (req.OpeningPrompt non-empty,
// req.Candidates non-empty) and returns the selected persona. On any
// failure (transport, timeout, rate-limit, unusable output) it falls
// back to req.FallbackPersona. Empty FallbackPersona means: return an
// empty PersonaID (the chip shows "none"; the harness's native persona
// runs). Never returns a non-error for a *skippable* failure (see the
// IsSkippable branch inside) — the caller of this function can rely on
// "either a real error, or a Result whose PersonaID is a legal
// fallback". A non-skippable failure (network drop) IS an error:
// §11.9's "inform and wait, never silent retry" rule holds — the
// caller's caller (the router) logs the failure and proceeds with the
// fallback persona.
func AutoPick(ctx context.Context, fm *fastmodel.Runner, req Request) (Result, error) {
	if req.OpeningPrompt == "" || len(req.Candidates) == 0 {
		return Result{PersonaID: req.FallbackPersona, Source: "fallback-workspace-default"}, nil
	}
	text, _, err := fm.DoText(ctx, fastmodel.Job{System: "You pick a persona. Reply with ONLY JSON of the form {\"persona\": id}.", Prompt: buildPrompt(req)})
	if err != nil {
		if fastmodel.IsSkippable(err) {
			return Result{PersonaID: req.FallbackPersona, Source: "fallback-fastmodel-skippable"}, nil
		}
		return Result{}, fmt.Errorf("personapick: fast model: %w", err)
	}
	id := pickID(text)
	// The prompt is explicit about "ONLY JSON" but a sloppy model can
	// wrap; tolerate a trailing/leading quote, comma, whitespace.
	if id == "" {
		return Result{PersonaID: req.FallbackPersona, Source: "fallback-unusable-output"}, nil
	}
	// Validate the id is one of the candidates — a hallucinated id
	// (the model returning a name we didn't list) is a *hallucination*,
	// not a pick. Fall back to the workspace default.
	for _, c := range req.Candidates {
		if c.ID == id {
			return Result{PersonaID: id, Source: "fast-model"}, nil
		}
	}
	return Result{PersonaID: req.FallbackPersona, Source: "fallback-hallucinated-id"}, nil
}

// pickID extracts a persona id from a fast-model reply. Accepts the
// exact JSON from the prompt ({"persona": "id"}), a bare quoted id, a
// bare bare id, and the "best: id" prose the design doc §4 example used
// (kept for robustness — a model that obeys the prompt is the common
// case, but we tolerate sloppiness on the reply shape rather than
// failing the pick).
func pickID(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	// exact JSON: {"persona": "id"}
	if strings.HasPrefix(s, "{") && strings.Contains(s, "\"persona\"") {
		// crude parse: find "persona" then colon then quote
		idx := strings.Index(s, "\"persona\"")
		if idx < 0 {
			return ""
		}
		rest := s[idx+len("\"persona\":"):]
		rest = strings.TrimSpace(rest)
		// rest begins with a quote + id + quote (possibly followed by ,)
		if len(rest) >= 2 && rest[0] == '"' {
			end := strings.Index(rest[1:], "\"")
			if end > 0 {
				return rest[1 : 1+end]
			}
		}
	}
	// bare quoted id "id"
	if len(s) >= 2 && (s[0] == '"' || s[0] == '\'') && s[len(s)-1] == s[0] {
		return s[1 : len(s)-1]
	}
	// "best: id" prose (design doc §4 example)
	if i := strings.Index(s, "best:"); i >= 0 {
		return strings.TrimSpace(s[i+len("best:"):])
	}
	// bare id, single token
	s = strings.Trim(s, " \t\r\n,")
	if !strings.ContainsAny(s, " \t\r\n,{}") {
		return s
	}
	return ""
}

// CandidateFromAgent is the adapter that converts a registry persona
// (contracts.Agent) to the minimal Candidate the one-shot prompt needs.
// The prompt stays bounded and testable; the adapter keeps the two
// from drifting apart.
func CandidateFromAgent(a contracts.Agent) Candidate {
	allowed, denied := a.EffectiveToolIntents()
	var allowedIntents, deniedIntents []string
	for _, i := range allowed {
		allowedIntents = append(allowedIntents, string(i))
	}
	for _, i := range denied {
		deniedIntents = append(deniedIntents, string(i))
	}
	return Candidate{
		ID:          a.ID,
		Name:        a.Name,
		Description: a.Description,
		Quadrant:    string(a.Quadrant),
		Tags:        a.Tags,
		Allowed:     allowedIntents,
		Denied:      deniedIntents,
	}
}
