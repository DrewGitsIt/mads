package store

import (
	"context"
	"errors"
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
	if *c != want {
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
