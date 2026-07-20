package contracts

import "time"

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

// AgentCapabilities declares what an agent CAN do (spec §9): named tools,
// MCP servers, and skills from the capability registry.
type AgentCapabilities struct {
	Tools      []string `json:"tools,omitempty"`
	MCPServers []string `json:"mcp_servers,omitempty"`
	Skills     []string `json:"skills,omitempty"`
}

// AgentPermissions declares what an agent CANNOT do (spec §9) — the security
// boundary.
type AgentPermissions struct {
	AllowedPaths   []string `json:"allowed_paths,omitempty"`
	DeniedCommands []string `json:"denied_commands,omitempty"`
}

// AgentPreferences are soft settings the system honors when possible
// (spec §9: model is a preference, not a hard requirement).
type AgentPreferences struct {
	Model string `json:"model,omitempty"`
}

// Agent is a registry agent definition (spec §9): a prompt plus capability
// list plus optional constraints. The minimum viable agent is a name and a
// prompt; everything else inherits from workspace/session defaults.
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

	Capabilities AgentCapabilities `json:"capabilities,omitempty"`
	Permissions  AgentPermissions  `json:"permissions,omitempty"`
	Preferences  AgentPreferences  `json:"preferences,omitempty"`
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
}

// WorkspaceRepo is one repository a workspace declares (spec §17). Exactly
// one repo is primary — it anchors worktree isolation; the rest are tracked
// only.
type WorkspaceRepo struct {
	Path    string `json:"path"`
	Primary bool   `json:"primary,omitempty"`
}

// WorkspaceDefaults are the settings sessions inherit from their workspace
// (spec §10: workspace overrides global, session overrides workspace).
type WorkspaceDefaults struct {
	Agent     string `json:"agent,omitempty"`
	Model     string `json:"model,omitempty"`
	Isolation string `json:"isolation,omitempty"` // current_dir | worktree
	Template  string `json:"template,omitempty"`  // pipeline template name
}

// Workspace bundles repos, defaults, and steering files (spec §17).
type Workspace struct {
	ID            string            `json:"id"`
	Name          string            `json:"name"`
	Description   string            `json:"description,omitempty"`
	Repos         []WorkspaceRepo   `json:"repos,omitempty"`
	Defaults      WorkspaceDefaults `json:"defaults,omitempty"`
	SteeringFiles []string          `json:"steering_files,omitempty"`
	CreatedAt     time.Time         `json:"created_at"`
	UpdatedAt     time.Time         `json:"updated_at"`
}

// PrimaryRepo returns the filesystem path of the workspace's primary repo —
// the one that anchors worktree isolation (spec §17) — and whether one is
// declared at all.
func (w Workspace) PrimaryRepo() (string, bool) {
	for _, r := range w.Repos {
		if r.Primary {
			return r.Path, true
		}
	}
	return "", false
}
