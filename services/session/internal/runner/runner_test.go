package runner

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/providers/fake"
	"github.com/drewkidwell/mads/providers/provider"
	"github.com/drewkidwell/mads/services/data/datatest"
)

// drainText collects EventText chunks from ch until an EventTurnEnd, failing
// the test if none arrives within the deadline.
func drainText(t *testing.T, ch <-chan provider.Event) string {
	t.Helper()
	var sb strings.Builder
	deadline := time.After(5 * time.Second)
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				t.Fatal("event channel closed before turn_end")
			}
			switch ev.Kind {
			case provider.EventText:
				sb.WriteString(ev.Text)
			case provider.EventTurnEnd:
				return sb.String()
			case provider.EventError:
				t.Fatalf("unexpected error event: %s", ev.Text)
			}
		case <-deadline:
			t.Fatal("timed out waiting for turn_end")
		}
	}
}

// newSession creates a session with a throwaway actor (the server assigns
// the ID; nothing depends on the actor used for this one call).
func newSession(t *testing.T, base string) contracts.Session {
	t.Helper()
	bootstrap := dataclient.New(base, "test-bootstrap")
	sess := contracts.Session{Name: "test", Provider: "fake"}
	if err := bootstrap.CreateSession(context.Background(), &sess); err != nil {
		t.Fatalf("create session: %v", err)
	}
	return sess
}

func TestRoundTrip(t *testing.T) {
	ctx := context.Background()
	base := datatest.Start(t)
	sess := newSession(t, base)
	data := dataclient.New(base, sess.ID)

	reg := provider.NewRegistry()
	if err := reg.Register(fake.New()); err != nil {
		t.Fatalf("register fake: %v", err)
	}

	r := New(sess.ID, data, reg)
	if err := r.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = r.Shutdown(ctx) })

	ch, unsubscribe := r.Subscribe()
	defer unsubscribe()

	if err := r.Send(ctx, "echo hi"); err != nil {
		t.Fatalf("send: %v", err)
	}
	got := drainText(t, ch)
	if got != "you said hi" {
		t.Fatalf("reply = %q, want %q", got, "you said hi")
	}

	got2, err := data.Session(ctx, sess.ID)
	if err != nil {
		t.Fatalf("load session: %v", err)
	}
	if got2.Activity != contracts.SessionActive {
		t.Fatalf("activity = %q, want active", got2.Activity)
	}
	if got2.ProviderRef == "" {
		t.Fatal("provider_ref not persisted")
	}
}

func TestBounceAndResume(t *testing.T) {
	ctx := context.Background()
	base := datatest.Start(t)
	sess := newSession(t, base)
	data := dataclient.New(base, sess.ID)

	// One shared *fake.Fake across two registries/runners: its history is
	// keyed by ref in memory, so this is what actually proves continuity
	// across a bounce rather than just re-reading persisted state.
	shared := fake.New()

	reg1 := provider.NewRegistry()
	if err := reg1.Register(shared); err != nil {
		t.Fatalf("register fake: %v", err)
	}
	r1 := New(sess.ID, data, reg1)
	if err := r1.Start(ctx); err != nil {
		t.Fatalf("start r1: %v", err)
	}
	ch1, unsub1 := r1.Subscribe()
	if err := r1.Send(ctx, "echo hi"); err != nil {
		t.Fatalf("send r1: %v", err)
	}
	drainText(t, ch1)
	unsub1()
	if err := r1.Shutdown(ctx); err != nil {
		t.Fatalf("shutdown r1: %v", err)
	}

	reg2 := provider.NewRegistry()
	if err := reg2.Register(shared); err != nil {
		t.Fatalf("register fake: %v", err)
	}
	r2 := New(sess.ID, data, reg2)
	if err := r2.Start(ctx); err != nil {
		t.Fatalf("start r2: %v", err)
	}
	t.Cleanup(func() { _ = r2.Shutdown(ctx) })
	ch2, unsub2 := r2.Subscribe()
	defer unsub2()

	if err := r2.Send(ctx, "recall"); err != nil {
		t.Fatalf("send r2: %v", err)
	}
	got := drainText(t, ch2)
	if got != "earlier you said: hi" {
		t.Fatalf("reply = %q, want %q", got, "earlier you said: hi")
	}
}

func TestShutdownReleasesLeases(t *testing.T) {
	ctx := context.Background()
	base := datatest.Start(t)
	sess := newSession(t, base)
	data := dataclient.New(base, sess.ID)
	other := dataclient.New(base, "someone-else")

	task := contracts.Task{SessionID: sess.ID, Title: "A", Source: "user"}
	if err := data.CreateTask(ctx, &task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	reg := provider.NewRegistry()
	if err := reg.Register(fake.New()); err != nil {
		t.Fatalf("register fake: %v", err)
	}

	if err := data.Claim(ctx, task.ID, time.Minute); err != nil {
		t.Fatalf("claim: %v", err)
	}

	r := New(sess.ID, data, reg)
	if err := r.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := r.Shutdown(ctx); err != nil {
		t.Fatalf("shutdown: %v", err)
	}

	got, err := data.Session(ctx, sess.ID)
	if err != nil {
		t.Fatalf("load session: %v", err)
	}
	if got.Activity != contracts.SessionInactive {
		t.Fatalf("activity = %q, want inactive", got.Activity)
	}

	if err := other.Claim(ctx, task.ID, time.Minute); err != nil {
		t.Fatalf("claim after release should succeed, got: %v", err)
	}
}
