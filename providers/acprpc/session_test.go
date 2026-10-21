package acprpc

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/drewkidwell/mads/providers/provider"
)

// gooseConfig is the goose binding's knob set as these tests exercise it —
// the same facts the real descriptor will carry (meta-namespace identity,
// set_mode transport in the fake profile, extension system prompt).
func gooseConfig() Config {
	return Config{
		TrustModes: map[provider.Trust]string{
			provider.TrustFull:     "auto",
			provider.TrustLimited:  "approve",
			provider.TrustReadOnly: "approve",
		},
		SendModeAtSessionNew: true,
		ModelConfigOptionID:  "model",
		SystemPrompt: SystemPromptStrategy{
			Mode:   SystemPromptExtensionMethod,
			Method: "_goose/unstable/session/system-prompt/set",
			Params: func(sessionID, text string) any {
				return map[string]string{"sessionId": sessionID, "mode": "append", "key": "mads-agent", "text": text}
			},
		},
		ToolIdentity: ToolIdentity{Mode: ToolIdentityMetaNamespace, Namespace: "goose"},
		ToolClassTable: []ToolClassRule{
			{Extension: "developer", Name: "shell", Class: "execute"},
			{Extension: "developer", Name: "text_editor", ByCommand: map[string]string{"view": "read", "write": "edit", "str_replace": "edit", "insert": "edit"}},
			{Extension: "todo", Name: "todo__todo_write", Class: "think"},
		},
		PermissionAsksEveryCall: true,
		Keys:                    KeyStrategy{Mode: KeyModeMessageRun, CreatedNamespace: "goose"},
		Settle:                  SettlePoints{ToolDone: true, PromptResponse: true},
	}
}

// syntheticConfig targets the synthetic conforming profile: kind-based
// identity, different mode ids applied through a category-"mode"
// configOption, no loadSession/close capabilities, cancelled stop-reason.
func syntheticConfig() Config {
	return Config{
		TrustModes: map[provider.Trust]string{
			provider.TrustFull:     "vendor-full",
			provider.TrustLimited:  "vendor-safe",
			provider.TrustReadOnly: "vendor-safe",
		},
		SendModeAtSessionNew: true,
		ModelConfigOptionID:  "model",
		ToolIdentity:         ToolIdentity{Mode: ToolIdentityKindBased},
	}
}

func startSession(t *testing.T, e *Engine, opts provider.StartOptions) provider.Session {
	t.Helper()
	if opts.Mode == "" {
		opts.Mode = provider.ModeACP
	}
	sess, err := e.Start(context.Background(), opts)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = sess.Close() })
	return sess
}

// drainTurn collects events until (and including) EventTurnEnd. It fails
// the test if the channel closes or 15s pass first.
func drainTurn(t *testing.T, sess provider.Session) []provider.Event {
	t.Helper()
	var events []provider.Event
	deadline := time.After(15 * time.Second)
	for {
		select {
		case ev, ok := <-sess.Events():
			if !ok {
				t.Fatalf("events channel closed before turn end; got %+v", events)
			}
			events = append(events, ev)
			if ev.Kind == provider.EventTurnEnd {
				return events
			}
		case <-deadline:
			t.Fatalf("timed out waiting for turn end; got %+v", events)
		}
	}
}

func sendTurn(t *testing.T, sess provider.Session, prompt string) []provider.Event {
	t.Helper()
	if err := sess.Send(context.Background(), prompt); err != nil {
		t.Fatalf("Send(%q): %v", prompt, err)
	}
	return drainTurn(t, sess)
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
	last := events[len(events)-1]
	if last.Kind != provider.EventTurnEnd {
		t.Fatalf("last event is %s, not turn_end", last.Kind)
	}
	return last
}

func errorTexts(events []provider.Event) []string {
	var out []string
	for _, ev := range events {
		if ev.Kind == provider.EventError {
			out = append(out, ev.Text)
		}
	}
	return out
}

// TestStartRelativeCwdIsAbsolutized — goose 1.51 (and ACP generally) rejects
// a relative cwd in session/new with -32602 "cwd must be an absolute path";
// claude-code tolerates one. The goose fake profile mirrors goose's rejection
// (requireAbsCwd), so this pins the engine's absolutization: a caller passing
// a relative cwd — the runner does, with "-cwd ." — must still start cleanly
// on goose.
// TestStartRelativeCwdIsAbsolutized — goose 1.51 (and ACP generally) rejects
// a relative cwd in session/new with -32602 "cwd must be an absolute path";
// claude-code tolerates one. The goose fake profile mirrors goose's rejection
// (requireAbsCwd), so this pins the engine's absolutization: the runner sends
// exactly Cwd="." (its "-cwd ." default), which must still start cleanly on
// goose — before the fix, goose rejected it and the session child exited.
func TestStartRelativeCwdIsAbsolutized(t *testing.T) {
	e := testEngine(t, gooseConfig(), "goose", t.TempDir())
	sess, err := e.Start(context.Background(), provider.StartOptions{Mode: provider.ModeACP, Cwd: "."})
	if err != nil {
		t.Fatalf("Cwd=%q: Start: %v (want success — the engine must send an absolute cwd, goose rejects relative)", ".", err)
	}
	_ = sess.Close()
}

func TestEchoTurnStreamsTextAndUsage(t *testing.T) {
	e := testEngine(t, gooseConfig(), "goose", t.TempDir())
	sess := startSession(t, e, provider.StartOptions{})
	events := sendTurn(t, sess, "echo hi")
	if got := turnText(events); got != "you said hi" {
		t.Fatalf("text = %q", got)
	}
	end := turnEnd(t, events)
	if end.Interrupted {
		t.Fatal("clean turn marked interrupted")
	}
	u := end.Usage
	if u == nil || u.InputTokens != 20 || u.OutputTokens != 10 {
		t.Fatalf("usage = %+v", u)
	}
	if u.ContextTokens != 30 || u.ContextWindow != 128000 {
		t.Fatalf("usage_update snapshot missing from turn end: %+v", u)
	}
}

func TestThoughtChunksMapToEventThinking(t *testing.T) {
	e := testEngine(t, gooseConfig(), "goose", t.TempDir())
	sess := startSession(t, e, provider.StartOptions{})
	events := sendTurn(t, sess, "echo hi")
	for _, ev := range events {
		if ev.Kind == provider.EventThinking && ev.Text == "thinking..." {
			return
		}
	}
	t.Fatalf("no EventThinking in %+v", events)
}

func TestUnknownVariantAndStopReasonTolerated(t *testing.T) {
	e := testEngine(t, gooseConfig(), "goose", t.TempDir())
	sess := startSession(t, e, provider.StartOptions{})
	events := sendTurn(t, sess, "weird")
	if got := turnText(events); got != "survived the plan" {
		t.Fatalf("text = %q", got)
	}
	end := turnEnd(t, events)
	if end.Interrupted {
		t.Fatal("unknown stopReason must be a plain turn end, not an interruption")
	}
	if errs := errorTexts(events); len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
}

func TestRefusalSurfacesErrorThenTurnEnd(t *testing.T) {
	e := testEngine(t, gooseConfig(), "goose", t.TempDir())
	sess := startSession(t, e, provider.StartOptions{})
	events := sendTurn(t, sess, "refuse")
	errs := errorTexts(events)
	if len(errs) != 1 || !strings.Contains(errs[0], "refused") {
		t.Fatalf("errors = %v", errs)
	}
	if turnEnd(t, events).Interrupted {
		t.Fatal("refusal is not an interruption")
	}
}

func TestCancelledStopReasonMarksInterrupted(t *testing.T) {
	e := testEngine(t, gooseConfig(), "goose", t.TempDir())
	sess := startSession(t, e, provider.StartOptions{})
	events := sendTurn(t, sess, "cancelled-stop")
	if !turnEnd(t, events).Interrupted {
		t.Fatal("stopReason cancelled must mark the turn interrupted")
	}
}

func TestErrorSurfaces(t *testing.T) {
	cases := []struct {
		prompt string
		want   string
	}{
		{"auth", "agent authentication required"},
		{"credits", "top up at https://example.com/topup"},
		{"busy", "session already has active run"},
	}
	for _, tc := range cases {
		t.Run(tc.prompt, func(t *testing.T) {
			e := testEngine(t, gooseConfig(), "goose", t.TempDir())
			sess := startSession(t, e, provider.StartOptions{})
			events := sendTurn(t, sess, tc.prompt)
			errs := errorTexts(events)
			if len(errs) != 1 || !strings.Contains(errs[0], tc.want) {
				t.Fatalf("errors = %v, want one containing %q", errs, tc.want)
			}
			turnEnd(t, events)
		})
	}
}

// TestInterruptMarksTurnEnd is the interrupted-flag-over-stopReason proof,
// run against both cancel resolutions seen in the wild: goose 1.48 resolved
// end_turn (the flag is the only signal); goose 1.50 and the synthetic
// profile (claude/hermes behavior) resolve cancelled.
func TestInterruptMarksTurnEnd(t *testing.T) {
	for _, tc := range []struct {
		profile string
		cfg     Config
	}{
		{"goose", gooseConfig()},
		{"goose-1.48", gooseConfig()},
		{"synthetic", syntheticConfig()},
	} {
		t.Run(tc.profile, func(t *testing.T) {
			e := testEngine(t, tc.cfg, tc.profile, t.TempDir())
			sess := startSession(t, e, provider.StartOptions{})
			if err := sess.Send(context.Background(), "slow"); err != nil {
				t.Fatal(err)
			}
			// Let a few ticks stream so the cancel lands mid-turn.
			<-sess.Events()
			if err := sess.Interrupt(context.Background()); err != nil {
				t.Fatal(err)
			}
			events := drainTurn(t, sess)
			if !turnEnd(t, events).Interrupted {
				t.Fatalf("turn end not marked interrupted (profile %s)", tc.profile)
			}
		})
	}
}

func TestSecondSendWhileTurnOpenFailsImmediately(t *testing.T) {
	e := testEngine(t, gooseConfig(), "goose", t.TempDir())
	sess := startSession(t, e, provider.StartOptions{})
	if err := sess.Send(context.Background(), "slow"); err != nil {
		t.Fatal(err)
	}
	if err := sess.Send(context.Background(), "echo nope"); err == nil {
		t.Fatal("second Send during an open turn must fail")
	} else if !strings.Contains(err.Error(), "prompt in flight") {
		t.Fatalf("unexpected error: %v", err)
	} else if !errors.Is(err, provider.ErrTurnInFlight) {
		// The Runner queues on this sentinel instead of failing the send.
		t.Fatalf("refusal %v is not provider.ErrTurnInFlight", err)
	}
	if err := sess.Interrupt(context.Background()); err != nil {
		t.Fatal(err)
	}
	drainTurn(t, sess)
}

// compactPhases lists a turn's EventCompact phases ("done" carries its
// verdict as "done:ok" / "done:failed").
func compactPhases(events []provider.Event) []string {
	var out []string
	for _, ev := range events {
		if ev.Kind != provider.EventCompact {
			continue
		}
		p := ev.Compact.Phase
		if p == "done" {
			if ev.Compact.Success {
				p += ":ok"
			} else {
				p += ":failed"
			}
		}
		out = append(out, p)
	}
	return out
}

// TestCompactPromptsTheAgentsCommand: Compact sends CompactCommand as a
// prompt and reports the turn as a start/done EventCompact pair ahead of its
// turn end, keeping the agent's ack out of the text stream; the next Send
// is an ordinary turn again.
func TestCompactPromptsTheAgentsCommand(t *testing.T) {
	cfg := gooseConfig()
	cfg.CompactCommand = "/compact"
	e := testEngine(t, cfg, "goose", t.TempDir())
	sess := startSession(t, e, provider.StartOptions{})
	comp, ok := sess.(provider.Compactor)
	if !ok {
		t.Fatal("session is not a provider.Compactor")
	}
	if err := comp.Compact(context.Background(), ""); err != nil {
		t.Fatalf("Compact: %v", err)
	}
	events := drainTurn(t, sess)
	if got := compactPhases(events); len(got) != 2 || got[0] != "start" || got[1] != "done:ok" {
		t.Fatalf("compact phases = %v, want [start done:ok]", got)
	}
	if txt := turnText(events); txt != "" {
		t.Fatalf("compaction ack leaked into the text stream: %q", txt)
	}
	turnEnd(t, events)
	tr := sess.(interface{ Transcript() []provider.Message }).Transcript()
	if len(tr) != 1 || tr[0].Role != "compact" || tr[0].Compact == nil {
		t.Fatalf("transcript = %+v, want one compact marker (no /compact prompt, no ack)", tr)
	}

	events = sendTurn(t, sess, "echo after")
	if got := compactPhases(events); len(got) != 0 {
		t.Fatalf("an ordinary turn after compaction reported %v", got)
	}
	if got := turnText(events); got != "you said after" {
		t.Fatalf("text after compaction = %q", got)
	}
}

// TestCompactRefusals: no CompactCommand means no compaction at all, and
// instructions are refused unless CompactFocus — never sent and dropped.
func TestCompactRefusals(t *testing.T) {
	e := testEngine(t, gooseConfig(), "goose", t.TempDir())
	sess := startSession(t, e, provider.StartOptions{})
	if err := sess.(provider.Compactor).Compact(context.Background(), ""); !errors.Is(err, provider.ErrUnsupported) {
		t.Fatalf("Compact without CompactCommand = %v, want ErrUnsupported", err)
	}

	cfg := gooseConfig()
	cfg.CompactCommand = "/compact"
	e = testEngine(t, cfg, "goose", t.TempDir())
	sess = startSession(t, e, provider.StartOptions{})
	if err := sess.(provider.Compactor).Compact(context.Background(), "keep the plan"); !errors.Is(err, provider.ErrUnsupported) {
		t.Fatalf("Compact with instructions, no CompactFocus = %v, want ErrUnsupported", err)
	}

	cfg.CompactFocus = true
	e = testEngine(t, cfg, "goose", t.TempDir())
	sess = startSession(t, e, provider.StartOptions{})
	if err := sess.(provider.Compactor).Compact(context.Background(), "keep the plan"); err != nil {
		t.Fatalf("Compact with instructions under CompactFocus: %v", err)
	}
	drainTurn(t, sess)
	// The prompt isn't in the transcript any more; ask the agent what it saw.
	if got := turnText(sendTurn(t, sess, "recall")); got != "earlier you said: /compact keep the plan" {
		t.Fatalf("agent saw %q, want \"/compact keep the plan\" sent", got)
	}
}

func TestCrashClosesEventsWithoutTurnEnd(t *testing.T) {
	e := testEngine(t, gooseConfig(), "goose", t.TempDir())
	sess := startSession(t, e, provider.StartOptions{})
	if err := sess.Send(context.Background(), "crash"); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(15 * time.Second)
	for {
		select {
		case ev, ok := <-sess.Events():
			if !ok {
				return // closed without a turn end: the process-died signal
			}
			if ev.Kind == provider.EventTurnEnd {
				t.Fatal("crashed turn must not produce a turn end")
			}
		case <-deadline:
			t.Fatal("events channel never closed after crash")
		}
	}
}

// TestPermissionSelectionByKind proves replies are selected from the
// offered options by kind against optionIds that never mirror the kind
// names — the fake executes the tool only when the reply names one of its
// own allow ids, so a hardcoded-id engine fails the allow cases, and the
// denial cases observe the exact id (or cancelled outcome) agent-side.
func TestPermissionSelectionByKind(t *testing.T) {
	cases := []struct {
		name    string
		profile string
		cfg     Config
		trust   provider.Trust
		prompt  string
		want    string
	}{
		// goose profile: meta-namespace identity + ByCommand class split.
		{"allow read under read_only", "goose", gooseConfig(), provider.TrustReadOnly, "attempt-view", "used text_editor view"},
		{"allow edit under limited", "goose", gooseConfig(), provider.TrustLimited, "attempt-write", "used text_editor write"},
		{"deny execute under limited", "goose", gooseConfig(), provider.TrustLimited, "attempt-shell", "denied via opt-r"},
		{"deny edit under read_only", "goose", gooseConfig(), provider.TrustReadOnly, "attempt-write", "denied via opt-r"},
		{"deny unknown tool under limited", "goose", gooseConfig(), provider.TrustLimited, "attempt-unknown", "denied via opt-r"},
		// synthetic profile: kind-based identity, different adversarial ids.
		{"kind-based allow read", "synthetic", syntheticConfig(), provider.TrustReadOnly, "attempt-view", "used text_editor view"},
		{"kind-based deny execute", "synthetic", syntheticConfig(), provider.TrustLimited, "attempt-shell", "denied via syn-no"},
		// The fallback ladder: a two-option menu (no _always pair, hermes
		// style) and an _always-only menu, both polarities.
		{"two-option allow", "synthetic", syntheticConfig(), provider.TrustReadOnly, "attempt-view-two", "used text_editor view"},
		{"always-only allow falls back", "synthetic", syntheticConfig(), provider.TrustReadOnly, "attempt-view-always", "used text_editor view (always)"},
		{"always-only reject falls back", "synthetic", syntheticConfig(), provider.TrustLimited, "attempt-shell-always", "denied via syn-no-4ever"},
		// No option of the needed polarity → outcome cancelled, observed
		// agent-side.
		{"no reject polarity cancels", "synthetic", syntheticConfig(), provider.TrustLimited, "attempt-shell-allowonly", "denied via cancelled"},
		// Ask-before-announce (kiro-cli's shape): no tool_call update
		// precedes the request, so the gate classifies the identity embedded
		// in the request's own toolCall — the spec kind field or the _meta
		// namespace, per the same ToolIdentity knob.
		{"ask-first allow edit under limited (embedded kind)", "synthetic", syntheticConfig(), provider.TrustLimited, "attempt-write-askfirst", "used text_editor write"},
		{"ask-first deny execute under limited (embedded kind)", "synthetic", syntheticConfig(), provider.TrustLimited, "attempt-shell-askfirst", "denied via syn-no"},
		{"ask-first allow edit under limited (embedded meta)", "goose", gooseConfig(), provider.TrustLimited, "attempt-write-askfirst", "used text_editor write"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := testEngine(t, tc.cfg, tc.profile, t.TempDir())
			sess := startSession(t, e, provider.StartOptions{Trust: tc.trust})
			events := sendTurn(t, sess, tc.prompt)
			if got := turnText(events); got != tc.want {
				t.Fatalf("reply = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestTitleOnlyIdentityDeniesAllBelowFull(t *testing.T) {
	cfg := syntheticConfig()
	cfg.ToolIdentity = ToolIdentity{} // zero value: TitleOnly
	e := testEngine(t, cfg, "synthetic", t.TempDir())
	sess := startSession(t, e, provider.StartOptions{Trust: provider.TrustLimited})
	events := sendTurn(t, sess, "attempt-view")
	if got := turnText(events); got != "denied via syn-no" {
		t.Fatalf("reply = %q — TitleOnly must deny even a read below full trust", got)
	}
}

func TestFullTrustAllowsWithoutGating(t *testing.T) {
	e := testEngine(t, gooseConfig(), "goose", t.TempDir())
	sess := startSession(t, e, provider.StartOptions{Trust: provider.TrustFull})
	// Mode "auto": the fake never asks permission; the tool events flow.
	events := sendTurn(t, sess, "attempt-shell")
	if got := turnText(events); got != "used shell" {
		t.Fatalf("reply = %q", got)
	}
	var sawUse, sawResult bool
	for _, ev := range events {
		switch ev.Kind {
		case provider.EventToolUse:
			sawUse = true
			if ev.Tool == nil || ev.Tool.Name != "shell" || ev.Tool.Kind != provider.ToolKindExecute {
				t.Fatalf("tool_use = %+v", ev.Tool)
			}
		case provider.EventToolResult:
			sawResult = true
		}
	}
	if !sawUse || !sawResult {
		t.Fatalf("missing tool events (use=%v result=%v)", sawUse, sawResult)
	}
}

func TestNonTerminalToolUpdateSkipped(t *testing.T) {
	e := testEngine(t, gooseConfig(), "goose", t.TempDir())
	sess := startSession(t, e, provider.StartOptions{})
	events := sendTurn(t, sess, "fail-tool")
	var results []provider.Event
	for _, ev := range events {
		if ev.Kind == provider.EventToolResult {
			results = append(results, ev)
		}
	}
	// The in_progress variant is skipped; only the terminal failed lands.
	if len(results) != 1 || !results[0].IsError || results[0].Text != "boom: exit 1" {
		t.Fatalf("tool results = %+v", results)
	}
}

func TestModeMappingAndTransport(t *testing.T) {
	t.Run("goose set_mode", func(t *testing.T) {
		e := testEngine(t, gooseConfig(), "goose", t.TempDir())
		sess := startSession(t, e, provider.StartOptions{Trust: provider.TrustFull})
		if got := turnText(sendTurn(t, sess, "mode")); got != "mode: auto" {
			t.Fatalf("reply = %q", got)
		}
	})
	t.Run("synthetic config-option transport", func(t *testing.T) {
		// The synthetic profile rejects session/set_mode outright — reaching
		// the mapped mode proves the runtime transport detection picked the
		// category-"mode" configOption.
		e := testEngine(t, syntheticConfig(), "synthetic", t.TempDir())
		sess := startSession(t, e, provider.StartOptions{Trust: provider.TrustLimited})
		if got := turnText(sendTurn(t, sess, "mode")); got != "mode: vendor-safe" {
			t.Fatalf("reply = %q", got)
		}
	})
}

func TestModeNotOfferedFailsStart(t *testing.T) {
	cfg := gooseConfig()
	cfg.TrustModes[provider.TrustFull] = "bogus-mode"
	e := testEngine(t, cfg, "goose", t.TempDir())
	_, err := e.Start(context.Background(), provider.StartOptions{Mode: provider.ModeACP})
	if err == nil || !strings.Contains(err.Error(), "not among the agent's available modes") {
		t.Fatalf("err = %v", err)
	}
}

func TestMissingTrustModeMapping(t *testing.T) {
	t.Run("limited fails loudly", func(t *testing.T) {
		cfg := gooseConfig()
		delete(cfg.TrustModes, provider.TrustLimited)
		e := testEngine(t, cfg, "goose", t.TempDir())
		_, err := e.Start(context.Background(), provider.StartOptions{Mode: provider.ModeACP, Trust: provider.TrustLimited})
		if err == nil || !strings.Contains(err.Error(), "no vendor mode mapped") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("full skips the call", func(t *testing.T) {
		cfg := gooseConfig()
		delete(cfg.TrustModes, provider.TrustFull)
		e := testEngine(t, cfg, "goose", t.TempDir())
		sess := startSession(t, e, provider.StartOptions{Trust: provider.TrustFull})
		// The agent keeps its default mode; the session works.
		if got := turnText(sendTurn(t, sess, "echo ok")); got != "you said ok" {
			t.Fatalf("reply = %q", got)
		}
	})
}

func TestModelSelection(t *testing.T) {
	t.Run("set via config option", func(t *testing.T) {
		e := testEngine(t, gooseConfig(), "goose", t.TempDir())
		sess := startSession(t, e, provider.StartOptions{Model: "m-test"})
		if got := turnText(sendTurn(t, sess, "model")); got != "model: m-test" {
			t.Fatalf("reply = %q", got)
		}
	})
	t.Run("bad model fails Start", func(t *testing.T) {
		e := testEngine(t, gooseConfig(), "goose", t.TempDir())
		if _, err := e.Start(context.Background(), provider.StartOptions{Mode: provider.ModeACP, Model: "bad-model"}); err == nil {
			t.Fatal("bad model must fail Start")
		}
	})
	t.Run("empty knob means unsupported", func(t *testing.T) {
		cfg := gooseConfig()
		cfg.ModelConfigOptionID = ""
		e := testEngine(t, cfg, "goose", t.TempDir())
		_, err := e.Start(context.Background(), provider.StartOptions{Mode: provider.ModeACP, Model: "m-test"})
		if !errors.Is(err, provider.ErrUnsupported) {
			t.Fatalf("err = %v, want ErrUnsupported", err)
		}
	})
}

func TestPreseedConfigOptions(t *testing.T) {
	cfg := gooseConfig()
	cfg.PreseedConfigOptions = []ConfigOptionValue{{ID: "model", Value: "preseeded"}}
	e := testEngine(t, cfg, "goose", t.TempDir())
	sess := startSession(t, e, provider.StartOptions{})
	if got := turnText(sendTurn(t, sess, "model")); got != "model: preseeded" {
		t.Fatalf("reply = %q", got)
	}
}

func TestSystemPromptStrategies(t *testing.T) {
	t.Run("extension method delivers", func(t *testing.T) {
		e := testEngine(t, gooseConfig(), "goose", t.TempDir())
		sess := startSession(t, e, provider.StartOptions{SystemPrompt: "be terse"})
		if got := turnText(sendTurn(t, sess, "sysprompt")); got != "sysprompt: be terse" {
			t.Fatalf("reply = %q", got)
		}
	})
	t.Run("method_not_found falls back to first-send prefix", func(t *testing.T) {
		e := testEngine(t, gooseConfig(), "goose", t.TempDir(), "MADS_FAKE_ACP_NO_SYSPROMPT=1")
		sess := startSession(t, e, provider.StartOptions{SystemPrompt: "be terse"})
		if got := turnText(sendTurn(t, sess, "reflect this")); got != "be terse\n\nreflect this" {
			t.Fatalf("first send = %q", got)
		}
		// The prefix rides the first Send only.
		if got := turnText(sendTurn(t, sess, "reflect again")); got != "reflect again" {
			t.Fatalf("second send = %q", got)
		}
	})
	t.Run("zero value is first-send prefix", func(t *testing.T) {
		cfg := gooseConfig()
		cfg.SystemPrompt = SystemPromptStrategy{}
		e := testEngine(t, cfg, "goose", t.TempDir())
		sess := startSession(t, e, provider.StartOptions{SystemPrompt: "persona"})
		if got := turnText(sendTurn(t, sess, "reflect x")); got != "persona\n\nreflect x" {
			t.Fatalf("reply = %q", got)
		}
	})
	t.Run("none drops by declared decision", func(t *testing.T) {
		cfg := gooseConfig()
		cfg.SystemPrompt = SystemPromptStrategy{Mode: SystemPromptNone}
		e := testEngine(t, cfg, "goose", t.TempDir())
		sess := startSession(t, e, provider.StartOptions{SystemPrompt: "persona"})
		if got := turnText(sendTurn(t, sess, "reflect x")); got != "reflect x" {
			t.Fatalf("reply = %q", got)
		}
	})
}

func TestResumeWithoutLoadSessionFailsLoudly(t *testing.T) {
	e := testEngine(t, syntheticConfig(), "synthetic", t.TempDir())
	_, err := e.Start(context.Background(), provider.StartOptions{Mode: provider.ModeACP, Resume: "sess_1"})
	if !errors.Is(err, provider.ErrUnsupported) {
		t.Fatalf("err = %v, want ErrUnsupported", err)
	}
	if err == nil || !strings.Contains(err.Error(), "loadSession") {
		t.Fatalf("err = %v, want a loadSession mention", err)
	}
}

func TestResumeUnknownRefFailsLoudly(t *testing.T) {
	e := testEngine(t, gooseConfig(), "goose", t.TempDir())
	_, err := e.Start(context.Background(), provider.StartOptions{Mode: provider.ModeACP, Resume: "sess_never_existed"})
	if err == nil || !strings.Contains(err.Error(), "Session not found") {
		t.Fatalf("err = %v", err)
	}
}

func TestResumeReplaysAndRecalls(t *testing.T) {
	dir := t.TempDir()
	e := testEngine(t, gooseConfig(), "goose", dir)
	sess := startSession(t, e, provider.StartOptions{})
	sendTurn(t, sess, "echo alpha")
	ref := sess.Ref()
	if ref == "" {
		t.Fatal("no ref after session/new")
	}
	if err := sess.Close(); err != nil {
		t.Fatal(err)
	}

	resumed := startSession(t, e, provider.StartOptions{Resume: ref})
	if resumed.Ref() != ref {
		t.Fatalf("resumed ref = %q, want %q", resumed.Ref(), ref)
	}
	type replayer interface{ ReplayMessages() []provider.Message }
	msgs := resumed.(replayer).ReplayMessages()
	var roles []string
	for _, m := range msgs {
		roles = append(roles, m.Role+":"+m.Text)
	}
	want := []string{"user:echo alpha", "agent:you said alpha"}
	if strings.Join(roles, "|") != strings.Join(want, "|") {
		t.Fatalf("replay = %v, want %v", roles, want)
	}
	if got := turnText(sendTurn(t, resumed, "recall")); got != "earlier you said: alpha" {
		t.Fatalf("recall = %q", got)
	}
}

func TestMCPCapabilityGating(t *testing.T) {
	servers := []provider.MCPServerRef{
		{Name: "stdio-one", Command: "server --flag"},
		{Name: "http-one", Type: "http", URL: "http://127.0.0.1:9/mcp"},
		{Name: "sse-one", Type: "sse", URL: "http://127.0.0.1:9/sse"},
	}
	t.Run("http advertised rides, sse dropped", func(t *testing.T) {
		e := testEngine(t, gooseConfig(), "goose", t.TempDir())
		sess := startSession(t, e, provider.StartOptions{MCPServers: servers})
		if got := turnText(sendTurn(t, sess, "mcp")); got != "mcp: stdio-one,http-one:http" {
			t.Fatalf("reply = %q", got)
		}
	})
	t.Run("http unadvertised dropped", func(t *testing.T) {
		e := testEngine(t, syntheticConfig(), "synthetic", t.TempDir())
		sess := startSession(t, e, provider.StartOptions{MCPServers: servers})
		if got := turnText(sendTurn(t, sess, "mcp")); got != "mcp: stdio-one" {
			t.Fatalf("reply = %q", got)
		}
	})
}

func TestCloseGatedOnCapability(t *testing.T) {
	t.Run("advertised close is called", func(t *testing.T) {
		dir := t.TempDir()
		e := testEngine(t, gooseConfig(), "goose", dir)
		sess := startSession(t, e, provider.StartOptions{})
		sendTurn(t, sess, "echo hi")
		if err := sess.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(dir, "close-called")); err != nil {
			t.Fatalf("session/close never reached the agent: %v", err)
		}
	})
	t.Run("unadvertised close is withheld", func(t *testing.T) {
		dir := t.TempDir()
		e := testEngine(t, syntheticConfig(), "synthetic", dir)
		sess := startSession(t, e, provider.StartOptions{})
		sendTurn(t, sess, "echo hi")
		if err := sess.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(dir, "close-called")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("session/close was sent to an agent that never advertised it (stat err = %v)", err)
		}
	})
}

func TestNonACPModeUnsupported(t *testing.T) {
	e := testEngine(t, gooseConfig(), "goose", t.TempDir())
	for _, mode := range []provider.Mode{provider.ModePTY, ""} {
		if _, err := e.Start(context.Background(), provider.StartOptions{Mode: mode}); !errors.Is(err, provider.ErrUnsupported) {
			t.Fatalf("mode %q: err = %v, want ErrUnsupported", mode, err)
		}
	}
}

// TestACPLiveTranscriptCapturesAssistantReply pins the W7.8 fix
// (docs/GOOSE-ADAPTER-DESIGN.md's "assistant reply gap"): an ACP session's
// own running transcript — not just its one-shot, loss-prone events
// channel — must carry the assistant's streamed reply, so a
// TranscriptReader-backed harness (goose's ACP path, kiro) serves GET
// /transcript even with no on-disk transcript file of its own (claude-code
// doesn't need this; goose/kiro do).
func TestACPLiveTranscriptCapturesAssistantReply(t *testing.T) {
	e := testEngine(t, gooseConfig(), "goose", t.TempDir())
	sess, err := e.Start(context.Background(), provider.StartOptions{Mode: provider.ModeACP})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sess.Close() }()

	if err := sess.Send(context.Background(), "echo alpha"); err != nil {
		t.Fatal(err)
	}
	evs := drainTurn(t, sess)
	if !strings.Contains(turnText(evs), "you said alpha") {
		t.Fatalf("live events missing the reply: %q", turnText(evs))
	}
	type transcripter interface{ Transcript() []provider.Message }
	msgs := sess.(transcripter).Transcript()
	if len(msgs) != 2 {
		t.Fatalf("transcript = %d messages, want 2 (user, agent): %+v", len(msgs), msgs)
	}
	if msgs[0].Role != "user" || msgs[0].Text != "echo alpha" {
		t.Fatalf("transcript[0] = %+v, want the user prompt", msgs[0])
	}
	if msgs[1].Role != "agent" || msgs[1].Text != "you said alpha" {
		t.Fatalf("transcript[1] = %+v, want the assistant reply", msgs[1])
	}
}

// A turn the agent fails (auth_required here) leaves the failure in the
// running transcript as an agent entry marked IsError — the shape
// claude-code's transcript gives an API error — so the conversation can show
// the user's message as not delivered, not merely unanswered.
func TestACPLiveTranscriptRecordsTurnError(t *testing.T) {
	e := testEngine(t, gooseConfig(), "goose", t.TempDir())
	sess := startSession(t, e, provider.StartOptions{})
	turnEnd(t, sendTurn(t, sess, "auth"))
	type transcripter interface{ Transcript() []provider.Message }
	msgs := sess.(transcripter).Transcript()
	if len(msgs) != 2 || msgs[0].Role != "user" {
		t.Fatalf("transcript = %+v, want the user prompt then the failure", msgs)
	}
	if m := msgs[1]; m.Role != "agent" || !m.IsError || !strings.Contains(m.Text, "agent authentication required") {
		t.Fatalf("transcript[1] = %+v, want an IsError agent entry carrying the error", m)
	}
}

// TestCompactionTranscriptShowsOneMarker: a compaction turn is a single
// Role "compact" entry in Transcript() — neither the "/compact" prompt nor
// the agent's "Compaction complete" ack — and the turns around it are
// untouched.
func TestCompactionTranscriptShowsOneMarker(t *testing.T) {
	cfg := gooseConfig()
	cfg.CompactCommand = "/compact"
	e := testEngine(t, cfg, "goose", t.TempDir())
	sess := startSession(t, e, provider.StartOptions{})
	sendTurn(t, sess, "echo before")
	if err := sess.(provider.Compactor).Compact(context.Background(), ""); err != nil {
		t.Fatalf("Compact: %v", err)
	}
	drainTurn(t, sess)
	sendTurn(t, sess, "echo after")
	var roles []string
	for _, m := range sess.(interface{ Transcript() []provider.Message }).Transcript() {
		roles = append(roles, m.Role)
		if m.Text == "/compact" || m.Text == "Compaction complete" {
			t.Fatalf("raw compaction plumbing in transcript: %+v", m)
		}
	}
	if got := strings.Join(roles, ","); got != "user,agent,compact,user,agent" {
		t.Fatalf("transcript roles = %s, want user,agent,compact,user,agent", got)
	}
}
