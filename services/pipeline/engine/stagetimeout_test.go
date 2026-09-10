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

// siblingTimeoutTemplate exercises per-stage resolution rather than a
// single process-global: "quick" carries no timeout: of its own and must
// run out its full budget under the long cfg.StageTimeout below, while
// "slow" carries an explicit short one and must be cut off by IT rather
// than by whatever "quick" resolved to (a shared/cached timeout variable
// would either time "quick" out for no reason or let "slow" run the
// cfg-level 5s instead of its own 300ms).
const siblingTimeoutTemplate = `name: siblings
stages:
  - name: quick
    command: "true"
  - name: slow
    command: sleep 2
    timeout: 300ms
`

// TestStageTimeoutOverridesLongerConfigDefault covers acceptance case 1: a
// stage's own timeout: fires even though the engine-wide default
// (StageTimeout) would have comfortably let the same command finish, and a
// sibling stage with no override of its own is unaffected by that shorter
// value.
func TestStageTimeoutOverridesLongerConfigDefault(t *testing.T) {
	h := newHarness(t)
	parsed, verr := template.Check([]byte(siblingTimeoutTemplate), func(string) bool { return true })
	if verr != nil {
		t.Fatal(verr)
	}
	seeded := h.seed(contracts.Task{Title: "siblings"})

	start := time.Now()
	sum, err := Run(context.Background(), Config{
		Data: h.data, Provider: fake.New(), SessionID: h.sess.ID,
		Template: parsed, Cwd: h.cwd, Concurrency: 1,
		StageTimeout: 5 * time.Second, LeaseTTL: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	// "quick" (no override) must have actually run and succeeded — if it
	// had inherited "slow"'s 300ms budget instead of the 5s default it
	// would still have passed (it's instant), so the real signal here is
	// that the run reached and then stalled on "slow", not that "quick"
	// merely didn't fail.
	if elapsed := time.Since(start); elapsed >= 2*time.Second {
		t.Fatalf("run took %s — slow's 300ms stage timeout did not override the 5s config default", elapsed)
	}
	if len(sum.Blocked) != 1 || sum.Blocked[0] != seeded[0].ID {
		t.Fatalf("summary: %+v; task: %+v", sum, h.task(t, seeded[0].ID))
	}
}

// TestDefaultsTimeoutAppliesWhenStageOmitsOverride covers half of
// acceptance case 2: a template-level defaults.timeout: reaches a stage
// that sets none of its own, beating even a much longer engine-wide
// StageTimeout — proving the stage actually resolved through
// Defaults.Timeout rather than falling straight to cfg.StageTimeout.
func TestDefaultsTimeoutAppliesWhenStageOmitsOverride(t *testing.T) {
	h := newHarness(t)
	const tmpl = `name: defaults-fallback
defaults:
  timeout: 300ms
stages:
  - name: build
    command: sleep 2
`
	parsed, verr := template.Check([]byte(tmpl), func(string) bool { return true })
	if verr != nil {
		t.Fatal(verr)
	}
	seeded := h.seed(contracts.Task{Title: "defaults-fallback"})

	start := time.Now()
	sum, err := Run(context.Background(), Config{
		Data: h.data, Provider: fake.New(), SessionID: h.sess.ID,
		Template: parsed, Cwd: h.cwd, Concurrency: 1,
		StageTimeout: 5 * time.Second, LeaseTTL: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed >= 2*time.Second {
		t.Fatalf("run took %s — defaults.timeout (300ms) did not apply; the 5s config default was used instead", elapsed)
	}
	if len(sum.Blocked) != 1 || sum.Blocked[0] != seeded[0].ID {
		t.Fatalf("summary: %+v; task: %+v", sum, h.task(t, seeded[0].ID))
	}
}

// TestStageTimeoutOverridesDefaults covers the other half of acceptance
// case 2: a stage's own timeout: beats a shorter defaults.timeout:, so a
// command that would have been killed by the template default runs to
// completion under the stage's explicit, longer budget.
func TestStageTimeoutOverridesDefaults(t *testing.T) {
	h := newHarness(t)
	const tmpl = `name: stage-beats-defaults
defaults:
  timeout: 300ms
stages:
  - name: build
    command: sleep 1
    timeout: 5s
`
	parsed, verr := template.Check([]byte(tmpl), func(string) bool { return true })
	if verr != nil {
		t.Fatal(verr)
	}
	seeded := h.seed(contracts.Task{Title: "stage-beats-defaults"})

	sum, err := Run(context.Background(), Config{
		Data: h.data, Provider: fake.New(), SessionID: h.sess.ID,
		Template: parsed, Cwd: h.cwd, Concurrency: 1,
		StageTimeout: time.Minute, LeaseTTL: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(sum.Done) != 1 || sum.Done[0] != seeded[0].ID {
		t.Fatalf("stage's 5s override should have beaten defaults.timeout's 300ms and let the 1s sleep finish; summary: %+v, task: %+v",
			sum, h.task(t, seeded[0].ID))
	}
}

// TestStageTimeoutPrecedence unit-tests engine.stageTimeout directly,
// covering the pointer-chain defensiveness (nil Template, nil Defaults,
// nil per-level Timeout) and the non-positive/sentinel fallthrough that the
// harness-based tests above can't cheaply exercise one branch at a time.
func TestStageTimeoutPrecedence(t *testing.T) {
	const cfgDefault = 10 * time.Minute
	stageOverride := template.Duration(time.Second)
	templateDefault := template.Duration(2 * time.Minute)
	zero := template.Duration(0)
	negative := template.Duration(-time.Second)

	// A genuine invalidDuration sentinel, produced the same way the real
	// parser produces one (an unparseable timeout: string), rather than
	// poking at pkg/template's unexported constant from outside the
	// package.
	badTemplate, err := template.Parse([]byte("name: bad\nstages:\n  - name: s\n    command: x\n    timeout: not-a-duration\n"))
	if err != nil {
		t.Fatal(err)
	}
	sentinel := badTemplate.Stages[0].Timeout

	cases := []struct {
		name  string
		stage template.Stage
		tmpl  *template.Template
		want  time.Duration
	}{
		{
			name:  "stage override wins over template default",
			stage: template.Stage{Timeout: &stageOverride},
			tmpl:  &template.Template{Defaults: &template.Defaults{Timeout: &templateDefault}},
			want:  time.Second,
		},
		{
			name:  "template default applies when stage omits one",
			stage: template.Stage{},
			tmpl:  &template.Template{Defaults: &template.Defaults{Timeout: &templateDefault}},
			want:  2 * time.Minute,
		},
		{
			name:  "cfg default applies when neither stage nor template sets one",
			stage: template.Stage{},
			tmpl:  &template.Template{},
			want:  cfgDefault,
		},
		{
			name:  "nil Template (bare unit-test Config) falls to cfg default",
			stage: template.Stage{},
			tmpl:  nil,
			want:  cfgDefault,
		},
		{
			name:  "nil Defaults falls to cfg default",
			stage: template.Stage{},
			tmpl:  &template.Template{Defaults: nil},
			want:  cfgDefault,
		},
		{
			name:  "zero stage override is not an override — falls through to template default",
			stage: template.Stage{Timeout: &zero},
			tmpl:  &template.Template{Defaults: &template.Defaults{Timeout: &templateDefault}},
			want:  2 * time.Minute,
		},
		{
			name:  "negative template default is not an override — falls through to cfg default",
			stage: template.Stage{},
			tmpl:  &template.Template{Defaults: &template.Defaults{Timeout: &negative}},
			want:  cfgDefault,
		},
		{
			name:  "sentinel invalid stage timeout falls through to cfg default",
			stage: template.Stage{Timeout: sentinel},
			tmpl:  &template.Template{},
			want:  cfgDefault,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := &engine{cfg: Config{Template: c.tmpl, StageTimeout: cfgDefault}}
			if got := e.stageTimeout(c.stage); got != c.want {
				t.Fatalf("stageTimeout() = %s, want %s", got, c.want)
			}
		})
	}
}

// TestStageTimeoutIsRetried proves the single most important case in the
// retry design and the one acceptance criteria singles out as currently
// unproven: a stage that hits its own timeout is retried exactly like any
// other infra failure, not treated as unretryable just because a
// context.WithTimeout cancellation caused it. build's command drops a
// marker on its way into a long sleep on the first run — so the first try
// blows well past its 200ms budget and gets killed by runStage's sctx — and
// finds the marker already there on the retry, exiting immediately.
func TestStageTimeoutIsRetried(t *testing.T) {
	h := newHarness(t)
	const tmpl = `name: timeout-retry
stages:
  - name: build
    command: sh -c 'test -f timeout.marker || (touch timeout.marker && sleep 5)'
    timeout: 200ms
    retry: { max: 1 }
`
	parsed, verr := template.Check([]byte(tmpl), func(string) bool { return true })
	if verr != nil {
		t.Fatal(verr)
	}
	seeded := h.seed(contracts.Task{Title: "timeout then pass"})

	sum := h.run(t, fake.New(), parsed, 1)
	if len(sum.Done) != 1 || sum.Done[0] != seeded[0].ID {
		t.Fatalf("summary: %+v, want done (the timed-out first try retried and the second passed): %+v",
			sum, h.task(t, seeded[0].ID))
	}
	if fails := h.events(t, "stage.failed"); len(fails) != 0 {
		t.Errorf("stage.failed: %+v, want none — a timed-out stage is retried, not surfaced as a failure", fails)
	}
	if retried := h.events(t, "stage.retried"); len(retried) != 1 {
		t.Errorf("stage.retried: %d, want exactly 1: %+v", len(retried), retried)
	}
}
