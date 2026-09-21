package goosecli

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/drewkidwell/mads/providers/provider"
)

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
