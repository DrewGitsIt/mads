package kirocli

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

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
// stand-in binary: process spawn, acp JSON-RPC protocol, session/cancel
// interrupt, session/load resume and transcript replay — all real subprocess
// paths, no kiro-cli required. This is §11.8: the same suite that passes
// claude-code passes kiro-cli's declared matrix with zero core changes.
func TestConformanceAgainstScriptedBinary(t *testing.T) {
	conformance.Run(t, testProvider(t), script())
}

// A process death mid-turn closes the event stream with no turn end — the
// supervision signal, same contract as claude-code's.
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

// TestRealKiroSmoke exercises the real binary. Opt-in: kiro-cli is not
// installed on this machine, so this both spends real tokens elsewhere and
// cannot be exercised here — it must skip cleanly rather than fail.
// MADS_KIRO_SMOKE=1 go test ./providers/kirocli -run Smoke
func TestRealKiroSmoke(t *testing.T) {
	if os.Getenv("MADS_KIRO_SMOKE") != "1" {
		t.Skip("set MADS_KIRO_SMOKE=1 (spends tokens) to run against the real kiro-cli binary")
	}
	if _, err := exec.LookPath("kiro-cli"); err != nil {
		t.Skip("no kiro-cli binary on PATH")
	}
	p := New(Config{})
	res, err := p.OneShot(context.Background(), provider.OneShotRequest{
		Prompt: "reply with exactly: mk-smoke", Cwd: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Text, "mk-smoke") {
		t.Fatalf("smoke result: %+v", res)
	}
}
