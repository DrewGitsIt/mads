package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/drewkidwell/mads/pkg/capability"
	"github.com/drewkidwell/mads/pkg/contracts"
)

// Bootstrap applies the production migration set and seeds the shipped
// capability registry as one inseparable step. They're inseparable because
// migration 0020_playwright_skill attaches a "playwright-ui-testing" skill
// ref to the executor and verifier system agents; a store that has migrated
// but not yet seeded holds those agents referencing a capability absent
// from the capabilities table, which checkAgentCapabilityRefs (registries.go)
// would reject on the very next edit. Every real caller that opens a store
// for actual use — an API consumer's path, not a unit test exercising
// lower-level store mechanics in isolation — should call Bootstrap instead
// of Migrate directly, so that window is never observable: today that's
// services/data/main.go and services/data/datatest (every downstream
// service's test harness goes through the latter). Store-package unit tests
// that don't touch agent/capability records at all keep using the bare
// Migrate-only openMigratedDB helper (log_test.go) — seeding a capability
// there would append a capability.created event that several of those tests
// (e.g. TestOutboxAtomicity's exact event-count assertions) don't expect;
// tests that do exercise the capability-ref invariant call
// SeedShippedCapabilities directly after openMigratedDB (registries_test.go,
// shipped_test.go).
func Bootstrap(ctx context.Context, db *sql.DB) error {
	if err := Migrate(db, Migrations()); err != nil {
		return err
	}
	return SeedShippedCapabilities(ctx, NewCapabilities(NewLog(db)))
}

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
