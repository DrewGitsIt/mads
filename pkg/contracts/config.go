package contracts

import "net/url"

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

// StreamSpeed caps how fast the conversation renders streamed text, decoupled
// from the model's raw delivery rate (a "typewriter" smoothing). "off" renders
// as tokens arrive; slow/medium/fast target ~10/20/30 tok/s.
type StreamSpeed string

const (
	StreamSpeedOff    StreamSpeed = "off"
	StreamSpeedSlow   StreamSpeed = "slow"
	StreamSpeedMedium StreamSpeed = "medium"
	StreamSpeedFast   StreamSpeed = "fast"
)

// StreamSpeeds is the full registry, for validation and enumeration.
var StreamSpeeds = []StreamSpeed{StreamSpeedOff, StreamSpeedSlow, StreamSpeedMedium, StreamSpeedFast}

// Valid reports whether s is a registered stream speed.
func (s StreamSpeed) Valid() bool {
	for _, v := range StreamSpeeds {
		if s == v {
			return true
		}
	}
	return false
}

// EnvMode controls how much of the process environment a spawned agent CLI
// sees (env_mode, distinct from trust — trust governs what an agent can DO,
// env_mode governs what it can SEE). inherit is the pre-existing behavior:
// the agent gets the full environment of whatever spawned it. isolated
// restricts it to a curated base allowlist plus the profile's bound mads
// credentials plus its EnvPassthrough names — see pkg/agentenv, the single
// shared construction helper both spawn sites use.
type EnvMode string

const (
	// EnvModeInherit is also the zero value, so an unset EnvMode behaves
	// exactly like today's env handling with no migration needed.
	EnvModeInherit  EnvMode = "inherit"
	EnvModeIsolated EnvMode = "isolated"
)

// EnvModes is the full registry, for validation and enumeration.
var EnvModes = []EnvMode{EnvModeInherit, EnvModeIsolated}

// Valid reports whether m is a registered env mode.
func (m EnvMode) Valid() bool {
	for _, v := range EnvModes {
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

	// SynthesisAutoApprove applies every synthesis proposal immediately on
	// creation, skipping manual review (spec §4's "power-user preference").
	// Zero value (false) is the correct default: a fresh install reviews
	// proposals before they become real tasks/notes.
	SynthesisAutoApprove bool `json:"synthesis_auto_approve"`

	// Session defaults (§3.7 General panel).
	DefaultWorkspace string       `json:"default_workspace,omitempty"`
	DefaultProvider  string       `json:"default_provider,omitempty"`
	DefaultTrust     SessionTrust `json:"default_trust"`
	DefaultTemplate  string       `json:"default_template,omitempty"`
	FastModel        string       `json:"fast_model,omitempty"`

	// FastModelBaseURL/FastModelAPIKeyEnv configure the Settings-driven Fast
	// Model backend (spec §11/§13): an OpenAI-compatible endpoint the Router
	// resolves per job spawn, falling back to the -fastmodel-* flags and then
	// to unset (jobs skip silently). FastModelAPIKeyEnv is the credential
	// NAME only — never a value — same pattern as CredentialBindings below.
	FastModelBaseURL   string `json:"fast_model_base_url,omitempty"`
	FastModelAPIKeyEnv string `json:"fast_model_api_key_env,omitempty"`

	// FastModelMode selects the backend tier (see FastModelMode). Empty is a
	// pre-mode-selector install — see FastModelEffectiveMode.
	FastModelMode FastModelMode `json:"fast_model_mode,omitempty"`

	// Appearance (§3.7 General + §3.9 Theme & Palette panels).
	DisplayMode   string      `json:"display_mode"`
	Keybindings   string      `json:"keybindings"`
	Theme         ThemeMode   `json:"theme"`
	Palette       string      `json:"palette"`
	StreamSpeed   StreamSpeed `json:"stream_speed"`
	EditorCommand string      `json:"editor_command,omitempty"`

	// Operational limits (§3.7 General panel, restart-required).
	ConcurrencyLimit    int          `json:"concurrency_limit"`
	SynthesisDebounceMS int          `json:"synthesis_debounce_ms"`
	DigestDebounceMS    int          `json:"digest_debounce_ms"`
	LogRetentionDays    int          `json:"log_retention_days"`
	FeatureFlags        FeatureFlags `json:"feature_flags"`

	// OnboardingComplete gates the first-run wizard (§3.9): false until the
	// wizard's last step finishes.
	OnboardingComplete bool `json:"onboarding_complete"`

	// UpdateFeedURL is the release feed checked on user request (§14.3, W7.5).
	// Empty means the update-check feature is off — no releases exist yet, so
	// an empty default is the honest starting state.
	UpdateFeedURL string `json:"update_feed_url,omitempty"`

	// ActiveProfileID is the currently active profile, empty if none
	// (profiles-v2). Install state, not itself part of any profile snapshot —
	// set by profile activation, alongside the captured fields it restores.
	ActiveProfileID string `json:"active_profile_id,omitempty"`

	// CredentialBindings is the live set of credential NAMES active for this
	// install (profiles-v2, spec §13 — names only, values never cross into
	// the Data Service). Empty/absent means "all stored credentials active"
	// (backward compatible with pre-profiles-v2 installs).
	CredentialBindings []string `json:"credential_bindings,omitempty"`

	// EnvMode controls what a spawned agent's process environment looks
	// like (see EnvMode doc). Empty behaves as EnvModeInherit.
	EnvMode EnvMode `json:"env_mode,omitempty"`

	// EnvPassthrough is the list of global env var NAMES explicitly allowed
	// through to a spawned agent in isolated mode, on top of the base
	// allowlist and bound credentials. Ignored in inherit mode.
	EnvPassthrough []string `json:"env_passthrough,omitempty"`
}

// RestartRequiredFields lists Config JSON field names whose change only
// takes effect after a service restart (profiles-v2 switch UX): used to
// badge a profile switch's restart-required fields separately from the ones
// that apply live.
var RestartRequiredFields = map[string]bool{
	"concurrency_limit":     true,
	"log_retention_days":    true,
	"synthesis_debounce_ms": true,
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
		StreamSpeed: StreamSpeedOff,

		ConcurrencyLimit:    3,
		SynthesisDebounceMS: 2000,
		DigestDebounceMS:    2000,
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
	if c.DefaultTrust != "" && !c.DefaultTrust.Valid() {
		v.Add("default_trust", "must be one of: full, limited, read_only")
	}
	if c.EnvMode != "" && !c.EnvMode.Valid() {
		v.Add("env_mode", "must be one of: inherit, isolated")
	}
	if !c.Theme.Valid() {
		v.Add("theme", "must be one of: dark, light, system")
	}
	if c.StreamSpeed != "" && !c.StreamSpeed.Valid() {
		v.Add("stream_speed", "must be one of: off, slow, medium, fast")
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
	if c.DigestDebounceMS < 0 {
		v.Add("digest_debounce_ms", "must not be negative")
	}
	if c.LogRetentionDays < 1 {
		v.Add("log_retention_days", "must be at least 1")
	}
	if c.UpdateFeedURL != "" {
		u, err := url.Parse(c.UpdateFeedURL)
		if err != nil || !u.IsAbs() || (u.Scheme != "http" && u.Scheme != "https") {
			v.Add("update_feed_url", "must be an absolute http(s) URL")
		}
	}
	if c.FastModelBaseURL != "" {
		u, err := url.Parse(c.FastModelBaseURL)
		if err != nil || !u.IsAbs() || (u.Scheme != "http" && u.Scheme != "https") {
			v.Add("fast_model_base_url", "must be an absolute http(s) URL")
		}
	}
	if c.FastModelMode != "" && !c.FastModelMode.Valid() {
		v.Add("fast_model_mode", "must be one of: provider, endpoint, off")
	}

	return v
}

// FastModelEffectiveMode resolves the mode actually in effect, honoring
// installs from before the three-way mode selector existed: an unset
// FastModelMode defaults to endpoint if FastModelBaseURL was already
// configured (preserving that install's working setup), else provider (the
// new zero-config default — spec: "flips the default from dormant to
// working").
func (c *Config) FastModelEffectiveMode() FastModelMode {
	if c.FastModelMode != "" {
		return c.FastModelMode
	}
	if c.FastModelBaseURL != "" {
		return FastModelModeEndpoint
	}
	return FastModelModeProvider
}
