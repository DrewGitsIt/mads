package runner

import (
	"context"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/providers/fake"
	"github.com/drewkidwell/mads/providers/provider"
	"github.com/drewkidwell/mads/services/data/datatest"
)

// seedNative stands in for a conversation that exists outside mads (an
// import): it runs one turn on its own runner so f holds a real history
// under a ref, then returns that ref. The session record it used is
// discarded — what the tests care about is the provider-side original.
func seedNative(t *testing.T, base string, f *fake.Fake) string {
	t.Helper()
	ctx := context.Background()
	seedSess := newSession(t, base)
	r := New(seedSess.ID, dataclient.New(base, seedSess.ID), registryOf(t, f))
	if err := r.Start(ctx); err != nil {
		t.Fatalf("start seed: %v", err)
	}
	ch, unsubscribe := r.Subscribe()
	if err := r.Send(ctx, "echo native"); err != nil {
		t.Fatalf("send seed: %v", err)
	}
	drainText(t, ch)
	unsubscribe()
	r.mu.Lock()
	ref := r.lastRef
	r.mu.Unlock()
	if ref == "" {
		// A never-announcing scenario: the runner can't learn the ref, but
		// the fake allocates them sequentially and this was its first.
		ref = "fake-1"
	}
	if err := r.Shutdown(ctx); err != nil {
		t.Fatalf("shutdown seed: %v", err)
	}
	return ref
}

func registryOf(t *testing.T, f *fake.Fake) *provider.Registry {
	t.Helper()
	reg := provider.NewRegistry()
	if err := reg.Register(f); err != nil {
		t.Fatalf("register fake: %v", err)
	}
	return reg
}

// newImportedSession creates the record an import would: ProviderRef is the
// native conversation, ForkOnResume says never to append to it.
func newImportedSession(t *testing.T, base, ref string) contracts.Session {
	t.Helper()
	sess := contracts.Session{Name: "imported", Provider: "fake", ProviderRef: ref, ForkOnResume: true}
	if err := dataclient.New(base, "test-bootstrap").CreateSession(context.Background(), &sess); err != nil {
		t.Fatalf("create imported session: %v", err)
	}
	return sess
}

func waitForRecord(t *testing.T, data *dataclient.Client, id string, ok func(contracts.Session) bool) contracts.Session {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		got, err := data.Session(context.Background(), id)
		if err != nil {
			t.Fatalf("load session: %v", err)
		}
		if ok(got) {
			return got
		}
		if time.Now().After(deadline) {
			t.Fatalf("session record never reached the expected state: %+v", got)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func nativeHistory(t *testing.T, f *fake.Fake, ref string) []provider.Message {
	t.Helper()
	msgs, err := f.Transcript(context.Background(), ref)
	if err != nil {
		t.Fatalf("transcript %s: %v", ref, err)
	}
	return msgs
}

// TestForkOnResumeLifecycle is the acceptance test for fork_on_resume: the
// first wake forks (StartOptions.Fork), the new ref and the cleared flag land
// together, the original is never written, and a later wake resumes the fork
// in place without forking again.
func TestForkOnResumeLifecycle(t *testing.T) {
	ctx := context.Background()
	base := datatest.Start(t)
	f := fake.New()
	orig := seedNative(t, base, f)
	before := nativeHistory(t, f, orig)
	sess := newImportedSession(t, base, orig)
	data := dataclient.New(base, sess.ID)

	r := New(sess.ID, data, registryOf(t, f))
	if err := r.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	calls := f.StartCalls()
	if last := calls[len(calls)-1]; last.Resume != orig || !last.Fork {
		t.Fatalf("first wake Start = {Resume:%q Fork:%v}, want {Resume:%q Fork:true}", last.Resume, last.Fork, orig)
	}
	// Before the fork exists the record still points at the original, flag
	// set: that is what a crash here would leave behind.
	got, err := data.Session(ctx, sess.ID)
	if err != nil {
		t.Fatalf("load session: %v", err)
	}
	if got.ProviderRef != orig || !got.ForkOnResume {
		t.Fatalf("after Start record = {ref:%q fork:%v}, want {ref:%q fork:true}", got.ProviderRef, got.ForkOnResume, orig)
	}
	if turns := r.Transcript(ctx); len(turns) == 0 {
		t.Fatal("transcript is blank before the fork exists, want the original's history")
	}

	ch, unsubscribe := r.Subscribe()
	if err := r.Send(ctx, "echo forked"); err != nil {
		t.Fatalf("send: %v", err)
	}
	drainText(t, ch)
	unsubscribe()

	got = waitForRecord(t, data, sess.ID, func(s contracts.Session) bool { return !s.ForkOnResume })
	if got.ProviderRef == "" || got.ProviderRef == orig {
		t.Fatalf("provider_ref = %q after the first turn, want a new ref distinct from %q", got.ProviderRef, orig)
	}
	forkRef := got.ProviderRef
	if after := nativeHistory(t, f, orig); len(after) != len(before) {
		t.Fatalf("original history grew from %d to %d messages; the native conversation must never be appended to", len(before), len(after))
	}
	if forked := nativeHistory(t, f, forkRef); len(forked) <= len(before) {
		t.Fatalf("fork history has %d messages, want the original's %d plus the new turn", len(forked), len(before))
	}
	if err := r.Shutdown(ctx); err != nil {
		t.Fatalf("shutdown: %v", err)
	}

	// Later wake: resume the fork, no fork flag.
	r2 := New(sess.ID, data, registryOf(t, f))
	if err := r2.Start(ctx); err != nil {
		t.Fatalf("restart: %v", err)
	}
	t.Cleanup(func() { _ = r2.Shutdown(ctx) })
	calls = f.StartCalls()
	if last := calls[len(calls)-1]; last.Resume != forkRef || last.Fork {
		t.Fatalf("second wake Start = {Resume:%q Fork:%v}, want {Resume:%q Fork:false}", last.Resume, last.Fork, forkRef)
	}
	got, err = data.Session(ctx, sess.ID)
	if err != nil {
		t.Fatalf("load session: %v", err)
	}
	if got.ProviderRef != forkRef || got.ForkOnResume {
		t.Fatalf("after second wake record = {ref:%q fork:%v}, want {ref:%q fork:false}", got.ProviderRef, got.ForkOnResume, forkRef)
	}
}

// TestForkOnResumeCrashBeforePersistReforks covers a first wake that dies
// before any new ref is persisted (the fake never announces one): the record
// must still name the untouched original with the flag set, so the next wake
// forks again instead of appending to — or losing — the original.
func TestForkOnResumeCrashBeforePersistReforks(t *testing.T) {
	ctx := context.Background()
	base := datatest.Start(t)
	f, err := fake.NewWithScenario(fake.Scenario{Name: "never", RefAnnounce: fake.RefAnnounceNever})
	if err != nil {
		t.Fatalf("scenario: %v", err)
	}
	orig := seedNative(t, base, f)
	before := nativeHistory(t, f, orig)
	sess := newImportedSession(t, base, orig)
	data := dataclient.New(base, sess.ID)

	r := New(sess.ID, data, registryOf(t, f))
	if err := r.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	ch, unsubscribe := r.Subscribe()
	if err := r.Send(ctx, "echo lost"); err != nil {
		t.Fatalf("send: %v", err)
	}
	drainText(t, ch)
	unsubscribe()
	if err := r.Shutdown(ctx); err != nil {
		t.Fatalf("shutdown: %v", err)
	}

	got, err := data.Session(ctx, sess.ID)
	if err != nil {
		t.Fatalf("load session: %v", err)
	}
	if got.ProviderRef != orig || !got.ForkOnResume {
		t.Fatalf("record after a wake that never persisted a new ref = {ref:%q fork:%v}, want {ref:%q fork:true}", got.ProviderRef, got.ForkOnResume, orig)
	}
	if after := nativeHistory(t, f, orig); len(after) != len(before) {
		t.Fatalf("original history grew from %d to %d messages", len(before), len(after))
	}

	r2 := New(sess.ID, data, registryOf(t, f))
	if err := r2.Start(ctx); err != nil {
		t.Fatalf("restart: %v", err)
	}
	t.Cleanup(func() { _ = r2.Shutdown(ctx) })
	calls := f.StartCalls()
	if last := calls[len(calls)-1]; last.Resume != orig || !last.Fork {
		t.Fatalf("re-wake Start = {Resume:%q Fork:%v}, want {Resume:%q Fork:true}", last.Resume, last.Fork, orig)
	}
}

// TestForkOnResumeSwitchTransportBeforeNewRef: a transport switch while the
// fork's ref is still unknown re-forks from the original (nothing has been
// written to a fork yet) rather than starting an unrelated fresh session, and
// once the ref is known a switch is a plain resume of it.
func TestForkOnResumeSwitchTransportBeforeNewRef(t *testing.T) {
	ctx := context.Background()
	base := datatest.Start(t)
	f := fake.New()
	orig := seedNative(t, base, f)
	sess := newImportedSession(t, base, orig)
	data := dataclient.New(base, sess.ID)

	r := New(sess.ID, data, registryOf(t, f))
	if err := r.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = r.Shutdown(ctx) })

	if _, err := r.SwitchTransport(ctx, provider.ModePTY); err != nil {
		t.Fatalf("switch: %v", err)
	}
	calls := f.StartCalls()
	if last := calls[len(calls)-1]; last.Resume != orig || !last.Fork {
		t.Fatalf("switch Start = {Resume:%q Fork:%v}, want a re-fork of %q", last.Resume, last.Fork, orig)
	}
	if got, _ := data.Session(ctx, sess.ID); got.ProviderRef != orig || !got.ForkOnResume {
		t.Fatalf("record after switch = {ref:%q fork:%v}, want the original still pending", got.ProviderRef, got.ForkOnResume)
	}
}
