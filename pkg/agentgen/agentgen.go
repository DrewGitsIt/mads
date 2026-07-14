// Package agentgen builds draft agent definitions on demand (spec §9.5–9.6):
// task context or a natural-language description in, a full draft
// contracts.Agent out (persona prompt, smallest sufficient capability set,
// matching permissions). Generation is ephemeral by design — the draft is
// only ever returned to the caller, never written to the registry. Every
// draft is also scored against the existing registry (pkg/agentselect's
// similarity helper) so a near-duplicate request surfaces a warning instead
// of silently minting a twin (spec §9's "avoid unnecessary proliferation").
package agentgen

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/drewkidwell/mads/pkg/agentselect"
	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/providers/fastmodel"
)

// similarityThreshold is the score at/above which a draft is flagged as a
// near-duplicate of an existing agent (matches agentselect.FindSimilar's
// documented starting point).
const similarityThreshold = 0.6

// generationSystem is the fixed, non-user-visible persona-generation prompt
// (spec §9.5: "the generation prompt is not user-visible... but the
// resulting agent definition is fully viewable and editable"). Both
// generation paths share it — only the user-supplied context differs.
const generationSystem = `You are the mads agent-builder assistant. Given a description of a task ` +
	`or a natural-language request, draft a complete agent definition for the mads platform: a ` +
	`persona prompt, a short description, the smallest sufficient set of capabilities, and matching ` +
	`permissions. Reply with ONLY a JSON object of this exact shape, no prose, no code fences:

{
  "name": "kebab-case-identifier",
  "description": "one-sentence summary",
  "prompt": "the agent's full persona/system prompt",
  "quadrant": "autonomous_executor" | "technical_partner" | "investigator" | "research_analyst",
  "tags": ["short", "keywords"],
  "capabilities": {"tools": [], "mcp_servers": [], "skills": []},
  "permissions": {"allowed_paths": [], "denied_commands": []}
}`

// Generator runs agent generation and enrichment against a live Data
// Service and Fast Model — mirrors agentselect.Selector's shape.
type Generator struct {
	Data      *dataclient.Client
	FastModel *fastmodel.Runner
}

// New builds a Generator.
func New(data *dataclient.Client, fm *fastmodel.Runner) *Generator {
	return &Generator{Data: data, FastModel: fm}
}

// Result is what both generation paths and the AgentDraftGenerated event
// carry.
type Result struct {
	Draft   contracts.Agent      `json:"draft"`
	Similar *agentselect.Similar `json:"similar,omitempty"`
}

// generatedAgent is the Fast Model's structured reply shape for both
// generation paths.
type generatedAgent struct {
	Name         string                      `json:"name"`
	Description  string                      `json:"description"`
	Prompt       string                      `json:"prompt"`
	Quadrant     string                      `json:"quadrant"`
	Tags         []string                    `json:"tags"`
	Capabilities contracts.AgentCapabilities `json:"capabilities"`
	Permissions  contracts.AgentPermissions  `json:"permissions"`
}

// Validate requires the two fields a usable agent can't do without — a name
// and a prompt (spec §9: "the minimum viable agent is a name and a prompt").
// A validation failure feeds fastmodel.Runner.Do's retry-with-feedback loop.
func (g *generatedAgent) Validate() error {
	var probs []string
	if g.Name == "" {
		probs = append(probs, "name is required")
	}
	if g.Prompt == "" {
		probs = append(probs, "prompt is required")
	}
	if len(probs) > 0 {
		return errors.New(strings.Join(probs, "; "))
	}
	return nil
}

// GenerateFromTask builds a draft from task context — the auto-select
// fallback path (spec §9.5): auto-select finds no match, so a fresh agent is
// drafted for this specific task.
func (g *Generator) GenerateFromTask(ctx context.Context, req agentselect.Request) (Result, error) {
	draft, err := g.generate(ctx, taskPrompt(req), agentselect.Classify(req))
	if err != nil {
		return Result{}, err
	}
	return g.withSimilar(ctx, draft)
}

// GenerateFromDescription builds a draft from a free-text NL description —
// the Library editor's "describe what you want" path (spec §9.6).
func (g *Generator) GenerateFromDescription(ctx context.Context, description string) (Result, error) {
	fallback := agentselect.Classify(agentselect.Request{Description: description})
	draft, err := g.generate(ctx, descriptionPrompt(description), fallback)
	if err != nil {
		return Result{}, err
	}
	return g.withSimilar(ctx, draft)
}

// generate runs the shared Fast Model call and maps its reply onto a
// contracts.Agent. fallbackQuadrant is used when the model's reply doesn't
// name a registered quadrant, so the draft is immediately usable by
// auto-select's quadrant filter.
func (g *Generator) generate(ctx context.Context, prompt string, fallbackQuadrant contracts.Quadrant) (contracts.Agent, error) {
	var out generatedAgent
	if _, err := g.FastModel.Do(ctx, fastmodel.Job{System: generationSystem, Prompt: prompt}, &out); err != nil {
		return contracts.Agent{}, fmt.Errorf("agentgen: generate: %w", err)
	}

	quadrant := contracts.Quadrant(out.Quadrant)
	if !quadrant.Valid() {
		quadrant = fallbackQuadrant
	}

	return contracts.Agent{
		Name:         out.Name,
		Description:  out.Description,
		Prompt:       out.Prompt,
		Quadrant:     quadrant,
		Tags:         out.Tags,
		Capabilities: out.Capabilities,
		Permissions:  out.Permissions,
		System:       false, // a generated draft is never a system agent
	}, nil
}

// withSimilar scores draft against the live registry and attaches a warning
// when it lands at or above similarityThreshold.
func (g *Generator) withSimilar(ctx context.Context, draft contracts.Agent) (Result, error) {
	existing, err := g.Data.Agents(ctx)
	if err != nil {
		return Result{}, fmt.Errorf("agentgen: list agents: %w", err)
	}
	return Result{Draft: draft, Similar: agentselect.FindSimilar(draft, existing, similarityThreshold)}, nil
}

// taskPrompt embeds the caller's title/description verbatim so tests (and,
// in production, the Fast Model) can key off stable, unmodified task text.
func taskPrompt(req agentselect.Request) string {
	var b strings.Builder
	b.WriteString("Generate an agent definition for this task.\n\n")
	fmt.Fprintf(&b, "Title: %s\n", req.Title)
	if req.Description != "" {
		fmt.Fprintf(&b, "Description: %s\n", req.Description)
	}
	if len(req.Labels) > 0 {
		fmt.Fprintf(&b, "Labels: %s\n", strings.Join(req.Labels, ", "))
	}
	if len(req.RequiredTools) > 0 {
		fmt.Fprintf(&b, "Required tools: %s\n", strings.Join(req.RequiredTools, ", "))
	}
	if len(req.RequiredMCPServers) > 0 {
		fmt.Fprintf(&b, "Required MCP servers: %s\n", strings.Join(req.RequiredMCPServers, ", "))
	}
	if len(req.RequiredSkills) > 0 {
		fmt.Fprintf(&b, "Required skills: %s\n", strings.Join(req.RequiredSkills, ", "))
	}
	return b.String()
}

// descriptionPrompt embeds the caller's free-text description verbatim.
func descriptionPrompt(description string) string {
	return "Generate an agent definition for this natural-language request.\n\n" + description
}
