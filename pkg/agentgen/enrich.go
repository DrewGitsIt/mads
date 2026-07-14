package agentgen

import (
	"context"
	"fmt"
	"strings"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/providers/fastmodel"
)

const describeSystem = `You write a single short, human-readable sentence describing what a mads ` +
	`agent does, given its full persona prompt. Reply with ONLY the sentence — no prose, no quotes, ` +
	`no code fences.`

const suggestCapabilitiesSystem = `You propose a capability set for a mads agent, given its prompt or ` +
	`description: the smallest sufficient set of tools, MCP servers, and skills. Reply with ONLY a ` +
	`JSON object of this exact shape, no prose, no code fences:

{"tools": [], "mcp_servers": [], "skills": []}`

const refineSystem = `You reword a mads agent's persona prompt per a free-text instruction, preserving ` +
	`its intent and constraints unless the instruction says otherwise. Reply with ONLY the revised ` +
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

// SuggestCapabilities proposes a capability set (tools/mcp servers/skills)
// for a given prompt/description, without touching the registry.
func (g *Generator) SuggestCapabilities(ctx context.Context, prompt string) (contracts.AgentCapabilities, error) {
	var out contracts.AgentCapabilities
	if _, err := g.FastModel.Do(ctx, fastmodel.Job{System: suggestCapabilitiesSystem, Prompt: prompt}, &out); err != nil {
		return contracts.AgentCapabilities{}, fmt.Errorf("agentgen: suggest capabilities: %w", err)
	}
	return out, nil
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
