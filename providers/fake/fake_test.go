package fake_test

import (
	"context"
	"errors"
	"fmt"
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
		Echo:           func(marker string) string { return "echo " + marker },
		Slow:           func() string { return "slow" },
		Recall:         func() string { return "recall" },
		AttemptWrite:   func() string { return "attempt-write" },
		AttemptCommand: func() string { return "attempt-command" },
		Timeout:        2 * time.Second,
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

// A scripted Rule.Tools entry produces an EventToolUse through OnEvent when
// OneShot is driven directly, not just through the interactive Session path
// (TestScenarioToolsAndChunks above).
func TestOneShotToolUseEmitsEvent(t *testing.T) {
	sc, err := fake.LoadScenario("testdata/demo.yaml")
	if err != nil {
		t.Fatal(err)
	}
	f, err := fake.NewWithScenario(sc)
	if err != nil {
		t.Fatal(err)
	}
	cwd := t.TempDir()
	var kinds []provider.EventKind
	var tool *provider.ToolUse
	res, err := f.OneShot(context.Background(), provider.OneShotRequest{
		Prompt: "please add auth",
		Cwd:    cwd,
		OnEvent: func(ev provider.Event) {
			kinds = append(kinds, ev.Kind)
			if ev.Kind == provider.EventToolUse {
				tool = ev.Tool
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if tool == nil || tool.Path != "auth/login.go" {
		t.Errorf("tool event: %+v", tool)
	}
	if len(kinds) < 2 || kinds[0] != provider.EventToolUse {
		t.Errorf("want tool use before text, got %v", kinds)
	}
	if res.Text != "Working on auth. Done." {
		t.Errorf("text: %q", res.Text)
	}
}

// Scenario.AnnounceOneShotRef opts a fake into the claude-code-shaped
// behavior: OneShot announces provider.EventSessionRef before the invocation
// completes, OneShotResult.Ref carries the same value, and Transcript(ref)
// backs it with a normalized history (tool_use, tool_result, agent text) — a
// caller polling mid-invocation sees the tool_use turn with no matching
// tool_result yet, exactly the "still working" state the drill-in's liveness
// row renders.
func TestOneShotAnnouncesRefAndBacksTranscript(t *testing.T) {
	sc, err := fake.LoadScenario("testdata/demo.yaml")
	if err != nil {
		t.Fatal(err)
	}
	sc.AnnounceOneShotRef = true
	f, err := fake.NewWithScenario(sc)
	if err != nil {
		t.Fatal(err)
	}
	cwd := t.TempDir()

	var announced string
	var midFlightRoles []string
	res, err := f.OneShot(context.Background(), provider.OneShotRequest{
		Prompt: "please add auth",
		Cwd:    cwd,
		OnEvent: func(ev provider.Event) {
			if ev.Kind != provider.EventSessionRef {
				return
			}
			announced = ev.Ref
			// The ref is announced before the chunked reply streams (demo.yaml's
			// tool runs first) — Transcript(ref) at this instant must already
			// show the tool_use turn, with no tool_result after it yet.
			msgs, err := f.Transcript(context.Background(), ev.Ref)
			if err != nil {
				t.Fatal(err)
			}
			for _, m := range msgs {
				midFlightRoles = append(midFlightRoles, m.Role)
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if announced == "" {
		t.Fatal("OneShot never announced a session ref")
	}
	if res.Ref != announced {
		t.Errorf("OneShotResult.Ref = %q, want the announced ref %q", res.Ref, announced)
	}
	if len(midFlightRoles) != 1 || midFlightRoles[0] != "user" {
		t.Errorf("mid-flight (at announcement) transcript roles: %v, want just [user]", midFlightRoles)
	}

	msgs, err := f.Transcript(context.Background(), res.Ref)
	if err != nil {
		t.Fatal(err)
	}
	var roles []string
	for _, m := range msgs {
		roles = append(roles, m.Role)
	}
	want := []string{"user", "tool", "tool_result", "agent"}
	if len(roles) != len(want) {
		t.Fatalf("final transcript roles: %v, want %v", roles, want)
	}
	for i := range want {
		if roles[i] != want[i] {
			t.Errorf("final transcript role %d: %q, want %q (all: %v)", i, roles[i], want[i], roles)
		}
	}
}

// The zero-value scenario keeps OneShot's original behavior: no ref
// announced, no transcript to back it — the fallback-to-raw-log case.
func TestOneShotNoRefWithoutAnnounceOptIn(t *testing.T) {
	f := fake.New()
	var kinds []provider.EventKind
	res, err := f.OneShot(context.Background(), provider.OneShotRequest{
		Prompt:  "echo hi",
		OnEvent: func(ev provider.Event) { kinds = append(kinds, ev.Kind) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Ref != "" {
		t.Errorf("OneShotResult.Ref = %q, want empty without AnnounceOneShotRef", res.Ref)
	}
	for _, k := range kinds {
		if k == provider.EventSessionRef {
			t.Fatalf("unexpected EventSessionRef without AnnounceOneShotRef: %v", kinds)
		}
	}
}

// TestFakeComplete proves Fake implements provider.Completer using the same
// scripted dialect as OneShot, but leaves no ref or transcript behind —
// Complete's hermetic "no session left behind" contract.
func TestFakeComplete(t *testing.T) {
	f := fake.New()
	completer, ok := interface{}(f).(provider.Completer)
	if !ok {
		t.Fatal("fake.Fake does not implement provider.Completer")
	}
	res, err := completer.Complete(context.Background(), provider.CompleteRequest{Prompt: "echo hi"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "you said hi" {
		t.Errorf("Complete text = %q, want %q", res.Text, "you said hi")
	}
}

// TestFakeCompleteScriptedRateLimitAndCrash proves Complete surfaces the same
// scripted rate-limit/crash outcomes OneShot does.
func TestFakeCompleteScriptedRateLimitAndCrash(t *testing.T) {
	f := fake.New()
	_, err := f.Complete(context.Background(), provider.CompleteRequest{Prompt: "ratelimit"})
	var rle *provider.RateLimitError
	if !errors.As(err, &rle) || rle.RetryAfter != time.Second {
		t.Fatalf("want RateLimitError with RetryAfter=1s, got %v", err)
	}
	if _, err := f.Complete(context.Background(), provider.CompleteRequest{Prompt: "crash"}); err == nil {
		t.Error("scripted Complete crash returned no error")
	}
}

// TestOneShotTranscriptDirBridgesSeparateInstances proves WithTranscriptDir
// closes the real gap: in production, the process that runs OneShot (`mads
// run`) and the process that later reads Transcript(ref) (the Router,
// serving the drill-in feed) are never the same *fake.Fake — separate Go
// processes, each with its own New()/NewWithScenario() call. A second,
// entirely distinct Fake instance pointed at the same dir must still see
// the first instance's announced ref.
func TestOneShotTranscriptDirBridgesSeparateInstances(t *testing.T) {
	dir := t.TempDir()
	writer, err := fake.NewWithScenario(fake.Scenario{
		Name:               "writer",
		AnnounceOneShotRef: true,
		Rules: []fake.Rule{
			{Match: "please add auth", Tools: []fake.ToolStep{{Name: "Bash", Input: `{"command":"make test"}`}}, Chunks: []string{"done"}},
		},
	}, fake.WithTranscriptDir(dir))
	if err != nil {
		t.Fatal(err)
	}
	res, err := writer.OneShot(context.Background(), provider.OneShotRequest{Prompt: "please add auth", Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if res.Ref == "" {
		t.Fatal("OneShot never announced a session ref")
	}

	reader := fake.New(fake.WithTranscriptDir(dir))
	if _, err := reader.Transcript(context.Background(), "no-such-ref"); err == nil {
		t.Fatal("reader resolved a ref it was never told about")
	}
	msgs, err := reader.Transcript(context.Background(), res.Ref)
	if err != nil {
		t.Fatalf("reader (a separate Fake instance) could not read writer's ref: %v", err)
	}
	var roles []string
	for _, m := range msgs {
		roles = append(roles, m.Role)
	}
	want := []string{"user", "tool", "tool_result", "agent"}
	if len(roles) != len(want) {
		t.Fatalf("roles = %v, want %v", roles, want)
	}
	for i, r := range want {
		if roles[i] != r {
			t.Errorf("roles[%d] = %q, want %q (roles=%v)", i, roles[i], r, roles)
		}
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
	// A fresh session's ref is only known once the first turn begins (models
	// providers/claudecode's late wire announcement — see fakeSession.Ref).
	if err := first.Send(ctx, "echo hi"); err != nil {
		t.Fatalf("send: %v", err)
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

// A read_only session refuses a file write: the tool never executes (no
// file lands on disk) and the turn ends via a refusal, not a crash.
func TestTrustReadOnlyRefusesWrite(t *testing.T) {
	cwd := t.TempDir()
	f := fake.New()
	sess, err := f.Start(context.Background(), provider.StartOptions{
		Mode: provider.ModeACP, Cwd: cwd, Trust: provider.TrustReadOnly,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sess.Close() }()
	if err := sess.Send(context.Background(), "attempt-write"); err != nil {
		t.Fatal(err)
	}
	events, ended := collect(t, sess)
	if !ended {
		t.Fatal("stream closed without turn end")
	}
	for _, ev := range events {
		if ev.Kind == provider.EventToolUse {
			t.Fatalf("read_only session executed a tool: %+v", ev.Tool)
		}
	}
	if events[0].Kind != provider.EventError {
		t.Fatalf("want a refusal error event first, got %v", events[0].Kind)
	}
	if _, err := os.Stat(filepath.Join(cwd, "trust-probe.txt")); !os.IsNotExist(err) {
		t.Fatalf("read_only session wrote a file, stat err = %v", err)
	}
}

// A limited session refuses command execution but still allows file writes.
func TestTrustLimitedRefusesCommandAllowsWrite(t *testing.T) {
	cwd := t.TempDir()
	f := fake.New()
	sess, err := f.Start(context.Background(), provider.StartOptions{
		Mode: provider.ModeACP, Cwd: cwd, Trust: provider.TrustLimited,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sess.Close() }()

	if err := sess.Send(context.Background(), "attempt-command"); err != nil {
		t.Fatal(err)
	}
	events, ended := collect(t, sess)
	if !ended {
		t.Fatal("stream closed without turn end")
	}
	for _, ev := range events {
		if ev.Kind == provider.EventToolUse {
			t.Fatalf("limited session executed a command tool: %+v", ev.Tool)
		}
	}

	if err := sess.Send(context.Background(), "attempt-write"); err != nil {
		t.Fatal(err)
	}
	events, ended = collect(t, sess)
	if !ended {
		t.Fatal("stream closed without turn end")
	}
	var sawWrite bool
	for _, ev := range events {
		if ev.Kind == provider.EventToolUse {
			sawWrite = true
		}
	}
	if !sawWrite {
		t.Fatal("limited session refused a file write, want it allowed")
	}
	if _, err := os.Stat(filepath.Join(cwd, "trust-probe.txt")); err != nil {
		t.Fatalf("limited session did not write the file: %v", err)
	}
}

// A full-trust session (and the zero-value default) allows everything —
// today's behavior must not regress.
func TestTrustFullAllowsEverything(t *testing.T) {
	for _, trust := range []provider.Trust{provider.TrustFull, ""} {
		cwd := t.TempDir()
		f := fake.New()
		sess, err := f.Start(context.Background(), provider.StartOptions{
			Mode: provider.ModeACP, Cwd: cwd, Trust: trust,
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := sess.Send(context.Background(), "attempt-command"); err != nil {
			t.Fatal(err)
		}
		events, ended := collect(t, sess)
		if !ended {
			t.Fatal("stream closed without turn end")
		}
		var sawTool bool
		for _, ev := range events {
			if ev.Kind == provider.EventToolUse {
				sawTool = true
			}
		}
		if !sawTool {
			t.Fatalf("trust %q refused a tool, want full access", trust)
		}
		_ = sess.Close()
	}
}

// "write-file <path> <content>" really writes the caller's file under the
// session cwd and reports it as a mutating tool use on that path.
func TestWriteFileDialect(t *testing.T) {
	cwd := t.TempDir()
	sess, err := fake.New().Start(context.Background(), provider.StartOptions{Mode: provider.ModeACP, Cwd: cwd})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sess.Close() }()
	if err := sess.Send(context.Background(), "write-file notes/a.txt hello there"); err != nil {
		t.Fatal(err)
	}
	events, ended := collect(t, sess)
	if !ended {
		t.Fatal("stream closed without turn end")
	}
	var tool *provider.ToolUse
	for _, ev := range events {
		if ev.Kind == provider.EventToolUse {
			tool = ev.Tool
		}
	}
	if tool == nil || tool.Path != "notes/a.txt" || !tool.Mutates {
		t.Fatalf("tool event = %+v, want a mutating write of notes/a.txt", tool)
	}
	got, err := os.ReadFile(filepath.Join(cwd, "notes", "a.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hello there\n" {
		t.Fatalf("file = %q, want %q", got, "hello there\n")
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

// "fail once <x>" fails its first send, recording the failure as an IsError
// agent entry (claude-code's API-error shape), and echoes when sent again —
// the retry journey's lever.
func TestFailOnceThenEcho(t *testing.T) {
	f := fake.New()
	sess, err := f.Start(context.Background(), provider.StartOptions{Mode: provider.ModeACP})
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := sess.Send(context.Background(), "fail once hi"); err != nil {
			t.Fatal(err)
		}
		if _, ended := collect(t, sess); !ended {
			t.Fatal("turn never ended")
		}
	}
	msgs, err := f.Transcript(context.Background(), sess.Ref())
	if err != nil {
		t.Fatal(err)
	}
	var shape []string
	for _, m := range msgs {
		shape = append(shape, fmt.Sprintf("%s:%v:%s", m.Role, m.IsError, m.Text))
	}
	want := []string{
		"user:false:fail once hi",
		"agent:true:" + (&provider.RateLimitError{RetryAfter: time.Second, Message: "scripted rate limit"}).Error(),
		"user:false:fail once hi",
		"agent:false:you said hi",
	}
	if strings.Join(shape, "\n") != strings.Join(want, "\n") {
		t.Fatalf("transcript =\n%s\nwant\n%s", strings.Join(shape, "\n"), strings.Join(want, "\n"))
	}
}

// A resumed one-shot continues the ref in place: it recalls the earlier
// turn, reports and announces the resumed ref, appends to its history, and
// ResumeCalls records it (failed resumes too).
func TestOneShotResume(t *testing.T) {
	f, err := fake.NewWithScenario(fake.Scenario{Name: "r", AnnounceOneShotRef: true})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	first, err := f.OneShot(ctx, provider.OneShotRequest{Prompt: "echo mk-1"})
	if err != nil || first.Ref == "" {
		t.Fatalf("seed: %+v, %v", first, err)
	}
	var announced string
	res, err := f.OneShot(ctx, provider.OneShotRequest{
		Prompt: "recall", Resume: first.Ref,
		OnEvent: func(ev provider.Event) {
			if ev.Kind == provider.EventSessionRef {
				announced = ev.Ref
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Text, "mk-1") {
		t.Fatalf("resumed reply %q lost the earlier turn", res.Text)
	}
	if res.Ref != first.Ref || announced != first.Ref {
		t.Fatalf("Ref = %q, announced %q; want the resumed %q", res.Ref, announced, first.Ref)
	}
	msgs, err := f.Transcript(ctx, first.Ref)
	if err != nil || len(msgs) != 4 {
		t.Fatalf("history after resume = %+v, %v; want both turns (4 messages)", msgs, err)
	}

	if _, err := f.OneShot(ctx, provider.OneShotRequest{Prompt: "echo x", Resume: "nope"}); err == nil {
		t.Fatal("resuming an unknown ref must fail, not start fresh")
	}
	if got := f.ResumeCalls(); strings.Join(got, ",") != first.Ref+",nope" {
		t.Fatalf("ResumeCalls = %q, want [%s nope]", got, first.Ref)
	}
}

// The ref can come from an interactive session, and a resume works without
// AnnounceOneShotRef (a resumed one-shot always has its ref); a ref attached
// to a live session can't be resumed underneath it.
func TestOneShotResumeOfSessionRef(t *testing.T) {
	f := fake.New()
	ctx := context.Background()
	sess, err := f.Start(ctx, provider.StartOptions{Mode: provider.ModeACP})
	if err != nil {
		t.Fatal(err)
	}
	if err := sess.Send(ctx, "echo mk-s"); err != nil {
		t.Fatal(err)
	}
	if _, ended := collect(t, sess); !ended {
		t.Fatal("no turn end")
	}
	ref := sess.Ref()
	if _, err := f.OneShot(ctx, provider.OneShotRequest{Prompt: "recall", Resume: ref}); err == nil {
		t.Fatal("resuming a ref attached to a live session must fail")
	}
	_ = sess.Close()
	res, err := f.OneShot(ctx, provider.OneShotRequest{Prompt: "recall", Resume: ref})
	if err != nil {
		t.Fatal(err)
	}
	if res.Ref != ref || !strings.Contains(res.Text, "mk-s") {
		t.Fatalf("got %+v, want ref %q recalling mk-s", res, ref)
	}
}

// Resumed rules match resumed one-shots only, so a scenario can script the
// resumed attempt apart from a fresh one with the same prompt.
func TestScenarioResumedRule(t *testing.T) {
	f, err := fake.NewWithScenario(fake.Scenario{
		Name:               "resumed-rule",
		AnnounceOneShotRef: true,
		Rules: []fake.Rule{
			{Match: "continue", Resumed: true, Reply: "picked up"},
			{Match: "continue", Reply: "fresh start"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	fresh, err := f.OneShot(ctx, provider.OneShotRequest{Prompt: "continue"})
	if err != nil || fresh.Text != "fresh start" {
		t.Fatalf("fresh: %+v, %v", fresh, err)
	}
	resumed, err := f.OneShot(ctx, provider.OneShotRequest{Prompt: "continue", Resume: fresh.Ref})
	if err != nil || resumed.Text != "picked up" {
		t.Fatalf("resumed: %+v, %v", resumed, err)
	}
	// A resumed-only catch-all doesn't shadow the fresh rules after it.
	if _, err := fake.NewWithScenario(fake.Scenario{Name: "c", Rules: []fake.Rule{
		{Resumed: true, Reply: "r"}, {Match: "x", Reply: "y"},
	}}); err != nil {
		t.Fatalf("resumed catch-all before fresh rules rejected: %v", err)
	}
}

// Under WithTranscriptDir a resume works from another Fake instance: the
// pipeline engine picking up after the process that ran the attempt died.
func TestOneShotResumeAcrossInstances(t *testing.T) {
	dir := t.TempDir()
	sc := fake.Scenario{Name: "x", AnnounceOneShotRef: true}
	a, _ := fake.NewWithScenario(sc, fake.WithTranscriptDir(dir))
	first, err := a.OneShot(context.Background(), provider.OneShotRequest{Prompt: "echo mk-disk"})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := fake.NewWithScenario(sc, fake.WithTranscriptDir(dir))
	res, err := b.OneShot(context.Background(), provider.OneShotRequest{Prompt: "recall", Resume: first.Ref})
	if err != nil || !strings.Contains(res.Text, "mk-disk") {
		t.Fatalf("cross-instance resume: %+v, %v", res, err)
	}
}
