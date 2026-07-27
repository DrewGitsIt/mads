package store

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/drewkidwell/mads/pkg/contracts"
)

func TestConfigSeededAndUpdatable(t *testing.T) {
	l := openMigratedDB(t)
	cfg := NewConfig(l)
	ctx := context.Background()

	c, err := cfg.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := contracts.DefaultConfig()
	if !reflect.DeepEqual(*c, want) {
		t.Fatalf("seeded config = %+v, want DefaultConfig() = %+v", *c, want)
	}

	c.Theme = contracts.ThemeLight
	c.Palette = "space"
	c.ConcurrencyLimit = 5
	if err := cfg.Update(ctx, c, "user"); err != nil {
		t.Fatal(err)
	}

	got, err := cfg.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got.Theme != contracts.ThemeLight || got.Palette != "space" || got.ConcurrencyLimit != 5 {
		t.Fatalf("update did not persist: %+v", got)
	}
}

// TestConfigGetBackfillsMissingStreamSpeed reproduces the 2026-07-27
// incident: an install whose config row predates the stream_speed field has
// no "stream_speed" key in its stored JSON at all. Get must return the
// DefaultConfig() value ("off"), not the Go zero value (""), which the
// renderer's stream pacer cannot handle (see ui/src/conversation/streaming).
func TestConfigGetBackfillsMissingStreamSpeed(t *testing.T) {
	l := openMigratedDB(t)
	cfg := NewConfig(l)
	ctx := context.Background()

	const preFieldJSON = `{"synthesis_mode":"light","agent_selection_mode":"light",` +
		`"default_model_tier":"balanced","default_trust":"limited","display_mode":"comfortable",` +
		`"keybindings":"default","theme":"dark","palette":"clay","concurrency_limit":3,` +
		`"synthesis_debounce_ms":2000,"log_retention_days":30,` +
		`"feature_flags":{"multi_repo":false,"linked_sessions":false,"remote_registry":false},` +
		`"onboarding_complete":false}`
	if _, err := l.db.ExecContext(ctx, `UPDATE config SET data = ? WHERE id = 1`, preFieldJSON); err != nil {
		t.Fatal(err)
	}

	got, err := cfg.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got.StreamSpeed != contracts.StreamSpeedOff {
		t.Fatalf("StreamSpeed = %q, want %q", got.StreamSpeed, contracts.StreamSpeedOff)
	}
}

func TestConfigUpdateValidatesEverything(t *testing.T) {
	l := openMigratedDB(t)
	cfg := NewConfig(l)
	ctx := context.Background()

	bad := contracts.DefaultConfig()
	bad.Theme = "invalid"
	bad.ConcurrencyLimit = 0
	bad.LogRetentionDays = -1

	var verr *contracts.ValidationError
	err := cfg.Update(ctx, &bad, "user")
	if !errors.As(err, &verr) {
		t.Fatalf("want ValidationError, got %v", err)
	}
	if len(verr.Errors) != 3 {
		t.Errorf("want every problem reported together, got %+v", verr.Errors)
	}
}
