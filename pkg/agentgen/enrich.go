package agentgen

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/providers/fastmodel"
)

const describeSystem = `You write a single short, human-readable sentence describing what a mads ` +
	`agent does, given its full persona prompt. Reply with ONLY the sentence — no prose, no quotes, ` +
	`no code fences.`

const suggestCapabilitiesSystem = `You propose a capability set for a mads persona, given its prompt ` +
	`or description: the smallest sufficient set of tool intents, MCP servers, and skills. allowlist ` +
	`names values from the closed ToolIntent set only: read, write, edit, shell, search, fetch, skill, ` +
	`mcp — never a concrete tool name. mcp_servers and skills MUST each be chosen only from the ` +
	`"Available skills" / "Available MCP servers" lists given in the request below — never invent a ` +
	`name that isn't listed; leave the list empty if nothing available fits. Reply with ONLY a JSON ` +
	`object of this exact shape, no prose, no code fences:

{"allowlist": [], "mcp_servers": [], "skills": []}`

const refineSystem = `You reword a mads agent's persona prompt per a free-text instruction, preserving ` +
	`its intent and constraints unless the instruction says otherwise. If the prompt is organised ` +
	`under the Role: / Approach: / Boundaries: / Output: headings, keep those headings. Reply with ONLY the revised ` +
	`prompt text — no prose, no quotes, no code fences.`

// Describe drafts a short human-readable description from an existing
// agent's prompt, for the Library editor's "describe this agent" action.
func (g *Generator) Describe(ctx context.Context, prompt string) (string, error) {
	text, _, err := g.FastModel.DoText(ctx, fastmodel.Job{System: describeSystem, Prompt: prompt})
	if err != nil {
		return "", fmt.Errorf("agentgen: describe: %w", err)
	}
	return strings.TrimSpace(text), nil
}

// Suggestion is SuggestCapabilities' reply: registry-grounded skill and MCP
// server names plus a ToolIntent allowlist. Dropped names anything the model
// emitted that isn't in the registry (or the intent vocabulary) — removed
// from the suggestion so it can't fail the save, but reported so the user
// sees it.
type Suggestion struct {
	Allowlist  []contracts.ToolIntent `json:"allowlist,omitempty"`
	MCPServers []string               `json:"mcp_servers,omitempty"`
	Skills     []string               `json:"skills,omitempty"`
	Dropped    []string               `json:"dropped,omitempty"`
}

// SuggestCapabilities proposes a capability set for a given
// prompt/description, grounded in the live capability registry, without
// touching it. It no longer proposes the deprecated concrete-name
// capabilities.tools: tool allowance is the ToolIntent allowlist.
func (g *Generator) SuggestCapabilities(ctx context.Context, prompt string) (Suggestion, error) {
	caps, err := g.availableCapabilities(ctx)
	if err != nil {
		return Suggestion{}, err
	}
	knownSkills, knownMCP := capabilityNames(caps)

	var out struct {
		Allowlist  []contracts.ToolIntent `json:"allowlist"`
		MCPServers []string               `json:"mcp_servers"`
		Skills     []string               `json:"skills"`
	}
	job := fastmodel.Job{System: suggestCapabilitiesSystem, Prompt: prompt + registrySection(caps)}
	if _, err := g.FastModel.Do(ctx, job, &out); err != nil {
		return Suggestion{}, fmt.Errorf("agentgen: suggest capabilities: %w", err)
	}

	skills, droppedSkills := knownOnly(out.Skills, knownSkills)
	mcpServers, droppedMCP := knownOnly(out.MCPServers, knownMCP)
	dropped := append(droppedSkills, droppedMCP...)
	for _, i := range out.Allowlist {
		if !i.Valid() {
			dropped = append(dropped, string(i))
		}
	}
	if len(dropped) > 0 {
		log.Printf("agentgen: suggest: dropped reference(s) absent from the registry: %s", strings.Join(dropped, ", "))
	}
	return Suggestion{
		Allowlist:  validIntentsOnly(out.Allowlist),
		MCPServers: mcpServers,
		Skills:     skills,
		Dropped:    dropped,
	}, nil
}

// Refine rewords a prompt per free-text instructions (e.g. "make it more
// concise", "add a constraint about never touching prod").
func (g *Generator) Refine(ctx context.Context, prompt, instruction string) (string, error) {
	refinePrompt := fmt.Sprintf("Prompt:\n%s\n\nInstruction: %s", prompt, instruction)
	text, _, err := g.FastModel.DoText(ctx, fastmodel.Job{System: refineSystem, Prompt: refinePrompt})
	if err != nil {
		return "", fmt.Errorf("agentgen: refine: %w", err)
	}
	return strings.TrimSpace(text), nil
}
