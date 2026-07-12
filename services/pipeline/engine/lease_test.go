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
