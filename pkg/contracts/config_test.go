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

// TestDefaultConfigStageTimeoutMatchesPreExistingCLIDefault pins the fix for
// the UI/CLI stage-timeout divergence: cmd/mads/run.go's -stage-timeout used
// to default to 30m while services/pipeline/main.go (the Router-spawned
// pipeline child behind every UI-launched run) never set StageTimeout at
// all and fell through to the engine's own 10m default. Both launch paths
// now read PipelineStageTimeoutMinutes from the same config, so this pins
// the default at 30 — the safer, more permissive of the two old values —
// rather than 10.
func TestDefaultConfigStageTimeoutMatchesPreExistingCLIDefault(t *testing.T) {
	c := DefaultConfig()
	if c.PipelineStageTimeoutMinutes != 30 {
		t.Fatalf("DefaultConfig().PipelineStageTimeoutMinutes = %d, want 30", c.PipelineStageTimeoutMinutes)
	}
	if c.PipelineStageTimeoutMinutes != DefaultPipelineStageTimeoutMinutes {
		t.Fatalf("DefaultConfig().PipelineStageTimeoutMinutes = %d, want DefaultPipelineStageTimeoutMinutes = %d",
			c.PipelineStageTimeoutMinutes, DefaultPipelineStageTimeoutMinutes)
	}
}

// TestConfigRoundTripsPipelineStageTimeout guards the JSON wire shape
// (json tag, no accidental omitempty) since services/data's config store
// persists Config as an opaque JSON blob (services/data/internal/store/config.go).
func TestConfigRoundTripsPipelineStageTimeout(t *testing.T) {
	c := DefaultConfig()
	c.PipelineStageTimeoutMinutes = 45

	data, err := json.Marshal(&c)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"pipeline_stage_timeout_minutes":45`) {
		t.Fatalf("marshaled config missing pipeline_stage_timeout_minutes:45: %s", data)
	}

	var got Config
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got.PipelineStageTimeoutMinutes != 45 {
		t.Fatalf("PipelineStageTimeoutMinutes round-tripped as %d, want 45", got.PipelineStageTimeoutMinutes)
	}
}

// TestConfigPreFieldDecodesToPreventSleep: AllowSleepDuringPipelines is
// negatively named so a stored config JSON without the key (every install
// predating it) decodes to "prevent sleep" with no migration.
func TestConfigPreFieldDecodesToPreventSleep(t *testing.T) {
	const preFieldJSON = `{"synthesis_mode":"light","synthesis_auto_approve":false,` +
		`"concurrency_limit":3,"pipeline_stage_timeout_minutes":30}`
	var got Config
	if err := json.Unmarshal([]byte(preFieldJSON), &got); err != nil {
		t.Fatal(err)
	}
	if got.AllowSleepDuringPipelines {
		t.Fatal("pre-field config decoded AllowSleepDuringPipelines=true, want false (prevent sleep)")
	}
	if DefaultConfig().AllowSleepDuringPipelines {
		t.Fatal("DefaultConfig() allows sleep, want prevent sleep")
	}

	c := DefaultConfig()
	c.AllowSleepDuringPipelines = true
	data, err := json.Marshal(&c)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"allow_sleep_during_pipelines":true`) {
		t.Fatalf("marshaled config missing allow_sleep_during_pipelines:true: %s", data)
	}
	var back Config
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatal(err)
	}
	if !back.AllowSleepDuringPipelines {
		t.Fatal("AllowSleepDuringPipelines=true did not round-trip")
	}
}

func TestValidateConfigReportsEveryProblem(t *testing.T) {
	c := Config{
		SynthesisMode:               "bogus",
		AgentSelectionMode:          "bogus",
		DefaultModelTier:            "bogus",
		CostAlertCents:              -1,
		PipelineStageTimeoutMinutes: -1,
		DefaultTrust:                "bogus",
		Theme:                       "bogus",
		Palette:                     "bogus",
		ConcurrencyLimit:            0,
		SynthesisDebounceMS:         -1,
		LogRetentionDays:            0,
		UpdateFeedURL:               "not-a-url",
		EnvMode:                     "bogus",
	}
	verr := ValidateConfig(&c)
	if verr.Empty() {
		t.Fatal("expected validation failures")
	}
	want := 13
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

func TestValidateConfigDefaultIsolation(t *testing.T) {
	base := DefaultConfig()
	for _, ok := range []string{"", IsolationWorktree, IsolationCurrentDir} {
		base.DefaultIsolation = ok
		if verr := ValidateConfig(&base); !verr.Empty() {
			t.Fatalf("default_isolation %q should be valid: %+v", ok, verr.Errors)
		}
	}
	base.DefaultIsolation = "bogus"
	if verr := ValidateConfig(&base); verr.Empty() {
		t.Fatal("unknown default_isolation should be invalid")
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

// Empty is the 10-minute default, so a config predating the setting is valid.
func TestValidateConfigDelegateFade(t *testing.T) {
	c := DefaultConfig()
	for _, f := range append([]DelegateFade{""}, DelegateFades...) {
		c.DelegateFade = f
		if verr := ValidateConfig(&c); !verr.Empty() {
			t.Errorf("delegate_fade %q rejected: %+v", f, verr.Errors)
		}
	}
	c.DelegateFade = "2m"
	if verr := ValidateConfig(&c); verr.Empty() {
		t.Error("delegate_fade 2m accepted")
	}
}
