package engine

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/inhibit"
	"github.com/drewkidwell/mads/pkg/template"
	"github.com/drewkidwell/mads/providers/fake"
)

// fakeLocker is a race-safe inhibit.Locker double: engine tests run with
// -race, and the acquire happens on Run's calling goroutine while release
// fires from a defer, so both counters need a lock.
type fakeLocker struct {
	mu                 sync.Mutex
	acquired, released int
	lastWhy            string
}

func (f *fakeLocker) Acquire(why string) inhibit.Handle {
	f.mu.Lock()
	f.acquired++
	f.lastWhy = why
	f.mu.Unlock()
	return inhibit.HandleFunc(func() {
		f.mu.Lock()
		f.released++
		f.mu.Unlock()
	})
}

func (f *fakeLocker) counts() (acquired, released int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.acquired, f.released
}

// The inhibitor is held for the whole run and released once it completes.
func TestInhibitorHeldForRun(t *testing.T) {
	h := newHarness(t)
	h.seed(contracts.Task{Title: "t1"})
	fl := &fakeLocker{}
	sum, err := Run(context.Background(), Config{
		Data: h.data, Provider: fake.New(), SessionID: h.sess.ID,
		Template: h.defaultTemplate(), Cwd: h.cwd,
		StageTimeout: 30 * time.Second, SleepInhibitor: fl,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(sum.Done) != 1 {
		t.Fatalf("summary: %+v", sum)
	}
	if acquired, released := fl.counts(); acquired != 1 || released != 1 {
		t.Fatalf("acquired=%d released=%d, want 1/1", acquired, released)
	}
	if !strings.Contains(fl.lastWhy, h.sess.ID) {
		t.Fatalf("why %q doesn't name the session %q", fl.lastWhy, h.sess.ID)
	}
}

// A launch-check failure (here, a dependency cycle) never reaches e.run, but
// the inhibitor must still be released: it's acquired before the launch
// checks and released by Run's defer regardless of how Run returns.
func TestInhibitorReleasedOnLaunchFailure(t *testing.T) {
	h := newHarness(t)
	seeded := h.seed(contracts.Task{Title: "a"}, contracts.Task{Title: "b"})
	ta, tb := seeded[0], seeded[1]
	ta.Dependencies = []string{tb.ID}
	tb.Dependencies = []string{ta.ID}
	for _, task := range []*contracts.Task{&ta, &tb} {
		if err := h.data.UpdateTask(context.Background(), task); err != nil {
			t.Fatal(err)
		}
	}
	fl := &fakeLocker{}
	_, err := Run(context.Background(), Config{
		Data: h.data, Provider: fake.New(), SessionID: h.sess.ID,
		Template: h.defaultTemplate(), Cwd: h.cwd, SleepInhibitor: fl,
	})
	if err == nil || !strings.Contains(err.Error(), "cycles") {
		t.Fatalf("want cycle launch failure, got %v", err)
	}
	if acquired, released := fl.counts(); acquired != 1 || released != 1 {
		t.Fatalf("acquired=%d released=%d, want 1/1", acquired, released)
	}
}

// Cancelling mid-run must still release the inhibitor: a task is kept
// in-flight (a delayed agent reply) so the scheduler's drain-then-ctx.Err()
// path is actually exercised, not just the launch-check early return.
func TestInhibitorReleasedOnCancel(t *testing.T) {
	h := newHarness(t)
	f := mustScenario(t, fake.Rule{Match: "Task: napper", Reply: "done", DelayMs: 1500})
	seeded := h.seed(contracts.Task{Title: "napper"})
	parsed, verr := template.Check([]byte(soloTemplate), func(string) bool { return true })
	if verr != nil {
		t.Fatal(verr)
	}
	fl := &fakeLocker{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := Run(ctx, Config{
			Data: h.data, Provider: f, SessionID: h.sess.ID,
			Template: parsed, Cwd: h.cwd, Concurrency: 1,
			StageTimeout: 30 * time.Second, SleepInhibitor: fl,
		})
		done <- err
	}()

	// Wait for the task to actually be in flight before cancelling.
	deadline := time.Now().Add(2 * time.Second)
	for {
		task := h.task(t, seeded[0].ID)
		if task.Status == contracts.StatusInProgress {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("task never reached in_progress")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()

	err := <-done
	if err == nil {
		t.Fatal("want an error from a cancelled run")
	}
	if acquired, released := fl.counts(); acquired != 1 || released != 1 {
		t.Fatalf("acquired=%d released=%d, want 1/1", acquired, released)
	}
}

// The Config toggle disables the behavior entirely.
func TestInhibitorDisabled(t *testing.T) {
	h := newHarness(t)
	h.seed(contracts.Task{Title: "t1"})
	fl := &fakeLocker{}
	sum, err := Run(context.Background(), Config{
		Data: h.data, Provider: fake.New(), SessionID: h.sess.ID,
		Template: h.defaultTemplate(), Cwd: h.cwd,
		StageTimeout: 30 * time.Second, SleepInhibitor: fl,
		DisableSleepInhibit: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(sum.Done) != 1 {
		t.Fatalf("summary: %+v", sum)
	}
	if acquired, _ := fl.counts(); acquired != 0 {
		t.Fatalf("acquired=%d, want 0 (disabled)", acquired)
	}
}

// With no SleepInhibitor injected, Run falls back to the real D-Bus-backed
// inhibit.NewLocker(); this environment isn't expected to have a usable
// D-Bus session/system bus, so this is the engine-level proof that a
// missing D-Bus degrades to a warning and the run proceeds unaffected.
func TestInhibitorDefaultsGracefully(t *testing.T) {
	h := newHarness(t)
	h.seed(contracts.Task{Title: "t1"})
	sum, err := Run(context.Background(), Config{
		Data: h.data, Provider: fake.New(), SessionID: h.sess.ID,
		Template: h.defaultTemplate(), Cwd: h.cwd,
		StageTimeout: 30 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(sum.Done) != 1 {
		t.Fatalf("summary: %+v", sum)
	}
}
