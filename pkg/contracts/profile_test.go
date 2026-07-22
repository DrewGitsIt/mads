package contracts

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestSnapshotConfigExcludesInstallState(t *testing.T) {
	cfg := DefaultConfig()
	cfg.OnboardingComplete = true
	cfg.UpdateFeedURL = "https://example.com/feed"
	cfg.ActiveProfileID = "prf_existing"
	cfg.CredentialBindings = []string{"openai"}

	snap, err := SnapshotConfig(&cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, excluded := range []string{"onboarding_complete", "update_feed_url", "active_profile_id"} {
		if _, ok := snap[excluded]; ok {
			t.Fatalf("%s must never be captured in a snapshot", excluded)
		}
	}
	if _, ok := snap["theme"]; !ok {
		t.Fatal("theme should be captured like any other setting")
	}
	if _, ok := snap["credential_bindings"]; !ok {
		t.Fatal("credential_bindings should be captured like any other setting")
	}
}

func TestApplySnapshotLeavesFieldsAbsentFromSnapshotUntouched(t *testing.T) {
	cfg := DefaultConfig()
	cfg.FastModel = "haiku"
	cfg.OnboardingComplete = true

	// Simulate a snapshot saved before FastModel existed on Config: the field
	// simply never made it into the sparse map.
	snap := ProfileSnapshot{
		"concurrency_limit": json.RawMessage("9"),
	}

	applied, err := ApplySnapshot(&cfg, snap)
	if err != nil {
		t.Fatal(err)
	}
	if applied.ConcurrencyLimit != 9 {
		t.Fatalf("captured field should apply: %+v", applied)
	}
	if applied.FastModel != "haiku" {
		t.Fatalf("field absent from snapshot must be left untouched, got %q", applied.FastModel)
	}
	if !applied.OnboardingComplete {
		t.Fatal("install state must never be touched by activation")
	}
}

func TestApplySnapshotIgnoresExcludedFieldsEvenIfPresent(t *testing.T) {
	cfg := DefaultConfig()
	cfg.OnboardingComplete = true

	snap := ProfileSnapshot{
		"onboarding_complete": json.RawMessage("false"),
	}
	applied, err := ApplySnapshot(&cfg, snap)
	if err != nil {
		t.Fatal(err)
	}
	if !applied.OnboardingComplete {
		t.Fatal("onboarding_complete must never be overwritten by a snapshot, even a malformed one that carries it")
	}
}

func TestProfileDriftReportsDifferingFields(t *testing.T) {
	cfg := DefaultConfig()
	snap, err := SnapshotConfig(&cfg)
	if err != nil {
		t.Fatal(err)
	}

	if drift, err := ProfileDrift(&cfg, snap); err != nil || drift != nil {
		t.Fatalf("identical snapshot should report no drift, got %+v (err=%v)", drift, err)
	}

	cfg.Theme = ThemeLight
	cfg.ConcurrencyLimit = 99
	drift, err := ProfileDrift(&cfg, snap)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"concurrency_limit", "theme"}
	if !reflect.DeepEqual(drift, want) {
		t.Fatalf("drift = %v, want %v", drift, want)
	}
}

func TestProfileDriftIgnoresFieldsAbsentFromSnapshot(t *testing.T) {
	cfg := DefaultConfig()
	snap := ProfileSnapshot{
		"theme": json.RawMessage(`"dark"`),
	}
	cfg.FastModel = "sonnet" // never captured — shouldn't count as drift

	drift, err := ProfileDrift(&cfg, snap)
	if err != nil {
		t.Fatal(err)
	}
	if drift != nil {
		t.Fatalf("field absent from snapshot must not count as drift, got %v", drift)
	}
}

// Fast Model config is snapshot-eligible (profiles-v2) and, per spec §13's
// credential names-only pattern, FastModelAPIKeyEnv only ever carries the
// env var NAME — Config has no field capable of holding a credential value,
// so a snapshot capturing it captures nothing more sensitive than that name.
func TestSnapshotConfigCapturesFastModelFieldsByNameOnly(t *testing.T) {
	cfg := DefaultConfig()
	cfg.FastModelBaseURL = "https://api.example.com/v1"
	cfg.FastModelAPIKeyEnv = "OPENAI_API_KEY"
	cfg.FastModel = "gpt-4o-mini"

	snap, err := SnapshotConfig(&cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"fast_model_base_url", "fast_model_api_key_env", "fast_model"} {
		if _, ok := snap[field]; !ok {
			t.Fatalf("%s should be captured like any other setting", field)
		}
	}
	var gotEnvName string
	if err := json.Unmarshal(snap["fast_model_api_key_env"], &gotEnvName); err != nil {
		t.Fatal(err)
	}
	if gotEnvName != "OPENAI_API_KEY" {
		t.Fatalf("fast_model_api_key_env snapshot = %q, want the env var name unchanged", gotEnvName)
	}

	restored := DefaultConfig()
	applied, err := ApplySnapshot(&restored, snap)
	if err != nil {
		t.Fatal(err)
	}
	if applied.FastModelAPIKeyEnv != "OPENAI_API_KEY" || applied.FastModelBaseURL != "https://api.example.com/v1" {
		t.Fatalf("ApplySnapshot round-trip: %+v", applied)
	}
}

func TestRestartRequiredFields(t *testing.T) {
	for _, f := range []string{"concurrency_limit", "log_retention_days", "synthesis_debounce_ms"} {
		if !RestartRequiredFields[f] {
			t.Errorf("%s should be listed as restart-required", f)
		}
	}
	// Fast Model fields resolve per job spawn (the Router re-reads Config on
	// every Fast Model call), so a Settings change applies live — neither
	// belongs in RestartRequiredFields.
	for _, f := range []string{"fast_model_base_url", "fast_model_api_key_env", "fast_model"} {
		if RestartRequiredFields[f] {
			t.Errorf("%s applies live and should not be listed as restart-required", f)
		}
	}
}
