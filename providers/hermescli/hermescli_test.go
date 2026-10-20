package hermescli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/drewkidwell/mads/providers/conformance"
	"github.com/drewkidwell/mads/providers/provider"
)

// testProvider wires the binding to the stand-in with its own scratch
// HERMES_HOME (Config.Home), which is both the child's store and the
// TranscriptReader's.
func testProvider(t *testing.T) *Provider {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return New(Config{Bin: exe, Home: t.TempDir(), Env: []string{"MADS_FAKE_HERMES=1"}})
}

func TestRegistersCleanly(t *testing.T) {
	if err := provider.NewRegistry().Register(New(Config{})); err != nil {
		t.Fatal(err)
	}
}

func TestMatricesValidate(t *testing.T) {
	p := New(Config{})
	if err := p.Matrix().Validate(); err != nil {
		t.Fatal(err)
	}
	if err := p.IntentMatrix().Validate(); err != nil {
		t.Fatal(err)
	}
	// Drew's decision: full trust only, declared honestly.
	for _, b := range []provider.Behavior{provider.BehaviorTrustLimited, provider.BehaviorTrustReadOnly, provider.BehaviorTrustAutoReview} {
		if p.Matrix()[b] != provider.Unsupported {
			t.Errorf("%s = %s, want unsupported", b, p.Matrix()[b])
		}
	}
}

func script() conformance.Script {
	return conformance.Script{
		Echo:    func(marker string) string { return "echo " + marker },
		Slow:    func() string { return "slow" },
		Recall:  func() string { return "recall" },
		Timeout: 10 * time.Second, // every probe spawns a real subprocess
	}
}

// Every declared row against the stand-in: ACP echo, mid-turn cancel,
// one-shot (+stream, +resume over guarded session/load), resume across
// processes, synchronous ref, the state.db transcript, model discovery
// from the selector, per-turn usage, and the unsupported rows' refusals.
func TestConformanceAgainstScriptedBinary(t *testing.T) {
	conformance.Run(t, testProvider(t), script())
}

func startSession(t *testing.T, p *Provider, opts provider.StartOptions) provider.Session {
	t.Helper()
	if opts.Mode == "" {
		opts.Mode = provider.ModeACP
	}
	if opts.Cwd == "" {
		opts.Cwd = t.TempDir()
	}
	sess, err := p.Start(context.Background(), opts)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = sess.Close() })
	return sess
}

func drainTurnWithin(t *testing.T, sess provider.Session, timeout time.Duration) []provider.Event {
	t.Helper()
	var events []provider.Event
	deadline := time.After(timeout)
	for {
		select {
		case ev, ok := <-sess.Events():
			if !ok {
				t.Fatalf("events closed before turn end; got %+v", events)
			}
			events = append(events, ev)
			if ev.Kind == provider.EventTurnEnd {
				return events
			}
		case <-deadline:
			t.Fatalf("no turn end within %s; got %+v", timeout, events)
		}
	}
}

func sendTurn(t *testing.T, sess provider.Session, prompt string) []provider.Event {
	t.Helper()
	if err := sess.Send(context.Background(), prompt); err != nil {
		t.Fatalf("Send(%q): %v", prompt, err)
	}
	return drainTurnWithin(t, sess, 15*time.Second)
}

func turnText(events []provider.Event) string {
	var b strings.Builder
	for _, ev := range events {
		if ev.Kind == provider.EventText {
			b.WriteString(ev.Text)
		}
	}
	return b.String()
}

func turnEnd(t *testing.T, events []provider.Event) provider.Event {
	t.Helper()
	ev := events[len(events)-1]
	if ev.Kind != provider.EventTurnEnd {
		t.Fatalf("last event %s, want turn end", ev.Kind)
	}
	return ev
}

// Every level but full is refused before anything spawns — the Bin here
// does not exist, so a spawn would fail with an exec error instead.
func TestNonFullTrustRefused(t *testing.T) {
	p := New(Config{Bin: filepath.Join(t.TempDir(), "no-such-hermes-acp"), Home: t.TempDir()})
	for _, trust := range []provider.Trust{provider.TrustLimited, provider.TrustReadOnly, provider.TrustAutoReview} {
		_, err := p.Start(context.Background(), provider.StartOptions{Mode: provider.ModeACP, Trust: trust})
		if !errors.Is(err, provider.ErrUnsupported) || !strings.Contains(err.Error(), "full trust only") {
			t.Errorf("Start(trust=%s) = %v, want ErrUnsupported saying full trust only", trust, err)
		}
	}
	// The runner hands an auto_review session over as limited
	// (provider.EffectiveTrust): still refused.
	if eff := provider.EffectiveTrust(provider.TrustAutoReview, p.Matrix()); eff != provider.TrustLimited {
		t.Fatalf("EffectiveTrust(auto_review) = %s", eff)
	}
}

func TestPTYUnsupported(t *testing.T) {
	_, err := testProvider(t).Start(context.Background(), provider.StartOptions{Mode: provider.ModePTY})
	if !errors.Is(err, provider.ErrUnsupported) {
		t.Fatalf("Start(pty) = %v, want ErrUnsupported", err)
	}
}

// Full trust sets hermes's dont_ask mode, a requested model rides
// session/set_model, and a system prompt is prefixed to the first send.
func TestSessionSetup(t *testing.T) {
	sess := startSession(t, testProvider(t), provider.StartOptions{Model: "copilot:gpt-4o", SystemPrompt: "You are a careful reviewer."})
	if sess.Ref() == "" {
		t.Fatal("Ref empty at Start-return")
	}
	got := turnText(sendTurn(t, sess, "whoami"))
	for _, want := range []string{"model copilot:gpt-4o", "mode dont_ask", `first "<mads-system-prompt bytes=\"27\">\nYou are a careful reviewer.\n</mads-system-prompt>\n\nwhoami"`} {
		if !strings.Contains(got, want) {
			t.Errorf("whoami = %q, want it to contain %q", got, want)
		}
	}
}

func TestUnknownModelFailsStart(t *testing.T) {
	_, err := testProvider(t).Start(context.Background(), provider.StartOptions{Mode: provider.ModeACP, Cwd: t.TempDir(), Model: "nope:nope"})
	if err == nil || !strings.Contains(err.Error(), "session/set_model") {
		t.Fatalf("Start(model=nope:nope) = %v, want a session/set_model failure", err)
	}
}

// hermes reports usage cumulatively per process; turns must come out per
// turn, and a cancelled turn (totals unchanged) as zero.
func TestUsageIsPerTurn(t *testing.T) {
	sess := startSession(t, testProvider(t), provider.StartOptions{})
	for i, tc := range []struct {
		prompt  string
		in, out int
	}{{"echo a", 1000, 10}, {"echo b", 1000, 10}, {"tool", 2000, 15}, {"echo c", 1000, 10}} {
		end := turnEnd(t, sendTurn(t, sess, tc.prompt))
		if end.Usage == nil || end.Usage.InputTokens != tc.in || end.Usage.OutputTokens != tc.out {
			t.Fatalf("turn %d (%s) usage = %+v, want in %d out %d", i, tc.prompt, end.Usage, tc.in, tc.out)
		}
		if end.Usage.ContextWindow != 128000 {
			t.Errorf("turn %d context window = %d, want the usage_update size", i, end.Usage.ContextWindow)
		}
	}
	if err := sess.Send(context.Background(), "slow"); err != nil {
		t.Fatal(err)
	}
	<-sess.Events()
	if err := sess.Interrupt(context.Background()); err != nil {
		t.Fatal(err)
	}
	end := turnEnd(t, drainTurnWithin(t, sess, 15*time.Second))
	if !end.Interrupted || end.Usage == nil || end.Usage.InputTokens != 0 || end.Usage.OutputTokens != 0 {
		t.Fatalf("cancelled turn end = %+v usage %+v, want interrupted with zero usage", end, end.Usage)
	}
}

// The text after a tool loses hermes's injected break; the tool shows up as
// one call and one result under the live id.
func TestToolTurnNormalized(t *testing.T) {
	sess := startSession(t, testProvider(t), provider.StartOptions{})
	events := sendTurn(t, sess, "tool")
	if got := turnText(events); got != "I found alpha.txt." {
		t.Errorf("text = %q, want the break stripped", got)
	}
	var uses, results []provider.Event
	for _, ev := range events {
		switch ev.Kind {
		case provider.EventToolUse:
			uses = append(uses, ev)
		case provider.EventToolResult:
			results = append(results, ev)
		}
	}
	if len(uses) != 1 || len(results) != 1 || uses[0].Tool.ID != results[0].Tool.ID {
		t.Fatalf("uses %+v results %+v, want one matched pair", uses, results)
	}
	if uses[0].Tool.Kind != provider.ToolKindExecute || uses[0].Tool.Title != "terminal: ls" {
		t.Errorf("tool = %+v, want kind execute titled from the wire", uses[0].Tool)
	}
}

// The edit ask arrives under a synthetic id: answered (allow, full trust),
// and its stray tool_call_update never becomes a result event.
func TestSyntheticApprovalIDDropped(t *testing.T) {
	sess := startSession(t, testProvider(t), provider.StartOptions{})
	events := sendTurn(t, sess, "ask-edit")
	var results []provider.Event
	for _, ev := range events {
		if ev.Kind == provider.EventToolResult {
			results = append(results, ev)
		}
	}
	if len(results) != 1 || results[0].Tool.ID == "edit-approval-1" || results[0].IsError {
		t.Fatalf("results = %+v, want only the live call's, allowed", results)
	}
}

func TestNormalizerBreakRules(t *testing.T) {
	n := normalizer{announced: map[string]bool{}}
	text := func(s string) string {
		ev, _ := n.apply(provider.Event{Kind: provider.EventText, Text: s})
		return ev.Text
	}
	if got := text("\n\nplain"); got != "\n\nplain" {
		t.Errorf("before any tool = %q, want untouched", got)
	}
	n.apply(provider.Event{Kind: provider.EventToolUse, Tool: &provider.ToolUse{ID: "t1"}})
	if got := text("\n"); got != "\n" {
		t.Errorf("blank delta = %q, want untouched (hermes waits past it)", got)
	}
	if got := text("\n\nafter"); got != "after" {
		t.Errorf("first visible delta = %q, want break stripped", got)
	}
	if got := text("\n\nsecond"); got != "\n\nsecond" {
		t.Errorf("later delta = %q, want untouched", got)
	}
	if _, ok := n.apply(provider.Event{Kind: provider.EventToolResult, Tool: &provider.ToolUse{ID: "perm-check-1"}}); ok {
		t.Error("result for an unannounced id passed through")
	}
	// A counter restart (fresh process totals below the last) reports the
	// figure as this turn's own.
	n.prevIn, n.prevOut = 5000, 50
	ev, _ := n.apply(provider.Event{Kind: provider.EventTurnEnd, Usage: &provider.Usage{InputTokens: 300, OutputTokens: 3}})
	if ev.Usage.InputTokens != 300 || ev.Usage.OutputTokens != 3 {
		t.Errorf("after reset usage = %+v", ev.Usage)
	}
}

// hermes answers session/load of an unknown id with an empty success; the
// guard fails it loudly first — and never as ErrUnsupported (resume works).
func TestResumeUnknownRefFails(t *testing.T) {
	p := testProvider(t)
	_, err := p.Start(context.Background(), provider.StartOptions{Mode: provider.ModeACP, Resume: "00000000-0000-4000-8000-000000000000"})
	if err == nil || errors.Is(err, provider.ErrUnsupported) || !strings.Contains(err.Error(), "state.db") {
		t.Fatalf("resume with no store = %v, want a loud not-found", err)
	}
	// With a store present but no such session.
	sess := startSession(t, p, provider.StartOptions{})
	sendTurn(t, sess, "echo seed")
	_, err = p.Start(context.Background(), provider.StartOptions{Mode: provider.ModeACP, Resume: "00000000-0000-4000-8000-000000000000"})
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("resume unknown ref = %v, want not found", err)
	}
	_, err = p.OneShot(context.Background(), provider.OneShotRequest{Prompt: "echo x", Resume: "nope"})
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("one-shot resume unknown ref = %v, want not found", err)
	}
}

// The transcript after an interrupt, and again after a resume rewrote every
// row: the stored post-cancel prompt shows as what the user typed, the
// partial reply as what they saw, and the load's row rewrite changes
// nothing.
func TestTranscriptSurvivesInterruptAndReload(t *testing.T) {
	p := testProvider(t)
	cwd := t.TempDir()
	sess := startSession(t, p, provider.StartOptions{Cwd: cwd})
	sendTurn(t, sess, "  echo hello  ")
	if err := sess.Send(context.Background(), "slow"); err != nil {
		t.Fatal(err)
	}
	<-sess.Events()
	_ = sess.Interrupt(context.Background())
	drainTurnWithin(t, sess, 15*time.Second)
	sendTurn(t, sess, "echo after")
	sendTurn(t, sess, "tool")
	ref := sess.Ref()
	_ = sess.Close()

	check := func(when string) {
		t.Helper()
		msgs, err := p.Transcript(context.Background(), ref)
		if err != nil {
			t.Fatalf("%s: Transcript: %v", when, err)
		}
		var got []string
		for _, m := range msgs {
			s := m.Role + ":" + m.Text
			if m.Tool != nil {
				s += "[" + m.Tool.Name + "]"
			}
			got = append(got, s)
		}
		want := []string{
			"user:echo hello", "agent:you said hello",
			"user:slow", "agent:" + msgs[3].Text,
			"user:echo after", "agent:you said after",
			"user:tool", "tool:[terminal]", `tool_result:{"output": "alpha.txt", "exit_code": 0, "error": null}[terminal]`, "agent:I found alpha.txt.",
		}
		if strings.Join(got, "\n") != strings.Join(want, "\n") {
			t.Fatalf("%s: transcript\n%s\nwant\n%s", when, strings.Join(got, "\n"), strings.Join(want, "\n"))
		}
		if !strings.HasPrefix(msgs[3].Text, "tick 0") {
			t.Errorf("%s: partial reply = %q", when, msgs[3].Text)
		}
		if msgs[7].Tool.ID != msgs[8].Tool.ID || !strings.HasPrefix(msgs[7].Tool.ID, "call_") {
			t.Errorf("%s: tool ids %q / %q, want the stored call_ pair", when, msgs[7].Tool.ID, msgs[8].Tool.ID)
		}
	}
	check("before resume")

	resumed := startSession(t, p, provider.StartOptions{Cwd: cwd, Resume: ref})
	if got := turnText(sendTurn(t, resumed, "recall")); !strings.Contains(got, "echo hello") {
		t.Fatalf("recall after resume = %q", got)
	}
	_ = resumed.Close()
	msgs, _ := p.Transcript(context.Background(), ref)
	if n := len(msgs); n != 12 || msgs[10].Text != "recall" {
		t.Fatalf("after resume: %d messages (%+v), want the 10 plus the recall exchange", n, msgs)
	}
}

func TestOneShot(t *testing.T) {
	p := testProvider(t)
	var streamed []string
	res, err := p.OneShot(context.Background(), provider.OneShotRequest{Prompt: "echo one", Cwd: t.TempDir(), OnEvent: func(ev provider.Event) {
		if ev.Kind == provider.EventText {
			streamed = append(streamed, ev.Text)
		}
	}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "you said one" || res.Ref == "" || res.Usage.OutputTokens != 10 || len(streamed) == 0 {
		t.Fatalf("OneShot = %+v streamed %q", res, streamed)
	}
	again, err := p.OneShot(context.Background(), provider.OneShotRequest{Prompt: "recall", Resume: res.Ref})
	if err != nil || !strings.Contains(again.Text, "echo one") || again.Ref != res.Ref {
		t.Fatalf("resumed OneShot = %+v, %v", again, err)
	}
}

func TestOneShotRateLimit(t *testing.T) {
	_, err := testProvider(t).OneShot(context.Background(), provider.OneShotRequest{Prompt: "ratelimit"})
	var rle *provider.RateLimitError
	if !errors.As(err, &rle) {
		t.Fatalf("OneShot(ratelimit) = %v, want *RateLimitError", err)
	}
}

func TestModels(t *testing.T) {
	models, err := testProvider(t).Models(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 3 || models[0].ID != "copilot:gpt-4.1" || !models[0].Default || models[1].Default {
		t.Fatalf("Models = %+v, want the selector with copilot:gpt-4.1 current", models)
	}
}

func TestModelsUnavailable(t *testing.T) {
	_, err := New(Config{Bin: filepath.Join(t.TempDir(), "missing")}).Models(context.Background())
	if !errors.Is(err, provider.ErrDiscoveryUnavailable) {
		t.Fatalf("Models with a missing binary = %v, want ErrDiscoveryUnavailable", err)
	}
}

func TestHomeResolution(t *testing.T) {
	uh, err := os.UserHomeDir()
	if err != nil {
		t.Skip(err)
	}
	t.Setenv("HERMES_HOME", "")
	t.Setenv("HERMES_DATA_DIR_SUFFIX", "")
	if got := New(Config{}).home(); got != filepath.Join(uh, ".hermes") {
		t.Errorf("default home = %q", got)
	}
	if got := New(Config{Env: []string{"HERMES_HOME=/srv/h"}}).home(); got != "/srv/h" {
		t.Errorf("Env home = %q", got)
	}
	if got := New(Config{Env: []string{"HERMES_HOME=/srv/h"}, Home: "/pinned"}).home(); got != "/pinned" {
		t.Errorf("Config.Home should win over Env, got %q", got)
	}
	t.Setenv("HERMES_HOME", "~/alt")
	if got := New(Config{}).home(); got != filepath.Join(uh, "alt") {
		t.Errorf("inherited ~ home = %q", got)
	}
	t.Setenv("HERMES_HOME", "")
	t.Setenv("HERMES_DATA_DIR_SUFFIX", "-dev")
	if got := New(Config{}).home(); got != filepath.Join(uh, ".hermes-dev") {
		t.Errorf("suffixed home = %q", got)
	}
}

func TestRateLimitHeuristic(t *testing.T) {
	for msg, want := range map[string]bool{
		"HTTP 429: rate limit exceeded": true,
		"Too Many Requests":             true,
		"auth required":                 false,
	} {
		if got := rateLimit(msg) != nil; got != want {
			t.Errorf("rateLimit(%q) = %v, want %v", msg, got, want)
		}
	}
}
