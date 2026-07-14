package engine

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/template"
	"github.com/drewkidwell/mads/providers/fake"
)

// A stage that hits RateLimitError twice then succeeds ends the task done,
// with zero on_fail loop-back iterations consumed and no rate-limit text in
// any agent prompt.
func TestRateLimitPausesThenSucceeds(t *testing.T) {
	h := newHarness(t)
	// Canary first: if the engine ever leaked rate-limit text into feedback,
	// the retried prompt would contain "rate limit" and this rule — checked
	// first — would fire instead of the real one.
	f := mustScenario(t,
		fake.Rule{Match: "rate limit", Reply: "STAGE: fail — feedback leaked rate limit text"},
		fake.Rule{Match: "Task: ratelimited task", RateLimitTimes: 2, Reply: "STAGE: pass"},
	)
	seeded := h.seed(contracts.Task{Title: "ratelimited task"})
	tpl, verr := template.Check([]byte(soloTemplate), func(string) bool { return true })
	if verr != nil {
		t.Fatal(verr)
	}
	sum, err := Run(context.Background(), Config{
		Data: h.data, Provider: f, SessionID: h.sess.ID,
		Template: tpl, Cwd: h.cwd, Concurrency: 1,
		StageTimeout:     30 * time.Second,
		RateLimitBackoff: 10 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(sum.Done) != 1 || sum.Done[0] != seeded[0].ID {
		t.Fatalf("done: %v, want just %s; task: %+v", sum.Done, seeded[0].ID, h.task(t, seeded[0].ID))
	}
	if fails := h.events(t, "stage.failed"); len(fails) != 0 {
		t.Errorf("stage.failed: %+v, want none", fails)
	}
	if rl := h.events(t, "stage.rate_limited"); len(rl) != 2 {
		t.Errorf("stage.rate_limited: %d, want 2: %+v", len(rl), rl)
	}
	if rlc := h.events(t, "stage.rate_limit_cleared"); len(rlc) != 2 {
		t.Errorf("stage.rate_limit_cleared: %d, want 2: %+v", len(rlc), rlc)
	}
	task := h.task(t, seeded[0].ID)
	for _, n := range task.Notes {
		if n.Type == contracts.NoteObservation && strings.Contains(n.Content, "loop-back") {
			t.Errorf("loop-back note recorded (a loop_back iteration was consumed): %+v", n)
		}
	}
}

// A provider that never recovers within the retry cap blocks the task with a
// blocker note naming the rate limit, not a generic stage failure.
func TestRateLimitNeverRecoversBlocks(t *testing.T) {
	h := newHarness(t)
	f := mustScenario(t, fake.Rule{Match: "Task: stuck task", RateLimit: true, Reply: "unused"})
	seeded := h.seed(contracts.Task{Title: "stuck task"})
	tpl, verr := template.Check([]byte(soloTemplate), func(string) bool { return true })
	if verr != nil {
		t.Fatal(verr)
	}
	sum, err := Run(context.Background(), Config{
		Data: h.data, Provider: f, SessionID: h.sess.ID,
		Template: tpl, Cwd: h.cwd, Concurrency: 1,
		StageTimeout:        30 * time.Second,
		RateLimitBackoff:    5 * time.Millisecond,
		RateLimitMaxRetries: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(sum.Blocked) != 1 || sum.Blocked[0] != seeded[0].ID {
		t.Fatalf("blocked: %v, want just %s", sum.Blocked, seeded[0].ID)
	}
	task := h.task(t, seeded[0].ID)
	var blockerNote string
	for _, n := range task.Notes {
		if n.Type == contracts.NoteBlocker {
			blockerNote = n.Content
		}
	}
	if !strings.Contains(blockerNote, "rate limit") || !strings.Contains(blockerNote, "scripted rate limit") {
		t.Errorf("blocker note = %q, want it to name the rate limit (not a generic stage failure)", blockerNote)
	}
	// Bounded, not unbounded: RateLimitMaxRetries pause-and-retries happen
	// (each emitting stage.rate_limited), then the next attempt blocks
	// without emitting another rate-limited event.
	if rl := h.events(t, "stage.rate_limited"); len(rl) != 2 {
		t.Errorf("stage.rate_limited: %d, want 2 (RateLimitMaxRetries): %+v", len(rl), rl)
	}
	if fails := h.events(t, "stage.failed"); len(fails) != 0 {
		t.Errorf("stage.failed: %+v, want none (blocked via the rate-limit path, not the generic failure path)", fails)
	}
}
