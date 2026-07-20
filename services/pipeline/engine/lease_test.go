package engine

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/pkg/template"
	"github.com/drewkidwell/mads/providers/fake"
)

// soloTemplate is the smallest pipeline: one silent agent stage. Lease
// behavior is about what happens while a stage makes no writes at all.
const soloTemplate = `name: solo
stages:
  - name: executor
    agent: executor
`

// runSolo starts a non-isolated run with a short lease TTL in the background
// and returns the summary channel.
func (h *harness) runSolo(t *testing.T, p *fake.Fake, ttl time.Duration) <-chan *Summary {
	t.Helper()
	parsed, verr := template.Check([]byte(soloTemplate), func(string) bool { return true })
	if verr != nil {
		t.Fatal(verr)
	}
	out := make(chan *Summary, 1)
	go func() {
		sum, err := Run(context.Background(), Config{
			Data: h.data, Provider: p, SessionID: h.sess.ID,
			Template: parsed, Cwd: h.cwd, Concurrency: 1,
			StageTimeout: 30 * time.Second, LeaseTTL: ttl,
		})
		if err != nil {
			t.Error(err)
		}
		out <- sum
	}()
	return out
}

// The dogfood bug: writes are the only lease renewal, and an agent stage can
// be silent for longer than any TTL — the reaper then blocks a task that is
// actively running. The heartbeat must keep the lease held for the whole
// stage: a foreign claim mid-stage is refused, and the task completes.
func TestLeaseHeartbeatOutlivesSilentStage(t *testing.T) {
	h := newHarness(t)
	f := mustScenario(t, fake.Rule{Match: "Task: slow", Reply: "done", DelayMs: 1500})
	seeded := h.seed(contracts.Task{Title: "slow"})
	done := h.runSolo(t, f, 400*time.Millisecond)

	// Well past the TTL, mid-stage: the lease must still be held.
	time.Sleep(900 * time.Millisecond)
	thief := dataclient.New(h.base, "thief")
	err := thief.Claim(context.Background(), seeded[0].ID, time.Minute)
	if !errors.Is(err, dataclient.ErrConflict) {
		t.Fatalf("foreign claim during a silent stage: err=%v (want conflict — the heartbeat must be renewing)", err)
	}

	sum := <-done
	if len(sum.Done) != 1 {
		t.Fatalf("summary: %+v; task: %+v", sum, h.task(t, seeded[0].ID))
	}
}

// A machine suspend stalls every heartbeat past the TTL at once; on wake the
// reaper has released the lease and blocked the task. The engine, still
// alive and mid-stage, must take the lease back AND put the status back —
// simulated here by doing exactly what the reaper does (release + block) out
// from under a running stage.
func TestSuspendWakeRestoresTask(t *testing.T) {
	h := newHarness(t)
	f := mustScenario(t, fake.Rule{Match: "Task: napper", Reply: "done", DelayMs: 1500})
	seeded := h.seed(contracts.Task{Title: "napper"})
	done := h.runSolo(t, f, 400*time.Millisecond)

	// Mid-stage, play reaper: free the lease and block the task.
	time.Sleep(600 * time.Millisecond)
	if err := h.data.Release(context.Background(), seeded[0].ID); err != nil {
		t.Fatal(err)
	}
	swept := h.task(t, seeded[0].ID)
	swept.Status = contracts.StatusBlocked
	if err := h.data.UpdateTask(context.Background(), &swept); err != nil {
		t.Fatal(err)
	}

	sum := <-done
	if len(sum.Done) != 1 {
		t.Fatalf("summary after simulated suspend: %+v; task: %+v", sum, h.task(t, seeded[0].ID))
	}
	if got := h.task(t, seeded[0].ID); got.Status != contracts.StatusDone {
		t.Errorf("task status = %s, want done", got.Status)
	}
}

// A rate-limit pause is not a silent stage — it's the engine itself waiting —
// but the lease must be held across it exactly the same way: the heartbeat
// keeps renewing through a backoff far longer than the TTL, so a foreign
// claim mid-pause is refused and the task still finishes.
func TestRateLimitPauseHoldsLease(t *testing.T) {
	h := newHarness(t)
	f := mustScenario(t, fake.Rule{Match: "Task: throttled", Reply: "done", RateLimitTimes: 1})
	seeded := h.seed(contracts.Task{Title: "throttled"})
	parsed, verr := template.Check([]byte(soloTemplate), func(string) bool { return true })
	if verr != nil {
		t.Fatal(verr)
	}
	const ttl = 150 * time.Millisecond
	const backoff = 600 * time.Millisecond // several heartbeat ticks (ttl/3) deep
	done := make(chan *Summary, 1)
	go func() {
		sum, err := Run(context.Background(), Config{
			Data: h.data, Provider: f, SessionID: h.sess.ID,
			Template: parsed, Cwd: h.cwd, Concurrency: 1,
			StageTimeout: 30 * time.Second, LeaseTTL: ttl,
			RateLimitBackoff: backoff,
		})
		if err != nil {
			t.Error(err)
		}
		done <- sum
	}()

	// Mid-pause, well past the TTL: the lease must still be held.
	time.Sleep(backoff / 2)
	thief := dataclient.New(h.base, "thief")
	err := thief.Claim(context.Background(), seeded[0].ID, time.Minute)
	if !errors.Is(err, dataclient.ErrConflict) {
		t.Fatalf("foreign claim during a rate-limit pause: err=%v (want conflict — the heartbeat must be renewing)", err)
	}

	sum := <-done
	if len(sum.Done) != 1 || sum.Done[0] != seeded[0].ID {
		t.Fatalf("summary: %+v; task: %+v", sum, h.task(t, seeded[0].ID))
	}
}

// The other side of the heartbeat: when a renewal is refused because another
// actor holds the lease, the task is no longer ours — the engine must stop
// driving it and write nothing more, leaving the outcome to the new holder.
func TestLostLeaseAbandonsTask(t *testing.T) {
	h := newHarness(t)
	f := mustScenario(t, fake.Rule{Match: "Task: stolen", Reply: "done", DelayMs: 1500})
	seeded := h.seed(contracts.Task{Title: "stolen"})
	done := h.runSolo(t, f, 400*time.Millisecond)

	// Mid-stage, yank the lease out from under the engine: release as the
	// engine's own actor, then claim as someone else. The next heartbeat
	// renewal conflicts.
	time.Sleep(600 * time.Millisecond)
	thief := dataclient.New(h.base, "thief")
	if err := h.data.Release(context.Background(), seeded[0].ID); err != nil {
		t.Fatal(err)
	}
	if err := thief.Claim(context.Background(), seeded[0].ID, time.Minute); err != nil {
		t.Fatal(err)
	}

	sum := <-done
	if len(sum.Done) != 0 {
		t.Fatalf("engine completed a task it no longer owned: %+v", sum)
	}
	got := h.task(t, seeded[0].ID)
	if got.Status != contracts.StatusInProgress {
		t.Errorf("abandoned task status = %s, want in_progress (untouched after the steal)", got.Status)
	}
	// The thief's lease survives the engine's exit (no release of a lease
	// the engine no longer holds).
	if err := thief.Claim(context.Background(), seeded[0].ID, time.Minute); err != nil {
		t.Errorf("thief's lease was disturbed: %v", err)
	}
}

// A cancelled Run must release its leases promptly rather than leaving them
// to expire on their own — this is what services/pipeline's SIGTERM ->
// ctx-cancellation path (main.go's signal.NotifyContext) relies on for
// "cancel releases leases". The rate-limit backoff pause is the one place
// runTask's own code (not the provider) waits on a timer that also selects
// on ctx.Done(), so a cancel here proves the propagation rather than just
// waiting out a timeout.
func TestCancelDuringRateLimitPauseReleasesLease(t *testing.T) {
	h := newHarness(t)
	f := mustScenario(t, fake.Rule{Match: "Task: cancelme", Reply: "done", RateLimitTimes: 1})
	seeded := h.seed(contracts.Task{Title: "cancelme"})
	parsed, verr := template.Check([]byte(soloTemplate), func(string) bool { return true })
	if verr != nil {
		t.Fatal(verr)
	}
	const backoff = 10 * time.Second // long enough that only cancel, not the timer, can end the pause
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan *Summary, 1)
	go func() {
		sum, _ := Run(ctx, Config{
			Data: h.data, Provider: f, SessionID: h.sess.ID,
			Template: parsed, Cwd: h.cwd, Concurrency: 1,
			StageTimeout: 30 * time.Second, LeaseTTL: time.Minute,
			RateLimitBackoff: backoff,
		})
		done <- sum
	}()

	time.Sleep(200 * time.Millisecond) // past the rate-limited attempt, now mid-pause
	cancel()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return promptly after ctx cancellation during the rate-limit pause")
	}

	leases, err := h.data.Leases(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(leases) != 0 {
		t.Fatalf("leases after cancel = %+v, want none (cancel must release, not just abandon)", leases)
	}
	if got := h.task(t, seeded[0].ID); got.Status != contracts.StatusInProgress {
		t.Errorf("cancelled task status = %s, want in_progress (cancel releases the lease, not the status)", got.Status)
	}
}
