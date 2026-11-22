//go:build perf

package fake_test

import (
	"context"
	"testing"
	"time"

	"github.com/drewkidwell/mads/providers/fake"
	"github.com/drewkidwell/mads/providers/provider"
)

func TestPermissionDialectParksOnTheHook(t *testing.T) {
	asked := make(chan provider.PermissionAsk, 1)
	release := make(chan bool)
	f := fake.New()
	s, err := f.Start(context.Background(), provider.StartOptions{
		Mode: provider.ModeACP,
		Permissions: func(ctx context.Context, ask provider.PermissionAsk) provider.PermissionDecision {
			asked <- ask
			return provider.PermissionDecision{Allow: <-release}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Send(context.Background(), "permission Write"); err != nil {
		t.Fatal(err)
	}
	var ask provider.PermissionAsk
	select {
	case ask = <-asked:
	case <-time.After(5 * time.Second):
		t.Fatal("the permission hook was never asked")
	}
	if ask.Tool != "Write" || ask.ToolUseID == "" {
		t.Fatalf("ask = %+v", ask)
	}
	// Parked: no turn end until the decision.
	select {
	case ev := <-s.Events():
		if ev.Kind != provider.EventToolUse {
			t.Fatalf("first event %s, want tool_use", ev.Kind)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no tool_use")
	}
	select {
	case ev := <-s.Events():
		t.Fatalf("event %s before the decision", ev.Kind)
	case <-time.After(200 * time.Millisecond):
	}
	release <- true
	deadline := time.After(5 * time.Second)
	for {
		select {
		case ev := <-s.Events():
			if ev.Kind == provider.EventTurnEnd {
				return
			}
		case <-deadline:
			t.Fatal("no turn end after the decision")
		}
	}
}
