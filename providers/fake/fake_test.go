package fake_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/drewkidwell/mads/providers/conformance"
	"github.com/drewkidwell/mads/providers/fake"
	"github.com/drewkidwell/mads/providers/provider"
)

// script is the fake's built-in dialect, in conformance terms.
func script() conformance.Script {
	return conformance.Script{
		Echo:    func(marker string) string { return "echo " + marker },
		Slow:    func() string { return "slow" },
		Recall:  func() string { return "recall" },
		Timeout: 2 * time.Second,
	}
}

// The fake passes the full conformance suite — the point of its existence.
func TestConformance(t *testing.T) {
	conformance.Run(t, fake.New(), script())
}

// A scenario-scripted fake still passes conformance: the built-in dialect
// backstops every unmatched prompt.
func TestConformanceWithScenario(t *testing.T) {
	sc, err := fake.LoadScenario("testdata/demo.yaml")
	if err != nil {
		t.Fatal(err)
	}
	f, err := fake.NewWithScenario(sc)
	if err != nil {
		t.Fatal(err)
	}
	conformance.Run(t, f, script())
}

func TestRegistersCleanly(t *testing.T) {
	if err := provider.NewRegistry().Register(fake.New()); err != nil {
		t.Fatal(err)
	}
}

// collect reads one turn's events; ended reports whether a turn end arrived
// before the stream closed or timed out.
func collect(t *testing.T, sess provider.Session) (events []provider.Event, ended bool) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case ev, ok := <-sess.Events():
			if !ok {
				return events, false
			}
			events = append(events, ev)
			if ev.Kind == provider.EventTurnEnd {
				return events, true
			}
		case <-deadline:
			t.Fatal("no turn end and no stream close within 2s")
		}
	}
}

// A scripted rule: tool events first (with the file really written), then
// the reply in exact chunks, then a turn end — and it all lands in the
// transcript.
func TestScenarioToolsAndChunks(t *testing.T) {
	sc, err := fake.LoadScenario("testdata/demo.yaml")
	if err != nil {
		t.Fatal(err)
	}
	f, err := fake.NewWithScenario(sc)
	if err != nil {
		t.Fatal(err)
	}
	cwd := t.TempDir()
	sess, err := f.Start(context.Background(), provider.StartOptions{Mode: provider.ModeACP, Cwd: cwd})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sess.Close() }()
	if err := sess.Send(context.Background(), "please add auth"); err != nil {
		t.Fatal(err)
	}
	events, ended := collect(t, sess)
	if !ended {
		t.Fatal("stream closed without turn end")
	}

	var kinds []provider.EventKind
	var text strings.Builder
	for _, ev := range events {
		kinds = append(kinds, ev.Kind)
		text.WriteString(ev.Text)
	}
	want := []provider.EventKind{
		provider.EventToolUse, provider.EventText, provider.EventText, provider.EventTurnEnd,
	}
	if len(kinds) != len(want) {
		t.Fatalf("events %v, want %v", kinds, want)
	}
	for i := range want {
		if kinds[i] != want[i] {
			t.Fatalf("event %d: %s, want %s (all: %v)", i, kinds[i], want[i], kinds)
		}
	}
	if text.String() != "Working on auth. Done." {
		t.Errorf("streamed text %q", text.String())
	}
	if events[0].Tool == nil || events[0].Tool.Path != "auth/login.go" {
		t.Errorf("tool event: %+v", events[0].Tool)
	}
	if events[len(events)-1].Usage == nil || events[len(events)-1].Usage.OutputTokens == 0 {
		t.Errorf("turn end usage: %+v", events[len(events)-1].Usage)
	}

	// The scripted edit really happened (downstream file tracking sees it).
	raw, err := os.ReadFile(filepath.Join(cwd, "auth", "login.go"))
	if err != nil || !strings.Contains(string(raw), "package auth") {
		t.Errorf("scripted file write: %q, %v", raw, err)
	}

	// And the exchange is in the transcript, tool message included.
	msgs, err := f.Transcript(context.Background(), sess.Ref())
	if err != nil {
		t.Fatal(err)
	}
	var roles []string
	for _, m := range msgs {
		roles = append(roles, m.Role)
	}
	if len(roles) != 3 || roles[0] != "user" || roles[1] != "tool" || roles[2] != "agent" {
		t.Errorf("transcript roles: %v", roles)
	}

	// Scenario models replace the default list.
	models, err := f.Models(context.Background())
	if err != nil || len(models) != 1 || models[0].ID != "demo-1" {
		t.Errorf("scenario models: %v, %v", models, err)
	}
}

// Crash on cue: the event stream closes with NO turn end — exactly the
// signal downstream supervision (Router) watches for.
func TestCrashOnCue(t *testing.T) {
	f := fake.New()
	sess, err := f.Start(context.Background(), provider.StartOptions{Mode: provider.ModeACP})
	if err != nil {
		t.Fatal(err)
	}
	if err := sess.Send(context.Background(), "crash"); err != nil {
		t.Fatal(err)
	}
	if _, ended := collect(t, sess); ended {
		t.Fatal("crash produced a clean turn end; supervision would never notice")
	}
	if err := sess.Send(context.Background(), "echo hi"); err == nil {
		t.Error("Send on a crashed session must fail")
	}
	// A one-shot crash is a plain error.
	fc, err := fake.NewWithScenario(fake.Scenario{Name: "c", Rules: []fake.Rule{{Match: "boom", Crash: true}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fc.OneShot(context.Background(), provider.OneShotRequest{Prompt: "boom"}); err == nil {
		t.Error("scripted one-shot crash returned no error")
	}
}

// Rate limit on cue surfaces as the typed error (§11.9 inform-and-wait).
func TestRateLimitOnCue(t *testing.T) {
	_, err := fake.New().OneShot(context.Background(), provider.OneShotRequest{Prompt: "ratelimit"})
	var rle *provider.RateLimitError
	if !errors.As(err, &rle) || rle.RetryAfter != time.Second {
		t.Fatalf("want RateLimitError with RetryAfter=1s, got %v", err)
	}
}

// RateLimitTimes scripts "rate-limited N times, then succeeds": the first N
// matching turns fail with the typed error, the rest run normally.
func TestRateLimitTimesThenSucceeds(t *testing.T) {
	f, err := fake.NewWithScenario(fake.Scenario{Name: "rl", Rules: []fake.Rule{
		{Match: "retry me", Reply: "all good", RateLimitTimes: 2},
	}})
	if err != nil {
		t.Fatal(err)
	}
	req := provider.OneShotRequest{Prompt: "retry me"}
	for i := 0; i < 2; i++ {
		_, err := f.OneShot(context.Background(), req)
		var rle *provider.RateLimitError
		if !errors.As(err, &rle) {
			t.Fatalf("attempt %d: want RateLimitError, got %v", i+1, err)
		}
	}
	res, err := f.OneShot(context.Background(), req)
	if err != nil {
		t.Fatalf("third attempt: want success, got %v", err)
	}
	if res.Text != "all good" {
		t.Errorf("third attempt: text = %q, want %q", res.Text, "all good")
	}
}

// RateLimit and RateLimitTimes together are rejected as ambiguous.
func TestScenarioValidationRateLimitTimesExclusive(t *testing.T) {
	_, err := fake.NewWithScenario(fake.Scenario{Name: "bad-rl", Rules: []fake.Rule{
		{Match: "x", RateLimit: true, RateLimitTimes: 2},
	}})
	if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("want mutually-exclusive rejection, got %v", err)
	}
}

// Delay on cue keeps the turn observably in flight, and an interrupt during
// the delay ends it with the partial text.
func TestDelayAndInterrupt(t *testing.T) {
	f, err := fake.NewWithScenario(fake.Scenario{Name: "d", Rules: []fake.Rule{{
		Match: "think", Chunks: []string{"a", "b", "c"}, DelayMs: 500,
	}}})
	if err != nil {
		t.Fatal(err)
	}
	sess, err := f.Start(context.Background(), provider.StartOptions{Mode: provider.ModeACP})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sess.Close() }()
	start := time.Now()
	if err := sess.Send(context.Background(), "think"); err != nil {
		t.Fatal(err)
	}
	if err := sess.Interrupt(context.Background()); err != nil {
		t.Fatal(err)
	}
	events, ended := collect(t, sess)
	if !ended {
		t.Fatal("no turn end after interrupt")
	}
	last := events[len(events)-1]
	if !last.Interrupted {
		t.Errorf("turn end not marked interrupted: %+v", last)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("interrupt during delay took %s; should land within the first delay window", elapsed)
	}
}

func TestResumeUnknownRef(t *testing.T) {
	_, err := fake.New().Start(context.Background(), provider.StartOptions{
		Mode: provider.ModeACP, Resume: "fake-999",
	})
	if err == nil || !strings.Contains(err.Error(), "unknown session ref") {
		t.Fatalf("resume of unknown ref: %v", err)
	}
}

// TestDoubleAttachRejected is the regression guard for the hard-sequencing
// requirement a transport switch depends on: a real harness (e.g.
// claude-code) refuses a second attachment to a still-live session, so the
// old process must be fully dead before the new one starts. The fake must
// enforce the same rule, or a Runner bug that starts the new transport
// before the old one closes would go undetected here.
func TestDoubleAttachRejected(t *testing.T) {
	ctx := context.Background()
	f := fake.New()

	first, err := f.Start(ctx, provider.StartOptions{Mode: provider.ModeACP})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	ref := first.Ref()

	if _, err := f.Start(ctx, provider.StartOptions{Mode: provider.ModePTY, Resume: ref}); err == nil {
		t.Fatal("resume of a still-live ref succeeded, want rejection")
	} else if !strings.Contains(err.Error(), "already attached") {
		t.Fatalf("resume of a still-live ref: %v, want an already-attached error", err)
	}

	if err := first.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	second, err := f.Start(ctx, provider.StartOptions{Mode: provider.ModePTY, Resume: ref})
	if err != nil {
		t.Fatalf("resume after close: %v", err)
	}
	if second.Ref() != ref {
		t.Fatalf("resumed ref = %q, want %q", second.Ref(), ref)
	}
}

// Scenario validation reports every problem at once (§10.3 ethos).
func TestScenarioValidation(t *testing.T) {
	_, err := fake.NewWithScenario(fake.Scenario{Name: "bad", Rules: []fake.Rule{
		{Match: "", Reply: "shadow"}, // catch-all not last
		{Match: "x", Tools: []fake.ToolStep{{Name: "", Content: "body"}}},
	}})
	if err == nil {
		t.Fatal("invalid scenario accepted")
	}
	for _, want := range []string{"shadows later rules", "name is required", "content without a path"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q", err, want)
		}
	}
}

// Usage is deterministic — the same exchange always costs the same.
func TestDeterministicUsage(t *testing.T) {
	f := fake.New()
	a, err := f.OneShot(context.Background(), provider.OneShotRequest{Prompt: "echo hello"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := f.OneShot(context.Background(), provider.OneShotRequest{Prompt: "echo hello"})
	if err != nil {
		t.Fatal(err)
	}
	if a.Usage != b.Usage || a.Usage.OutputTokens == 0 {
		t.Fatalf("usage not deterministic: %+v vs %+v", a.Usage, b.Usage)
	}
}
