package agentgen

import "strings"

// The mads prompt scaffold. Keep the headings in step with the Library
// editor's guidance (ui/src/library/agents/PromptScaffold.tsx): a generated
// prompt should look like one the user was told to write.
var scaffoldHeadings = [...]string{"Role", "Approach", "Boundaries", "Output"}

// promptSections is the Fast Model's structured form of a persona prompt,
// one field per scaffold heading. Asking for the sections as JSON fields —
// rather than asking for a prompt "in scaffold format" — makes the structure
// a property of the parser, not of the model's obedience.
type promptSections struct {
	Role       string `json:"role"`
	Approach   string `json:"approach"`
	Boundaries string `json:"boundaries"`
	Output     string `json:"output"`
}

func (p promptSections) empty() bool {
	return p.Role == "" && p.Approach == "" && p.Boundaries == "" && p.Output == ""
}

// render assembles the sections under their scaffold headings, skipping any
// the model left blank.
func (p promptSections) render() string {
	bodies := [...]string{p.Role, p.Approach, p.Boundaries, p.Output}
	var parts []string
	for i, body := range bodies {
		if body = strings.TrimSpace(body); body != "" {
			parts = append(parts, scaffoldHeadings[i]+": "+body)
		}
	}
	return strings.Join(parts, "\n\n")
}

// systemPrompt prefers the structured sections, falling back to a flat
// prompt when that is all the model returned.
func (g *generatedAgent) systemPrompt() string {
	if !g.PromptSections.empty() {
		return g.PromptSections.render()
	}
	return g.Prompt
}
