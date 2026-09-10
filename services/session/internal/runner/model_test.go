package runner

import (
	"context"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/providers/fake"
	"github.com/drewkidwell/mads/providers/provider"
	"github.com/drewkidwell/mads/services/data/datatest"
)

// newSessionWithModel is newSession plus a bound model — SetModel's whole
// subject is changing an existing binding, so these tests can't use the
// model-less record newSession creates.
func newSessionWithModel(t *testing.T, base, model string) contracts.Session {
	t.Helper()
	bootstrap := dataclient.New(base, "test-bootstrap")
	sess := contracts.Session{Name: "test", Provider: "fake", Model: model}
	if err := bootstrap.CreateSession(context.Background(), &sess); err != nil {
		t.Fatalf("create session: %v", err)
	}
	return sess
}

// startModelRunner boots a runner on a session bound to model, drives one
// full turn so the provider ref is announced (the fake's default timing is
// on-first-turn, and SetModel requires a resumable ref), and returns the
// runner plus the fake to read StartCalls off.
func startModelRunner(t *testing.T, model string) (*Runner, *fake.Fake, <-chan provider.Event) {
	t.Helper()
	ctx := context.Background()
	base := datatest.Start(t)
	sess := newSessionWithModel(t, base, model)
	data := dataclient.New(base, sess.ID)

	reg := provider.NewRegistry()
	fp := fake.New()
	if err := reg.Register(fp); err != nil {
		t.Fatalf("register fake: %v", err)
	}

	r := New(sess.ID, data, reg)
	if err := r.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = r.Shutdown(context.Background()) })

	ch, unsubscribe := r.Subscribe()
	t.Cleanup(unsubscribe)
	if err := r.Send(ctx, "echo hi"); err != nil {
		t.Fatalf("send: %v", err)
	}
	drainText(t, ch)
	return r, fp, ch
}

// waitForStarts polls until the fake has recorded at least n Start calls,
// for the deferred apply — onTurnEnd hands the switch to a goroutine, so
// there is no synchronous moment to assert on.
func waitForStarts(t *testing.T, fp *fake.Fake, n int) []provider.StartOptions {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		if calls := fp.StartCalls(); len(calls) >= n {
			return calls
		}
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for %d provider starts, saw %d", n, len(fp.StartCalls()))
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// endTurn interrupts the in-flight turn and drains to its turn_end, which is
// what clears busy and fires onTurnEnd's deferred-apply.
func endTurn(t *testing.T, r *Runner, ch <-chan provider.Event) {
	t.Helper()
	if err := r.Interrupt(context.Background()); err != nil {
		t.Fatalf("interrupt: %v", err)
	}
	deadline := time.After(5 * time.Second)
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				t.Fatal("event channel closed before turn_end")
			}
			if ev.Kind == provider.EventTurnEnd {
				return
			}
		case <-deadline:
			t.Fatal("timed out waiting for turn_end")
		}
	}
}

// TestSetModelCyclesProviderWhenIdle: with no turn in flight the switch
// applies straight away — the provider process is cycled onto the new model
// and resumed by ref, so the conversation carries over rather than
// restarting. This is the half that was missing entirely before: the model
// used to be read once at spawn and never again.
func TestSetModelCyclesProviderWhenIdle(t *testing.T) {
	ctx := context.Background()
	r, fp, _ := startModelRunner(t, "model-a")

	before := fp.StartCalls()
	if got := before[0].Model; got != "model-a" {
		t.Fatalf("initial start Model = %q, want %q", got, "model-a")
	}

	queued, err := r.SetModel(ctx, "model-b")
	if err != nil {
		t.Fatalf("SetModel: %v", err)
	}
	if queued {
		t.Fatal("SetModel queued = true while idle, want false (nothing to queue behind)")
	}

	calls := fp.StartCalls()
	if len(calls) != len(before)+1 {
		t.Fatalf("provider starts = %d, want %d (one cycle onto the new model)", len(calls), len(before)+1)
	}
	last := calls[len(calls)-1]
	if last.Model != "model-b" {
		t.Fatalf("cycled start Model = %q, want %q", last.Model, "model-b")
	}
	if last.Resume == "" {
		t.Fatal("cycled start Resume is empty — the switch dropped the conversation instead of resuming it")
	}
}

// TestSetModelQueuesBehindInFlightTurn is the contract that matters most: a
// model change never interrupts a running agent. While busy the request is
// parked (queued=true, no provider cycle at all), and it lands the moment
// the turn ends.
func TestSetModelQueuesBehindInFlightTurn(t *testing.T) {
	ctx := context.Background()
	r, fp, ch := startModelRunner(t, "model-a")
	before := len(fp.StartCalls())

	if err := r.Send(ctx, "slow"); err != nil {
		t.Fatalf("send slow: %v", err)
	}

	queued, err := r.SetModel(ctx, "model-b")
	if err != nil {
		t.Fatalf("SetModel mid-turn: %v", err)
	}
	if !queued {
		t.Fatal("SetModel queued = false mid-turn, want true")
	}
	if got := len(fp.StartCalls()); got != before {
		t.Fatalf("provider starts = %d, want %d — the switch must not cycle the process mid-turn", got, before)
	}

	endTurn(t, r, ch)

	calls := waitForStarts(t, fp, before+1)
	last := calls[len(calls)-1]
	if last.Model != "model-b" {
		t.Fatalf("start after turn end Model = %q, want %q", last.Model, "model-b")
	}
	if last.Resume == "" {
		t.Fatal("deferred switch's Resume is empty — it dropped the conversation instead of resuming it")
	}
}

// TestSetModelBackToLiveModelCancelsQueuedSwitch: switching away and back
// again mid-turn is a no-op overall. Without the explicit cancel in SetModel
// the stale pendingModel would still fire at turn end, cycling the provider
// process onto a model the user had already changed their mind about.
func TestSetModelBackToLiveModelCancelsQueuedSwitch(t *testing.T) {
	ctx := context.Background()
	r, fp, ch := startModelRunner(t, "model-a")
	before := len(fp.StartCalls())

	if err := r.Send(ctx, "slow"); err != nil {
		t.Fatalf("send slow: %v", err)
	}
	if queued, err := r.SetModel(ctx, "model-b"); err != nil || !queued {
		t.Fatalf("SetModel to model-b: queued = %v, err = %v; want true, nil", queued, err)
	}
	if queued, err := r.SetModel(ctx, "model-a"); err != nil || queued {
		t.Fatalf("SetModel back to the live model: queued = %v, err = %v; want false, nil", queued, err)
	}

	endTurn(t, r, ch)

	// Give the deferred-apply goroutine room to run before concluding it
	// didn't — a bare read here would pass even if the cancel were broken.
	time.Sleep(250 * time.Millisecond)
	if got := len(fp.StartCalls()); got != before {
		t.Fatalf("provider starts = %d, want %d — the cancelled switch cycled the process anyway", got, before)
	}
}

// TestSetModelToLiveModelIsNoop guards the idempotence path: re-selecting the
// model already running must not cycle the provider process, so a stray
// re-select (or a UI that re-sends state) never costs a restart.
func TestSetModelToLiveModelIsNoop(t *testing.T) {
	ctx := context.Background()
	r, fp, _ := startModelRunner(t, "model-a")
	before := len(fp.StartCalls())

	queued, err := r.SetModel(ctx, "model-a")
	if err != nil {
		t.Fatalf("SetModel: %v", err)
	}
	if queued {
		t.Fatal("SetModel queued = true for the live model, want false")
	}
	if got := len(fp.StartCalls()); got != before {
		t.Fatalf("provider starts = %d, want %d — re-selecting the live model must not cycle", got, before)
	}
}
