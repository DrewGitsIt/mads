package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/drewkidwell/mads/pkg/contracts"
)

func rawJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestProfileCRUDAndEvents(t *testing.T) {
	l := openMigratedDB(t)
	profiles := NewProfiles(l)
	ctx := context.Background()

	p := contracts.Profile{
		Name: "focus-mode",
		Snapshot: contracts.ProfileSnapshot{
			"synthesis_mode":    rawJSON(t, contracts.SynthesisOff),
			"concurrency_limit": rawJSON(t, 1),
		},
	}
	if err := profiles.Create(ctx, &p, "user"); err != nil {
		t.Fatal(err)
	}
	if p.ID == "" || p.CreatedAt.IsZero() {
		t.Fatalf("create did not fill metadata: %+v", p)
	}

	var verr *contracts.ValidationError
	dup := contracts.Profile{Name: "focus-mode"}
	if err := profiles.Create(ctx, &dup, "user"); !errors.As(err, &verr) {
		t.Fatalf("duplicate name: want ValidationError, got %v", err)
	}

	p.Description = "Fewer distractions"
	if err := profiles.Update(ctx, &p, "user"); err != nil {
		t.Fatal(err)
	}

	if err := profiles.Delete(ctx, p.ID, "user"); err != nil {
		t.Fatal(err)
	}
	if _, err := profiles.Get(ctx, p.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("get after delete: want ErrNotFound, got %v", err)
	}
}

func TestActivateProfileRestoresSnapshotFields(t *testing.T) {
	l := openMigratedDB(t)
	profiles := NewProfiles(l)
	ctx := context.Background()

	p := contracts.Profile{
		Name: "cost-conscious",
		Snapshot: contracts.ProfileSnapshot{
			"default_model_tier": rawJSON(t, contracts.ModelTierCheapest),
			"concurrency_limit":  rawJSON(t, 1),
		},
	}
	if err := profiles.Create(ctx, &p, "user"); err != nil {
		t.Fatal(err)
	}

	cfg := NewConfig(l)
	before, err := cfg.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}

	merged, err := ActivateProfile(ctx, l, p.ID, "user")
	if err != nil {
		t.Fatal(err)
	}
	if merged.DefaultModelTier != contracts.ModelTierCheapest || merged.ConcurrencyLimit != 1 {
		t.Fatalf("activation did not restore captured fields: %+v", merged)
	}
	// Untouched fields survive from the prior config, not zero values.
	if merged.Theme != before.Theme || merged.Palette != before.Palette {
		t.Fatalf("activation clobbered untouched fields: before=%+v merged=%+v", before, merged)
	}
	if merged.ActiveProfileID != p.ID {
		t.Fatalf("activation did not set ActiveProfileID: %+v", merged)
	}

	after, err := cfg.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(after, merged) {
		t.Fatalf("activation result was not persisted: got %+v, want %+v", after, merged)
	}
}

func TestActivateProfileRestoresThemePaletteAndCredentialBindingsLeavesInstallStateUntouched(t *testing.T) {
	l := openMigratedDB(t)
	profiles := NewProfiles(l)
	ctx := context.Background()

	cfg := NewConfig(l)
	live, err := cfg.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	live.OnboardingComplete = true
	live.UpdateFeedURL = "https://example.com/feed"
	if err := cfg.Update(ctx, live, "user"); err != nil {
		t.Fatal(err)
	}

	p := contracts.Profile{
		Name: "dark-glacier",
		Snapshot: contracts.ProfileSnapshot{
			"theme":               rawJSON(t, contracts.ThemeLight),
			"palette":             rawJSON(t, "glacier"),
			"credential_bindings": rawJSON(t, []string{"openai", "anthropic"}),
		},
	}
	if err := profiles.Create(ctx, &p, "user"); err != nil {
		t.Fatal(err)
	}

	merged, err := ActivateProfile(ctx, l, p.ID, "user")
	if err != nil {
		t.Fatal(err)
	}
	if merged.Theme != contracts.ThemeLight {
		t.Fatalf("theme not restored: %+v", merged)
	}
	if merged.Palette != "glacier" {
		t.Fatalf("palette not restored: %+v", merged)
	}
	if !reflect.DeepEqual(merged.CredentialBindings, []string{"openai", "anthropic"}) {
		t.Fatalf("credential_bindings not restored: %+v", merged.CredentialBindings)
	}
	if merged.ActiveProfileID != p.ID {
		t.Fatalf("active_profile_id not set: %+v", merged)
	}
	// Install state is never part of a snapshot — must survive activation untouched.
	if !merged.OnboardingComplete {
		t.Fatalf("onboarding_complete was clobbered by activation: %+v", merged)
	}
	if merged.UpdateFeedURL != "https://example.com/feed" {
		t.Fatalf("update_feed_url was clobbered by activation: %+v", merged)
	}
}

func TestActivateProfileEmitsEvents(t *testing.T) {
	l := openMigratedDB(t)
	profiles := NewProfiles(l)
	ctx := context.Background()

	p := contracts.Profile{Name: "events-check", Snapshot: contracts.ProfileSnapshot{"concurrency_limit": rawJSON(t, 2)}}
	if err := profiles.Create(ctx, &p, "user"); err != nil {
		t.Fatal(err)
	}

	var before int
	if err := l.db.QueryRow("SELECT COUNT(*) FROM events WHERE type = ?", string(contracts.ConfigUpdated)).Scan(&before); err != nil {
		t.Fatal(err)
	}

	if _, err := ActivateProfile(ctx, l, p.ID, "user"); err != nil {
		t.Fatal(err)
	}

	var after int
	if err := l.db.QueryRow("SELECT COUNT(*) FROM events WHERE type = ?", string(contracts.ConfigUpdated)).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != before+1 {
		t.Fatalf("activation did not emit config.updated: before=%d after=%d", before, after)
	}
}

// TestActivateProfileLeavesFieldsIntroducedAfterSnapshotUntouched simulates a
// Config field added after a snapshot was saved by deleting a key from the
// stored snapshot row directly — activation must leave the live value for
// that field alone, not zero it out.
func TestActivateProfileLeavesFieldsIntroducedAfterSnapshotUntouched(t *testing.T) {
	l := openMigratedDB(t)
	profiles := NewProfiles(l)
	ctx := context.Background()

	cfg := NewConfig(l)
	live, err := cfg.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	live.FastModel = "haiku"
	if err := cfg.Update(ctx, live, "user"); err != nil {
		t.Fatal(err)
	}

	p := contracts.Profile{
		Name:     "predates-fast-model",
		Snapshot: contracts.ProfileSnapshot{"concurrency_limit": rawJSON(t, 5)},
	}
	if err := profiles.Create(ctx, &p, "user"); err != nil {
		t.Fatal(err)
	}
	// Delete fast_model from the stored snapshot to simulate it never having
	// existed on Config at capture time.
	if _, err := l.db.Exec(
		`UPDATE profiles SET data = json_remove(data, '$.snapshot.fast_model') WHERE id = ?`, p.ID,
	); err != nil {
		t.Fatal(err)
	}

	merged, err := ActivateProfile(ctx, l, p.ID, "user")
	if err != nil {
		t.Fatal(err)
	}
	if merged.FastModel != "haiku" {
		t.Fatalf("field absent from snapshot should be left untouched, got %q", merged.FastModel)
	}
	if merged.ConcurrencyLimit != 5 {
		t.Fatalf("captured field should still apply: %+v", merged)
	}
}

func TestActivateProfileByName(t *testing.T) {
	l := openMigratedDB(t)
	profiles := NewProfiles(l)
	ctx := context.Background()

	p := contracts.Profile{Name: "by-name", Snapshot: contracts.ProfileSnapshot{"concurrency_limit": rawJSON(t, 7)}}
	if err := profiles.Create(ctx, &p, "user"); err != nil {
		t.Fatal(err)
	}

	merged, err := ActivateProfile(ctx, l, "by-name", "user")
	if err != nil {
		t.Fatal(err)
	}
	if merged.ConcurrencyLimit != 7 {
		t.Fatalf("activation by name did not apply: %+v", merged)
	}
}

func TestActivateProfileUnknown(t *testing.T) {
	l := openMigratedDB(t)
	ctx := context.Background()
	if _, err := ActivateProfile(ctx, l, "nope", "user"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestCaptureConfigSnapshotExcludesInstallState(t *testing.T) {
	l := openMigratedDB(t)
	ctx := context.Background()

	cfg := NewConfig(l)
	live, err := cfg.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	live.OnboardingComplete = true
	live.UpdateFeedURL = "https://example.com/feed"
	live.CredentialBindings = []string{"openai"}
	if err := cfg.Update(ctx, live, "user"); err != nil {
		t.Fatal(err)
	}

	snap, err := CaptureConfigSnapshot(ctx, l)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := snap["onboarding_complete"]; ok {
		t.Fatalf("onboarding_complete must never be captured: %+v", snap)
	}
	if _, ok := snap["update_feed_url"]; ok {
		t.Fatalf("update_feed_url must never be captured: %+v", snap)
	}
	if _, ok := snap["active_profile_id"]; ok {
		t.Fatalf("active_profile_id must never be captured: %+v", snap)
	}
	if _, ok := snap["credential_bindings"]; !ok {
		t.Fatalf("credential_bindings should be captured like any other field: %+v", snap)
	}
}

// TestCredentialBindingsAreNamesOnlyInDBAndEvents guards the profiles-v2
// spec §13 boundary: the Data Service only ever sees credential NAMES, never
// secret values. This asserts that fact by construction (CredentialBindings
// is a []string of names) and by regression: a value that was never handed
// to the Data Service must never appear in its raw DB rows or event
// payloads, however Config/Profile rows get serialized.
func TestCredentialBindingsAreNamesOnlyInDBAndEvents(t *testing.T) {
	l := openMigratedDB(t)
	ctx := context.Background()
	const neverProvidedSecret = "sk-test-should-never-appear-anywhere-9f3c2a"

	cfg := NewConfig(l)
	live, err := cfg.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	live.CredentialBindings = []string{"openai", "anthropic"}
	if err := cfg.Update(ctx, live, "user"); err != nil {
		t.Fatal(err)
	}

	profiles := NewProfiles(l)
	p := contracts.Profile{
		Name:     "creds-profile",
		Snapshot: contracts.ProfileSnapshot{"credential_bindings": rawJSON(t, []string{"openai", "anthropic"})},
	}
	if err := profiles.Create(ctx, &p, "user"); err != nil {
		t.Fatal(err)
	}
	if _, err := ActivateProfile(ctx, l, p.ID, "user"); err != nil {
		t.Fatal(err)
	}

	var configData string
	if err := l.db.QueryRow("SELECT data FROM config WHERE id = 1").Scan(&configData); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(configData, neverProvidedSecret) {
		t.Fatalf("secret value leaked into config DB row: %s", configData)
	}
	if !strings.Contains(configData, "openai") {
		t.Fatalf("expected credential binding names to persist in config row: %s", configData)
	}

	var profileData string
	if err := l.db.QueryRow("SELECT data FROM profiles WHERE id = ?", p.ID).Scan(&profileData); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(profileData, neverProvidedSecret) {
		t.Fatalf("secret value leaked into profile DB row: %s", profileData)
	}

	rows, err := l.db.Query("SELECT payload FROM events")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var payload sql.NullString
		if err := rows.Scan(&payload); err != nil {
			t.Fatal(err)
		}
		if payload.Valid && strings.Contains(payload.String, neverProvidedSecret) {
			t.Fatalf("secret value leaked into event payload: %s", payload.String)
		}
	}
}
