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
	"log"
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
//
// The drafted shape expresses the persona's tool allowance as the closed
// ToolIntent vocabulary (allowlist/denylist — agent-concept redesign §2),
// not as harness-specific concrete tool names or command strings. That was
// the single biggest harness leak in the old draft shape
// (`capabilities.tools: ["Bash"]`, `denied_commands: ["rm -rf *"]`); the
// redesign removes it — a persona is harness-free by construction.
const generationSystem = `You are the mads agent-builder assistant. Given a description of a task ` +
	`or a natural-language request, draft a complete persona definition for the mads platform: a ` +
	`persona prompt, a short description, the smallest sufficient set of tool allowances, and matching ` +
	`permissions. allowlist and denylist each name values from the closed ToolIntent set: ` +
	`read, write, edit, shell, search, fetch, skill, mcp — never a concrete tool name or a ` +
	`command string (those are harness vocabulary; the persona is harness-free). denylist ` +
	`overrides allowlist: an intent named in both is denied. capabilities.skills and ` +
	`capabilities.mcp_servers MUST each be chosen only from the "Available skills" / "Available ` +
	`MCP servers" lists given in the request below — never invent a name that isn't listed; ` +
	`leave the list empty if nothing available fits. The persona's system prompt is written as the ` +
	`four sections of the mads prompt scaffold, each addressed to the agent in the second person: ` +
	`role (what it is responsible for), approach (how it works — style, tools it favors, order of ` +
	`operations), boundaries (what it must never do), output (what a finished turn looks like). ` +
	`Reply with ONLY a JSON object of this exact shape, no prose, no code fences:

{
  "name": "kebab-case-identifier",
  "description": "one-sentence summary",
  "prompt_sections": {
    "role": "...",
    "approach": "...",
    "boundaries": "...",
    "output": "..."
  },
  "quadrant": "autonomous_executor" | "technical_partner" | "investigator" | "research_analyst",
  "tags": ["short", "keywords"],
  "allowlist": ["shell", "read", "write"],
  "denylist": [],
  "capabilities": {"mcp_servers": [], "skills": []},
  "permissions": {"allowed_paths": []}
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
	// Dropped names any skill or MCP server the model emitted that isn't in
	// the capability registry — silently dropped from Draft.Capabilities
	// rather than persisted as fiction, but never silently: named here and
	// logged.
	Dropped []string `json:"dropped,omitempty"`
}

// generatedAgent is the Fast Model's structured reply shape for both
// generation paths. Allowlist/Denylist is the persona's tool allowance in
// the closed ToolIntent vocabulary (agent-concept redesign §2); the old
// concrete-name `capabilities.tools` and `permissions.denied_commands` are
// gone from the shape entirely — they were the harness leak.
type generatedAgent struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	// PromptSections is the requested shape; Prompt is accepted as a
	// fallback when a model replies with a flat prompt anyway.
	PromptSections promptSections              `json:"prompt_sections"`
	Prompt         string                      `json:"prompt"`
	Quadrant       string                      `json:"quadrant"`
	Tags           []string                    `json:"tags"`
	Allowlist      []contracts.ToolIntent      `json:"allowlist,omitempty"`
	Denylist       []contracts.ToolIntent      `json:"denylist,omitempty"`
	Capabilities   contracts.AgentCapabilities `json:"capabilities"`
	Permissions    contracts.AgentPermissions  `json:"permissions"`
}

// Validate requires the two fields a usable agent can't do without — a name
// and a prompt (spec §9: "the minimum viable agent is a name and a prompt").
// A validation failure feeds fastmodel.Runner.Do's retry-with-feedback loop.
func (g *generatedAgent) Validate() error {
	var probs []string
	if g.Name == "" {
		probs = append(probs, "name is required")
	}
	if g.PromptSections.empty() && g.Prompt == "" {
		probs = append(probs, "prompt_sections is required")
	} else if g.Prompt == "" && g.PromptSections.Role == "" {
		probs = append(probs, "prompt_sections.role is required")
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
	draft, dropped, err := g.generate(ctx, taskPrompt(req), agentselect.Classify(req))
	if err != nil {
		return Result{}, err
	}
	draft.Capabilities = mergeRequired(draft.Capabilities, req)
	// Task-level required-tools (the pipeline's concrete tool names, pre-
	// redesign) fold into the persona-level allowlist as their ToolIntent
	// equivalents — a required "shell" means the persona must allow shell.
	// Unknown names (not in the closed vocabulary) are ignored: they were
	// harness vocabulary and have no persona-level equivalent.
	if len(req.RequiredTools) > 0 {
		added := 0
		for _, name := range req.RequiredTools {
			if i := contracts.LegacyToolToIntent[name]; i != "" {
				for _, have := range draft.Allowlist {
					if have == i {
						i = ""
					}
				}
				if i != "" {
					draft.Allowlist = append(draft.Allowlist, i)
					added++
				}
			}
		}
		if added > 0 {
			log.Printf("agentgen: folded %d required tool name(s) into the persona allowlist", added)
		}
	}
	return g.withSimilar(ctx, draft, dropped)
}

// mergeRequired forces req's effective required capabilities into caps,
// regardless of whether the Fast Model's own reply declared them (owner
// decision: a generated draft must declare at least the effective required
// set, so it survives its own auto-select capability filter on retry). These
// names are not inventions subject to the registry-grounding drop in
// generate/knownOnly — they arrived via agentselect.EffectiveCapabilities +
// ExpandCapabilities, which only ever emit validated registry names.
func mergeRequired(caps contracts.AgentCapabilities, req agentselect.Request) contracts.AgentCapabilities {
	caps.Tools = unionStrings(caps.Tools, req.RequiredTools)
	caps.MCPServers = unionStrings(caps.MCPServers, req.RequiredMCPServers)
	caps.Skills = unionStrings(caps.Skills, req.RequiredSkills)
	return caps
}

func unionStrings(have, add []string) []string {
	set := make(map[string]struct{}, len(have))
	for _, h := range have {
		set[h] = struct{}{}
	}
	out := append([]string{}, have...)
	for _, a := range add {
		if _, ok := set[a]; ok {
			continue
		}
		set[a] = struct{}{}
		out = append(out, a)
	}
	return out
}

// GenerateFromDescription builds a draft from a free-text NL description —
// the Library editor's "describe what you want" path (spec §9.6).
func (g *Generator) GenerateFromDescription(ctx context.Context, description string) (Result, error) {
	fallback := agentselect.Classify(agentselect.Request{Description: description})
	draft, dropped, err := g.generate(ctx, descriptionPrompt(description), fallback)
	if err != nil {
		return Result{}, err
	}
	return g.withSimilar(ctx, draft, dropped)
}

// generate runs the shared Fast Model call and maps its reply onto a
// contracts.Agent. fallbackQuadrant is used when the model's reply doesn't
// name a registered quadrant, so the draft is immediately usable by
// auto-select's quadrant filter. Every skill/MCP server name the model emits
// is checked against the live capability registry first: known names are
// grounded into the generation prompt, and any name the model invents anyway
// is dropped before the draft is built — agentgen may only ever persist
// capabilities that exist (owner decision), never silently.
func (g *Generator) generate(ctx context.Context, prompt string, fallbackQuadrant contracts.Quadrant) (contracts.Agent, []string, error) {
	caps, err := g.availableCapabilities(ctx)
	if err != nil {
		return contracts.Agent{}, nil, err
	}
	knownSkills, knownMCP := capabilityNames(caps)

	var out generatedAgent
	fullPrompt := prompt + registrySection(caps)
	if _, err := g.FastModel.Do(ctx, fastmodel.Job{System: generationSystem, Prompt: fullPrompt}, &out); err != nil {
		return contracts.Agent{}, nil, fmt.Errorf("agentgen: generate: %w", err)
	}

	quadrant := contracts.Quadrant(out.Quadrant)
	if !quadrant.Valid() {
		quadrant = fallbackQuadrant
	}

	skills, droppedSkills := knownOnly(out.Capabilities.Skills, knownSkills)
	mcpServers, droppedMCP := knownOnly(out.Capabilities.MCPServers, knownMCP)
	dropped := append(droppedSkills, droppedMCP...)
	if len(dropped) > 0 {
		log.Printf("agentgen: dropped capability reference(s) absent from the registry: %s", strings.Join(dropped, ", "))
	}

	return contracts.Agent{
		Name:        out.Name,
		Description: out.Description,
		Prompt:      out.systemPrompt(),
		Quadrant:    quadrant,
		Tags:        out.Tags,
		// Persona-level tool allowance (agent-concept redesign §2): the
		// closed ToolIntent vocabulary, not harness-specific names. Unknown
		// intent names a sloppy model emits are dropped (like the
		// capability-ref drop above) — logged, not silently persisted.
		Allowlist: validIntentsOnly(out.Allowlist),
		Denylist:  validIntentsOnly(out.Denylist),
		Capabilities: contracts.AgentCapabilities{
			MCPServers: mcpServers,
			Skills:     skills,
		},
		Permissions: out.Permissions,
		System:      false, // a generated draft is never a system agent
	}, dropped, nil
}

// validIntentsOnly keeps only values from the closed ToolIntent vocabulary —
// a Fast Model emitting a concrete tool name ("Bash") or an invented value
// is silently filtered here (and named in the log) rather than persisted as
// fiction, matching the knownOnly discipline for capability refs.
func validIntentsOnly(in []contracts.ToolIntent) []contracts.ToolIntent {
	kept := make([]contracts.ToolIntent, 0, len(in))
	for _, i := range in {
		if i.Valid() {
			kept = append(kept, i)
		} else {
			log.Printf("agentgen: dropped unknown tool intent %q (not in the closed set)", i)
		}
	}
	return kept
}

// availableCapabilities fetches the live capability registry, for grounding
// the generation prompt.
func (g *Generator) availableCapabilities(ctx context.Context) ([]contracts.Capability, error) {
	caps, err := g.Data.Capabilities(ctx)
	if err != nil {
		return nil, fmt.Errorf("agentgen: list capabilities: %w", err)
	}
	return caps, nil
}

// capabilityNames splits the registry into skill and MCP server names.
func capabilityNames(caps []contracts.Capability) (skills, mcpServers []string) {
	for _, c := range caps {
		switch c.Type {
		case contracts.CapabilitySkill:
			skills = append(skills, c.Name)
		case contracts.CapabilityMCP:
			mcpServers = append(mcpServers, c.Name)
		}
	}
	return skills, mcpServers
}

// registrySection lists the registry entries the model must choose
// capabilities.skills/mcp_servers from — with their descriptions, so the
// choice is grounded in what each entry does rather than its name alone — or
// says plainly that none exist.
func registrySection(caps []contracts.Capability) string {
	var b strings.Builder
	b.WriteString("\n\nAvailable skills:")
	writeEntries(&b, caps, contracts.CapabilitySkill)
	b.WriteString("\nAvailable MCP servers:")
	writeEntries(&b, caps, contracts.CapabilityMCP)
	return b.String()
}

func writeEntries(b *strings.Builder, caps []contracts.Capability, typ contracts.CapabilityType) {
	n := 0
	for _, c := range caps {
		if c.Type != typ {
			continue
		}
		n++
		b.WriteString("\n- ")
		b.WriteString(c.Name)
		if d := strings.TrimSpace(c.Description); d != "" {
			b.WriteString(": ")
			b.WriteString(d)
		}
	}
	if n == 0 {
		b.WriteString(" none available")
	}
}

// knownOnly splits names into those present in known and those that aren't —
// the latter never reach the persisted draft.
func knownOnly(names, known []string) (kept, dropped []string) {
	knownSet := make(map[string]struct{}, len(known))
	for _, k := range known {
		knownSet[k] = struct{}{}
	}
	for _, n := range names {
		if _, ok := knownSet[n]; ok {
			kept = append(kept, n)
		} else {
			dropped = append(dropped, n)
		}
	}
	return kept, dropped
}

// withSimilar scores draft against the live registry and attaches a warning
// when it lands at or above similarityThreshold.
func (g *Generator) withSimilar(ctx context.Context, draft contracts.Agent, dropped []string) (Result, error) {
	existing, err := g.Data.Agents(ctx)
	if err != nil {
		return Result{}, fmt.Errorf("agentgen: list agents: %w", err)
	}
	return Result{
		Draft:   draft,
		Similar: agentselect.FindSimilar(draft, existing, similarityThreshold),
		Dropped: dropped,
	}, nil
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
