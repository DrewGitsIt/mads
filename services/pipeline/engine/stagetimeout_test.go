package engine

import (
	"context"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/template"
	"github.com/drewkidwell/mads/providers/fake"
)

const sleepTemplate = `name: sleeper
stages:
  - name: build
    command: sleep 5
`

// A suspended machine must not let a stage run for the wall-clock duration
// of the sleep on top of its budget: context.WithTimeout is driven by
// CLOCK_MONOTONIC, which pauses for the whole suspend, so the watchdog polls
// a real ticker but judges elapsed time against the injected wall clock. A
// StageTimeout of an hour that the injected clock reports as long since
// expired must cancel the stage almost immediately, not after the real
// five-second sleep (let alone the fictional hour).
func TestStageTimeoutUsesWallClock(t *testing.T) {
	h := newHarness(t)
	parsed, verr := template.Check([]byte(sleepTemplate), func(string) bool { return true })
	if verr != nil {
		t.Fatal(verr)
	}
	seeded := h.seed(contracts.Task{Title: "sleeper"})
	clock := newFakeClock(time.Now())

	done := make(chan *Summary, 1)
	start := time.Now()
	go func() {
		sum, err := Run(context.Background(), Config{
			Data: h.data, Provider: fake.New(), SessionID: h.sess.ID,
			Template: parsed, Cwd: h.cwd, Concurrency: 1,
			StageTimeout: time.Hour, LeaseTTL: time.Minute,
			Clock: clock,
		})
		if err != nil {
			t.Error(err)
		}
		done <- sum
	}()

	// Let the stage actually start, then jump the wall clock past the
	// hour-long budget without ever letting real time catch up to it.
	time.Sleep(100 * time.Millisecond)
	clock.Advance(2 * time.Hour)

	select {
	case sum := <-done:
		if elapsed := time.Since(start); elapsed >= 5*time.Second {
			t.Fatalf("stage ran for %s — the real 5s sleep completed, wall-clock timeout did not cancel it early", elapsed)
		}
		if len(sum.Blocked) != 1 || sum.Blocked[0] != seeded[0].ID {
			t.Fatalf("summary: %+v; task: %+v", sum, h.task(t, seeded[0].ID))
		}
	case <-time.After(4 * time.Second):
		t.Fatal("Run did not return promptly after the wall-clock stage timeout")
	}
}
