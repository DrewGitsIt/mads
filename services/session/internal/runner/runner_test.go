package runner

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/providers/fake"
	"github.com/drewkidwell/mads/providers/provider"
	"github.com/drewkidwell/mads/services/data/datatest"
)

// drainText collects EventText chunks from ch until an EventTurnEnd, failing
// the test if none arrives within the deadline.
func drainText(t *testing.T, ch <-chan provider.Event) string {
	t.Helper()
	var sb strings.Builder
	deadline := time.After(5 * time.Second)
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				t.Fatal("event channel closed before turn_end")
			}
			switch ev.Kind {
			case provider.EventText:
				sb.WriteString(ev.Text)
			case provider.EventTurnEnd:
				return sb.String()
			case provider.EventError:
				t.Fatalf("unexpected error event: %s", ev.Text)
			}
		case <-deadline:
			t.Fatal("timed out waiting for turn_end")
		}
	}
}

// newSession creates a session with a throwaway actor (the server assigns
// the ID; nothing depends on the actor used for this one call).
func newSession(t *testing.T, base string) contracts.Session {
	t.Helper()
	bootstrap := dataclient.New(base, "test-bootstrap")
	sess := contracts.Session{Name: "test", Provider: "fake"}
	if err := bootstrap.CreateSession(context.Background(), &sess); err != nil {
		t.Fatalf("create session: %v", err)
	}
	return sess
}

func TestRoundTrip(t *testing.T) {
	ctx := context.Background()
	base := datatest.Start(t)
	sess := newSession(t, base)
	data := dataclient.New(base, sess.ID)

	reg := provider.NewRegistry()
	if err := reg.Register(fake.New()); err != nil {
		t.Fatalf("register fake: %v", err)
	}

	r := New(sess.ID, data, reg)
	if err := r.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = r.Shutdown(ctx) })

	ch, unsubscribe := r.Subscribe()
	defer unsubscribe()

	if err := r.Send(ctx, "echo hi"); err != nil {
		t.Fatalf("send: %v", err)
	}
	got := drainText(t, ch)
	if got != "you said hi" {
		t.Fatalf("reply = %q, want %q", got, "you said hi")
	}

	got2, err := data.Session(ctx, sess.ID)
	if err != nil {
		t.Fatalf("load session: %v", err)
	}
	if got2.Activity != contracts.SessionActive {
		t.Fatalf("activity = %q, want active", got2.Activity)
	}
	if got2.ProviderRef == "" {
		t.Fatal("provider_ref not persisted")
	}
}

func TestTranscript(t *testing.T) {
	ctx := context.Background()
	base := datatest.Start(t)
	sess := newSession(t, base)
	data := dataclient.New(base, sess.ID)

	reg := provider.NewRegistry()
	if err := reg.Register(fake.New()); err != nil {
		t.Fatalf("register fake: %v", err)
	}

	r := New(sess.ID, data, reg)
	if err := r.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = r.Shutdown(ctx) })

	ch, unsubscribe := r.Subscribe()
	defer unsubscribe()

	if err := r.Send(ctx, "echo hi"); err != nil {
		t.Fatalf("send: %v", err)
	}
	drainText(t, ch)

	msgs := r.Transcript(ctx)
	if len(msgs) != 2 {
		t.Fatalf("len(msgs) = %d, want 2: %+v", len(msgs), msgs)
	}
	if msgs[0].Role != "user" || msgs[0].Text != "echo hi" {
		t.Errorf("msgs[0] = %+v, want {role: user, text: echo hi}", msgs[0])
	}
	if msgs[1].Role != "agent" || msgs[1].Text != "you said hi" {
		t.Errorf("msgs[1] = %+v, want {role: agent, text: you said hi}", msgs[1])
	}
}

func TestBounceAndResume(t *testing.T) {
	ctx := context.Background()
	base := datatest.Start(t)
	sess := newSession(t, base)
	data := dataclient.New(base, sess.ID)

	// One shared *fake.Fake across two registries/runners: its history is
	// keyed by ref in memory, so this is what actually proves continuity
	// across a bounce rather than just re-reading persisted state.
	shared := fake.New()

	reg1 := provider.NewRegistry()
	if err := reg1.Register(shared); err != nil {
		t.Fatalf("register fake: %v", err)
	}
	r1 := New(sess.ID, data, reg1)
	if err := r1.Start(ctx); err != nil {
		t.Fatalf("start r1: %v", err)
	}
	ch1, unsub1 := r1.Subscribe()
	if err := r1.Send(ctx, "echo hi"); err != nil {
		t.Fatalf("send r1: %v", err)
	}
	drainText(t, ch1)
	unsub1()
	if err := r1.Shutdown(ctx); err != nil {
		t.Fatalf("shutdown r1: %v", err)
	}

	reg2 := provider.NewRegistry()
	if err := reg2.Register(shared); err != nil {
		t.Fatalf("register fake: %v", err)
	}
	r2 := New(sess.ID, data, reg2)
	if err := r2.Start(ctx); err != nil {
		t.Fatalf("start r2: %v", err)
	}
	t.Cleanup(func() { _ = r2.Shutdown(ctx) })
	ch2, unsub2 := r2.Subscribe()
	defer unsub2()

	if err := r2.Send(ctx, "recall"); err != nil {
		t.Fatalf("send r2: %v", err)
	}
	got := drainText(t, ch2)
	if got != "earlier you said: hi" {
		t.Fatalf("reply = %q, want %q", got, "earlier you said: hi")
	}
}

// waitForTranscriptLen polls Transcript until it has at least n messages.
// Needed for turns taken while the terminal transport is active: pumpPTY
// deliberately doesn't rebroadcast turn_end to the hub (providers/fake
// mirrors every chunk onto both Events and Terminal; rebroadcasting would
// double the text a hub subscriber sees), so drainText's hub-subscription
// wait doesn't apply there — Transcript, read straight from the provider's
// history, is the transport-agnostic way to observe a turn's completion.
func waitForTranscriptLen(t *testing.T, ctx context.Context, r *Runner, n int) []contracts.TranscriptMessage {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		msgs := r.Transcript(ctx)
		if len(msgs) >= n {
			return msgs
		}
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for transcript length %d, got %d: %+v", n, len(msgs), msgs)
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// TestSwitchTransportPreservesRef is the acceptance test for the core
// design: chat, terminal, and a subsequent chat turn all land on the same
// provider ref and appear in the shared transcript in order.
func TestSwitchTransportPreservesRef(t *testing.T) {
	ctx := context.Background()
	base := datatest.Start(t)
	sess := newSession(t, base)
	data := dataclient.New(base, sess.ID)

	reg := provider.NewRegistry()
	if err := reg.Register(fake.New()); err != nil {
		t.Fatalf("register fake: %v", err)
	}

	r := New(sess.ID, data, reg)
	if err := r.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = r.Shutdown(ctx) })

	// Each phase subscribes fresh: pumpPTY's terminal-byte relay broadcasts
	// to the hub too (so a live terminal view can render it), and those
	// chunks would otherwise sit unread in an earlier subscription's buffer
	// and corrupt a later drainText's accumulated text.
	ch1, unsub1 := r.Subscribe()
	if err := r.Send(ctx, "echo hi"); err != nil {
		t.Fatalf("send: %v", err)
	}
	if got := drainText(t, ch1); got != "you said hi" {
		t.Fatalf("reply = %q, want %q", got, "you said hi")
	}
	unsub1()

	ref1, err := r.SwitchTransport(ctx, provider.ModePTY)
	if err != nil {
		t.Fatalf("switch to terminal: %v", err)
	}

	// The fake's Send doesn't gate on mode — this is fake-specific
	// looseness, not something a real PTY transport would allow (real
	// terminal input goes through TerminalWrite, never Send). Completion is
	// observed via Transcript, not the hub: pumpPTY doesn't rebroadcast
	// turn_end (see waitForTranscriptLen's doc).
	if err := r.Send(ctx, "echo again"); err != nil {
		t.Fatalf("send in terminal mode: %v", err)
	}
	waitForTranscriptLen(t, ctx, r, 4)

	ref2, err := r.SwitchTransport(ctx, provider.ModeACP)
	if err != nil {
		t.Fatalf("switch to chat: %v", err)
	}
	if ref2 != ref1 {
		t.Fatalf("ref changed across switches: terminal=%q chat=%q, want equal", ref1, ref2)
	}

	ch2, unsub2 := r.Subscribe()
	defer unsub2()
	if err := r.Send(ctx, "echo once more"); err != nil {
		t.Fatalf("send after switching back: %v", err)
	}
	if got := drainText(t, ch2); got != "you said once more" {
		t.Fatalf("reply = %q, want %q", got, "you said once more")
	}

	msgs := r.Transcript(ctx)
	var texts []string
	for _, m := range msgs {
		texts = append(texts, m.Role+":"+m.Text)
	}
	want := []string{
		"user:echo hi", "agent:you said hi",
		"user:echo again", "agent:you said again",
		"user:echo once more", "agent:you said once more",
	}
	if len(texts) != len(want) {
		t.Fatalf("transcript = %v, want %v", texts, want)
	}
	for i := range want {
		if texts[i] != want[i] {
			t.Fatalf("transcript[%d] = %q, want %q (full: %v)", i, texts[i], want[i], texts)
		}
	}
}

// TestSwitchTransportRefusesMidTurn enforces single-writer discipline: a
// switch mid-turn is refused, and interrupting first is the documented
// escape hatch.
func TestSwitchTransportRefusesMidTurn(t *testing.T) {
	ctx := context.Background()
	base := datatest.Start(t)
	sess := newSession(t, base)
	data := dataclient.New(base, sess.ID)

	reg := provider.NewRegistry()
	if err := reg.Register(fake.New()); err != nil {
		t.Fatalf("register fake: %v", err)
	}

	r := New(sess.ID, data, reg)
	if err := r.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = r.Shutdown(ctx) })

	ch, unsubscribe := r.Subscribe()
	defer unsubscribe()

	if err := r.Send(ctx, "slow"); err != nil {
		t.Fatalf("send: %v", err)
	}
	if err := r.Send(ctx, "echo hi"); err != ErrTurnInFlight {
		t.Fatalf("second send mid-turn: err = %v, want ErrTurnInFlight", err)
	}

	if _, err := r.SwitchTransport(ctx, provider.ModePTY); err != ErrSwitchBusy {
		t.Fatalf("switch mid-turn: err = %v, want ErrSwitchBusy", err)
	}

	if err := r.Interrupt(ctx); err != nil {
		t.Fatalf("interrupt: %v", err)
	}
	// Drain until the interrupted turn_end so busy clears.
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				t.Fatal("event channel closed before turn_end")
			}
			if ev.Kind == provider.EventTurnEnd {
				goto drained
			}
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for turn_end after interrupt")
		}
	}
drained:

	if _, err := r.SwitchTransport(ctx, provider.ModePTY); err != nil {
		t.Fatalf("switch after interrupt: %v", err)
	}
}

// TestTrustReachesProviderLaunch is the session-child-level proof that
// sess.Trust actually reaches the provider's StartOptions: a read_only
// session's attempted file write must refuse (EventError), exercising the
// full wiring path (sess.Trust -> cast -> StartOptions.Trust -> fake's
// toolAllowed) rather than unit-testing the cast in isolation.
func TestTrustReachesProviderLaunch(t *testing.T) {
	ctx := context.Background()
	base := datatest.Start(t)
	bootstrap := dataclient.New(base, "test-bootstrap")
	sess := contracts.Session{Name: "test", Provider: "fake", Trust: contracts.TrustReadOnly}
	if err := bootstrap.CreateSession(ctx, &sess); err != nil {
		t.Fatalf("create session: %v", err)
	}
	data := dataclient.New(base, sess.ID)

	reg := provider.NewRegistry()
	if err := reg.Register(fake.New()); err != nil {
		t.Fatalf("register fake: %v", err)
	}

	r := New(sess.ID, data, reg)
	if err := r.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = r.Shutdown(ctx) })

	ch, unsubscribe := r.Subscribe()
	defer unsubscribe()

	if err := r.Send(ctx, "attempt-write"); err != nil {
		t.Fatalf("send: %v", err)
	}

	var sawError bool
	deadline := time.After(5 * time.Second)
loop:
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				t.Fatal("event channel closed before turn_end")
			}
			switch ev.Kind {
			case provider.EventError:
				sawError = true
			case provider.EventToolUse:
				t.Fatalf("tool use event observed under read_only trust: %+v", ev)
			case provider.EventTurnEnd:
				break loop
			}
		case <-deadline:
			t.Fatal("timed out waiting for turn_end")
		}
	}
	if !sawError {
		t.Fatal("want a refusal (EventError) for a read_only session's attempted write, got none")
	}
}

func TestShutdownReleasesLeases(t *testing.T) {
	ctx := context.Background()
	base := datatest.Start(t)
	sess := newSession(t, base)
	data := dataclient.New(base, sess.ID)
	other := dataclient.New(base, "someone-else")

	task := contracts.Task{SessionID: sess.ID, Title: "A", Source: "user"}
	if err := data.CreateTask(ctx, &task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	reg := provider.NewRegistry()
	if err := reg.Register(fake.New()); err != nil {
		t.Fatalf("register fake: %v", err)
	}

	if err := data.Claim(ctx, task.ID, time.Minute); err != nil {
		t.Fatalf("claim: %v", err)
	}

	r := New(sess.ID, data, reg)
	if err := r.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := r.Shutdown(ctx); err != nil {
		t.Fatalf("shutdown: %v", err)
	}

	got, err := data.Session(ctx, sess.ID)
	if err != nil {
		t.Fatalf("load session: %v", err)
	}
	if got.Activity != contracts.SessionInactive {
		t.Fatalf("activity = %q, want inactive", got.Activity)
	}

	if err := other.Claim(ctx, task.ID, time.Minute); err != nil {
		t.Fatalf("claim after release should succeed, got: %v", err)
	}
}

// TestInjectQueuesBehindInFlightTurn proves decision 1
// (docs/LINKED-SESSIONS-DESIGN.md): an Inject that arrives mid-turn never
// interrupts it, it queues — and is delivered only once the in-flight turn
// ends, in order.
func TestInjectQueuesBehindInFlightTurn(t *testing.T) {
	ctx := context.Background()
	base := datatest.Start(t)
	sess := newSession(t, base)
	data := dataclient.New(base, sess.ID)

	reg := provider.NewRegistry()
	if err := reg.Register(fake.New()); err != nil {
		t.Fatalf("register fake: %v", err)
	}

	r := New(sess.ID, data, reg)
	if err := r.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = r.Shutdown(ctx) })

	ch, unsubscribe := r.Subscribe()
	defer unsubscribe()

	if err := r.Send(ctx, "slow"); err != nil {
		t.Fatalf("send: %v", err)
	}

	attribution := contracts.Attribution{LinkID: "lnk_x", SourceName: "leader"}
	if err := r.Inject(ctx, "echo queued", attribution); err != nil {
		t.Fatalf("inject mid-turn: %v", err)
	}

	if err := r.Interrupt(ctx); err != nil {
		t.Fatalf("interrupt: %v", err)
	}

	// Drain the interrupted "slow" turn's own turn_end.
	deadline := time.After(5 * time.Second)
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				t.Fatal("event channel closed before first turn_end")
			}
			if ev.Kind == provider.EventTurnEnd {
				if !ev.Interrupted {
					t.Fatalf("first turn_end: interrupted = %v, want true", ev.Interrupted)
				}
				goto firstDrained
			}
		case <-deadline:
			t.Fatal("timed out waiting for interrupted turn_end")
		}
	}
firstDrained:

	// The queued injection's own turn should now run automatically.
	got := drainText(t, ch)
	if got != "you said queued" {
		t.Fatalf("queued injection reply = %q, want %q", got, "you said queued")
	}
}

// TestInjectAttributionReachesTranscript proves decision 2
// (docs/LINKED-SESSIONS-DESIGN.md): an injected message's Attribution
// survives into the Transcript, and ordinary user turns carry none.
func TestInjectAttributionReachesTranscript(t *testing.T) {
	ctx := context.Background()
	base := datatest.Start(t)
	sess := newSession(t, base)
	data := dataclient.New(base, sess.ID)

	reg := provider.NewRegistry()
	if err := reg.Register(fake.New()); err != nil {
		t.Fatalf("register fake: %v", err)
	}

	r := New(sess.ID, data, reg)
	if err := r.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = r.Shutdown(ctx) })

	ch, unsubscribe := r.Subscribe()
	defer unsubscribe()

	if err := r.Send(ctx, "slow"); err != nil {
		t.Fatalf("send: %v", err)
	}

	attribution := contracts.Attribution{LinkID: "lnk_x", SourceName: "leader"}
	if err := r.Inject(ctx, "echo queued", attribution); err != nil {
		t.Fatalf("inject mid-turn: %v", err)
	}
	if err := r.Interrupt(ctx); err != nil {
		t.Fatalf("interrupt: %v", err)
	}

	// Drain until the queued injection's turn_end lands.
	var turnEnds int
	deadline := time.After(5 * time.Second)
	for turnEnds < 2 {
		select {
		case ev, ok := <-ch:
			if !ok {
				t.Fatal("event channel closed before second turn_end")
			}
			if ev.Kind == provider.EventTurnEnd {
				turnEnds++
			}
		case <-deadline:
			t.Fatal("timed out waiting for queued injection's turn_end")
		}
	}

	msgs := r.Transcript(ctx)
	var found bool
	for _, m := range msgs {
		if m.Role != "user" {
			continue
		}
		if m.Text == "echo queued" {
			found = true
			if m.Attribution == nil {
				t.Fatalf("injected message %+v: attribution = nil, want set", m)
			}
			if m.Attribution.LinkID != attribution.LinkID || m.Attribution.SourceName != attribution.SourceName {
				t.Fatalf("attribution = %+v, want %+v", m.Attribution, attribution)
			}
			continue
		}
		if m.Attribution != nil {
			t.Fatalf("ordinary user message %+v: attribution = %+v, want nil", m, m.Attribution)
		}
	}
	if !found {
		t.Fatalf("transcript %+v: no message with text %q", msgs, "echo queued")
	}
}

// TestInjectRespectsTrustCapping proves decision 3
// (docs/LINKED-SESSIONS-DESIGN.md): what a subject agent may do in response
// to an injected message is bounded by the subject session's own trust
// level, exactly as for a user turn — a read_only session refuses an
// attempted file write asked for via injection.
func TestInjectRespectsTrustCapping(t *testing.T) {
	ctx := context.Background()
	base := datatest.Start(t)
	bootstrap := dataclient.New(base, "test-bootstrap")
	sess := contracts.Session{Name: "test", Provider: "fake", Trust: contracts.TrustReadOnly}
	if err := bootstrap.CreateSession(ctx, &sess); err != nil {
		t.Fatalf("create session: %v", err)
	}
	data := dataclient.New(base, sess.ID)

	reg := provider.NewRegistry()
	if err := reg.Register(fake.New()); err != nil {
		t.Fatalf("register fake: %v", err)
	}

	r := New(sess.ID, data, reg)
	if err := r.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = r.Shutdown(ctx) })

	ch, unsubscribe := r.Subscribe()
	defer unsubscribe()

	attribution := contracts.Attribution{LinkID: "lnk_x", SourceName: "leader"}
	if err := r.Inject(ctx, "attempt-write", attribution); err != nil {
		t.Fatalf("inject: %v", err)
	}

	var sawError bool
	deadline := time.After(5 * time.Second)
loop:
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				t.Fatal("event channel closed before turn_end")
			}
			switch ev.Kind {
			case provider.EventError:
				sawError = true
			case provider.EventToolUse:
				t.Fatalf("tool use event observed under read_only trust for an injected turn: %+v", ev)
			case provider.EventTurnEnd:
				break loop
			}
		case <-deadline:
			t.Fatal("timed out waiting for turn_end")
		}
	}
	if !sawError {
		t.Fatal("want a refusal (EventError) for a read_only session's attempted write via injection, got none")
	}
}
