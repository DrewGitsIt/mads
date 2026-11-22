//go:build !perf

package fake_test

import (
	"context"
	"testing"
	"time"

	"github.com/drewkidwell/mads/providers/fake"
	"github.com/drewkidwell/mads/providers/provider"
)

// TestNoPerfDialectsUntagged: a build without -tags perf (every release)
// has no "permission" dialect and ignores MADS_FAKE_START_DELAY_MS; both
// live in perf.go.
func TestNoPerfDialectsUntagged(t *testing.T) {
	t.Setenv("MADS_FAKE_START_DELAY_MS", "5000")
	asked := make(chan struct{}, 1)
	f := fake.New()
	t0 := time.Now()
	s, err := f.Start(context.Background(), provider.StartOptions{
		Mode: provider.ModeACP,
		Permissions: func(context.Context, provider.PermissionAsk) provider.PermissionDecision {
			asked <- struct{}{}
			return provider.PermissionDecision{Allow: true}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if d := time.Since(t0); d > 2*time.Second {
		t.Fatalf("Start took %s: MADS_FAKE_START_DELAY_MS honoured in an untagged build", d)
	}
	if err := s.Send(context.Background(), "permission Write"); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(5 * time.Second)
	for {
		select {
		case <-asked:
			t.Fatal(`the "permission" dialect asked the hook in an untagged build`)
		case ev := <-s.Events():
			if ev.Kind == provider.EventToolUse {
				t.Fatalf("tool call %+v in an untagged build", ev.Tool)
			}
			if ev.Kind == provider.EventTurnEnd {
				return
			}
		case <-deadline:
			t.Fatal("no turn end")
		}
	}
}
