package goosecli

import (
	"context"
	"errors"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/binlocate"
	"github.com/drewkidwell/mads/providers/conformance"
	"github.com/drewkidwell/mads/providers/provider"
)

func TestRegistersCleanly(t *testing.T) {
	if err := provider.NewRegistry().Register(New(Config{})); err != nil {
		t.Fatal(err)
	}
}

// script maps the conformance probes onto the fake's goose-shaped dialect.
// attempt-write drives developer/text_editor with a write command (edit
// class — denied under read_only); attempt-shell drives developer/shell
// (execute class — denied under limited).
func script() conformance.Script {
	return conformance.Script{
		Echo:           func(marker string) string { return "echo " + marker },
		Slow:           func() string { return "slow" },
		Recall:         func() string { return "recall" },
		AttemptWrite:   func() string { return "attempt-write" },
		AttemptCommand: func() string { return "attempt-shell" },
		Timeout:        10 * time.Second, // each probe spawns a real subprocess
	}
}

// The whole conformance suite runs against the adapter driving the scripted
// stand-in binary (kirocli precedent): ACP + PTY echo, mid-turn interrupt
// with the fake's capture-pinned end_turn-after-cancel result, OneShot +
// OneShotStream over the engine's ACP emulation, cross-process resume via
// session/load, synchronous ref announce, transcript via a really-recorded
// `session export`, model discovery, the scripted 8415/21 usage, both trust
// rows under the generalized announce-tolerant probe, and MCPServers on
// Start — every declared matrix row, with no goose binary installed.
func TestConformanceAgainstScriptedBinary(t *testing.T) {
	conformance.Run(t, conformanceProvider(t), script())
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

// drainTurn collects events until (and including) EventTurnEnd. It fails
// the test if the channel closes or 15s pass first — the scripted fake is
// instant, so the tight bound stays load-bearing for every non-smoke test.
func drainTurn(t *testing.T, sess provider.Session) []provider.Event {
	t.Helper()
	return drainTurnWithin(t, sess, 15*time.Second)
}

// drainTurnWithin is drainTurn with a caller-chosen deadline — the
// real-binary smoke needs minutes per turn (cold ollama model load is the
// worst case), which would be a bug-masking bound against the fake.
func drainTurnWithin(t *testing.T, sess provider.Session, timeout time.Duration) []provider.Event {
	t.Helper()
	var events []provider.Event
	deadline := time.After(timeout)
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

// toolEvents extracts the turn's EventToolUse / EventToolResult pair lists.
func toolEvents(events []provider.Event) (uses, results []provider.Event) {
	for _, ev := range events {
		switch ev.Kind {
		case provider.EventToolUse:
			uses = append(uses, ev)
		case provider.EventToolResult:
			results = append(results, ev)
		}
	}
	return uses, results
}

func TestStartRefKnownAtReturn(t *testing.T) {
	sess := startSession(t, testProvider(t), provider.StartOptions{})
	if sess.Ref() == "" {
		t.Fatal("Ref empty at Start-return — session/new's sessionId is synchronous")
	}
	if sess.Mode() != provider.ModeACP {
		t.Fatalf("Mode = %q", sess.Mode())
	}
}

func TestEventNormalization(t *testing.T) {
	// Full trust → goose "auto" mode: the tool runs without a permission
	// request, and the events normalize off the goose wire shapes.
	sess := startSession(t, testProvider(t), provider.StartOptions{})
	events := sendTurn(t, sess, "attempt-shell")
	uses, results := toolEvents(events)
	if len(uses) != 1 || uses[0].Tool == nil {
		t.Fatalf("tool uses = %+v", uses)
	}
	// The _meta.goose.toolCall toolName is the Name — never the humanized
	// title ("shell · echo hi").
	if uses[0].Tool.Name != "shell" {
		t.Fatalf("ToolUse.Name = %q, want the _meta toolName \"shell\"", uses[0].Tool.Name)
	}
	if len(results) != 1 || results[0].IsError {
		t.Fatalf("tool results = %+v", results)
	}
	if text := turnText(events); !strings.Contains(text, "used shell") {
		t.Fatalf("turn text = %q", text)
	}
}

func TestFailedToolUpdateIsErrorResult(t *testing.T) {
	sess := startSession(t, testProvider(t), provider.StartOptions{})
	events := sendTurn(t, sess, "fail-tool")
	_, results := toolEvents(events)
	if len(results) != 1 || !results[0].IsError {
		t.Fatalf("tool_call_update failed must normalize to EventToolResult IsError; got %+v", results)
	}
}

func TestTrustLimited(t *testing.T) {
	// Limited trust rides goose's "approve" mode: every tool call asks, and
	// the engine's class gate answers. The fake only executes on an allow
	// reply, so denial is observable in both the tool result and the text.
	sess := startSession(t, testProvider(t), provider.StartOptions{Trust: provider.TrustLimited})

	events := sendTurn(t, sess, "attempt-shell")
	if text := turnText(events); !strings.Contains(text, "denied via reject_once") {
		t.Fatalf("shell under limited: turn text = %q, want a reject_once denial", text)
	}
	if _, results := toolEvents(events); len(results) != 1 || !results[0].IsError {
		t.Fatalf("shell under limited: denied call must fail; results = %+v", toolResults(events))
	}

	events = sendTurn(t, sess, "attempt-unknown")
	if text := turnText(events); !strings.Contains(text, "denied via reject_once") {
		t.Fatalf("unknown tool under limited: turn text = %q, want a denial", text)
	}

	events = sendTurn(t, sess, "attempt-view")
	if text := turnText(events); !strings.Contains(text, "used text_editor view") {
		t.Fatalf("text_editor view under limited: turn text = %q, want it allowed", text)
	}
	if _, results := toolEvents(events); len(results) != 1 || results[0].IsError {
		t.Fatalf("view under limited: allowed call must complete; results = %+v", toolResults(events))
	}
}

// TestTrustLimitedAsksTheUser: with an asker wired (the Runner always
// wires one — chat transport), goose's approve-mode asks reach the user for
// edits, commands and unknown tools, while reads are answered by the
// engine without a card (PermissionAsksEveryCall). The decision picks the
// reply: the user's allow runs the shell call the class gate alone denies.
func TestTrustLimitedAsksTheUser(t *testing.T) {
	var mu sync.Mutex
	var asked []string
	asker := func(_ context.Context, ask provider.PermissionAsk) provider.PermissionDecision {
		mu.Lock()
		asked = append(asked, ask.Tool)
		mu.Unlock()
		return provider.PermissionDecision{Allow: ask.Tool == "shell"}
	}
	sess := startSession(t, testProvider(t), provider.StartOptions{Trust: provider.TrustLimited, Permissions: asker})

	if text := turnText(sendTurn(t, sess, "attempt-shell")); !strings.Contains(text, "used shell") {
		t.Fatalf("shell allowed by the user: turn text = %q", text)
	}
	if text := turnText(sendTurn(t, sess, "attempt-write")); !strings.Contains(text, "denied via reject_once") {
		t.Fatalf("write denied by the user: turn text = %q", text)
	}
	if text := turnText(sendTurn(t, sess, "attempt-view")); !strings.Contains(text, "used text_editor view") {
		t.Fatalf("view: turn text = %q", text)
	}
	mu.Lock()
	defer mu.Unlock()
	if strings.Join(asked, ",") != "shell,text_editor" {
		t.Fatalf("asked about %v, want shell then text_editor (the write) — never the read", asked)
	}
}

// TestTrustAutoReviewRidesSmartApprove: auto_review sets goose's
// smart_approve mode, whose reviewer lets the read through unasked and
// escalates the rest — each escalation reaching the user unfiltered (no
// PermissionAsksEveryCall second-guessing), the user's decision picking
// the reply.
func TestTrustAutoReviewRidesSmartApprove(t *testing.T) {
	var mu sync.Mutex
	var asked []string
	asker := func(_ context.Context, ask provider.PermissionAsk) provider.PermissionDecision {
		mu.Lock()
		asked = append(asked, ask.Tool)
		mu.Unlock()
		return provider.PermissionDecision{Allow: ask.Tool == "shell"}
	}
	sess := startSession(t, testProvider(t), provider.StartOptions{Trust: provider.TrustAutoReview, Permissions: asker})

	if text := turnText(sendTurn(t, sess, "attempt-view")); !strings.Contains(text, "used text_editor view") {
		t.Fatalf("view: turn text = %q, want it run unasked", text)
	}
	if text := turnText(sendTurn(t, sess, "attempt-shell")); !strings.Contains(text, "used shell") {
		t.Fatalf("shell allowed by the user: turn text = %q", text)
	}
	if text := turnText(sendTurn(t, sess, "attempt-unknown")); !strings.Contains(text, "denied via reject_once") {
		t.Fatalf("unknown denied by the user: turn text = %q", text)
	}
	mu.Lock()
	defer mu.Unlock()
	if strings.Join(asked, ",") != "shell,mystery_tool" {
		t.Fatalf("asked about %v, want shell then mystery_tool — never the reviewed read", asked)
	}
}

func TestTrustReadOnly(t *testing.T) {
	sess := startSession(t, testProvider(t), provider.StartOptions{Trust: provider.TrustReadOnly})

	events := sendTurn(t, sess, "attempt-view")
	if text := turnText(events); !strings.Contains(text, "used text_editor view") {
		t.Fatalf("view under read_only: turn text = %q, want it allowed", text)
	}

	// The same tool, one rawInput command over: read_only additionally
	// denies text_editor write (the ByCommand split doing its job).
	events = sendTurn(t, sess, "attempt-write")
	if text := turnText(events); !strings.Contains(text, "denied via reject_once") {
		t.Fatalf("write under read_only: turn text = %q, want a denial", text)
	}
}

func toolResults(events []provider.Event) []provider.Event {
	_, results := toolEvents(events)
	return results
}

func TestOneShot(t *testing.T) {
	res, err := testProvider(t).OneShot(context.Background(), provider.OneShotRequest{Prompt: "echo hello"})
	if err != nil {
		t.Fatalf("OneShot: %v", err)
	}
	if res.Text != "you said hello" {
		t.Fatalf("text = %q", res.Text)
	}
	if res.Ref == "" {
		t.Fatal("Ref empty — the emulation knows the sessionId synchronously")
	}
	// The CostReporting=Native verification: usage comes from the scripted
	// prompt-result numbers (the capture's shape).
	if res.Usage.InputTokens != 8415 || res.Usage.OutputTokens != 21 {
		t.Fatalf("usage = %+v, want the scripted 8415/21", res.Usage)
	}
}

func TestOneShotRateLimit(t *testing.T) {
	_, err := testProvider(t).OneShot(context.Background(), provider.OneShotRequest{Prompt: "please ratelimit"})
	var rle *provider.RateLimitError
	if !errors.As(err, &rle) {
		t.Fatalf("err = %v, want *provider.RateLimitError", err)
	}
	if !strings.Contains(rle.Message, "rate limit") {
		t.Fatalf("message = %q", rle.Message)
	}
}

func TestRateLimitHeuristic(t *testing.T) {
	positives := []string{
		"Error in agent response stream: 429 rate limit exceeded, please slow down",
		"upstream said Too Many Requests",
		"provider is rate-limiting this key",
	}
	for _, msg := range positives {
		if rateLimit(msg) == nil {
			t.Errorf("rateLimit(%q) = nil, want a match", msg)
		}
	}
	negatives := []string{
		// Credits exhaustion is a distinct failure with its own engine
		// mapping — it must NOT read as a rate limit.
		"agent credits exhausted — top up at https://example.com/topup (Error in agent response stream)",
		"Error in agent response stream: connection reset by peer",
		"",
	}
	for _, msg := range negatives {
		if rle := rateLimit(msg); rle != nil {
			t.Errorf("rateLimit(%q) = %v, want nil", msg, rle)
		}
	}
}

func TestModels(t *testing.T) {
	models, err := testProvider(t).Models(context.Background())
	if err != nil {
		t.Fatalf("Models: %v", err)
	}
	want := []provider.Model{
		{ID: "qwen2.5", Name: "Qwen 2.5"},
		{ID: "qwen3.8", Name: "Qwen 3.8", Default: true},
	}
	if len(models) != len(want) {
		t.Fatalf("models = %+v, want %+v", models, want)
	}
	for i := range want {
		if models[i] != want[i] {
			t.Fatalf("models[%d] = %+v, want %+v", i, models[i], want[i])
		}
	}
}

func TestModelsExecFailureIsDiscoveryUnavailable(t *testing.T) {
	p := New(Config{Bin: "/nonexistent/goose-not-installed"})
	_, err := p.Models(context.Background())
	if !errors.Is(err, provider.ErrDiscoveryUnavailable) {
		t.Fatalf("err = %v, want ErrDiscoveryUnavailable", err)
	}
}

func TestMatrixValidates(t *testing.T) {
	p := New(Config{})
	if err := p.Matrix().Validate(); err != nil {
		t.Fatalf("Matrix().Validate(): %v", err)
	}
	if err := p.IntentMatrix().Validate(); err != nil {
		t.Fatalf("IntentMatrix().Validate(): %v", err)
	}
	if p.Name() != "goose" {
		t.Fatalf("Name = %q", p.Name())
	}
}

// gooseRefPattern is the goose session-id shape, e.g. "20260829_2" (design
// doc Resume row; pty.go's banner parse pins the same shape).
var gooseRefPattern = regexp.MustCompile(`^\d{8}_\d+$`)

// skipUnlessRealGooseSmoke gates the real-binary smokes (claudecode/kirocli
// convention). Once opted in, a non-serving ollama FAILS the run rather
// than skipping — the env var is an explicit claim that the stack is up.
func skipUnlessRealGooseSmoke(t *testing.T) {
	t.Helper()
	if os.Getenv("MADS_GOOSE_SMOKE") != "1" {
		t.Skip("set MADS_GOOSE_SMOKE=1 to run against the real goose binary (needs a serving local ollama — free tokens, but slow; ollama down fails the run, it does not skip)")
	}
	// The same lookup the provider runs (pkg/binlocate): PATH, then the CLI
	// goose Desktop bundles inside its app and never links onto PATH.
	if _, err := binlocate.Find("goose"); err != nil {
		t.Skip("no goose binary on PATH or in a known install location")
	}
}

// TestRealGooseSmoke drives one real `goose acp` session end to end
// (registration checklist item 5, docs/GOOSE-ADAPTER-DESIGN.md): protocol
// mechanics only — ref shape at Start-return, turn ends, usage presence,
// interrupt semantics, the transcript normalizer — never model output
// content. qwen3.8 is a local model; instruction-following is not a
// protocol claim, and a turn could legally be tool-only, so nothing here
// asserts on EventText.
// MADS_GOOSE_SMOKE=1 go test ./providers/goosecli -run RealGoose
func TestRealGooseSmoke(t *testing.T) {
	skipUnlessRealGooseSmoke(t)

	// Zero config: EnvMode inherit, so HOME/XDG reach the user's
	// ~/.config/goose/config.yaml (provider + model come from there).
	p := New(Config{})
	sess := startSession(t, p, provider.StartOptions{Trust: provider.TrustFull})
	ref := sess.Ref()
	if !gooseRefPattern.MatchString(ref) {
		t.Fatalf("Ref() = %q at Start-return, want ^\\d{8}_\\d+$ (RefAnnounce's Native-strength ACP claim)", ref)
	}

	// Turn 1: usage rides the turn end — the declared CostReporting Native
	// row. ContextWindow is logged, not asserted: it's config-dependent.
	if err := sess.Send(context.Background(), "Reply with one short sentence."); err != nil {
		t.Fatalf("Send: %v", err)
	}
	events := drainTurnWithin(t, sess, 240*time.Second)
	end := events[len(events)-1]
	if end.Usage == nil || end.Usage.OutputTokens == 0 {
		t.Fatalf("turn-end Usage = %+v, want OutputTokens > 0 (CostReporting Native row)", end.Usage)
	}
	t.Logf("turn 1 usage: input=%d output=%d context=%d/%d",
		end.Usage.InputTokens, end.Usage.OutputTokens, end.Usage.ContextTokens, end.Usage.ContextWindow)

	// Turn 2: interrupt mid-turn. Wait for the turn's first event before
	// cancelling — Interrupt straight after Send would race the turn's
	// start, and the engine's interrupted flag only marks a turn it lands
	// inside of.
	if err := sess.Send(context.Background(), "Count from 1 to 2000, one number per line."); err != nil {
		t.Fatalf("Send: %v", err)
	}
	select {
	case ev, ok := <-sess.Events():
		if !ok {
			t.Fatal("events channel closed before the interrupt turn produced anything")
		}
		if ev.Kind == provider.EventTurnEnd {
			t.Fatal("counting turn ended before Interrupt could land — lengthen the counting prompt")
		}
	case <-time.After(240 * time.Second):
		t.Fatal("no event within 240s of Send on the interrupt turn")
	}
	if err := sess.Interrupt(context.Background()); err != nil {
		t.Fatalf("Interrupt: %v", err)
	}
	events = drainTurnWithin(t, sess, 60*time.Second)
	if end := events[len(events)-1]; !end.Interrupted {
		t.Fatalf("turn end after Interrupt: Interrupted = false, want true (%+v)", end)
	}

	// Turn 3: the session survives the interrupt and serves another turn —
	// the turn ending is the assertion.
	if err := sess.Send(context.Background(), "Reply with one short sentence."); err != nil {
		t.Fatalf("Send after interrupt: %v", err)
	}
	drainTurnWithin(t, sess, 240*time.Second)

	// Transcript: close first so goose persists the session (startSession's
	// cleanup would close too late, after the export), then normalize.
	// Mechanics only: both roles present, no content inspection.
	if err := sess.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	msgs, err := p.Transcript(context.Background(), ref)
	if err != nil {
		t.Fatalf("Transcript(%q): %v", ref, err)
	}
	var haveUser, haveAgent bool
	for _, m := range msgs {
		switch m.Role {
		case "user":
			haveUser = true
		case "agent":
			haveAgent = true
		}
	}
	if len(msgs) == 0 || !haveUser || !haveAgent {
		t.Fatalf("transcript: %d messages, user=%v agent=%v — want both roles present", len(msgs), haveUser, haveAgent)
	}
}

// TestRealGooseOneShotSmoke exercises OneShot-over-ACP against the real
// binary: the ref arrives synchronously from session/new and usage from
// the prompt result — mechanics only, res.Text is deliberately untouched
// (local-model output is not contract).
// MADS_GOOSE_SMOKE=1 go test ./providers/goosecli -run RealGoose
func TestRealGooseOneShotSmoke(t *testing.T) {
	skipUnlessRealGooseSmoke(t)

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
	defer cancel()
	res, err := New(Config{}).OneShot(ctx, provider.OneShotRequest{
		Prompt: "Reply with one short sentence.", Cwd: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !gooseRefPattern.MatchString(res.Ref) {
		t.Fatalf("OneShot Ref = %q, want ^\\d{8}_\\d+$", res.Ref)
	}
	if res.Usage.OutputTokens == 0 {
		t.Fatalf("OneShot Usage = %+v, want OutputTokens > 0", res.Usage)
	}
}
