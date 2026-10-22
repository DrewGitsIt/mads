package runner

import (
	"context"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/pkg/linkwrap"
	"github.com/drewkidwell/mads/providers/fake"
	"github.com/drewkidwell/mads/providers/provider"
	"github.com/drewkidwell/mads/services/data/datatest"
)

// startTerminalRunner starts a runner on the fake provider and switches it
// to terminal mode.
func startTerminalRunner(t *testing.T) (*Runner, *fake.Fake, *dataclient.Client, string) {
	t.Helper()
	ctx := context.Background()
	base := datatest.Start(t)
	sess := newSession(t, base)
	data := dataclient.New(base, sess.ID)
	f := fake.New()
	reg := provider.NewRegistry()
	if err := reg.Register(f); err != nil {
		t.Fatalf("register fake: %v", err)
	}
	r := New(sess.ID, data, reg)
	if err := r.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = r.Shutdown(ctx) })
	if _, err := r.SwitchTransport(ctx, provider.ModePTY); err != nil {
		t.Fatalf("switch to terminal: %v", err)
	}
	return r, f, data, sess.ID
}

// typedUserTexts waits until the fake has recorded want user messages on
// the runner's current session, and returns them.
func typedUserTexts(t *testing.T, r *Runner, f *fake.Fake, want int) []string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var texts []string
		if ref := r.currentSession().Ref(); ref != "" {
			msgs, _ := f.Transcript(context.Background(), ref)
			for _, m := range msgs {
				if m.Role == "user" {
					texts = append(texts, m.Text)
				}
			}
		}
		if len(texts) >= want || time.Now().After(deadline) {
			return texts
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (r *Runner) currentSession() provider.Session {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.sess
}

// In terminal mode nothing ends a turn the runner could see, so messages
// are typed into the TUI rather than marking one in flight: two injections
// and a SendOrQueue sent back to back while the first one's reply is still
// streaming ("slow" streams until interrupted) are all typed, in order,
// none queued, busy never set — and switching back to chat then works.
func TestTerminalModeTypesMessagesWithoutBusy(t *testing.T) {
	ctx := context.Background()
	r, f, data, sessionID := startTerminalRunner(t)
	leader := contracts.Attribution{LinkID: "lnk_1", SourceName: "leader"}

	if err := r.Inject(ctx, "slow", leader); err != nil {
		t.Fatalf("inject: %v", err)
	}
	parts := []contracts.InjectPart{
		{Text: "echo first\nsecond line", Attribution: leader},
		{Text: "echo other", Attribution: contracts.Attribution{LinkID: "lnk_2", SourceName: "peer"}},
	}
	if err := r.InjectParts(ctx, parts); err != nil {
		t.Fatalf("inject parts: %v", err)
	}
	queued, err := r.SendOrQueue(ctx, "echo mine\nand more")
	if err != nil {
		t.Fatalf("send or queue: %v", err)
	}
	if queued != nil {
		t.Fatalf("SendOrQueue queued %+v, want it typed", queued)
	}

	want := []string{
		linkwrap.Wrap("slow", leader),
		linkwrap.Wrap(parts[0].Text, parts[0].Attribution) + "\n\n" + linkwrap.Wrap(parts[1].Text, parts[1].Attribution),
		"echo mine\nand more",
	}
	got := typedUserTexts(t, r, f, len(want))
	if len(got) != len(want) {
		t.Fatalf("typed %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("typed[%d] = %q, want %q", i, got[i], want[i])
		}
	}

	r.mu.Lock()
	busy, pending := r.busy, len(r.pending)
	r.mu.Unlock()
	if busy || pending != 0 {
		t.Fatalf("busy = %v, pending = %d; want false, 0", busy, pending)
	}
	if q := r.QueuedMessages(); len(q) != 0 {
		t.Fatalf("queued = %+v, want none", q)
	}

	// Each injected part is still audited under its own link.
	var actors []string
	envs, err := data.Events(ctx, 0, sessionID, []string{string(contracts.LinkMessageInjected)})
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range envs {
		actors = append(actors, e.Actor)
	}
	if len(actors) != 3 || actors[0] != "link-lnk_1" || actors[1] != "link-lnk_1" || actors[2] != "link-lnk_2" {
		t.Fatalf("audit actors = %q, want [link-lnk_1 link-lnk_1 link-lnk_2]", actors)
	}

	if _, err := r.SwitchTransport(ctx, provider.ModeACP); err != nil {
		t.Fatalf("switch to chat after terminal sends: %v", err)
	}
}

// What queues during a Land in terminal mode is all typed once it ends: no
// turn end will come to deliver the rest one by one (the first, "slow",
// never ends its own).
func TestTerminalModeTypesWhatQueuedDuringLand(t *testing.T) {
	ctx := context.Background()
	r, f, _, _ := startTerminalRunner(t)

	r.mu.Lock()
	r.landing = true
	r.landDone = make(chan struct{})
	r.mu.Unlock()
	if err := r.Send(ctx, "echo refused"); err != ErrTurnInFlight {
		t.Fatalf("Send during Land: err = %v, want ErrTurnInFlight", err)
	}
	for _, text := range []string{"slow", "echo two"} {
		q, err := r.SendOrQueue(ctx, text)
		if err != nil || q == nil {
			t.Fatalf("SendOrQueue(%q) during Land = %+v, %v; want queued", text, q, err)
		}
	}
	if err := r.Inject(ctx, "echo three", contracts.Attribution{SourceName: "your pipeline run"}); err != nil {
		t.Fatalf("inject: %v", err)
	}
	r.endLanding()

	got := typedUserTexts(t, r, f, 3)
	want := []string{"slow", "echo two", linkwrap.Wrap("echo three", contracts.Attribution{SourceName: "your pipeline run"})}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Fatalf("typed %q, want %q", got, want)
	}
	r.mu.Lock()
	busy, pending := r.busy, len(r.pending)
	r.mu.Unlock()
	if busy || pending != 0 {
		t.Fatalf("busy = %v, pending = %d; want false, 0", busy, pending)
	}
}
