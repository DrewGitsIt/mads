package acprpc

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/drewkidwell/mads/providers/provider"
)

// The forwarded permission path (handlePermission → forwardPermission):
// at limited trust with StartOptions.Permissions set, the agent's
// session/request_permission reaches the asker and the user's decision
// picks the reply. These run against the same adversarial fake as
// TestPermissionSelectionByKind — optionIds never mirror kinds, and the
// fake reports exactly which id (or outcome, or error) it received.

// recordingAsker is a PermissionAsker that records every ask and answers
// with decide; a nil decide blocks until ctx is done and records that.
type recordingAsker struct {
	mu        sync.Mutex
	asks      []provider.PermissionAsk
	cancelled int
	asked     chan provider.PermissionAsk
	decide    func(provider.PermissionAsk) provider.PermissionDecision
}

func newRecordingAsker(decide func(provider.PermissionAsk) provider.PermissionDecision) *recordingAsker {
	return &recordingAsker{asked: make(chan provider.PermissionAsk, 16), decide: decide}
}

func (r *recordingAsker) ask(ctx context.Context, ask provider.PermissionAsk) provider.PermissionDecision {
	r.mu.Lock()
	r.asks = append(r.asks, ask)
	r.mu.Unlock()
	r.asked <- ask
	if r.decide != nil {
		return r.decide(ask)
	}
	<-ctx.Done()
	r.mu.Lock()
	r.cancelled++
	r.mu.Unlock()
	return provider.PermissionDecision{Message: "withdrawn"}
}

func (r *recordingAsker) snapshot() (asks []provider.PermissionAsk, cancelled int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]provider.PermissionAsk(nil), r.asks...), r.cancelled
}

// waitCancelled polls until the asker has observed n ctx cancellations.
func (r *recordingAsker) waitCancelled(t *testing.T, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, c := r.snapshot(); c >= n {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	_, c := r.snapshot()
	t.Fatalf("asker saw %d ctx cancellations, want %d", c, n)
}

func allowAll(provider.PermissionAsk) provider.PermissionDecision {
	return provider.PermissionDecision{Allow: true}
}

func denyAll(provider.PermissionAsk) provider.PermissionDecision {
	return provider.PermissionDecision{Message: "no"}
}

// TestForwardedPermissionDecisions: the user's allow selects the offered
// allow_once option (never allow_always, though the menu lists it first),
// a deny selects reject_once, and a menu with no _once option of the
// decided polarity is answered "cancelled" rather than persisting the
// answer agent-side through an _always option.
func TestForwardedPermissionDecisions(t *testing.T) {
	cases := []struct {
		name    string
		profile string
		cfg     Config
		prompt  string
		decide  func(provider.PermissionAsk) provider.PermissionDecision
		want    string
	}{
		{"allow selects allow_once (goose)", "goose", gooseConfig(), "attempt-shell", allowAll, "used shell"},
		{"deny selects reject_once (goose)", "goose", gooseConfig(), "attempt-shell", denyAll, "denied via opt-r"},
		{"allow selects allow_once (kind-based)", "synthetic", syntheticConfig(), "attempt-shell", allowAll, "used shell"},
		{"deny selects reject_once (kind-based)", "synthetic", syntheticConfig(), "attempt-shell", denyAll, "denied via syn-no"},
		// The class gate would have allowed this edit on its own; with an
		// asker the user decides instead, and their deny wins.
		{"edit asks and a deny wins", "goose", gooseConfig(), "attempt-write", denyAll, "denied via opt-r"},
		// The class gate would have denied an unknown tool; the agent asked,
		// so the user decides, and their allow wins.
		{"unknown asks and an allow wins", "goose", gooseConfig(), "attempt-unknown", allowAll, "used mystery_tool"},
		{"always-only menu: allow never picks always", "synthetic", syntheticConfig(), "attempt-shell-always", allowAll, "denied via cancelled"},
		{"always-only menu: deny never picks always", "synthetic", syntheticConfig(), "attempt-shell-always", denyAll, "denied via cancelled"},
		{"two-option menu allow", "synthetic", syntheticConfig(), "attempt-view-two", allowAll, "used text_editor view"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			asker := newRecordingAsker(tc.decide)
			e := testEngine(t, tc.cfg, tc.profile, t.TempDir())
			sess := startSession(t, e, provider.StartOptions{Trust: provider.TrustLimited, Permissions: asker.ask})
			events := sendTurn(t, sess, tc.prompt)
			if got := turnText(events); got != tc.want {
				t.Fatalf("reply = %q, want %q", got, tc.want)
			}
			if asks, _ := asker.snapshot(); len(asks) != 1 {
				t.Fatalf("asker called %d times, want 1", len(asks))
			}
		})
	}
}

// TestForwardedAskCarriesCallIdentity: the ask names the exact call the
// agent announced — ToolUseID equals the EventToolUse's Tool.ID, so the
// Runner's card attaches to that row and pkg/toolstatus parks/blocks the
// right call — with the classified tool name and the call's rawInput. The
// ask-before-announce shape (kiro-cli) takes all three from the request's
// embedded toolCall instead.
func TestForwardedAskCarriesCallIdentity(t *testing.T) {
	t.Run("announced call", func(t *testing.T) {
		asker := newRecordingAsker(denyAll)
		e := testEngine(t, gooseConfig(), "goose", t.TempDir())
		sess := startSession(t, e, provider.StartOptions{Trust: provider.TrustLimited, Permissions: asker.ask})
		events := sendTurn(t, sess, "attempt-shell")
		var useID, resultID string
		var resultErr bool
		for _, ev := range events {
			switch ev.Kind {
			case provider.EventToolUse:
				useID = ev.Tool.ID
			case provider.EventToolResult:
				resultID, resultErr = ev.Tool.ID, ev.IsError
			}
		}
		asks, _ := asker.snapshot()
		if len(asks) != 1 {
			t.Fatalf("asks = %d, want 1", len(asks))
		}
		ask := asks[0]
		if useID == "" || ask.ToolUseID != useID || resultID != useID {
			t.Fatalf("ask.ToolUseID = %q, tool_use id %q, tool_result id %q — all must match", ask.ToolUseID, useID, resultID)
		}
		if !resultErr {
			t.Fatal("the denied call's tool_result must be an error (the Runner keeps it blocked)")
		}
		if ask.Tool != "shell" {
			t.Fatalf("ask.Tool = %q, want the classified identity %q", ask.Tool, "shell")
		}
		var in map[string]string
		if err := json.Unmarshal(ask.Input, &in); err != nil || in["command"] != "echo hi" {
			t.Fatalf("ask.Input = %s, want the call's rawInput", ask.Input)
		}
		if ask.ParentToolUseID != "" || ask.AgentID != "" || ask.Reason != "" {
			t.Fatalf("ask = %+v: no ACP agent sends subagent identity or a reason", ask)
		}
	})
	t.Run("ask before announce", func(t *testing.T) {
		asker := newRecordingAsker(allowAll)
		e := testEngine(t, syntheticConfig(), "synthetic", t.TempDir())
		sess := startSession(t, e, provider.StartOptions{Trust: provider.TrustLimited, Permissions: asker.ask})
		events := sendTurn(t, sess, "attempt-shell-askfirst")
		if got := turnText(events); got != "used shell" {
			t.Fatalf("reply = %q", got)
		}
		asks, _ := asker.snapshot()
		if len(asks) != 1 || asks[0].ToolUseID == "" || asks[0].Tool != "shell" || len(asks[0].Input) == 0 {
			t.Fatalf("asks = %+v, want one ask carrying the embedded toolCall's id, title and rawInput", asks)
		}
		var useID string
		for _, ev := range events {
			if ev.Kind == provider.EventToolUse {
				useID = ev.Tool.ID
			}
		}
		if useID != asks[0].ToolUseID {
			t.Fatalf("announced id %q != asked id %q", useID, asks[0].ToolUseID)
		}
	})
}

// TestAsksEveryCallAllowsSafeClassesLocally: goose's approve mode asks for
// every call, so with PermissionAsksEveryCall the engine answers reads
// itself (no card) — while an agent without the knob that asks about a
// read gets the user, because the harness judged it worth asking.
func TestAsksEveryCallAllowsSafeClassesLocally(t *testing.T) {
	t.Run("goose read: no ask", func(t *testing.T) {
		asker := newRecordingAsker(denyAll)
		e := testEngine(t, gooseConfig(), "goose", t.TempDir())
		sess := startSession(t, e, provider.StartOptions{Trust: provider.TrustLimited, Permissions: asker.ask})
		if got := turnText(sendTurn(t, sess, "attempt-view")); got != "used text_editor view" {
			t.Fatalf("reply = %q", got)
		}
		if asks, _ := asker.snapshot(); len(asks) != 0 {
			t.Fatalf("asker called for a read under approve mode: %+v", asks)
		}
	})
	t.Run("harness-judged read: asks", func(t *testing.T) {
		asker := newRecordingAsker(denyAll)
		e := testEngine(t, syntheticConfig(), "synthetic", t.TempDir())
		sess := startSession(t, e, provider.StartOptions{Trust: provider.TrustLimited, Permissions: asker.ask})
		if got := turnText(sendTurn(t, sess, "attempt-view")); got != "denied via syn-no" {
			t.Fatalf("reply = %q", got)
		}
		if asks, _ := asker.snapshot(); len(asks) != 1 {
			t.Fatalf("asker called %d times, want 1", len(asks))
		}
	})
}

// TestReadOnlyAndFullNeverAsk: read_only stays the engine's class gate —
// a hard guarantee that never waits on a click — and full allows without
// a card, both with an asker wired.
func TestReadOnlyAndFullNeverAsk(t *testing.T) {
	cases := []struct {
		trust  provider.Trust
		prompt string
		want   string
	}{
		{provider.TrustReadOnly, "attempt-view", "used text_editor view"},
		{provider.TrustReadOnly, "attempt-write", "denied via syn-no"},
		{provider.TrustReadOnly, "attempt-shell", "denied via syn-no"},
		{provider.TrustFull, "attempt-shell", "used shell"},
	}
	for _, tc := range cases {
		t.Run(string(tc.trust)+" "+tc.prompt, func(t *testing.T) {
			asker := newRecordingAsker(func(provider.PermissionAsk) provider.PermissionDecision {
				t.Error("asker called")
				return provider.PermissionDecision{Allow: true}
			})
			e := testEngine(t, syntheticConfig(), "synthetic", t.TempDir())
			sess := startSession(t, e, provider.StartOptions{Trust: tc.trust, Permissions: asker.ask})
			if got := turnText(sendTurn(t, sess, tc.prompt)); got != tc.want {
				t.Fatalf("reply = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestNoAskerKeepsClassGate: without an asker (a caller with nobody to
// ask) limited trust is the engine's class decision, unchanged — edit
// allowed, execute denied, nothing stalls.
func TestNoAskerKeepsClassGate(t *testing.T) {
	e := testEngine(t, gooseConfig(), "goose", t.TempDir())
	sess := startSession(t, e, provider.StartOptions{Trust: provider.TrustLimited})
	if got := turnText(sendTurn(t, sess, "attempt-write")); got != "used text_editor write" {
		t.Fatalf("edit: reply = %q", got)
	}
	if got := turnText(sendTurn(t, sess, "attempt-shell")); got != "denied via opt-r" {
		t.Fatalf("execute: reply = %q", got)
	}
}

// TestInterruptWithdrawsForwardedAsk: an interrupt while the user is still
// deciding cancels the asker's ctx and answers the open request with
// outcome "cancelled" — the spec's obligation after session/cancel — and
// the turn ends interrupted.
func TestInterruptWithdrawsForwardedAsk(t *testing.T) {
	asker := newRecordingAsker(nil) // blocks until withdrawn
	e := testEngine(t, gooseConfig(), "goose", t.TempDir())
	sess := startSession(t, e, provider.StartOptions{Trust: provider.TrustLimited, Permissions: asker.ask})
	if err := sess.Send(context.Background(), "attempt-shell"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-asker.asked:
	case <-time.After(10 * time.Second):
		t.Fatal("asker never called")
	}
	if err := sess.Interrupt(context.Background()); err != nil {
		t.Fatal(err)
	}
	events := drainTurn(t, sess)
	asker.waitCancelled(t, 1)
	if got := turnText(events); got != "denied via cancelled" {
		t.Fatalf("reply = %q, want the agent to observe outcome cancelled", got)
	}
	if !turnEnd(t, events).Interrupted {
		t.Fatal("turn end not marked interrupted")
	}
}

// TestAgentCancelRequestWithdrawsForwardedAsk: the agent's own
// $/cancel_request for an open ask cancels the asker's ctx and answers
// the request with the RequestCancelled error, not an outcome.
func TestAgentCancelRequestWithdrawsForwardedAsk(t *testing.T) {
	asker := newRecordingAsker(nil)
	e := testEngine(t, syntheticConfig(), "synthetic", t.TempDir())
	sess := startSession(t, e, provider.StartOptions{Trust: provider.TrustLimited, Permissions: asker.ask})
	events := sendTurn(t, sess, "attempt-shell-withdraw")
	asker.waitCancelled(t, 1)
	if got := turnText(events); got != "denied via error -32800" {
		t.Fatalf("reply = %q, want the agent to observe RequestCancelled", got)
	}
}

// TestCloseWithdrawsForwardedAsk: closing the session (and the process
// going with it) cancels an ask still waiting on the user.
func TestCloseWithdrawsForwardedAsk(t *testing.T) {
	asker := newRecordingAsker(nil)
	e := testEngine(t, gooseConfig(), "goose", t.TempDir())
	sess := startSession(t, e, provider.StartOptions{Trust: provider.TrustLimited, Permissions: asker.ask})
	if err := sess.Send(context.Background(), "attempt-shell"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-asker.asked:
	case <-time.After(10 * time.Second):
		t.Fatal("asker never called")
	}
	_ = sess.Close()
	asker.waitCancelled(t, 1)
}

// TestLateDecisionDiscarded: a decision returned after the ask was
// withdrawn writes nothing — the request already got its one answer.
func TestLateDecisionDiscarded(t *testing.T) {
	release := make(chan struct{})
	asker := newRecordingAsker(func(provider.PermissionAsk) provider.PermissionDecision {
		<-release
		return provider.PermissionDecision{Allow: true}
	})
	e := testEngine(t, gooseConfig(), "goose", t.TempDir())
	sess := startSession(t, e, provider.StartOptions{Trust: provider.TrustLimited, Permissions: asker.ask})
	if err := sess.Send(context.Background(), "attempt-shell"); err != nil {
		t.Fatal(err)
	}
	<-asker.asked
	if err := sess.Interrupt(context.Background()); err != nil {
		t.Fatal(err)
	}
	events := drainTurn(t, sess)
	close(release) // the allow lands after the cancelled answer
	if got := turnText(events); got != "denied via cancelled" {
		t.Fatalf("reply = %q", got)
	}
	// The session still works: no stray reply confused the fake.
	if got := turnText(sendTurn(t, sess, "echo still-here")); got != "you said still-here" {
		t.Fatalf("follow-up turn = %q", got)
	}
}

// TestAutoReviewForwardsEveryAsk: at auto_review the agent runs its own
// reviewer mode (goose smart_approve), so whatever reaches
// session/request_permission is an escalation and goes to the user —
// even a read, which limited under PermissionAsksEveryCall answers
// locally. The fake has no reviewer of its own: auto_review is mapped to
// its asking mode, standing in for a reviewer that escalated every call.
// Without an asker auto_review keeps limited's class gate, so nothing
// stalls.
func TestAutoReviewForwardsEveryAsk(t *testing.T) {
	cfg := func() Config {
		c := gooseConfig()
		c.TrustModes[provider.TrustAutoReview] = "approve"
		return c
	}
	cases := []struct {
		prompt string
		decide func(provider.PermissionAsk) provider.PermissionDecision
		want   string
	}{
		{"attempt-view", denyAll, "denied via opt-r"},
		{"attempt-shell", allowAll, "used shell"},
		{"attempt-write", denyAll, "denied via opt-r"},
	}
	for _, tc := range cases {
		t.Run(tc.prompt, func(t *testing.T) {
			asker := newRecordingAsker(tc.decide)
			e := testEngine(t, cfg(), "goose", t.TempDir())
			sess := startSession(t, e, provider.StartOptions{Trust: provider.TrustAutoReview, Permissions: asker.ask})
			if got := turnText(sendTurn(t, sess, tc.prompt)); got != tc.want {
				t.Fatalf("reply = %q, want %q", got, tc.want)
			}
			if asks, _ := asker.snapshot(); len(asks) != 1 {
				t.Fatalf("asker called %d times, want 1", len(asks))
			}
		})
	}
	t.Run("no asker: class gate", func(t *testing.T) {
		e := testEngine(t, cfg(), "goose", t.TempDir())
		sess := startSession(t, e, provider.StartOptions{Trust: provider.TrustAutoReview})
		if got := turnText(sendTurn(t, sess, "attempt-write")); got != "used text_editor write" {
			t.Fatalf("edit: reply = %q", got)
		}
		if got := turnText(sendTurn(t, sess, "attempt-shell")); got != "denied via opt-r" {
			t.Fatalf("execute: reply = %q", got)
		}
	})
}
