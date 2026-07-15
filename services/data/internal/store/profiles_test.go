package store

import (
	"context"
	"errors"
	"testing"

	"github.com/drewkidwell/mads/pkg/contracts"
)

func TestProfileCRUDAndEvents(t *testing.T) {
	l := openMigratedDB(t)
	profiles := NewProfiles(l)
	ctx := context.Background()

	p := contracts.Profile{
		Name: "focus-mode",
		Settings: contracts.ProfileSettings{
			SynthesisMode:    contracts.SynthesisOff,
			ConcurrencyLimit: 1,
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

func TestActivateProfileMergesOntoConfig(t *testing.T) {
	l := openMigratedDB(t)
	profiles := NewProfiles(l)
	ctx := context.Background()

	p := contracts.Profile{
		Name: "cost-conscious",
		Settings: contracts.ProfileSettings{
			DefaultModelTier: contracts.ModelTierCheapest,
			ConcurrencyLimit: 1,
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
		t.Fatalf("activation did not merge overridden fields: %+v", merged)
	}
	// Untouched fields survive from the prior config, not zero values.
	if merged.Theme != before.Theme || merged.Palette != before.Palette {
		t.Fatalf("activation clobbered untouched fields: before=%+v merged=%+v", before, merged)
	}

	after, err := cfg.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if *after != *merged {
		t.Fatalf("activation result was not persisted: got %+v, want %+v", after, merged)
	}
}

func TestActivateProfileByName(t *testing.T) {
	l := openMigratedDB(t)
	profiles := NewProfiles(l)
	ctx := context.Background()

	p := contracts.Profile{Name: "by-name", Settings: contracts.ProfileSettings{ConcurrencyLimit: 7}}
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
