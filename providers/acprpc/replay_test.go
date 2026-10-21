package acprpc

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/drewkidwell/mads/providers/provider"
)

// These tests replay the committed goose captures (1.50.1, plus the kept
// 1.48.0 transcript) byte-for-byte
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

// transcriptReplay is what one committed acp-transcript capture recorded:
// the session it opened, the model it set, and turn 1's tool and usage
// numbers. Turn 2 of every capture is the session/cancel turn.
type transcriptReplay struct {
	capture             string
	ref, model          string
	toolUses            int
	inTokens, outTokens int
	contextTokens       int
}

// TestReplayGooseTranscriptCapture replays the current capture (goose
// 1.50.1, where a cancelled prompt resolves stopReason "cancelled") and the
// kept 1.48.0 one (where it resolved "end_turn" and only the engine's own
// interrupted flag marks the turn). The 1.48.0 file also carries the
// thought chunks and multi-permission turn the 1.50.1 run did not produce.
func TestReplayGooseTranscriptCapture(t *testing.T) {
	for _, tc := range []transcriptReplay{
		{capture: "acp-transcript.jsonl", ref: "20261020_3", model: "qwen3.8-coder-nothink",
			toolUses: 1, inTokens: 6430, outTokens: 11, contextTokens: 6441},
		{capture: "acp-transcript-1.48.0.jsonl", ref: "20260829_19", model: "qwen3.8",
			toolUses: 3, inTokens: 8415, outTokens: 21, contextTokens: 8436},
	} {
		t.Run(tc.capture, func(t *testing.T) { replayTranscript(t, tc) })
	}
}

func replayTranscript(t *testing.T, tc transcriptReplay) {
	// The knob values mirror what the captured session did: mode "approve"
	// applied at session start, the captured model, the unstable
	// system-prompt extension. Full trust so the captured permission
	// requests resolve to the allow reply the capture recorded.
	cfg := gooseConfig()
	cfg.TrustModes[provider.TrustFull] = "approve"
	e := replayEngine(t, tc.capture, cfg)

	sess, err := e.Start(context.Background(), provider.StartOptions{
		Mode:         provider.ModeACP,
		Model:        tc.model,
		SystemPrompt: "You are the capture's agent persona.",
		Trust:        provider.TrustFull,
	})
	if err != nil {
		t.Fatalf("Start against the capture: %v", err)
	}
	defer func() { _ = sess.Close() }()
	if sess.Ref() != tc.ref {
		t.Fatalf("ref = %q, want the captured sessionId %q", sess.Ref(), tc.ref)
	}

	// Turn 1: the captured tool-using turn (permission requests, tool calls
	// with _meta.goose identity, message chunks, noise variants). Prompt
	// text is irrelevant — the replay matches by method.
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
	if toolUses != tc.toolUses {
		t.Fatalf("tool uses = %d, want the capture's %d", toolUses, tc.toolUses)
	}
	end := turnEnd(t, events)
	if end.Usage == nil || end.Usage.InputTokens != tc.inTokens || end.Usage.OutputTokens != tc.outTokens {
		t.Fatalf("usage = %+v, want the captured %d/%d", end.Usage, tc.inTokens, tc.outTokens)
	}
	if end.Usage.ContextTokens != tc.contextTokens || end.Usage.ContextWindow != 128000 {
		t.Fatalf("context snapshot = %+v, want the captured %d/128000", end.Usage, tc.contextTokens)
	}

	// Turn 2: the captured interrupted turn. 1.50.1 resolves "cancelled";
	// 1.48.0 resolved "end_turn", so there the engine's own flag must mark it.
	if err := sess.Send(context.Background(), "count slowly"); err != nil {
		t.Fatal(err)
	}
	if err := sess.Interrupt(context.Background()); err != nil {
		t.Fatal(err)
	}
	events = drainTurn(t, sess)
	if !turnEnd(t, events).Interrupted {
		t.Fatal("captured cancel turn not marked interrupted")
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
		Resume: "20261020_3",
	})
	if err != nil {
		t.Fatalf("Start(resume) against the capture: %v", err)
	}
	defer func() { _ = sess.Close() }()
	if sess.Ref() != "20261020_3" {
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
