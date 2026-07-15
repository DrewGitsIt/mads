package contracts

// AgentSelectionMode controls how a session picks its agent (HANDOFF §2.1 /
// docs/REBUILD-SPEC.md §10): light does minimal agent-matching, full runs
// the complete selection pipeline.
type AgentSelectionMode string

const (
	AgentSelectionLight AgentSelectionMode = "light"
	AgentSelectionFull  AgentSelectionMode = "full"
)

// AgentSelectionModes is the full registry, for validation and enumeration.
var AgentSelectionModes = []AgentSelectionMode{AgentSelectionLight, AgentSelectionFull}

// Valid reports whether m is a registered agent selection mode.
func (m AgentSelectionMode) Valid() bool {
	for _, v := range AgentSelectionModes {
		if m == v {
			return true
		}
	}
	return false
}

// ModelTier is the default cost/capability tier new sessions bind to
// (HANDOFF §2.1 / docs/REBUILD-SPEC.md §10) absent a more specific override.
type ModelTier string

const (
	ModelTierCheapest ModelTier = "cheapest"
	ModelTierBalanced ModelTier = "balanced"
	ModelTierBest     ModelTier = "best"
)

// ModelTiers is the full registry, for validation and enumeration.
var ModelTiers = []ModelTier{ModelTierCheapest, ModelTierBalanced, ModelTierBest}

// Valid reports whether t is a registered model tier.
func (t ModelTier) Valid() bool {
	for _, v := range ModelTiers {
		if t == v {
			return true
		}
	}
	return false
}

// ThemeMode is the display theme (MADS-UX §3.9 Theme & Palette panel):
// dark/light are explicit choices, system follows the OS preference.
type ThemeMode string

const (
	ThemeDark   ThemeMode = "dark"
	ThemeLight  ThemeMode = "light"
	ThemeSystem ThemeMode = "system"
)

// ThemeModes is the full registry, for validation and enumeration.
var ThemeModes = []ThemeMode{ThemeDark, ThemeLight, ThemeSystem}

// Valid reports whether m is a registered theme mode.
func (m ThemeMode) Valid() bool {
	for _, v := range ThemeModes {
		if m == v {
			return true
		}
	}
	return false
}

// Palettes is the full registry of design-system palettes (data-palette
// values the tokens stylesheet defines), for validation and enumeration.
var Palettes = []string{"clay", "space", "glacier"}

// ValidPalette reports whether p is a registered palette.
func ValidPalette(p string) bool {
	for _, v := range Palettes {
		if p == v {
			return true
		}
	}
	return false
}

// FeatureFlags are opt-in behaviors gated behind the Advanced settings panel
// (MADS-UX §3.7) — off by default since their supporting UI/backend is
// partial or absent.
type FeatureFlags struct {
	MultiRepo      bool `json:"multi_repo"`
	LinkedSessions bool `json:"linked_sessions"`
	RemoteRegistry bool `json:"remote_registry"`
}

// Config is the single-row global application configuration (MADS-UX §3.7
// Settings): pipeline/synthesis defaults, session defaults, appearance, and
// operational limits. One row per install — see
// services/data/internal/store/config.go.
type Config struct {
	// Pipeline & synthesis (§3.7 Pipeline & synthesis panel).
	SynthesisMode      SynthesisMode      `json:"synthesis_mode"`
	AgentSelectionMode AgentSelectionMode `json:"agent_selection_mode"`
	DefaultModelTier   ModelTier          `json:"default_model_tier"`
	CostAlertCents     int                `json:"cost_alert_cents,omitempty"`

	// Session defaults (§3.7 General panel).
	DefaultWorkspace string       `json:"default_workspace,omitempty"`
	DefaultProvider  string       `json:"default_provider,omitempty"`
	DefaultTrust     SessionTrust `json:"default_trust"`
	DefaultTemplate  string       `json:"default_template,omitempty"`
	FastModel        string       `json:"fast_model,omitempty"`

	// Appearance (§3.7 General + §3.9 Theme & Palette panels).
	DisplayMode   string    `json:"display_mode"`
	Keybindings   string    `json:"keybindings"`
	Theme         ThemeMode `json:"theme"`
	Palette       string    `json:"palette"`
	EditorCommand string    `json:"editor_command,omitempty"`

	// Operational limits (§3.7 General panel, restart-required).
	ConcurrencyLimit    int          `json:"concurrency_limit"`
	SynthesisDebounceMS int          `json:"synthesis_debounce_ms"`
	LogRetentionDays    int          `json:"log_retention_days"`
	FeatureFlags        FeatureFlags `json:"feature_flags"`

	// OnboardingComplete gates the first-run wizard (§3.9): false until the
	// wizard's last step finishes.
	OnboardingComplete bool `json:"onboarding_complete"`
}

// DefaultConfig is the config a fresh install starts with, before the
// first-run wizard runs.
func DefaultConfig() Config {
	return Config{
		SynthesisMode:      SynthesisLight,
		AgentSelectionMode: AgentSelectionLight,
		DefaultModelTier:   ModelTierBalanced,

		// TrustLimited, not TrustFull — HANDOFF §2.1: a fresh install
		// shouldn't default new sessions to unattended full-trust execution.
		DefaultTrust: TrustLimited,

		DisplayMode: "comfortable",
		Keybindings: "default",
		Theme:       ThemeDark,
		Palette:     "clay",

		ConcurrencyLimit:    3,
		SynthesisDebounceMS: 2000,
		LogRetentionDays:    30,
		FeatureFlags:        FeatureFlags{},

		OnboardingComplete: false,
	}
}

// ValidateConfig checks every field and reports every failure together
// (spec §10.3's "report every error at once" — the Settings validate-all
// acceptance criterion), rather than stopping at the first problem.
func ValidateConfig(c *Config) *ValidationError {
	v := &ValidationError{}

	if !c.SynthesisMode.Valid() {
		v.Add("synthesis_mode", "must be one of: off, light, full")
	}
	if !c.AgentSelectionMode.Valid() {
		v.Add("agent_selection_mode", "must be one of: light, full")
	}
	if !c.DefaultModelTier.Valid() {
		v.Add("default_model_tier", "must be one of: cheapest, balanced, best")
	}
	if c.CostAlertCents < 0 {
		v.Add("cost_alert_cents", "must not be negative")
	}
	if c.DefaultTrust != "" && c.DefaultTrust != TrustFull && c.DefaultTrust != TrustLimited && c.DefaultTrust != TrustReadOnly {
		v.Add("default_trust", "must be one of: full, limited, read_only")
	}
	if !c.Theme.Valid() {
		v.Add("theme", "must be one of: dark, light, system")
	}
	if c.Palette != "" && !ValidPalette(c.Palette) {
		v.Add("palette", "must be one of: clay, space, glacier")
	}
	if c.ConcurrencyLimit < 1 {
		v.Add("concurrency_limit", "must be at least 1")
	}
	if c.SynthesisDebounceMS < 0 {
		v.Add("synthesis_debounce_ms", "must not be negative")
	}
	if c.LogRetentionDays < 1 {
		v.Add("log_retention_days", "must be at least 1")
	}

	return v
}
