package job_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/pkg/registryclient"
	"github.com/drewkidwell/mads/providers/fastmodel"
	"github.com/drewkidwell/mads/services/data/datatest"
	"github.com/drewkidwell/mads/services/router/internal/job"
)

// Delivery to sleeping sessions (docs/LINKED-SESSIONS-DESIGN.md, 2026-10-20).

type deferFixture struct {
	base    string
	data    *dataclient.Client
	watched contracts.Session
	monitor contracts.Session
	link    contracts.Link
}

func newDeferFixture(t *testing.T, cadence contracts.LinkCadence) deferFixture {
	t.Helper()
	base := datatest.Start(t)
	data := dataclient.New(base, "router")
	enableLinkedSessions(t, data)
	f := deferFixture{base: base, data: data}
	f.watched = mustCreateSession(t, data, contracts.Session{Name: "watched session"})
	f.monitor = mustCreateSession(t, data, contracts.Session{Name: "sleeping monitor"})
	f.link = mustCreateLink(t, data, contracts.LinkRequest{
		GrantingSessionID: f.monitor.ID, SubjectSessionID: f.watched.ID,
		Grants: []contracts.Grant{contracts.GrantRead}, Cadence: cadence,
	})
	return f
}

func (f deferFixture) runner(backendReply string) *job.Runner {
	return job.New(f.data, fastmodel.NewWithBackend(&countingBackend{reply: backendReply}, fastmodel.Config{}), registryclient.New(""))
}

func runDigest(t *testing.T, f deferFixture, runner *job.Runner) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if _, err := runner.Spawn(ctx, job.DigestKind, f.watched.ID,
		job.GenerateDigest(f.watched.ID, []string{"user: hi\nassistant: did stuff\n"})); err != nil {
		t.Fatalf("Spawn digest: %v", err)
	}
	eventually(t, 5*time.Second, func() bool {
		evs, err := dataclient.New(f.base, "test-verify").Events(ctx, 0, f.watched.ID, []string{"job.completed"})
		return err == nil && len(evs) > 0
	})
}

func TestPerTurnDigestToSleepingMonitorDefersWithoutWaking(t *testing.T) {
	f := newDeferFixture(t, contracts.CadencePerTurn)
	runner := f.runner("compressed digest")
	waker := &stubWaker{addrs: map[string]string{}}
	runner.SetResolver(stubResolver{}) // monitor asleep
	runner.SetWaker(waker)

	runDigest(t, f, runner)

	if woken := waker.wokenIDs(); len(woken) != 0 {
		t.Fatalf("a digest woke %v; digests must never wake", woken)
	}
	ctx := context.Background()
	counts, err := f.data.DeferredLinkCounts(ctx)
	if err != nil || counts[f.monitor.ID] != 1 {
		t.Fatalf("deferred counts = %v, %v; want 1 waiting for the monitor", counts, err)
	}
	entries, err := f.data.DrainDeferredLinkDigests(ctx, f.link.ID)
	if err != nil || len(entries) != 1 {
		t.Fatalf("drain deferred = %v, %v", entries, err)
	}
	e := entries[0]
	if !e.Deferred || !e.Noop || e.Text != "compressed digest" {
		t.Errorf("entry = %+v, want deferred noop-able digest text", e)
	}
	if e.Attribution == nil || e.Attribution.LinkID != f.link.ID || e.Attribution.SourceName != f.watched.Name || !e.Attribution.DigestAttributed {
		t.Errorf("attribution = %+v, want the digest push's, stored whole", e.Attribution)
	}
}

func TestActionSignalWakesSleepingLeaderAndDoneDefers(t *testing.T) {
	f := newDeferFixture(t, contracts.CadenceOnSignal)
	server := newCaptureServer(t, http.StatusAccepted)
	waker := &stubWaker{addrs: map[string]string{f.monitor.ID: server.addr()}}
	runner := f.runner("x")
	runner.SetResolver(stubResolver{})
	runner.SetWaker(waker)

	task := seedBlockedTask(t, f.data, f.watched.ID, "wire it", "no creds")
	runSignal(t, f.base, runner, f.watched.ID, job.Signal{
		Kind: job.SignalTaskTerminal, TaskID: task.ID, ToStatus: string(contracts.StatusBlocked),
	})
	eventually(t, 5*time.Second, func() bool { return server.count() == 1 })
	if woken := waker.wokenIDs(); len(woken) != 1 || woken[0] != f.monitor.ID {
		t.Fatalf("woken = %v, want the sleeping non-implicit leader", woken)
	}

	// Done is informational: stored, no wake, no delivery.
	done := seedBlockedTask(t, f.data, f.watched.ID, "finish it", "n/a")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if _, err := runner.Spawn(ctx, job.SignalKind, f.watched.ID,
		job.DeliverSignal(f.watched.ID, job.Signal{Kind: job.SignalTaskTerminal, TaskID: done.ID, ToStatus: string(contracts.StatusDone)})); err != nil {
		t.Fatal(err)
	}
	eventually(t, 5*time.Second, func() bool {
		c, err := f.data.DeferredLinkCounts(ctx)
		return err == nil && c[f.monitor.ID] == 1
	})
	if woken := waker.wokenIDs(); len(woken) != 1 {
		t.Errorf("done signal woke the leader: woken = %v", woken)
	}
	if server.count() != 1 {
		t.Errorf("done signal delivered to a sleeping leader (%d deliveries)", server.count())
	}
	entries, _ := f.data.DrainDeferredLinkDigests(ctx, f.link.ID)
	if len(entries) != 1 || !strings.Contains(entries[0].Text, "is now done") || !entries[0].Noop {
		t.Errorf("deferred done entry = %+v", entries)
	}
}

// on_signal backlog keeps waiting for check_mailbox / the next signal wake:
// a start-time flush leaves it alone, and a signal wake leaves deferred
// entries for the flush.
func TestFlushLeavesOnSignalBacklogAndWakeLeavesDeferred(t *testing.T) {
	f := newDeferFixture(t, contracts.CadenceOnSignal)
	ctx := context.Background()
	if err := f.data.AppendLinkDigest(ctx, f.link.ID, contracts.LinkDigest{Text: "backlog digest"}); err != nil {
		t.Fatal(err)
	}
	attr := contracts.Attribution{LinkID: f.link.ID, SourceName: "watched session", DigestAttributed: true}
	if err := f.data.AppendLinkDigest(ctx, f.link.ID, contracts.LinkDigest{Text: "deferred one", Deferred: true, Noop: true, Attribution: &attr}); err != nil {
		t.Fatal(err)
	}

	server := newCaptureServer(t, http.StatusAccepted)
	runner := f.runner("x")
	runner.SetResolver(stubResolver{f.monitor.ID: server.addr()})
	if err := runner.Run(ctx, func(ctx context.Context, deps job.Deps) error {
		return job.FlushDeferred(ctx, deps, f.monitor.ID)
	}); err != nil {
		t.Fatal(err)
	}
	reqs := server.requests()
	if len(reqs) != 1 || !strings.HasPrefix(reqs[0].Text, "deferred one") || strings.Contains(reqs[0].Text, "backlog digest") {
		t.Fatalf("flush delivered %+v, want only the deferred entry", reqs)
	}
	backlog, err := f.data.DrainLinkDigests(ctx, f.link.ID)
	if err != nil || len(backlog) != 1 || backlog[0].Text != "backlog digest" {
		t.Fatalf("on_signal backlog after flush = %v, %v; want it intact", backlog, err)
	}

	// And the reverse: a signal wake drains the backlog but not deferred.
	_ = f.data.AppendLinkDigest(ctx, f.link.ID, contracts.LinkDigest{Text: "backlog 2"})
	_ = f.data.AppendLinkDigest(ctx, f.link.ID, contracts.LinkDigest{Text: "deferred two", Deferred: true, Attribution: &attr})
	runSignal(t, f.base, runner, f.watched.ID, job.Signal{Kind: job.SignalCrash, Reason: "boom"})
	eventually(t, 5*time.Second, func() bool { return server.count() == 2 })
	if text := server.requests()[1].Text; !strings.Contains(text, "backlog 2") || strings.Contains(text, "deferred two") {
		t.Errorf("signal wake text = %q, want backlog but not the deferred entry", text)
	}
	left, _ := f.data.DrainDeferredLinkDigests(ctx, f.link.ID)
	if len(left) != 1 || left[0].Text != "deferred two" {
		t.Errorf("deferred after signal wake = %v, want deferred two intact", left)
	}
}

func TestFlushDeliversInOrderOnceWithAttribution(t *testing.T) {
	f := newDeferFixture(t, contracts.CadencePerTurn)
	ctx := context.Background()
	base := time.Now().UTC().Add(-time.Hour)
	mk := func(text string, i int, noop bool, source string) {
		a := contracts.Attribution{LinkID: f.link.ID, SourceName: source, DigestAttributed: true}
		if err := f.data.AppendLinkDigest(ctx, f.link.ID, contracts.LinkDigest{
			Text: text, Deferred: true, Noop: noop, Attribution: &a, FromTurn: i, ToTurn: i,
			CreatedAt: base.Add(time.Duration(i) * time.Minute),
		}); err != nil {
			t.Fatal(err)
		}
	}
	mk("first", 1, true, "watched session")
	mk("second", 2, true, "watched session")

	server := newCaptureServer(t, http.StatusAccepted)
	runner := f.runner("x")
	runner.SetResolver(stubResolver{f.monitor.ID: server.addr()})
	flush := func(ctx context.Context, deps job.Deps) error { return job.FlushDeferred(ctx, deps, f.monitor.ID) }

	// Concurrent wakes: exactly one flush delivers.
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _ = runner.Run(ctx, flush) }()
	}
	wg.Wait()

	reqs := server.requests()
	if len(reqs) != 1 {
		t.Fatalf("%d flush deliveries under concurrent wakes, want exactly 1", len(reqs))
	}
	text := reqs[0].Text
	if i, j := strings.Index(text, "first"), strings.Index(text, "second"); i < 0 || j < i {
		t.Errorf("text %q: want first before second", text)
	}
	if !strings.HasSuffix(text, job.NoopInstruction) {
		t.Errorf("text %q: every entry is NOOP-able, want the NOOP line", text)
	}
	if reqs[0].Attribution.LinkID != f.link.ID || reqs[0].Attribution.SourceName != "watched session" || !reqs[0].Attribution.DigestAttributed {
		t.Errorf("attribution = %+v", reqs[0].Attribution)
	}
	if c, _ := f.data.DeferredLinkCounts(ctx); c[f.monitor.ID] != 0 {
		t.Errorf("waiting count after flush = %d, want 0", c[f.monitor.ID])
	}

	// A single non-NOOP-able entry replays verbatim, no NOOP line.
	mk("act on this", 3, false, "watched session")
	if err := runner.Run(ctx, flush); err != nil {
		t.Fatal(err)
	}
	reqs = server.requests()
	if len(reqs) != 2 || reqs[1].Text != "act on this" {
		t.Errorf("single replay = %+v, want the push text unchanged", reqs)
	}
}

// A failed replay (child answers 500) keeps the entries for the next start.
func TestFlushRequeuesOnFailure(t *testing.T) {
	f := newDeferFixture(t, contracts.CadencePerTurn)
	ctx := context.Background()
	a := contracts.Attribution{LinkID: f.link.ID, SourceName: "s", DigestAttributed: true}
	if err := f.data.AppendLinkDigest(ctx, f.link.ID, contracts.LinkDigest{Text: "keep me", Deferred: true, Noop: true, Attribution: &a}); err != nil {
		t.Fatal(err)
	}
	server := newCaptureServer(t, http.StatusInternalServerError)
	runner := f.runner("x")
	runner.SetResolver(stubResolver{f.monitor.ID: server.addr()})
	if err := runner.Run(ctx, func(ctx context.Context, deps job.Deps) error {
		return job.FlushDeferred(ctx, deps, f.monitor.ID)
	}); err == nil {
		t.Fatal("want an error from a failed replay")
	}
	left, _ := f.data.DrainDeferredLinkDigests(ctx, f.link.ID)
	if len(left) != 1 || left[0].Text != "keep me" || left[0].Attribution == nil {
		t.Errorf("after failed replay = %+v, want the entry kept whole", left)
	}
}

// A just-woken child answers 503 until its runner is ready; PostInject rides
// it out.
func TestPostInjectRetriesThrough503(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body contracts.InjectRequest
		_ = json.NewDecoder(r.Body).Decode(&body)
		if calls.Add(1) < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()
	status, _, err := job.PostInject(context.Background(), strings.TrimPrefix(srv.URL, "http://"),
		contracts.InjectRequest{Text: "hi"})
	if err != nil || status != http.StatusAccepted || calls.Load() != 3 {
		t.Fatalf("status %d err %v after %d calls, want 202 on the third", status, err, calls.Load())
	}
}
