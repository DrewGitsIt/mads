package store

import (
	"context"
	"testing"

	"github.com/drewkidwell/mads/pkg/contracts"
)

// TestShippedCapabilitiesSeeded is the capability-registry analogue of
// TestSystemAgentsSeeded: a fresh Data Service startup (migrate, then
// reconcile) must register the shipped playwright-ui-testing skill, healthy
// and marked System — and running the reconcile again must be a no-op, the
// same "created once" contract as the six system agents.
func TestShippedCapabilitiesSeeded(t *testing.T) {
	l := openMigratedDB(t)
	caps := NewCapabilities(l)
	ctx := context.Background()

	if err := SeedShippedCapabilities(ctx, caps); err != nil {
		t.Fatal(err)
	}

	c, err := caps.GetByName(ctx, "playwright-ui-testing")
	if err != nil {
		t.Fatalf("shipped capability missing: %v", err)
	}
	if c.Type != contracts.CapabilitySkill {
		t.Errorf("type = %q, want %q", c.Type, contracts.CapabilitySkill)
	}
	if !c.System {
		t.Error("shipped capability must be marked System")
	}
	if !c.Health.Healthy {
		t.Errorf("shipped capability should be healthy (its skill directory ships in the repo): %+v", c.Health)
	}
	if c.Config.Path == "" {
		t.Error("shipped capability has no config.path")
	}

	// Idempotent: re-running startup's reconcile must not error or duplicate.
	if err := SeedShippedCapabilities(ctx, caps); err != nil {
		t.Fatalf("second seed: %v", err)
	}
	list, err := caps.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("want exactly 1 capability after re-seeding, got %d", len(list))
	}
}

// TestShippedCapabilitiesAttachedToExecutorAndVerifier proves the shipped
// skill is actually wired into the two system agents whose job includes
// verifying UI work (spec §12: soft references by name — the migration that
// attaches these, 0019, predates SeedShippedCapabilities running, so this
// also proves the two don't need to run in a particular order).
func TestShippedCapabilitiesAttachedToExecutorAndVerifier(t *testing.T) {
	l := openMigratedDB(t)
	agents := NewAgents(l)
	ctx := context.Background()

	for _, name := range []string{"executor", "verifier"} {
		a, err := agents.GetByName(ctx, name)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, s := range a.Capabilities.Skills {
			if s == "playwright-ui-testing" {
				found = true
			}
		}
		if !found {
			t.Errorf("agent %q does not declare the playwright-ui-testing skill: %v", name, a.Capabilities.Skills)
		}
	}
}
