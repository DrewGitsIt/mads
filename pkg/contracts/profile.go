package contracts

import "time"

// ProfileSettings is the subset of Config a profile can override (MADS-UX
// §10.5). Every field is the zero value when unset — activation merges only
// the non-zero fields onto the current Config, leaving the rest untouched.
type ProfileSettings struct {
	SynthesisMode      SynthesisMode      `json:"synthesis_mode,omitempty"`
	AgentSelectionMode AgentSelectionMode `json:"agent_selection_mode,omitempty"`
	DefaultModelTier   ModelTier          `json:"default_model_tier,omitempty"`
	DefaultWorkspace   string             `json:"default_workspace,omitempty"`
	DefaultProvider    string             `json:"default_provider,omitempty"`
	DefaultTrust       SessionTrust       `json:"default_trust,omitempty"`
	DefaultTemplate    string             `json:"default_template,omitempty"`
	FastModel          string             `json:"fast_model,omitempty"`
	ConcurrencyLimit   int                `json:"concurrency_limit,omitempty"`
}

// Profile is a named, switchable bundle of settings overrides (MADS-UX
// §10.5): activating one merges its non-zero ProfileSettings fields onto the
// global Config.
type Profile struct {
	ID          string          `json:"id"`
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Settings    ProfileSettings `json:"settings"`
	System      bool            `json:"system,omitempty"` // ships with mads; editable, not deletable
	CreatedAt   time.Time       `json:"created_at"`
	UpdatedAt   time.Time       `json:"updated_at"`
}
