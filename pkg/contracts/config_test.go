package contracts

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDefaultConfigValid(t *testing.T) {
	c := DefaultConfig()
	if verr := ValidateConfig(&c); !verr.Empty() {
		t.Fatalf("DefaultConfig() failed its own validation: %+v", verr.Errors)
	}
}

func TestValidateConfigReportsEveryProblem(t *testing.T) {
	c := Config{
		SynthesisMode:       "bogus",
		AgentSelectionMode:  "bogus",
		DefaultModelTier:    "bogus",
		CostAlertCents:      -1,
		DefaultTrust:        "bogus",
		Theme:               "bogus",
		Palette:             "bogus",
		ConcurrencyLimit:    0,
		SynthesisDebounceMS: -1,
		LogRetentionDays:    0,
		UpdateFeedURL:       "not-a-url",
		EnvMode:             "bogus",
	}
	verr := ValidateConfig(&c)
	if verr.Empty() {
		t.Fatal("expected validation failures")
	}
	want := 12
	if len(verr.Errors) != want {
		t.Fatalf("got %d errors, want %d (report every problem at once): %+v", len(verr.Errors), want, verr.Errors)
	}
}

func TestValidateConfigUpdateFeedURL(t *testing.T) {
	base := DefaultConfig()

	base.UpdateFeedURL = ""
	if verr := ValidateConfig(&base); !verr.Empty() {
		t.Fatalf("empty update_feed_url should be valid (feature off): %+v", verr.Errors)
	}

	base.UpdateFeedURL = "https://example.com/feed.json"
	if verr := ValidateConfig(&base); !verr.Empty() {
		t.Fatalf("absolute https URL should be valid: %+v", verr.Errors)
	}

	base.UpdateFeedURL = "ftp://example.com/feed.json"
	if verr := ValidateConfig(&base); verr.Empty() {
		t.Fatal("non-http(s) scheme should be invalid")
	}

	base.UpdateFeedURL = "not-a-url"
	if verr := ValidateConfig(&base); verr.Empty() {
		t.Fatal("relative/non-absolute URL should be invalid")
	}
}

func TestValidateConfigEnvMode(t *testing.T) {
	base := DefaultConfig()

	base.EnvMode = ""
	if verr := ValidateConfig(&base); !verr.Empty() {
		t.Fatalf("empty env_mode should be valid (behaves as inherit): %+v", verr.Errors)
	}

	base.EnvMode = EnvModeInherit
	if verr := ValidateConfig(&base); !verr.Empty() {
		t.Fatalf("inherit env_mode should be valid: %+v", verr.Errors)
	}

	base.EnvMode = EnvModeIsolated
	if verr := ValidateConfig(&base); !verr.Empty() {
		t.Fatalf("isolated env_mode should be valid: %+v", verr.Errors)
	}

	base.EnvMode = "bogus"
	if verr := ValidateConfig(&base); verr.Empty() {
		t.Fatal("unregistered env_mode should be invalid")
	}
}

// TestConfigCredentialFieldsCarryNamesNotValues guards spec §13's
// names-only cross-service-boundary property: credential_bindings and
// env_passthrough are the only Config fields naming mads-bound credentials,
// and both must stay plain name lists, never NAME=VALUE pairs — a value
// embedded here would cross the Data Service HTTP boundary in cleartext,
// the exact thing bound-credential resolution exists to avoid.
func TestConfigCredentialFieldsCarryNamesNotValues(t *testing.T) {
	c := Config{
		CredentialBindings: []string{"OPENAI_API_KEY", "AWS_SECRET_ACCESS_KEY"},
		EnvPassthrough:     []string{"AWS_PROFILE", "MADS_CRED_A"},
	}
	if _, err := json.Marshal(c); err != nil {
		t.Fatal(err)
	}
	for _, name := range append(append([]string{}, c.CredentialBindings...), c.EnvPassthrough...) {
		if strings.Contains(name, "=") {
			t.Fatalf("credential-carrying field entry %q looks like a NAME=VALUE pair, not a bare name", name)
		}
	}
}

func TestValidateConfigFastModelBaseURL(t *testing.T) {
	base := DefaultConfig()

	base.FastModelBaseURL = ""
	if verr := ValidateConfig(&base); !verr.Empty() {
		t.Fatalf("empty fast_model_base_url should be valid (no endpoint configured): %+v", verr.Errors)
	}

	base.FastModelBaseURL = "https://api.example.com/v1"
	if verr := ValidateConfig(&base); !verr.Empty() {
		t.Fatalf("absolute https URL should be valid: %+v", verr.Errors)
	}

	base.FastModelBaseURL = "not-a-url"
	if verr := ValidateConfig(&base); verr.Empty() {
		t.Fatal("relative/non-absolute URL should be invalid")
	}
}

func TestValidateConfigFastModelMode(t *testing.T) {
	base := DefaultConfig()

	base.FastModelMode = ""
	if verr := ValidateConfig(&base); !verr.Empty() {
		t.Fatalf("empty fast_model_mode should be valid (pre-selector install): %+v", verr.Errors)
	}

	base.FastModelMode = FastModelModeProvider
	if verr := ValidateConfig(&base); !verr.Empty() {
		t.Fatalf("mode %q should be valid: %+v", base.FastModelMode, verr.Errors)
	}

	base.FastModelMode = "bogus"
	if verr := ValidateConfig(&base); verr.Empty() {
		t.Fatal("unrecognized fast_model_mode should be invalid")
	}
}

// FastModelEffectiveMode must flip the zero-config default from dormant to
// working (spec): an unconfigured fresh install resolves to provider mode,
// not off.
func TestFastModelEffectiveModeDefaults(t *testing.T) {
	c := DefaultConfig()
	if got := c.FastModelEffectiveMode(); got != FastModelModeProvider {
		t.Errorf("fresh install effective mode = %q, want %q", got, FastModelModeProvider)
	}
}

// A pre-mode-selector install that already configured an endpoint must keep
// working exactly as before — its unset FastModelMode resolves to endpoint,
// not the new provider default.
func TestFastModelEffectiveModePreservesExistingEndpoint(t *testing.T) {
	c := DefaultConfig()
	c.FastModelBaseURL = "https://api.example.com/v1"
	if got := c.FastModelEffectiveMode(); got != FastModelModeEndpoint {
		t.Errorf("effective mode = %q, want %q", got, FastModelModeEndpoint)
	}
}

// An explicit FastModelMode always wins, regardless of FastModelBaseURL.
func TestFastModelEffectiveModeExplicitWins(t *testing.T) {
	c := DefaultConfig()
	c.FastModelBaseURL = "https://api.example.com/v1"
	c.FastModelMode = FastModelModeOff
	if got := c.FastModelEffectiveMode(); got != FastModelModeOff {
		t.Errorf("effective mode = %q, want %q", got, FastModelModeOff)
	}
}
