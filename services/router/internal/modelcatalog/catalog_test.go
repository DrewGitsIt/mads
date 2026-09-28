package modelcatalog

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/drewkidwell/mads/providers/provider"
)

// fakeProvider is a minimal Provider that also discovers models, with the
// discovery result and call count under the test's control.
type fakeProvider struct {
	name      string
	discovery provider.Support
	calls     atomic.Int32
	// fn answers one discovery; nil means "two models, no error".
	fn func(ctx context.Context, call int) ([]provider.Model, error)
	// block, when non-nil, gates every call until the test closes it —
	// how the single-flight and join-window cases hold a call open.
	block chan struct{}
}

func (f *fakeProvider) Name() string { return f.name }

func (f *fakeProvider) Matrix() provider.Matrix {
	m := provider.Matrix{}
	for _, b := range provider.Behaviors() {
		m[b] = provider.Unsupported
	}
	m[provider.BehaviorInteractive] = provider.Native
	m[provider.BehaviorInterrupt] = provider.Native
	m[provider.BehaviorOneShot] = provider.Native
	m[provider.BehaviorACP] = provider.Native
	m[provider.BehaviorModelDiscovery] = f.discovery
	return m
}

func (f *fakeProvider) Start(context.Context, provider.StartOptions) (provider.Session, error) {
	return nil, provider.ErrUnsupported
}

func (f *fakeProvider) OneShot(context.Context, provider.OneShotRequest) (*provider.OneShotResult, error) {
	return nil, provider.ErrUnsupported
}

func (f *fakeProvider) Models(ctx context.Context) ([]provider.Model, error) {
	n := int(f.calls.Add(1))
	if f.block != nil {
		select {
		case <-f.block:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if f.fn != nil {
		return f.fn(ctx, n)
	}
	return []provider.Model{{ID: "a", Name: "A", Default: true}, {ID: "b", Name: "B"}}, nil
}

// clock is an injectable time source; the cache never sleeps in tests.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock() *clock { return &clock{t: time.Unix(1_700_000_000, 0)} }

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func newTestCatalog(t *testing.T, p *fakeProvider, opts Options) (*Catalog, *clock) {
	t.Helper()
	reg := provider.NewRegistry()
	if err := reg.Register(p); err != nil {
		t.Fatal(err)
	}
	cl := newClock()
	opts.Now = cl.Now
	opts.Logf = func(string, ...any) {}
	return New(reg, opts), cl
}

func TestGetDiscoversOnceThenServesCache(t *testing.T) {
	p := &fakeProvider{name: "p", discovery: provider.Native}
	c, _ := newTestCatalog(t, p, Options{})

	e := c.Get(context.Background(), "p")
	if e.State != StateReady || len(e.Models) != 2 {
		t.Fatalf("first Get = %+v, want ready with 2 models", e)
	}
	for i := 0; i < 5; i++ {
		if got := c.Get(context.Background(), "p"); got.State != StateReady || len(got.Models) != 2 {
			t.Fatalf("cached Get = %+v", got)
		}
	}
	if n := p.calls.Load(); n != 1 {
		t.Errorf("discovery ran %d times, want 1 — the cache is not holding", n)
	}
}

func TestStaleEntryServedImmediatelyWhileRefreshing(t *testing.T) {
	p := &fakeProvider{name: "p", discovery: provider.Native, fn: func(_ context.Context, n int) ([]provider.Model, error) {
		return []provider.Model{{ID: fmt.Sprintf("gen-%d", n)}}, nil
	}}
	c, cl := newTestCatalog(t, p, Options{TTL: time.Minute})

	if got := c.Get(context.Background(), "p"); got.Models[0].ID != "gen-1" {
		t.Fatalf("first Get = %+v", got)
	}
	cl.advance(2 * time.Minute)

	// Past TTL the caller must get the stale value at once, not wait on a
	// subprocess; the refresh lands behind it.
	if got := c.Get(context.Background(), "p"); got.Models[0].ID != "gen-1" {
		t.Errorf("stale Get = %+v, want the stale gen-1 served immediately", got)
	}
	waitFor(t, func() bool { return c.Lookup("p").Models[0].ID == "gen-2" })
}

func TestConcurrentGetsSingleFlight(t *testing.T) {
	p := &fakeProvider{name: "p", discovery: provider.Native, block: make(chan struct{})}
	c, _ := newTestCatalog(t, p, Options{JoinWait: 2 * time.Second})

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); c.Get(context.Background(), "p") }()
	}
	// Let them all arrive and coalesce before releasing the one call.
	waitFor(t, func() bool { return p.calls.Load() >= 1 })
	close(p.block)
	wg.Wait()

	if n := p.calls.Load(); n != 1 {
		t.Errorf("discovery ran %d times for 8 concurrent Gets, want 1", n)
	}
}

func TestUnavailableIsCachedWithBackoff(t *testing.T) {
	p := &fakeProvider{name: "p", discovery: provider.Native, fn: func(context.Context, int) ([]provider.Model, error) {
		return nil, fmt.Errorf("no binary: %w", provider.ErrDiscoveryUnavailable)
	}}
	c, cl := newTestCatalog(t, p, Options{FailureTTL: 30 * time.Second, FailureTTLMax: 5 * time.Minute})

	e := c.Get(context.Background(), "p")
	if e.State != StateUnavailable || e.Detail == "" || e.Err != nil {
		t.Fatalf("Get = %+v, want unavailable with a Detail and no hard Err", e)
	}
	// Within the backoff window a missing binary must not be re-probed on
	// every picker mount.
	for i := 0; i < 3; i++ {
		c.Get(context.Background(), "p")
	}
	if n := p.calls.Load(); n != 1 {
		t.Errorf("discovery ran %d times inside the backoff window, want 1", n)
	}
	cl.advance(31 * time.Second)
	c.Get(context.Background(), "p")
	waitFor(t, func() bool { return p.calls.Load() == 2 })

	// Second failure doubles the window: 30s is no longer enough.
	cl.advance(31 * time.Second)
	c.Get(context.Background(), "p")
	if n := p.calls.Load(); n != 2 {
		t.Errorf("discovery ran %d times, want 2 — backoff did not double", n)
	}
}

func TestBackoffResetsAfterSuccess(t *testing.T) {
	p := &fakeProvider{name: "p", discovery: provider.Native, fn: func(_ context.Context, n int) ([]provider.Model, error) {
		if n == 1 {
			return nil, fmt.Errorf("transient: %w", provider.ErrDiscoveryUnavailable)
		}
		return []provider.Model{{ID: "a"}}, nil
	}}
	c, cl := newTestCatalog(t, p, Options{TTL: time.Minute, FailureTTL: 30 * time.Second})

	c.Get(context.Background(), "p")
	cl.advance(31 * time.Second)
	c.Get(context.Background(), "p")
	waitFor(t, func() bool { return c.Lookup("p").State == StateReady })

	// Back on the success TTL, not a doubled failure window.
	cl.advance(31 * time.Second)
	c.Get(context.Background(), "p")
	if n := p.calls.Load(); n != 2 {
		t.Errorf("discovery ran %d times, want 2 — failure count did not reset", n)
	}
}

// A hard error is a bug, not a missing binary: it must NOT be laundered
// into the 200-with-Detail path, or a malformed response becomes an empty
// picker nobody investigates.
func TestUnexpectedErrorSurfacesAsErr(t *testing.T) {
	boom := errors.New("malformed catalog")
	p := &fakeProvider{name: "p", discovery: provider.Native, fn: func(context.Context, int) ([]provider.Model, error) {
		return nil, boom
	}}
	c, _ := newTestCatalog(t, p, Options{})

	e := c.Get(context.Background(), "p")
	if !errors.Is(e.Err, boom) {
		t.Fatalf("Err = %v, want the underlying error", e.Err)
	}
	if e.Detail != "" {
		t.Errorf("Detail = %q, want empty — Detail is for expected failures only", e.Detail)
	}
}

// An agent that answers with no models is "ready and empty", which the UI
// must be able to tell apart from "couldn't ask".
func TestReadyEmptyIsNotUnavailable(t *testing.T) {
	p := &fakeProvider{name: "p", discovery: provider.Native, fn: func(context.Context, int) ([]provider.Model, error) {
		return nil, nil
	}}
	c, _ := newTestCatalog(t, p, Options{})

	e := c.Get(context.Background(), "p")
	if e.State != StateReady {
		t.Errorf("State = %q, want ready", e.State)
	}
	if e.Models == nil {
		t.Error("Models is nil; the wire contract requires an empty array, never null")
	}
	if e.Detail != "" {
		t.Errorf("Detail = %q, want empty for a real but empty catalog", e.Detail)
	}
}

// Lookup is the contract session-defaults resolution depends on: answer
// from memory, never spawn, never block.
func TestLookupNeverSpawns(t *testing.T) {
	p := &fakeProvider{name: "p", discovery: provider.Native}
	c, _ := newTestCatalog(t, p, Options{})

	if e := c.Lookup("p"); e.State != StatePending {
		t.Errorf("cold Lookup = %+v, want pending", e)
	}
	if n := p.calls.Load(); n != 0 {
		t.Errorf("Lookup spawned discovery %d times, want 0", n)
	}
}

// A provider declaring model_discovery unsupported must never be probed,
// even though it satisfies the ModelDiscoverer interface — the matrix is
// the authority, not the type assertion.
func TestWarmSkipsProvidersDeclaringUnsupported(t *testing.T) {
	p := &fakeProvider{name: "p", discovery: provider.Unsupported}
	c, _ := newTestCatalog(t, p, Options{})

	c.Start(context.Background())
	time.Sleep(50 * time.Millisecond)
	if n := p.calls.Load(); n != 0 {
		t.Errorf("probed an unsupported provider %d times, want 0", n)
	}
	if e := c.Lookup("p"); e.State != StatePending {
		t.Errorf("Lookup = %+v, want pending (no entry created)", e)
	}
}

// A cold Get whose discovery outruns the join window must answer
// "refreshing" rather than pretending the provider has no models.
func TestColdGetPastJoinWindowReportsRefreshing(t *testing.T) {
	p := &fakeProvider{name: "p", discovery: provider.Native, block: make(chan struct{})}
	c, _ := newTestCatalog(t, p, Options{JoinWait: 20 * time.Millisecond})
	defer close(p.block)

	e := c.Get(context.Background(), "p")
	if e.State != StateRefreshing {
		t.Errorf("State = %q, want refreshing", e.State)
	}
	if len(e.Models) != 0 {
		t.Errorf("Models = %+v, want empty while still discovering", e.Models)
	}
}

// Refresh ignores the TTL: it is the "I just upgraded the CLI" affordance.
func TestRefreshBypassesTTL(t *testing.T) {
	p := &fakeProvider{name: "p", discovery: provider.Native, fn: func(_ context.Context, n int) ([]provider.Model, error) {
		return []provider.Model{{ID: fmt.Sprintf("gen-%d", n)}}, nil
	}}
	c, _ := newTestCatalog(t, p, Options{TTL: time.Hour})

	c.Get(context.Background(), "p")
	e := c.Refresh(context.Background(), "p")
	if len(e.Models) == 0 || e.Models[0].ID != "gen-2" {
		t.Fatalf("Refresh = %+v, want the re-read gen-2 despite a 1h TTL", e)
	}
}

// Discovery runs on the catalog's base context, not the caller's: a user
// navigating away mid-request must not cancel a spawn and poison the entry
// for everyone else.
func TestRequestCancellationDoesNotPoisonEntry(t *testing.T) {
	release := make(chan struct{})
	p := &fakeProvider{name: "p", discovery: provider.Native, block: release}
	c, _ := newTestCatalog(t, p, Options{JoinWait: time.Second})
	c.Start(context.Background())

	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(20 * time.Millisecond); cancel() }()
	if e := c.Get(ctx, "p"); e.State != StateRefreshing {
		t.Fatalf("cancelled Get = %+v, want refreshing", e)
	}
	// The underlying discovery survived the cancellation and lands.
	close(release)
	waitFor(t, func() bool { return c.Lookup("p").State == StateReady })
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("condition not met within 2s")
}
