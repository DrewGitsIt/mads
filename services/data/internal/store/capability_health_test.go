package store

import (
	"context"
	"testing"

	"github.com/drewkidwell/mads/pkg/contracts"
)

// TestSetCapabilityHealthTransitionOnly pins the sharpest form of the
// transition-only event rule (spec §12.6): the very first unhealthy report
// matches the zero-value default and must not emit, a genuine flip must emit
// exactly once, and a same-status repeat (even with a different reason) must
// never emit a second event.
func TestSetCapabilityHealthTransitionOnly(t *testing.T) {
	l := openMigratedDB(t)
	caps := NewCapabilities(l)
	ctx := context.Background()

	c := contracts.Capability{
		Name: "jira", Type: contracts.CapabilityMCP,
		Config: contracts.CapabilityConfig{Command: "npx @modelcontextprotocol/server-jira"},
	}
	if err := caps.Create(ctx, &c, "user"); err != nil {
		t.Fatal(err)
	}

	// First report is unhealthy, matching the zero-value default: no flip.
	if _, err := SetCapabilityHealth(ctx, caps, c.ID, false, "missing credential: JIRA_TOKEN", "user"); err != nil {
		t.Fatal(err)
	}
	if types := eventTypes(t, l, Filter{Types: []string{"capability.health_changed"}}); len(types) != 0 {
		t.Fatalf("initial unhealthy report: want no events, got %v", types)
	}

	// Genuine flip to healthy: exactly one event.
	if _, err := SetCapabilityHealth(ctx, caps, c.ID, true, "", "user"); err != nil {
		t.Fatal(err)
	}
	if types := eventTypes(t, l, Filter{Types: []string{"capability.health_changed"}}); len(types) != 1 {
		t.Fatalf("healthy flip: want 1 event, got %v", types)
	}

	// Same-status repeat, even with a different reason string, must not
	// emit a second event.
	if _, err := SetCapabilityHealth(ctx, caps, c.ID, true, "still fine", "user"); err != nil {
		t.Fatal(err)
	}
	if types := eventTypes(t, l, Filter{Types: []string{"capability.health_changed"}}); len(types) != 1 {
		t.Fatalf("same-status repeat: want still 1 event, got %v", types)
	}

	got, err := caps.Get(ctx, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Health.Healthy || got.Health.Reason != "still fine" {
		t.Errorf("health not persisted: %+v", got.Health)
	}

	// Flip back to unhealthy: another genuine transition, another event.
	if _, err := SetCapabilityHealth(ctx, caps, c.ID, false, "server not running", "user"); err != nil {
		t.Fatal(err)
	}
	if types := eventTypes(t, l, Filter{Types: []string{"capability.health_changed"}}); len(types) != 2 {
		t.Fatalf("flip back to unhealthy: want 2 events total, got %v", types)
	}
}

func TestSetCapabilityHealthNotFound(t *testing.T) {
	l := openMigratedDB(t)
	caps := NewCapabilities(l)
	ctx := context.Background()

	if _, err := SetCapabilityHealth(ctx, caps, "does-not-exist", true, "", "user"); err == nil {
		t.Fatal("want an error for an unknown capability")
	}
}
