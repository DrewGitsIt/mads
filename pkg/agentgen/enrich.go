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

const suggestCapabilitiesSystem = `You propose a capability set for a mads agent, given its prompt or ` +
	`description: the smallest sufficient set of tools, MCP servers, and skills. tools may name any ` +
	`general-purpose provider tool freely (e.g. shell, read, write). mcp_servers and skills MUST each ` +
	`be chosen only from the "Available skills" / "Available MCP servers" lists given in the request ` +
	`below — never invent a name that isn't listed; leave the list empty if nothing available fits. ` +
	`Reply with ONLY a JSON object of this exact shape, no prose, no code fences:

{"tools": [], "mcp_servers": [], "skills": []}`

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

// Suggestion is SuggestCapabilities' reply: provider tool names plus
// registry-grounded skill and MCP server names. Dropped names any skill or
// MCP server the model emitted that isn't in the registry — removed from the
// suggestion so it can't fail the save, but reported so the user sees it.
type Suggestion struct {
	Tools      []string `json:"tools,omitempty"`
	MCPServers []string `json:"mcp_servers,omitempty"`
	Skills     []string `json:"skills,omitempty"`
	Dropped    []string `json:"dropped,omitempty"`
}

// SuggestCapabilities proposes a capability set for a given
// prompt/description, grounded in the live capability registry, without
// touching it. Tools are provider tool names, not registry entries, and pass
// through unvalidated (as in generation).
func (g *Generator) SuggestCapabilities(ctx context.Context, prompt string) (Suggestion, error) {
	caps, err := g.availableCapabilities(ctx)
	if err != nil {
		return Suggestion{}, err
	}
	knownSkills, knownMCP := capabilityNames(caps)

	var out contracts.AgentCapabilities
	job := fastmodel.Job{System: suggestCapabilitiesSystem, Prompt: prompt + registrySection(caps)}
	if _, err := g.FastModel.Do(ctx, job, &out); err != nil {
		return Suggestion{}, fmt.Errorf("agentgen: suggest capabilities: %w", err)
	}

	skills, droppedSkills := knownOnly(out.Skills, knownSkills)
	mcpServers, droppedMCP := knownOnly(out.MCPServers, knownMCP)
	dropped := append(droppedSkills, droppedMCP...)
	if len(dropped) > 0 {
		log.Printf("agentgen: suggest: dropped capability reference(s) absent from the registry: %s", strings.Join(dropped, ", "))
	}
	return Suggestion{Tools: out.Tools, MCPServers: mcpServers, Skills: skills, Dropped: dropped}, nil
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
