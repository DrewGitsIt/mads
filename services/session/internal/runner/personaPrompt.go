package runner

import (
	"sort"
	"strings"

	"github.com/drewkidwell/mads/pkg/contracts"
)

// effectivePersonaPrompt returns the persona's system prompt plus, when the
// persona declares a tool allowance (Allowlist/Denylist over the closed
// ToolIntent vocabulary), a short "tool allowance" paragraph stating the
// same policy in language the harness model can act on.
//
// WHY: the persona's Allowlist/Denylist is the persona-level *tool policy*
// (agent-concept redesign §2). On a harness with a native tool-restriction
// surface (claude-code's --disallowedTools, resolved via its
// ToolIntentMap), the runner passes it through provider.ToolIntents and
// the adapter enforces it natively — no prompt needed. On a harness with
// NO native surface (goose, kiro — their IntentsMatrix declares
// Unsupported), the only channel is the system prompt, so the allowance
// must be *spoken* there: a persona that denies "shell" on goose still
// needs the model to *hear* "do not use the shell" or goose's built-in
// shell tool is still callable. This keeps "the persona is the same
// regardless of harness" true even where enforcement is prompt-level.
//
// The paragraph is deliberately short and imperative — it supplements,
// never replaces, the persona's own prompt (which is the identity/voice).
// A zero-value allowance (no Allowlist, no Denylist) adds nothing: the
// harness's native default is the whole story, and the persona's prompt
// is exactly what runs today (preserving the "no persona preamble, ever"
// guarantee for a persona with no tool policy).
func effectivePersonaPrompt(agent contracts.Agent, agentOK bool) string {
	if !agentOK {
		return ""
	}
	prompt := strings.TrimSpace(agent.Prompt)
	allowed, denied := agent.EffectiveToolIntents()
	if len(allowed) == 0 && len(denied) == 0 {
		return prompt
	}
	return prompt + "\n\n" + toolAllowanceParagraph(allowed, denied)
}

// toolAllowanceParagraph renders the persona's tool allowance in closed
// ToolIntent vocabulary (read, write, edit, shell, search, fetch, skill,
// mcp) — never concrete tool names, because those are harness vocabulary
// and the persona must stay harness-free.
func toolAllowanceParagraph(allowed, denied []contracts.ToolIntent) string {
	var b strings.Builder
	b.WriteString("Tool allowance (this persona's policy, applies on any harness): ")
	if len(denied) > 0 {
		b.WriteString("DO NOT use: " + intentList(denied) + ". ")
	}
	if len(allowed) > 0 {
		b.WriteString("Prefer when a task fits: " + intentList(allowed) + ". ")
	}
	b.WriteString("When a requested action falls outside this allowance, say so plainly rather than working around it.")
	return b.String()
}

func intentList(in []contracts.ToolIntent) string {
	sorted := make([]contracts.ToolIntent, len(in))
	copy(sorted, in)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	parts := make([]string, len(sorted))
	for i, v := range sorted {
		parts[i] = string(v)
	}
	return strings.Join(parts, ", ")
}
