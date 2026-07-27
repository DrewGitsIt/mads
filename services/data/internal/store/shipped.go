package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/drewkidwell/mads/pkg/capability"
	"github.com/drewkidwell/mads/pkg/contracts"
)

// SeedShippedCapabilities registers every capability.ShippedCapabilities
// entry that isn't already present in caps, computing a real health check so
// a fresh install's playwright-ui-testing capability (say) shows healthy
// immediately rather than waiting on a session to exercise it. An already-
// registered shipped capability is left untouched on subsequent calls — the
// same "created once, then the user's to edit" contract as the six system
// agents seeded in migration 0005_registries, just implemented in Go instead
// of a static SQL insert because a skill's Config.Path has to be a real,
// resolved filesystem path (capability.ShippedCapabilities), not a literal
// a migration file could hardcode. Safe to call on every Data Service
// startup (main.go, after Migrate, and services/data/datatest.Start for
// downstream tests) — idempotent by construction.
func SeedShippedCapabilities(ctx context.Context, caps *Registry[contracts.Capability]) error {
	for _, sc := range capability.ShippedCapabilities {
		if _, err := caps.GetByName(ctx, sc.Name); err == nil {
			continue
		} else if !errors.Is(err, ErrNotFound) {
			return fmt.Errorf("shipped capability %s: %w", sc.Name, err)
		}
		c := sc
		healthy, reason := capability.Health(ctx, c, false)
		c.Health = contracts.CapabilityHealth{Healthy: healthy, Reason: reason, CheckedAt: time.Now().UTC()}
		if err := caps.Create(ctx, &c, "system"); err != nil {
			return fmt.Errorf("shipped capability %s: %w", sc.Name, err)
		}
	}
	return nil
}
