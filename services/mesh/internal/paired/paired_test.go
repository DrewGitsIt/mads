package paired

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"log"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/pkg/devicekey"
	"github.com/drewkidwell/mads/pkg/localauth"
	"github.com/drewkidwell/mads/services/data/datatest"
)

func testFingerprint(seed byte) string {
	return string(devicekey.FingerprintOf(ed25519.PublicKey(bytes.Repeat([]byte{seed}, ed25519.PublicKeySize))))
}

// flakyData is a real Data Service (behind its front door) reached through
// a switch: while down, every request fails as an unreachable service would.
type flakyData struct {
	real, url string
	down      atomic.Bool
	// eventsBroken fails only the event log (batches and streams), as a
	// damaged row once did: the list still reads.
	eventsBroken atomic.Bool
	tokenFile    string
	// streams ends every open event stream when cancelled (breakEvents).
	streamsMu sync.Mutex
	streams   context.Context
	cut       context.CancelFunc
}

// breakEvents fails the event log from now on, open streams included.
func (f *flakyData) breakEvents() {
	f.eventsBroken.Store(true)
	f.streamsMu.Lock()
	f.cut()
	f.streamsMu.Unlock()
}

func newFlakyData(t *testing.T) *flakyData {
	t.Helper()
	g := datatest.StartGuarded(t)
	f := &flakyData{real: g.URL, tokenFile: g.TokenFile}
	f.streams, f.cut = context.WithCancel(context.Background())
	t.Cleanup(f.cut)
	target, _ := url.Parse(g.URL)
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.FlushInterval = -1
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if f.down.Load() || (f.eventsBroken.Load() && r.URL.Path == "/events") {
			http.Error(w, "down", http.StatusBadGateway)
			return
		}
		if r.URL.Path == "/events" {
			f.streamsMu.Lock()
			streams := f.streams
			f.streamsMu.Unlock()
			ctx, cancel := context.WithCancel(r.Context())
			defer cancel()
			stop := context.AfterFunc(streams, cancel)
			defer stop()
			r = r.WithContext(ctx)
		}
		proxy.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	f.url = srv.URL
	return f
}

// writer is a client for setting up rows, straight to the Data Service.
func (f *flakyData) writer() *dataclient.Client {
	return dataclient.New(f.real, "user", dataclient.WithTokenSource(localauth.FileSource(f.tokenFile)))
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func run(t *testing.T, d *Directory) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); d.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
}

// The directory loads the paired rows at start and follows changes: a new
// pairing, a grant change and an unpairing all show up without a restart.
// Viewed-only rows (devices this node reads, not ones that may reach it)
// aren't in it.
func TestDirectoryLoadsAndFollows(t *testing.T) {
	f := newFlakyData(t)
	w := f.writer()
	ctx := context.Background()
	no := false
	phone := &contracts.Peer{Name: "phone", Viewed: &no, Fingerprint: testFingerprint(1), Grant: &contracts.PeerGrant{Level: contracts.GrantChat}}
	if err := w.CreatePeer(ctx, phone); err != nil {
		t.Fatal(err)
	}
	if err := w.CreatePeer(ctx, &contracts.Peer{Name: "pop-os", MeshURL: "http://pop-os:17380"}); err != nil {
		t.Fatal(err)
	}

	d := New(dataclient.New(f.url, "mesh", dataclient.WithTokenSource(localauth.FileSource(f.tokenFile))))
	d.minBackoff, d.maxBackoff = 10*time.Millisecond, 50*time.Millisecond
	// The hook the Mesh Service ends revoked streams from: it runs after
	// every reload, and sees the reloaded list.
	var reloads atomic.Int32
	var sawUnpaired atomic.Bool
	d.OnReload(func() {
		reloads.Add(1)
		if _, ok := d.Lookup(testFingerprint(1)); !ok && reloads.Load() > 1 {
			sawUnpaired.Store(true)
		}
	})
	run(t, d)
	eventually(t, "the first load", d.Loaded)
	eventually(t, "OnReload after the first load", func() bool { return reloads.Load() >= 1 })
	if got, ok := d.Lookup(phone.Fingerprint); !ok || got.Name != "phone" || got.Level() != contracts.GrantChat {
		t.Fatalf("Lookup(phone) = %+v, %v", got, ok)
	}
	if d.Len() != 1 {
		t.Fatalf("Len = %d, want 1 (viewed-only rows aren't paired)", d.Len())
	}

	laptop := &contracts.Peer{Name: "laptop", Viewed: &no, Fingerprint: testFingerprint(2)}
	if err := w.CreatePeer(ctx, laptop); err != nil {
		t.Fatal(err)
	}
	eventually(t, "a new pairing", func() bool { _, ok := d.Lookup(laptop.Fingerprint); return ok })

	phone.Grant.Level = contracts.GrantView
	if err := w.UpdatePeer(ctx, phone); err != nil {
		t.Fatal(err)
	}
	eventually(t, "a grant change", func() bool { p, _ := d.Lookup(phone.Fingerprint); return p.Level() == contracts.GrantView })

	if err := w.DeletePeer(ctx, phone.ID); err != nil {
		t.Fatal(err)
	}
	eventually(t, "an unpairing", func() bool { _, ok := d.Lookup(phone.Fingerprint); return !ok })
	eventually(t, "OnReload after the unpairing", sawUnpaired.Load)
}

// The Data Service down at start (or going away later) never stops the
// directory: it retries, and catches up once the service is back.
func TestDirectoryRetriesWhileDataIsDown(t *testing.T) {
	f := newFlakyData(t)
	f.down.Store(true)
	d := New(dataclient.New(f.url, "mesh", dataclient.WithTokenSource(localauth.FileSource(f.tokenFile))))
	d.minBackoff, d.maxBackoff = 10*time.Millisecond, 50*time.Millisecond
	run(t, d)

	no := false
	fp := testFingerprint(3)
	if err := f.writer().CreatePeer(context.Background(), &contracts.Peer{Name: "phone", Viewed: &no, Fingerprint: fp}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond) // several failed attempts
	if d.Loaded() || d.Len() != 0 {
		t.Fatal("loaded through a Data Service that is down")
	}
	f.down.Store(false)
	eventually(t, "the load once the Data Service is back", func() bool { _, ok := d.Lookup(fp); return ok })
}

// Without the install token the Data Service refuses the read; the
// directory keeps retrying rather than giving up or crashing.
func TestDirectoryWithoutTheTokenStaysEmpty(t *testing.T) {
	f := newFlakyData(t)
	d := New(dataclient.New(f.url, "mesh"))
	d.minBackoff, d.maxBackoff = 10*time.Millisecond, 20*time.Millisecond
	run(t, d)
	time.Sleep(100 * time.Millisecond)
	if d.Loaded() {
		t.Fatal("loaded without the install token")
	}
}

// The follow cursor is the true newest peer.* event even past the Data
// Service's 500-event batch cap: a cursor stuck at the first batch would
// replay hundreds of events, one reload each.
func TestFollowCursorIsTheNewestEventPastTheBatchCap(t *testing.T) {
	f := newFlakyData(t)
	w := f.writer()
	ctx := context.Background()
	// 520 peer.updated, written as the store writes them (POST /events
	// refuses registry events).
	row := &contracts.Peer{Name: "x", MeshURL: "http://x:1"}
	if err := w.CreatePeer(ctx, row); err != nil {
		t.Fatal(err)
	}
	for i := range 520 {
		row.Hidden = i%2 == 0
		if err := w.UpdatePeer(ctx, row); err != nil {
			t.Fatal(err)
		}
	}
	evs, _ := w.Events(ctx, 0, "", peerEvents)
	if len(evs) != 500 {
		t.Fatalf("one batch = %d events; the cap this test relies on moved", len(evs))
	}
	got, err := w.LatestSeq(ctx, "", peerEvents)
	if err != nil {
		t.Fatal(err)
	}
	all, _ := w.Events(ctx, evs[len(evs)-1].Seq, "", peerEvents)
	if want := all[len(all)-1].Seq; got != want {
		t.Fatalf("LatestSeq = %d, want %d", got, want)
	}

	// And the directory, starting from it, reloads once (the initial
	// load), not once per old event.
	var lists atomic.Int32
	counted := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/peers" {
			lists.Add(1)
		}
		target, _ := url.Parse(f.url)
		httputil.NewSingleHostReverseProxy(target).ServeHTTP(rw, r)
	}))
	t.Cleanup(counted.Close)
	d := New(dataclient.New(counted.URL, "mesh", dataclient.WithTokenSource(localauth.FileSource(f.tokenFile))))
	d.minBackoff, d.maxBackoff = 10*time.Millisecond, 50*time.Millisecond
	run(t, d)
	eventually(t, "the first load", d.Loaded)
	time.Sleep(200 * time.Millisecond)
	if n := lists.Load(); n != 1 {
		t.Fatalf("GET /peers %d times, want 1: old events were replayed", n)
	}
}

// A stream that opens and closes at once (a broken Data Service, a proxy
// in between) doesn't reset the backoff, so it can't spin.
func TestAStreamThatClosesAtOnceBacksOff(t *testing.T) {
	f := newFlakyData(t)
	target, _ := url.Parse(f.url)
	proxy := httputil.NewSingleHostReverseProxy(target)
	var streams atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/events" && strings.Contains(r.Header.Get("Accept"), "text/event-stream") {
			streams.Add(1)
			rw.Header().Set("Content-Type", "text/event-stream")
			rw.WriteHeader(http.StatusOK)
			return // and close it
		}
		proxy.ServeHTTP(rw, r)
	}))
	t.Cleanup(srv.Close)
	d := New(dataclient.New(srv.URL, "mesh", dataclient.WithTokenSource(localauth.FileSource(f.tokenFile))))
	d.minBackoff, d.maxBackoff = 20*time.Millisecond, time.Second
	run(t, d)
	time.Sleep(500 * time.Millisecond)
	// Backing off 20, 40, 80, 160ms… fits a handful in 500ms; a reset on
	// every open would be hundreds.
	if n := streams.Load(); n < 1 || n > 8 {
		t.Fatalf("%d stream opens in 500ms, want a backed-off handful", n)
	}
}

// A stream that subscribed and held a while before ending (a token
// rotation, a Data Service restart) resets the backoff: endings spread out
// over time must not grow it as instant closes do.
func TestAHealthyStreamEndingResetsTheBackoff(t *testing.T) {
	f := newFlakyData(t)
	target, _ := url.Parse(f.url)
	proxy := httputil.NewSingleHostReverseProxy(target)
	opens := &openTimes{}
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/events" && strings.Contains(r.Header.Get("Accept"), "text/event-stream") {
			opens.add()
			rw.Header().Set("Content-Type", "text/event-stream")
			rw.WriteHeader(http.StatusOK)
			rw.(http.Flusher).Flush()
			select { // held, then ended with nothing delivered
			case <-time.After(60 * time.Millisecond):
			case <-r.Context().Done():
			}
			return
		}
		proxy.ServeHTTP(rw, r)
	}))
	t.Cleanup(srv.Close)
	d := New(dataclient.New(srv.URL, "mesh", dataclient.WithTokenSource(localauth.FileSource(f.tokenFile))))
	d.minBackoff, d.maxBackoff, d.healthyAfter = 10*time.Millisecond, time.Second, 50*time.Millisecond
	run(t, d)
	opens.assertSteady(t, 8)
}

// openTimes records when each stream was opened.
type openTimes struct {
	mu sync.Mutex
	at []time.Time
}

func (o *openTimes) add() { o.mu.Lock(); o.at = append(o.at, time.Now()); o.mu.Unlock() }

// assertSteady waits for n opens and fails if any gap between them grew
// past 300ms: reset each time, a round is the 60ms hold plus the 10ms
// minimum; growing, the waits double to 640ms by the eighth.
func (o *openTimes) assertSteady(t *testing.T, n int) {
	t.Helper()
	eventually(t, "the opens", func() bool { o.mu.Lock(); defer o.mu.Unlock(); return len(o.at) >= n })
	o.mu.Lock()
	defer o.mu.Unlock()
	for i := 1; i < n; i++ {
		if gap := o.at[i].Sub(o.at[i-1]); gap > 300*time.Millisecond {
			t.Fatalf("open %d came %s after the last: the backoff grew across healthy streams", i, gap)
		}
	}
}

// A reload that fails after a change was announced isn't the end of it:
// the event is consumed, so the directory starts over (cursor, list,
// stream) until the list reads, rather than serving a list it knows is
// stale — a revocation can't be lost to a Data Service blip.
func TestFailedReloadIsRetried(t *testing.T) {
	f := newFlakyData(t)
	w := f.writer()
	ctx := context.Background()
	no := false
	phone := &contracts.Peer{Name: "phone", Viewed: &no, Fingerprint: testFingerprint(4), Grant: &contracts.PeerGrant{Level: contracts.GrantChat}}
	if err := w.CreatePeer(ctx, phone); err != nil {
		t.Fatal(err)
	}
	d := New(dataclient.New(f.url, "mesh", dataclient.WithTokenSource(localauth.FileSource(f.tokenFile))))
	d.minBackoff, d.maxBackoff = 10*time.Millisecond, 50*time.Millisecond
	var reloads atomic.Int32
	d.OnReload(func() { reloads.Add(1) })
	run(t, d)
	eventually(t, "the first load", func() bool { return reloads.Load() >= 1 })
	time.Sleep(100 * time.Millisecond) // subscribed

	// The open stream still delivers (it predates the outage); the list
	// read it triggers fails.
	f.down.Store(true)
	if err := w.DeletePeer(ctx, phone.ID); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	if _, ok := d.Lookup(phone.Fingerprint); !ok {
		t.Fatal("the list changed while the Data Service was unreadable?")
	}
	f.down.Store(false)
	eventually(t, "the revocation, once the list reads again", func() bool { _, ok := d.Lookup(phone.Fingerprint); return !ok })
}

// A revocation never waits out a long backoff: the cap is short, and New
// uses it.
func TestBackoffIsCappedShort(t *testing.T) {
	if MaxBackoff > 5*time.Second {
		t.Fatalf("MaxBackoff = %s", MaxBackoff)
	}
	if d := New(nil); d.maxBackoff != MaxBackoff {
		t.Fatalf("New's maxBackoff = %s", d.maxBackoff)
	}
}

// While the Data Service stays down, retries aren't logged one per
// attempt: the first, every retryLogEvery-th, and the return.
func TestRetryLogsAreThinned(t *testing.T) {
	var mu sync.Mutex
	var buf strings.Builder
	prev := log.Writer()
	log.SetOutput(writerFunc(func(p []byte) (int, error) { mu.Lock(); defer mu.Unlock(); return buf.Write(p) }))
	defer log.SetOutput(prev)
	lines := func() string { mu.Lock(); defer mu.Unlock(); return buf.String() }

	f := newFlakyData(t)
	f.down.Store(true)
	d := New(dataclient.New(f.url, "mesh", dataclient.WithTokenSource(localauth.FileSource(f.tokenFile))))
	d.minBackoff, d.maxBackoff = time.Millisecond, time.Millisecond
	run(t, d)
	time.Sleep(300 * time.Millisecond) // well over retryLogEvery attempts
	attempts := strings.Count(lines(), "paired devices: load")
	if attempts == 0 || attempts > 1+300/retryLogEvery {
		t.Fatalf("%d retry lines in 300 ms of 1 ms retries:\n%s", attempts, lines())
	}
	f.down.Store(false)
	eventually(t, "the return logged", func() bool { return strings.Contains(lines(), "read again after") })
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

// With the event log broken (cursor reads and streams fail,
// the list still reads), the directory keeps reloading the list on its
// backoff: a revocation lands. It used to retry the cursor forever and
// serve the last list.
func TestRevocationLandsWithTheEventLogBroken(t *testing.T) {
	f := newFlakyData(t)
	w := f.writer()
	ctx := context.Background()
	no := false
	phone := &contracts.Peer{Name: "phone", Viewed: &no, Fingerprint: testFingerprint(1), Grant: &contracts.PeerGrant{Level: contracts.GrantChat}}
	if err := w.CreatePeer(ctx, phone); err != nil {
		t.Fatal(err)
	}
	d := New(dataclient.New(f.url, "mesh", dataclient.WithTokenSource(localauth.FileSource(f.tokenFile))))
	d.minBackoff, d.maxBackoff = 10*time.Millisecond, 50*time.Millisecond
	run(t, d)
	eventually(t, "the first load", func() bool { _, ok := d.Lookup(phone.Fingerprint); return ok })
	f.breakEvents()
	time.Sleep(100 * time.Millisecond) // the stream gone, cursor reads failing
	if err := w.DeletePeer(ctx, phone.ID); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the revocation", func() bool { _, ok := d.Lookup(phone.Fingerprint); return !ok })
}
