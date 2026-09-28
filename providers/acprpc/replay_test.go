package acprpc

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/drewkidwell/mads/providers/provider"
)

// These tests replay the committed goose 1.48.0 captures byte-for-byte
// (provenance: providers/goosecli/testdata/captures/README.md) through
// the engine: the fake serves each captured s2c line verbatim (response
// ids rewritten to the live request's — see faReplayMain), so the engine
// is proven against real goose output, not the hand-written profile's
// approximation. Referenced in place by relative path — moving the
// captures would churn a committed README for no gain.

func replayEngine(t *testing.T, capture string, cfg Config) *Engine {
	t.Helper()
	path, err := filepath.Abs(filepath.Join("..", "goosecli", "testdata", "captures", capture))
	if err != nil {
		t.Fatal(err)
	}
	return testEngine(t, cfg, "goose", t.TempDir(), "MADS_FAKE_ACP_REPLAY="+path)
}

func TestReplayGooseTranscriptCapture(t *testing.T) {
	// The knob values mirror what the captured session did: mode "approve"
	// applied at session start, model "qwen3.8", the unstable system-prompt
	// extension. Full trust so the captured permission requests resolve to
	// the allow reply the capture recorded.
	cfg := gooseConfig()
	cfg.TrustModes[provider.TrustFull] = "approve"
	e := replayEngine(t, "acp-transcript.jsonl", cfg)

	sess, err := e.Start(context.Background(), provider.StartOptions{
		Mode:         provider.ModeACP,
		Model:        "qwen3.8",
		SystemPrompt: "You are the capture's agent persona.",
		Trust:        provider.TrustFull,
	})
	if err != nil {
		t.Fatalf("Start against the capture: %v", err)
	}
	defer func() { _ = sess.Close() }()
	if sess.Ref() != "20260829_19" {
		t.Fatalf("ref = %q, want the captured sessionId", sess.Ref())
	}

	// Turn 1: the captured tool-using turn (three permission requests, tool
	// calls with _meta.goose identity, thought/message chunks, noise
	// variants). Prompt text is irrelevant — the replay matches by method.
	events := sendTurn(t, sess, "run the captured turn")
	if !strings.Contains(turnText(events), "acp-capture-ok") {
		t.Fatalf("captured reply text missing; text = %q", turnText(events))
	}
	var toolUses int
	for _, ev := range events {
		if ev.Kind == provider.EventToolUse {
			toolUses++
			if ev.Tool.Name == "" {
				t.Fatalf("tool_use without a name: %+v", ev.Tool)
			}
		}
	}
	if toolUses != 3 {
		t.Fatalf("tool uses = %d, want the capture's 3", toolUses)
	}
	end := turnEnd(t, events)
	if end.Usage == nil || end.Usage.InputTokens != 8415 || end.Usage.OutputTokens != 21 {
		t.Fatalf("usage = %+v, want the captured 8415/21", end.Usage)
	}
	if end.Usage.ContextTokens != 8436 || end.Usage.ContextWindow != 128000 {
		t.Fatalf("context snapshot = %+v, want the captured 8436/128000", end.Usage)
	}

	// Turn 2: the captured interrupted turn — goose resolves stopReason
	// "end_turn" on cancel, so the engine's own flag must mark it.
	if err := sess.Send(context.Background(), "count slowly"); err != nil {
		t.Fatal(err)
	}
	if err := sess.Interrupt(context.Background()); err != nil {
		t.Fatal(err)
	}
	events = drainTurn(t, sess)
	if !turnEnd(t, events).Interrupted {
		t.Fatal("captured cancel resolved end_turn — the interrupted flag must still mark the turn")
	}

	if err := sess.Close(); err != nil { // matches the captured session/close
		t.Fatal(err)
	}
}

func TestReplayGooseLoadCapture(t *testing.T) {
	// The load capture pins resume: session/load with mcpServers present
	// (the capture's own missing-field error pair is the pinned evidence the
	// engine never omits it), history replayed as session/update
	// notifications before the response resolves.
	cfg := gooseConfig()
	cfg.SendModeAtSessionNew = false // the captured client set no mode after load
	e := replayEngine(t, "acp-load-replay.jsonl", cfg)

	sess, err := e.Start(context.Background(), provider.StartOptions{
		Mode:   provider.ModeACP,
		Resume: "20260829_19",
	})
	if err != nil {
		t.Fatalf("Start(resume) against the capture: %v", err)
	}
	defer func() { _ = sess.Close() }()
	if sess.Ref() != "20260829_19" {
		t.Fatalf("ref = %q", sess.Ref())
	}

	type replayer interface{ ReplayMessages() []provider.Message }
	msgs := sess.(replayer).ReplayMessages()
	if len(msgs) == 0 {
		t.Fatal("session/load replay buffered no messages")
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
	if !haveUser || !haveAgent {
		t.Fatalf("replay roles incomplete (user=%v agent=%v): %+v", haveUser, haveAgent, msgs)
	}

	if err := sess.Close(); err != nil { // matches the captured session/close
		t.Fatal(err)
	}
}
