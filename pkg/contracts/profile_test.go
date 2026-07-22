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

// TestSnapshotConfigCapturesEnvMode guards env_mode/env_passthrough as
// profile-snapshot-eligible fields (profiles-v2 semantics): a saved profile
// must be able to restore a spawned agent's environment isolation, not just
// its trust level.
func TestSnapshotConfigCapturesEnvMode(t *testing.T) {
	cfg := DefaultConfig()
	cfg.EnvMode = EnvModeIsolated
	cfg.EnvPassthrough = []string{"AWS_PROFILE"}

	snap, err := SnapshotConfig(&cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := snap["env_mode"]; !ok {
		t.Fatal("env_mode should be captured like any other setting")
	}
	if _, ok := snap["env_passthrough"]; !ok {
		t.Fatal("env_passthrough should be captured like any other setting")
	}

	other := DefaultConfig()
	restored, err := ApplySnapshot(&other, snap)
	if err != nil {
		t.Fatal(err)
	}
	if restored.EnvMode != EnvModeIsolated {
		t.Fatalf("EnvMode = %q, want isolated", restored.EnvMode)
	}
	if !reflect.DeepEqual(restored.EnvPassthrough, []string{"AWS_PROFILE"}) {
		t.Fatalf("EnvPassthrough = %v, want [AWS_PROFILE]", restored.EnvPassthrough)
	}

	drift, err := ProfileDrift(&other, snap)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, f := range drift {
		if f == "env_mode" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected env_mode to be reported as drifted, got %v", drift)
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

func TestRestartRequiredFields(t *testing.T) {
	for _, f := range []string{"concurrency_limit", "log_retention_days", "synthesis_debounce_ms"} {
		if !RestartRequiredFields[f] {
			t.Errorf("%s should be listed as restart-required", f)
		}
	}
}
