package claudecode

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/golden"
	"github.com/drewkidwell/mads/providers/conformance"
	"github.com/drewkidwell/mads/providers/provider"
)

func script() conformance.Script {
	return conformance.Script{
		Echo:    func(marker string) string { return "echo " + marker },
		Slow:    func() string { return "slow" },
		Recall:  func() string { return "recall" },
		Timeout: 10 * time.Second, // each probe spawns a real subprocess
	}
}

// The whole conformance suite runs against the adapter driving the scripted
// stand-in binary: process spawn, stream protocol, interrupt control
// requests, resume flags, transcript files — all real subprocess paths,
// no claude required.
func TestConformanceAgainstScriptedBinary(t *testing.T) {
	conformance.Run(t, testProvider(t), script())
}

// One-shot parsing is pinned to a REAL captured `claude -p --output-format
// json` result (claude-code 2.1.207) — if the wire format drifts, this is
// the test that says so.
func TestOneShotParsesCapturedResult(t *testing.T) {
	raw, err := os.ReadFile("testdata/oneshot-result.json")
	if err != nil {
		t.Fatal(err)
	}
	res, err := parseOneShot(raw)
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "mk-fixture" {
		t.Errorf("text: %q", res.Text)
	}
	if res.Usage.InputTokens != 10 || res.Usage.OutputTokens != 42 {
		t.Errorf("usage: %+v", res.Usage)
	}
	if res.Usage.Model != "claude-haiku-4-5-20251001" {
		t.Errorf("model from modelUsage: %q", res.Usage.Model)
	}
}

// Transcript normalization is golden-pinned: the fixture mirrors the on-disk
// format (string and block user content, thinking blocks, tool results,
// sidechains, bookkeeping lines) and the golden holds exactly what the
// platform sees.
func TestTranscriptNormalizationGolden(t *testing.T) {
	f, err := os.Open("testdata/transcript.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	msgs, err := normalizeTranscript(f)
	if err != nil {
		t.Fatal(err)
	}
	got, err := json.MarshalIndent(msgs, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	golden.Assert(t, "transcript.golden.json", append(got, '\n'))
}

// Tool uses stream through as normalized events with the touched path
// extracted — the §5 tool_use attribution signal.
func TestStreamToolUseNormalization(t *testing.T) {
	p := testProvider(t)
	sess, err := p.Start(context.Background(), provider.StartOptions{Mode: provider.ModeACP})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sess.Close() }()
	if err := sess.Send(context.Background(), "edit api/login.go"); err != nil {
		t.Fatal(err)
	}
	var kinds []provider.EventKind
	var tool *provider.ToolUse
	deadline := time.After(10 * time.Second)
	for done := false; !done; {
		select {
		case ev, ok := <-sess.Events():
			if !ok {
				t.Fatal("stream closed before turn end")
			}
			kinds = append(kinds, ev.Kind)
			if ev.Kind == provider.EventToolUse {
				tool = ev.Tool
			}
			if ev.Kind == provider.EventTurnEnd {
				if ev.Usage == nil || ev.Usage.Model != "fake-sonnet" {
					t.Errorf("turn end usage should carry the init-line model: %+v", ev.Usage)
				}
				done = true
			}
		case <-deadline:
			t.Fatalf("no turn end; saw %v", kinds)
		}
	}
	if tool == nil || tool.Name != "Edit" || tool.Path != "api/login.go" {
		t.Errorf("tool event: %+v", tool)
	}
}

// A process death mid-turn closes the event stream with no turn end — the
// supervision signal.
func TestCrashMidTurnClosesWithoutTurnEnd(t *testing.T) {
	p := testProvider(t)
	sess, err := p.Start(context.Background(), provider.StartOptions{Mode: provider.ModeACP})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sess.Close() }()
	if err := sess.Send(context.Background(), "crash"); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(10 * time.Second)
	for {
		select {
		case ev, ok := <-sess.Events():
			if !ok {
				return // closed without turn end: correct
			}
			if ev.Kind == provider.EventTurnEnd {
				t.Fatal("crash produced a clean turn end")
			}
		case <-deadline:
			t.Fatal("stream never closed after crash")
		}
	}
}

// Rate limits surface as the typed error (§11.9), detected from CLI output.
func TestRateLimitSurfaces(t *testing.T) {
	_, err := testProvider(t).OneShot(context.Background(), provider.OneShotRequest{Prompt: "ratelimit"})
	var rle *provider.RateLimitError
	if !errors.As(err, &rle) || !strings.Contains(rle.Message, "Rate limit reached") {
		t.Fatalf("want RateLimitError, got %v", err)
	}
	if rateLimit("some unrelated failure") != nil {
		t.Error("false positive rate-limit detection")
	}
}

func TestRegistersCleanly(t *testing.T) {
	if err := provider.NewRegistry().Register(New(Config{})); err != nil {
		t.Fatal(err)
	}
}

// TestRealClaudeSmoke exercises the real binary. Opt-in: it spends real
// tokens. MADS_CLAUDE_SMOKE=1 go test ./providers/claudecode -run Smoke
func TestRealClaudeSmoke(t *testing.T) {
	if os.Getenv("MADS_CLAUDE_SMOKE") != "1" {
		t.Skip("set MADS_CLAUDE_SMOKE=1 (spends tokens) to run against the real claude binary")
	}
	if _, err := exec.LookPath("claude"); err != nil {
		t.Skip("no claude binary on PATH")
	}
	p := New(Config{ExtraArgs: []string{"--dangerously-skip-permissions"}})
	res, err := p.OneShot(context.Background(), provider.OneShotRequest{
		Prompt: "reply with exactly: mk-smoke", Model: "haiku", Cwd: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Text, "mk-smoke") || res.Usage.OutputTokens == 0 {
		t.Fatalf("smoke result: %+v", res)
	}
}
