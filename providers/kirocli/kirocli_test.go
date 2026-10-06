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
		Echo:           func(marker string) string { return "echo " + marker },
		Slow:           func() string { return "slow" },
		Recall:         func() string { return "recall" },
		AttemptWrite:   func() string { return "attempt-write" },
		AttemptCommand: func() string { return "attempt-command" },
		Timeout:        10 * time.Second, // each probe spawns a real subprocess
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

// A read_only session's attempted Write and a limited session's attempted
// Bash both deny at the real session/request_permission handshake — proving
// the kind-based gate in session.go, not just the wire types.
func TestTrustDeniesToolKind(t *testing.T) {
	cases := []struct {
		trust  provider.Trust
		prompt string
	}{
		{provider.TrustLimited, "attempt-command"},
		{provider.TrustReadOnly, "attempt-write"},
		{provider.TrustReadOnly, "attempt-command"},
	}
	for _, c := range cases {
		p := testProvider(t)
		sess, err := p.Start(context.Background(), provider.StartOptions{Mode: provider.ModeACP, Trust: c.trust})
		if err != nil {
			t.Fatal(err)
		}
		if err := sess.Send(context.Background(), c.prompt); err != nil {
			t.Fatal(err)
		}
		deadline := time.After(10 * time.Second)
		for done := false; !done; {
			select {
			case ev, ok := <-sess.Events():
				if !ok {
					t.Fatal("stream closed before turn end")
				}
				if ev.Kind == provider.EventToolUse {
					t.Errorf("trust %s: tool use event observed for %q (name=%s)", c.trust, c.prompt, ev.Tool.Name)
				}
				if ev.Kind == provider.EventTurnEnd {
					done = true
				}
			case <-deadline:
				t.Fatalf("trust %s: no turn end for %q", c.trust, c.prompt)
			}
		}
		_ = sess.Close()
	}
}

// With no asker wired (StartOptions.Permissions nil — nobody to ask), a
// limited session's attempted Write (an "edit" kind) is allowed by the
// engine's kind gate; only command execution is refused.
func TestTrustLimitedAllowsEdit(t *testing.T) {
	p := testProvider(t)
	sess, err := p.Start(context.Background(), provider.StartOptions{Mode: provider.ModeACP, Trust: provider.TrustLimited})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sess.Close() }()
	if err := sess.Send(context.Background(), "attempt-write"); err != nil {
		t.Fatal(err)
	}
	var sawTool bool
	deadline := time.After(10 * time.Second)
	for done := false; !done; {
		select {
		case ev, ok := <-sess.Events():
			if !ok {
				t.Fatal("stream closed before turn end")
			}
			if ev.Kind == provider.EventToolUse {
				sawTool = true
			}
			if ev.Kind == provider.EventTurnEnd {
				done = true
			}
		case <-deadline:
			t.Fatal("no turn end")
		}
	}
	if !sawTool {
		t.Fatal("limited session denied an edit-kind tool, want it allowed")
	}
}

// TestTrustLimitedForwardsAsks: with an asker wired (the Runner always wires
// one), kiro's own permission requests reach the user unfiltered and the
// user's decision is what runs: an allowed command executes even though
// the kind gate alone would refuse it, a denied edit never runs. kiro asks
// before announcing the call, so the ask's identity comes from the
// request's embedded toolCall.
func TestTrustLimitedForwardsAsks(t *testing.T) {
	cases := []struct {
		prompt  string
		allow   bool
		wantUse bool
	}{
		{"attempt-command", true, true},
		{"attempt-write", false, false},
	}
	for _, c := range cases {
		asks := make(chan provider.PermissionAsk, 4)
		asker := func(_ context.Context, ask provider.PermissionAsk) provider.PermissionDecision {
			asks <- ask
			return provider.PermissionDecision{Allow: c.allow}
		}
		p := testProvider(t)
		sess, err := p.Start(context.Background(), provider.StartOptions{Mode: provider.ModeACP, Trust: provider.TrustLimited, Permissions: asker})
		if err != nil {
			t.Fatal(err)
		}
		if err := sess.Send(context.Background(), c.prompt); err != nil {
			t.Fatal(err)
		}
		var useID string
		deadline := time.After(10 * time.Second)
		for done := false; !done; {
			select {
			case ev, ok := <-sess.Events():
				if !ok {
					t.Fatal("stream closed before turn end")
				}
				if ev.Kind == provider.EventToolUse {
					useID = ev.Tool.ID
				}
				if ev.Kind == provider.EventTurnEnd {
					done = true
				}
			case <-deadline:
				t.Fatalf("%s: no turn end", c.prompt)
			}
		}
		_ = sess.Close()
		if len(asks) != 1 {
			t.Fatalf("%s: asker called %d times, want 1", c.prompt, len(asks))
		}
		ask := <-asks
		if ask.ToolUseID == "" {
			t.Fatalf("%s: ask %+v names no call", c.prompt, ask)
		}
		if (useID != "") != c.wantUse {
			t.Errorf("%s: tool use observed = %v, want %v", c.prompt, useID != "", c.wantUse)
		}
		if useID != "" && useID != ask.ToolUseID {
			t.Errorf("%s: announced id %q != asked id %q", c.prompt, useID, ask.ToolUseID)
		}
	}
}

func TestRegistersCleanly(t *testing.T) {
	if err := provider.NewRegistry().Register(New(Config{})); err != nil {
		t.Fatal(err)
	}
}

// TestNoCompleter proves kiro-cli does not implement provider.Completer:
// Fast Model's "use my provider" resolution must go dormant for this
// provider rather than silently falling back to something non-hermetic.
func TestNoCompleter(t *testing.T) {
	if _, ok := any(New(Config{})).(provider.Completer); ok {
		t.Fatal("kirocli.Provider implements provider.Completer, want false (dormant path expected)")
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

// TestTrustAutoReviewFallsBackToLimited: kiro has no reviewer
// (BehaviorTrustAutoReview unsupported), so an auto_review session runs
// exactly as limited — kiro's own asks forwarded to the user, the user's
// allow running a command the kind gate alone would refuse — and the
// declaration EffectiveTrust reads says so.
func TestTrustAutoReviewFallsBackToLimited(t *testing.T) {
	p := testProvider(t)
	if got := provider.EffectiveTrust(provider.TrustAutoReview, p.Matrix()); got != provider.TrustLimited {
		t.Fatalf("EffectiveTrust(auto_review) = %q, want limited", got)
	}
	asks := make(chan provider.PermissionAsk, 4)
	asker := func(_ context.Context, ask provider.PermissionAsk) provider.PermissionDecision {
		asks <- ask
		return provider.PermissionDecision{Allow: true}
	}
	sess, err := p.Start(context.Background(), provider.StartOptions{Mode: provider.ModeACP, Trust: provider.TrustAutoReview, Permissions: asker})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sess.Close() }()
	if err := sess.Send(context.Background(), "attempt-command"); err != nil {
		t.Fatal(err)
	}
	var used bool
	deadline := time.After(10 * time.Second)
	for done := false; !done; {
		select {
		case ev, ok := <-sess.Events():
			if !ok {
				t.Fatal("stream closed before turn end")
			}
			if ev.Kind == provider.EventToolUse {
				used = true
			}
			if ev.Kind == provider.EventTurnEnd {
				done = true
			}
		case <-deadline:
			t.Fatal("no turn end")
		}
	}
	if len(asks) != 1 || !used {
		t.Fatalf("asks = %d, tool used = %v: want kiro's ask forwarded and the allowed command run, as at limited", len(asks), used)
	}
}
