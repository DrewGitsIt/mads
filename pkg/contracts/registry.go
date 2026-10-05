package contracts

import (
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Quadrant labels an agent's collaboration style (spec §9): the persona
// filter used by auto-selection and the agent picker.
type Quadrant string

const (
	QuadrantAutonomousExecutor Quadrant = "autonomous_executor"
	QuadrantTechnicalPartner   Quadrant = "technical_partner"
	QuadrantInvestigator       Quadrant = "investigator"
	QuadrantResearchAnalyst    Quadrant = "research_analyst"
)

// Quadrants is the full registry, for validation and enumeration.
var Quadrants = []Quadrant{
	QuadrantAutonomousExecutor, QuadrantTechnicalPartner, QuadrantInvestigator, QuadrantResearchAnalyst,
}

// Valid reports whether q is a registered quadrant.
func (q Quadrant) Valid() bool {
	for _, v := range Quadrants {
		if q == v {
			return true
		}
	}
	return false
}

// AgentCapabilities declares what an agent CAN do (spec §9): named MCP
// servers, and named skills from the capability registry. Both are
// already harness-agnostic (a skill is a document, an MCP server is a
// named process/URL) so they stay on the persona as concrete names.
//
// `Tools` is the pre-redesign shape (concrete harness tool names, e.g.
// "Read"/"bash"). It is deprecated: new personas should express the
// same restriction via the persona-level Allowlist/Denylist of
// [ToolIntent], which is harness-agnostic by construction. Readers
// that still consume the old shape (the registry's read-time
// migration, `mads personas` import, the data service's legacy
// deserializer) go through EffectiveToolIntents, which folds the
// deprecated shapes into a single canonical ToolIntent set.
type AgentCapabilities struct {
	Tools      []string `json:"tools,omitempty"` // DEPRECATED: use Agent.Allowlist / Agent.Denylist
	MCPServers []string `json:"mcp_servers,omitempty"`
	Skills     []string `json:"skills,omitempty"`
}

// AgentPermissions declares what an agent CANNOT do (spec §9) — the
// security boundary. AllowedPaths stays on the persona (path scoping
// is a role property, independent of harness).
//
// `DeniedCommands` is the pre-redesign shape (concrete shell command
// strings, e.g. "rm -rf *"). It is deprecated: the persona split
// expresses the same intent-level restriction with [Agent.Denylist]
// containing ToolIntentShell — see docs/AGENT-CONCEPT-REDESIGN.md §2
// and the design doc's Open item 2. EffectiveToolIntents folds the
// two shapes together so a persona created before the split keeps
// enforcing its command-block intent once the data service reads it.
type AgentPermissions struct {
	AllowedPaths   []string `json:"allowed_paths,omitempty"`
	DeniedCommands []string `json:"denied_commands,omitempty"` // DEPRECATED: use Agent.Denylist (ToolIntentShell)
}

// AgentPreferences are soft settings the system honors when possible
// (spec §9: model is a preference, not a hard requirement).
//
// DEPRECATED: a persona does not know which model it will run on; the
// model choice lives on the harness + the session start, per the
// agent-concept redesign. The field is kept only so a legacy record
// deserializes losslessly and a future migration can drop it in a
// single release window. New code must not read it.
type AgentPreferences struct {
	Model string `json:"model,omitempty"` // DEPRECATED: persona is harness-free
}

// Agent is a registry persona definition (spec §9 + the agent-concept
// redesign). It is harness-free by construction: the only field that
// touches a harness vocabulary is the allow/deny set, which is a set of
// ToolIntents (not concrete tool names), and the harness-resolved
// concrete tool mapping is owned by the provider's ToolIntentResolver
// (providers/provider).
//
// Minimum viable persona is a name and a prompt; everything else
// inherits from workspace/session defaults.
type Agent struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"` // registry lookup key: pipeline `agent:` references resolve by name
	Description string    `json:"description,omitempty"`
	Prompt      string    `json:"prompt"`
	Quadrant    Quadrant  `json:"quadrant,omitempty"`
	Tags        []string  `json:"tags,omitempty"`
	System      bool      `json:"system,omitempty"` // ships with mads; editable but not deletable (spec §12)
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`

	// Allowlist & Denylist declare the abstract tool INTENTS this persona
	// may use (docs/AGENT-CONCEPT-REDESIGN.md §2). An empty Allowlist
	// means "no restriction from the persona" (every intent is allowed —
	// the harness is then free to enforce its own policy). An empty
	// Denylist denies nothing at the persona level. Denylist overrides
	// Allowlist: an intent in the Denylist is denied even if it also
	// appears in the Allowlist.
	Allowlist []ToolIntent `json:"allowlist,omitempty"`
	Denylist  []ToolIntent `json:"denylist,omitempty"`

	Capabilities AgentCapabilities `json:"capabilities,omitempty"`
	Permissions  AgentPermissions  `json:"permissions,omitempty"`
	Preferences  AgentPreferences  `json:"preferences,omitempty"`
}

// EffectiveToolIntents returns the canonical ToolIntent policy for this
// persona: the union of the explicit Allowlist/Denylist (post-redesign)
// and the legacy Capabilities.Tools / Permissions.DeniedCommands fields
// (pre-redesign), with Denylist winning on any shared intent.
//
// This is the *single* read path a consumer of the persona should go
// through when deciding "can this persona do intent X" — the runner,
// the conformance suite, `mads personas`, and any future code all use
// it. It exists so we can migrate the shape over a few releases:
// legacy records keep working because their denied_commands entry maps
// to IntentShell deny, and their capabilities.tools entries map to
// their closest ToolIntent allow.
//
// Returns:
//   - allowed: the persona-level allowed intents (empty == allow-all)
//   - denied:  the persona-level denied intents (overrides allowed)
func (a *Agent) EffectiveToolIntents() (allowed, denied []ToolIntent) {
	allowed = uniqueIntents(append([]ToolIntent(nil), a.Allowlist...))
	denied = uniqueIntents(append([]ToolIntent(nil), a.Denylist...))

	// A deny is stronger than an allow on the same intent: drop every
	// explicitly-denied intent from the explicit allow set before we
	// fold in the legacy shapes. (The legacy fold below honors the same
	// invariant when it adds new allows/denies.)
	for _, d := range denied {
		allowed = dropIntent(allowed, d)
	}

	// Legacy folds: each capabilities.tools name becomes an Allowlist
	// entry for its mapped intent, when that intent is not already
	// expressed in the persona's own Allowlist or Denylist.
	for _, name := range a.Capabilities.Tools {
		mapped := LegacyToolToIntent[name]
		if mapped == "" {
			continue
		}
		if hasIntent(allowed, mapped) || hasIntent(denied, mapped) {
			continue
		}
		allowed = append(allowed, mapped)
	}
	// Legacy folds: each denied_commands entry becomes a Denylist entry
	// for IntentShell (the only intent the old `denied_commands` shape
	// could express). A deny wins: strip shell from allowed if it was
	// added via a legacy tool name ("bash") — the user's explicit
	// command block is the stronger statement.
	if len(a.Permissions.DeniedCommands) > 0 {
		if !hasIntent(denied, IntentShell) {
			denied = append(denied, IntentShell)
		}
		allowed = dropIntent(allowed, IntentShell)
	}
	return allowed, denied
}

func hasIntent(set []ToolIntent, i ToolIntent) bool {
	for _, x := range set {
		if x == i {
			return true
		}
	}
	return false
}

func dropIntent(set []ToolIntent, i ToolIntent) []ToolIntent {
	out := set[:0]
	for _, x := range set {
		if x != i {
			out = append(out, x)
		}
	}
	return out
}

// uniqueIntents deduplicates + canonical-ordering a ToolIntent slice
// without allocating a new slice when nothing changes.
func uniqueIntents(in []ToolIntent) []ToolIntent {
	seen := make(map[ToolIntent]bool, len(in))
	out := make([]ToolIntent, 0, len(in))
	for _, i := range in {
		if !i.Valid() || seen[i] {
			continue
		}
		seen[i] = true
		out = append(out, i)
	}
	return out
}

// CapabilityType distinguishes the two capability kinds (spec §12): skills
// (documents that teach) and MCP servers (running tools).
type CapabilityType string

const (
	CapabilitySkill CapabilityType = "skill"
	CapabilityMCP   CapabilityType = "mcp"
)

// CapabilityConfig is the type-specific wiring: Path for skills (the skill
// directory), Command or URL for MCP servers (local process or remote).
type CapabilityConfig struct {
	Path    string `json:"path,omitempty"`
	Command string `json:"command,omitempty"`
	URL     string `json:"url,omitempty"`
}

// CapabilityHealth is a capability's last-known health (spec §12.6):
// Healthy transitions are what drive capability.health_changed events. Reason
// holds only capability/env-var names and static diagnostic text — never a
// credential value (spec §13).
type CapabilityHealth struct {
	Healthy   bool      `json:"healthy"`
	Reason    string    `json:"reason,omitempty"`
	CheckedAt time.Time `json:"checked_at,omitempty"`
}

// Capability is a registry capability (spec §12). Credentials are referenced
// by environment-variable name only — values live in the credential store,
// never here (spec §13).
type Capability struct {
	ID                string           `json:"id"`
	Name              string           `json:"name"`
	Description       string           `json:"description,omitempty"`
	Type              CapabilityType   `json:"type"`
	Config            CapabilityConfig `json:"config"`
	CredentialEnvVars []string         `json:"credential_env_vars,omitempty"`
	Health            CapabilityHealth `json:"health,omitempty"`
	System            bool             `json:"system,omitempty"` // ships with mads; editable but not deletable (spec §12)
	CreatedAt         time.Time        `json:"created_at"`
	UpdatedAt         time.Time        `json:"updated_at"`
}

// Template is a stored pipeline template (spec §6, M9). Source is the YAML
// text — the single artifact behind both editor views (visual and source).
// The parsed, validated form lives in pkg/template; in-flight runs freeze
// their own copy at launch, so edits here never disturb them.
type Template struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	Source      string    `json:"source"`
	System      bool      `json:"system,omitempty"` // ships with mads; editable, not deletable
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	// Version increases by one on every save: 1 for a newly created
	// template, bumped again on each subsequent update. It exists purely as
	// an audit trail — attributing a historical pipeline run (spec §6's
	// pipeline.started event) to the exact template revision it actually
	// executed — not as a correctness mechanism: in-flight runs already hold
	// their own frozen, parsed copy of the template (services/pipeline/engine
	// reads it once at launch), so a save can never disturb them regardless
	// of what Version says.
	Version int `json:"version"`
}

// WorkspaceRepo is one repository a workspace declares (spec §17). Exactly
// one repo is primary — it anchors worktree isolation; the rest are tracked
// only.
type WorkspaceRepo struct {
	Path    string `json:"path"`
	Primary bool   `json:"primary,omitempty"`
}

// AbsPath returns Path with a leading "~" expanded to the user's home
// directory. Repo paths are stored as the user typed them ("~/repo/mads"
// stays home-relative in the DB), but nothing downstream expands ~ — not
// exec.Cmd.Dir, not git -C — so any consumer that touches the filesystem
// must go through this, not Path.
func (r WorkspaceRepo) AbsPath() string {
	return ExpandHome(r.Path)
}

// ExpandHome expands a leading "~" in a user-typed path to the user's home
// directory, leaving every other path as given.
func ExpandHome(path string) string {
	if path == "~" || strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, path[1:])
		}
	}
	return path
}

// WorkspaceDefaults are the settings sessions inherit from their workspace
// (spec §10: workspace overrides global, session overrides workspace).
//
// Per the agent-concept redesign (§7), Persona is the workspace's
// default persona (its `agent` id), and Provider + Model are the
// workspace's preferred harness + model for *auto-pick*, so a workspace
// with defaults set needs no per-session ritual (the "anti-fatigue"
// goal the redesign calls out). New personas/providers/models the
// user explicitly names on the session win over these defaults — the
// default is the FALLBACK, not a hard setting.
type WorkspaceDefaults struct {
	Agent     string `json:"agent,omitempty"`     // persona id (also referred to as default_persona)
	Model     string `json:"model,omitempty"`     // preferred model id (falls back to the harness default)
	Isolation string `json:"isolation,omitempty"` // current_dir | worktree
	Template  string `json:"template,omitempty"`  // pipeline template name
	Provider  string `json:"provider,omitempty"`  // preferred harness (falls back to global DefaultProvider)
	// Persona is the same id as `Agent`, added as an alias under the
	// redesign's vocabulary (docs/AGENT-CONCEPT-REDESIGN.md §7). It is
	// read/written identically to Agent; the JSON field is the single
	// source of truth, and Persona is a Go-side alias for clarity in
	// new code. See SetPersona() for the two-way read.
	Persona string `json:"default_persona,omitempty"`
}

// PersonaID returns the effective persona id (the `Agent` field is the
// single source of truth; `Persona` is the redesign-era alias that
// mirrors it whenever it is non-empty).
func (d WorkspaceDefaults) PersonaID() string {
	if d.Agent != "" {
		return d.Agent
	}
	return d.Persona
}

// SetPersona sets the effective persona id on the canonical `Agent`
// field (the JSON source of truth), so a workspace saved via the new
// Persona-aware UI path and one saved via the old `agent`-field path
// end up equivalent. It also clears the `Persona` alias field to keep
// the two from ever diverging.
func (d *WorkspaceDefaults) SetPersona(id string) {
	if d == nil {
		return
	}
	d.Agent = id
	d.Persona = ""
}

// Workspace bundles repos, defaults, and steering files (spec §17).
type Workspace struct {
	ID            string            `json:"id"`
	Name          string            `json:"name"`
	Description   string            `json:"description,omitempty"`
	Repos         []WorkspaceRepo   `json:"repos,omitempty"`
	Defaults      WorkspaceDefaults `json:"defaults,omitempty"`
	SteeringFiles []string          `json:"steering_files,omitempty"`
	// Capabilities lists registry capability names true of every task in this
	// workspace (owner decision: the ambient-environment baseline — "repos on
	// GitLab" — that a task's own RequiredCapabilities adds to, never
	// subtracts from). Empty means "no workspace-level constraint."
	Capabilities []string  `json:"capabilities,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// PrimaryRepo returns the filesystem path of the workspace's primary repo —
// the one that anchors worktree isolation (spec §17) — and whether one is
// declared at all. The path is home-expanded (AbsPath): callers hand it
// straight to chdir/git.
func (w Workspace) PrimaryRepo() (string, bool) {
	for _, r := range w.Repos {
		if r.Primary {
			return r.AbsPath(), true
		}
	}
	return "", false
}
