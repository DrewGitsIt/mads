package engine

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/template"
	"github.com/drewkidwell/mads/providers/fake"
	"github.com/drewkidwell/mads/providers/provider"
)

// scriptedCallProvider scripts OneShot purely by 1-based call count, independent
// of prompt content. provider-fake's Scenario rules match on prompt text,
// which can't tell a retry's Nth attempt from its (N+1)th: a policy retry's
// feedback is the previous attempt's error text, which is often byte-for-
// byte identical from one retry to the next (the same crash message, the
// same FailMarker line) — exactly the case these tests need to distinguish
// "fails twice, then succeeds" from "fails and keeps failing".
type scriptedCallProvider struct {
	mu    sync.Mutex
	calls int
	on    func(call int) (*provider.OneShotResult, error)
}

func (p *scriptedCallProvider) Name() string            { return "counting" }
func (p *scriptedCallProvider) Matrix() provider.Matrix { return provider.Matrix{} }
func (p *scriptedCallProvider) Start(context.Context, provider.StartOptions) (provider.Session, error) {
	return nil, provider.ErrUnsupported
}
func (p *scriptedCallProvider) OneShot(_ context.Context, _ provider.OneShotRequest) (*provider.OneShotResult, error) {
	p.mu.Lock()
	p.calls++
	call := p.calls
	p.mu.Unlock()
	return p.on(call)
}
func (p *scriptedCallProvider) Calls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

// Acceptance (1): a stage whose OneShot errors twice under retry: {max: 2}
// succeeds on the third try, consumes no on_fail/loop_back accounting, and
// leaves the task Done.
func TestPolicyRetryInfraFailureSucceedsOnThirdTry(t *testing.T) {
	h := newHarness(t)
	cp := &scriptedCallProvider{on: func(call int) (*provider.OneShotResult, error) {
		if call <= 2 {
			return nil, errors.New("transient: provider process died")
		}
		return &provider.OneShotResult{Text: "STAGE: pass"}, nil
	}}
	src := `name: retry-infra
stages:
  - name: executor
    agent: executor
    retry: { max: 2 }
`
	tpl, verr := template.Check([]byte(src), func(string) bool { return true })
	if verr != nil {
		t.Fatal(verr)
	}
	seeded := h.seed(contracts.Task{Title: "flaky infra"})
	sum, err := Run(context.Background(), Config{
		Data: h.data, Provider: cp, SessionID: h.sess.ID,
		Template: tpl, Cwd: h.cwd, Concurrency: 1,
		StageTimeout: 30 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(sum.Done) != 1 || sum.Done[0] != seeded[0].ID {
		t.Fatalf("summary: %+v; task: %+v", sum, h.task(t, seeded[0].ID))
	}
	if got := cp.Calls(); got != 3 {
		t.Errorf("OneShot calls: %d, want 3 (2 failures + the succeeding try)", got)
	}
	if fails := h.events(t, "stage.failed"); len(fails) != 0 {
		t.Errorf("stage.failed: %+v, want none — retries stay inside the retry path, never falling to on_fail", fails)
	}
	retried := h.events(t, "stage.retried")
	if len(retried) != 2 {
		t.Fatalf("stage.retried: %d, want 2: %+v", len(retried), retried)
	}
	task := h.task(t, seeded[0].ID)
	for _, n := range task.Notes {
		if n.Type == contracts.NoteObservation && strings.Contains(n.Content, "loop-back") {
			t.Errorf("loop-back note recorded (a loop_back iteration was consumed): %+v", n)
		}
	}
}

// Acceptance (2): a stage failing via the FailMarker is NOT retried under
// the default `when: error`, and IS retried under `when: always`.
func TestPolicyRetryFailMarkerWhenSplit(t *testing.T) {
	t.Run("default when=error does not retry a verdict", func(t *testing.T) {
		h := newHarness(t)
		f, err := fake.NewWithScenario(fake.Scenario{Name: "verdict-default", Rules: []fake.Rule{
			// If a retry ever happened, this stage would see feedback and
			// this more-specific rule would catch it and pass — so the
			// test fails loudly (task Done) instead of silently.
			{Match: "Address this feedback", Reply: "STAGE: pass"},
			{Match: "Pipeline stage: executor", Reply: "not good enough\nSTAGE: fail — bad output"},
		}})
		if err != nil {
			t.Fatal(err)
		}
		src := `name: retry-verdict-default
stages:
  - name: executor
    agent: executor
    retry: { max: 2 }
`
		tpl, verr := template.Check([]byte(src), func(string) bool { return true })
		if verr != nil {
			t.Fatal(verr)
		}
		seeded := h.seed(contracts.Task{Title: "rejected work"})
		sum := h.run(t, f, tpl, 1)
		if len(sum.Blocked) != 1 || sum.Blocked[0] != seeded[0].ID {
			t.Fatalf("summary: %+v, want the task blocked (never retried)", sum)
		}
		if retried := h.events(t, "stage.retried"); len(retried) != 0 {
			t.Errorf("stage.retried: %+v, want none — when: error (default) does not retry a FailMarker verdict", retried)
		}
	})

	t.Run("when=always retries a verdict", func(t *testing.T) {
		h := newHarness(t)
		f, err := fake.NewWithScenario(fake.Scenario{Name: "verdict-always", Rules: []fake.Rule{
			{Match: "Address this feedback", Reply: "STAGE: pass"},
			{Match: "Pipeline stage: executor", Reply: "not good enough\nSTAGE: fail — bad output"},
		}})
		if err != nil {
			t.Fatal(err)
		}
		src := `name: retry-verdict-always
stages:
  - name: executor
    agent: executor
    retry: { max: 2, when: always }
`
		tpl, verr := template.Check([]byte(src), func(string) bool { return true })
		if verr != nil {
			t.Fatal(verr)
		}
		seeded := h.seed(contracts.Task{Title: "rejected work retried"})
		sum := h.run(t, f, tpl, 1)
		if len(sum.Done) != 1 || sum.Done[0] != seeded[0].ID {
			t.Fatalf("summary: %+v, want the task done (retried, then passed): %+v", sum, h.task(t, seeded[0].ID))
		}
		if retried := h.events(t, "stage.retried"); len(retried) != 1 {
			t.Errorf("stage.retried: %+v, want exactly 1", retried)
		}
	})
}

// Acceptance (3): a non-zero-exit command stage gets the same when: error
// (default, not retried) vs when: always (retried) split as the agent case.
// The command itself creates a marker on its first (failing) run and
// succeeds once the marker is present on a second run — proving the second
// run reused the SAME directory (worktree never reset on retry) rather than
// a fresh one.
func TestPolicyRetryCommandVerdictWhenSplit(t *testing.T) {
	const cmd = `command: sh -c 'test -f retry.marker || (touch retry.marker && exit 1)'`

	t.Run("default when=error does not retry a non-zero exit", func(t *testing.T) {
		h := newHarness(t)
		src := "name: retry-cmd-default\nstages:\n  - name: check\n    " + cmd + "\n    retry: { max: 2 }\n"
		tpl, verr := template.Check([]byte(src), func(string) bool { return true })
		if verr != nil {
			t.Fatal(verr)
		}
		seeded := h.seed(contracts.Task{Title: "flaky check"})
		sum := h.run(t, fake.New(), tpl, 1)
		if len(sum.Blocked) != 1 || sum.Blocked[0] != seeded[0].ID {
			t.Fatalf("summary: %+v, want the task blocked (never retried)", sum)
		}
		if retried := h.events(t, "stage.retried"); len(retried) != 0 {
			t.Errorf("stage.retried: %+v, want none", retried)
		}
	})

	t.Run("when=always retries a non-zero exit", func(t *testing.T) {
		h := newHarness(t)
		src := "name: retry-cmd-always\nstages:\n  - name: check\n    " + cmd + "\n    retry: { max: 2, when: always }\n"
		tpl, verr := template.Check([]byte(src), func(string) bool { return true })
		if verr != nil {
			t.Fatal(verr)
		}
		seeded := h.seed(contracts.Task{Title: "flaky check retried"})
		sum := h.run(t, fake.New(), tpl, 1)
		if len(sum.Done) != 1 || sum.Done[0] != seeded[0].ID {
			t.Fatalf("summary: %+v, want the task done: %+v", sum, h.task(t, seeded[0].ID))
		}
		if retried := h.events(t, "stage.retried"); len(retried) != 1 {
			t.Errorf("stage.retried: %+v, want exactly 1", retried)
		}
	})
}

// Acceptance (4): retry exhaustion falls through to on_fail identically to
// an unretried failure — loop_back if configured (consuming exactly one
// loop-back, not one per retry attempt), else block with the same message
// shape a never-retried failure would get.
func TestPolicyRetryExhaustionFallsThroughToOnFail(t *testing.T) {
	t.Run("loop_back fires once retries are exhausted", func(t *testing.T) {
		h := newHarness(t)
		f, err := fake.NewWithScenario(fake.Scenario{Name: "exhaust-loopback", Rules: []fake.Rule{
			{Match: "Pipeline stage: code_review", Reply: "still bad\nSTAGE: fail — nope"},
		}})
		if err != nil {
			t.Fatal(err)
		}
		src := `name: retry-exhaust-loopback
stages:
  - name: executor
    agent: executor
  - name: code_review
    agent: code-reviewer
    retry: { max: 1, when: always }
    on_fail: { loop_back: executor, max_loops: 1 }
`
		tpl, verr := template.Check([]byte(src), func(string) bool { return true })
		if verr != nil {
			t.Fatal(verr)
		}
		seeded := h.seed(contracts.Task{Title: "needs work"})
		sum := h.run(t, f, tpl, 1)
		if len(sum.Blocked) != 1 || sum.Blocked[0] != seeded[0].ID {
			t.Fatalf("summary: %+v, want blocked after retry + one loop-back are both exhausted: %+v",
				sum, h.task(t, seeded[0].ID))
		}
		// Exactly one policy retry per code_review entry (max: 1), and
		// exactly one loop-back (max_loops: 1) — not one loop-back per
		// retry attempt.
		if retried := h.events(t, "stage.retried"); len(retried) != 2 {
			t.Errorf("stage.retried: %d, want 2 (one retry per code_review entry, entered twice via the single loop-back): %+v",
				len(retried), retried)
		}
		task := h.task(t, seeded[0].ID)
		var loopBacks int
		for _, n := range task.Notes {
			if n.Type == contracts.NoteObservation && strings.Contains(n.Content, "loop-back") {
				loopBacks++
			}
		}
		if loopBacks != 1 {
			t.Errorf("loop-back notes: %d, want exactly 1 (retry exhaustion, not each retry, drives the loop-back count)", loopBacks)
		}
	})

	t.Run("block message shape matches an unretried failure", func(t *testing.T) {
		unretried := func(t *testing.T) string {
			h := newHarness(t)
			f, err := fake.NewWithScenario(fake.Scenario{Name: "no-retry", Rules: []fake.Rule{
				{Match: "Pipeline stage: executor", Reply: "bad\nSTAGE: fail — same reason every time"},
			}})
			if err != nil {
				t.Fatal(err)
			}
			tpl, verr := template.Check([]byte("name: no-retry\nstages:\n  - name: executor\n    agent: executor\n"),
				func(string) bool { return true })
			if verr != nil {
				t.Fatal(verr)
			}
			seeded := h.seed(contracts.Task{Title: "t"})
			sum := h.run(t, f, tpl, 1)
			if len(sum.Blocked) != 1 {
				t.Fatalf("summary: %+v", sum)
			}
			return blockerNote(t, h, seeded[0].ID)
		}
		retriedThenExhausted := func(t *testing.T) string {
			h := newHarness(t)
			f, err := fake.NewWithScenario(fake.Scenario{Name: "retry-then-exhausted", Rules: []fake.Rule{
				{Match: "Pipeline stage: executor", Reply: "bad\nSTAGE: fail — same reason every time"},
			}})
			if err != nil {
				t.Fatal(err)
			}
			tpl, verr := template.Check([]byte("name: retry-then-exhausted\nstages:\n  - name: executor\n    agent: executor\n    retry: { max: 2, when: always }\n"),
				func(string) bool { return true })
			if verr != nil {
				t.Fatal(verr)
			}
			seeded := h.seed(contracts.Task{Title: "t"})
			sum := h.run(t, f, tpl, 1)
			if len(sum.Blocked) != 1 {
				t.Fatalf("summary: %+v", sum)
			}
			return blockerNote(t, h, seeded[0].ID)
		}
		a := unretried(t)
		b := retriedThenExhausted(t)
		if a != b {
			t.Errorf("blocker note shape differs:\nunretried:          %q\nretried+exhausted:  %q", a, b)
		}
	})
}

// blockerNote returns the task's blocker note content.
func blockerNote(t *testing.T, h *harness, taskID string) string {
	t.Helper()
	task := h.task(t, taskID)
	for _, n := range task.Notes {
		if n.Type == contracts.NoteBlocker {
			return n.Content
		}
	}
	t.Fatalf("no blocker note found for task %s", taskID)
	return ""
}

// Acceptance (6): each policy retry writes its own attempt log, and
// usage.recorded events (only posted on a completed one-shot, i.e. every
// attempt here since each one reaches the FailMarker check) carry 1, 2, 3.
func TestPolicyRetryEachAttemptGetsOwnLogAndUsage(t *testing.T) {
	h := newHarness(t)
	f, err := fake.NewWithScenario(fake.Scenario{Name: "attempt-numbering", Rules: []fake.Rule{
		{Match: "Address this feedback", Reply: "still working on it\nSTAGE: fail — keep trying"},
		{Match: "Pipeline stage: executor", Reply: "first pass\nSTAGE: fail — keep trying"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	// The rule above never lets the stage pass, so pin max so the run
	// terminates (blocked) after exactly 3 attempts total (1 + 2 retries),
	// with all 3 observable via their own logs and usage events.
	src := `name: retry-attempt-numbering
stages:
  - name: executor
    agent: executor
    retry: { max: 2, when: always }
`
	tpl, verr := template.Check([]byte(src), func(string) bool { return true })
	if verr != nil {
		t.Fatal(verr)
	}
	seeded := h.seed(contracts.Task{Title: "keeps failing"})
	madsHome := t.TempDir()
	sum, err := Run(context.Background(), Config{
		Data: h.data, Provider: f, SessionID: h.sess.ID,
		Template: tpl, Cwd: h.cwd, Concurrency: 1,
		StageTimeout: 30 * time.Second, MadsHome: madsHome,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(sum.Blocked) != 1 || sum.Blocked[0] != seeded[0].ID {
		t.Fatalf("summary: %+v", sum)
	}

	started := h.events(t, "pipeline.started")
	if len(started) != 1 {
		t.Fatalf("pipeline.started: %+v", started)
	}
	runSeq := started[0].Seq

	for _, attempt := range []int{1, 2, 3} {
		path := outputLogPath(madsHome, h.sess.ID, runSeq, seeded[0].ID, "executor", attempt)
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("attempt-%d log missing: %v", attempt, err)
		}
		if !strings.Contains(string(body), "outcome=fail") {
			t.Errorf("attempt-%d log missing fail outcome: %q", attempt, body)
		}
	}

	type stageAttempt struct {
		Stage   string `json:"stage"`
		Attempt int    `json:"attempt"`
	}
	var attempts []int
	for _, ev := range h.events(t, "usage.recorded") {
		var p stageAttempt
		if err := json.Unmarshal(ev.Payload, &p); err != nil {
			t.Fatal(err)
		}
		if p.Stage == "executor" {
			attempts = append(attempts, p.Attempt)
		}
	}
	if want := []int{1, 2, 3}; len(attempts) != len(want) ||
		attempts[0] != want[0] || attempts[1] != want[1] || attempts[2] != want[2] {
		t.Errorf("usage.recorded attempts: %v, want %v", attempts, want)
	}
}

// Acceptance (7): defaults.retry applies to a stage with no retry of its
// own; a stage's own retry overrides the template default.
func TestPolicyRetryDefaultsResolution(t *testing.T) {
	t.Run("defaults.retry applies with no stage-level retry", func(t *testing.T) {
		h := newHarness(t)
		f, err := fake.NewWithScenario(fake.Scenario{Name: "defaults-retry", Rules: []fake.Rule{
			{Match: "Address this feedback", Reply: "STAGE: pass"},
			{Match: "Pipeline stage: executor", Reply: "bad\nSTAGE: fail — retry via defaults"},
		}})
		if err != nil {
			t.Fatal(err)
		}
		src := `name: defaults-retry
defaults:
  retry: { max: 1, when: always }
stages:
  - name: executor
    agent: executor
`
		tpl, verr := template.Check([]byte(src), func(string) bool { return true })
		if verr != nil {
			t.Fatal(verr)
		}
		seeded := h.seed(contracts.Task{Title: "covered by defaults"})
		sum := h.run(t, f, tpl, 1)
		if len(sum.Done) != 1 || sum.Done[0] != seeded[0].ID {
			t.Fatalf("summary: %+v, want done via the template default retry policy: %+v",
				sum, h.task(t, seeded[0].ID))
		}
		if retried := h.events(t, "stage.retried"); len(retried) != 1 {
			t.Errorf("stage.retried: %+v, want exactly 1 (from defaults.retry)", retried)
		}
	})

	t.Run("a stage's own retry overrides the template default", func(t *testing.T) {
		h := newHarness(t)
		// defaults.retry says never retry; the stage's own retry says
		// always — the stage's own policy must win outright, not merge.
		f, err := fake.NewWithScenario(fake.Scenario{Name: "override-retry", Rules: []fake.Rule{
			{Match: "Address this feedback", Reply: "STAGE: pass"},
			{Match: "Pipeline stage: executor", Reply: "bad\nSTAGE: fail — retry via stage override"},
		}})
		if err != nil {
			t.Fatal(err)
		}
		src := `name: override-retry
defaults:
  retry: { max: 5, when: never }
stages:
  - name: executor
    agent: executor
    retry: { max: 1, when: always }
`
		tpl, verr := template.Check([]byte(src), func(string) bool { return true })
		if verr != nil {
			t.Fatal(verr)
		}
		seeded := h.seed(contracts.Task{Title: "stage overrides defaults"})
		sum := h.run(t, f, tpl, 1)
		if len(sum.Done) != 1 || sum.Done[0] != seeded[0].ID {
			t.Fatalf("summary: %+v, want done via the STAGE's own retry (when: always), not the template default (when: never): %+v",
				sum, h.task(t, seeded[0].ID))
		}
		if retried := h.events(t, "stage.retried"); len(retried) != 1 {
			t.Errorf("stage.retried: %+v, want exactly 1", retried)
		}
	})
}

// TestPolicyRetryStageAttemptSurvivesLoopBack proves stage.retried's
// stage_attempt names the stage-ENTRY attempt (e.attempts) the failing try
// actually ran under — and so the output log it actually wrote
// (<stage>-attempt-<n>.log) — while attempt keeps naming the LOCAL retry
// ordinal within that entry. This is the exact case the two-key split
// exists for: executor is entered three times (twice via code_review's
// loop_back, exhausting max_loops: 2), and the retry policy fires on
// executor's THIRD entry — before the fix both events used the same
// "attempt" key, so a consumer joining stage.retried to a log/transcript
// ref via "attempt" would land on executor-attempt-1.log instead of the
// executor-attempt-3.log the failing try (and this event's own "reason")
// actually describe.
func TestPolicyRetryStageAttemptSurvivesLoopBack(t *testing.T) {
	h := newHarness(t)
	cp := &scriptedCallProvider{on: func(call int) (*provider.OneShotResult, error) {
		switch call {
		case 1, 3: // executor entries 1 and 2: pass, sending the task to code_review
			return &provider.OneShotResult{Text: "STAGE: pass"}, nil
		case 2, 4: // code_review entries 1 and 2: fail -> loop_back into executor
			return &provider.OneShotResult{Text: "not good enough\nSTAGE: fail — needs more work"}, nil
		case 5: // executor entry 3, first try: infra failure -> policy retry
			return nil, errors.New("transient: provider process died")
		case 6: // executor entry 3, retry (its own stage-entry attempt 4): pass
			return &provider.OneShotResult{Text: "STAGE: pass"}, nil
		case 7: // code_review entry 3: pass -> task done
			return &provider.OneShotResult{Text: "STAGE: pass"}, nil
		}
		return nil, errors.New("unexpected call")
	}}
	src := `name: retry-stage-attempt
stages:
  - name: executor
    agent: executor
    retry: { max: 1 }
  - name: code_review
    agent: code-reviewer
    on_fail: { loop_back: executor, max_loops: 2 }
`
	tpl, verr := template.Check([]byte(src), func(string) bool { return true })
	if verr != nil {
		t.Fatal(verr)
	}
	seeded := h.seed(contracts.Task{Title: "loop then retry"})
	sum, err := Run(context.Background(), Config{
		Data: h.data, Provider: cp, SessionID: h.sess.ID,
		Template: tpl, Cwd: h.cwd, Concurrency: 1,
		StageTimeout: 30 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(sum.Done) != 1 || sum.Done[0] != seeded[0].ID {
		t.Fatalf("summary: %+v; task: %+v", sum, h.task(t, seeded[0].ID))
	}
	if got := cp.Calls(); got != 7 {
		t.Errorf("OneShot calls: %d, want 7 (2 loop-back round-trips + a failed/retried third executor entry + the passing review)", got)
	}

	retried := h.events(t, "stage.retried")
	if len(retried) != 1 {
		t.Fatalf("stage.retried: %d, want exactly 1: %+v", len(retried), retried)
	}
	type retriedPayload struct {
		Stage        string `json:"stage"`
		Attempt      int    `json:"attempt"`
		StageAttempt int    `json:"stage_attempt"`
	}
	var p retriedPayload
	if err := json.Unmarshal(retried[0].Payload, &p); err != nil {
		t.Fatal(err)
	}
	if p.Stage != "executor" || p.Attempt != 1 || p.StageAttempt != 3 {
		t.Errorf("stage.retried payload: %+v, want {stage: executor, attempt: 1, stage_attempt: 3}", p)
	}
}
