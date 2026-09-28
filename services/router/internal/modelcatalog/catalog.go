// Package modelcatalog caches every registered provider's discovered model
// list for the Router.
//
// Discovery is not cheap: each native path spawns the harness binary
// (claude ~1s, goose ~2s over a full ACP initialize + session/new, hermes
// ~9s because it fetches a live catalog). Those were being paid on the
// request path — GET /providers/{name}/models on every mount of the model
// picker, and worse, GET /sessions/defaults spawned an agent inline while
// resolving a session's default model. This package moves that cost to a
// background warm at boot and keeps the answer.
//
// It deliberately lives above the provider layer rather than decorating
// Provider or Registry. Two reasons, both structural: the registry's
// consumers type-assert a long list of optional strategy interfaces off
// provider values, and a decorator embedding the Provider interface
// silently swallows every assertion it does not explicitly forward; and a
// cache installed at registration would let a provider declaring
// model_discovery: native satisfy the conformance suite from a warm entry
// even with its binary deleted, inverting the matrix-honesty contract that
// Registry.Register exists to protect. Keeping the cache here makes that
// impossible rather than something a test has to catch.
package modelcatalog

import (
	"context"
	"errors"
	"log"
	"sync"
	"time"

	"github.com/drewkidwell/mads/providers/provider"
)

// State is what the cache knows about one provider, which is more than
// "has models or not" — the UI renders "discovering…", "no models" and
// "couldn't ask" differently, and collapsing them is what made a missing
// binary look like a provider with an empty catalog.
type State string

const (
	// StatePending — never attempted.
	StatePending State = "pending"
	// StateRefreshing — an attempt is in flight and there is no prior data.
	StateRefreshing State = "refreshing"
	// StateReady — a real answer. The list may legitimately be empty.
	StateReady State = "ready"
	// StateUnavailable — the last attempt failed the expected way
	// (provider.ErrDiscoveryUnavailable): binary absent, agent not logged
	// in, harness too old.
	StateUnavailable State = "unavailable"
)

// Entry is one provider's cached catalog.
type Entry struct {
	Models    []provider.Model
	Detail    string // human-readable cause; set only when Unavailable
	State     State
	FetchedAt time.Time
	// Err is set only for an UNEXPECTED discovery failure — one that does
	// not wrap provider.ErrDiscoveryUnavailable, e.g. a malformed response
	// the adapter could not parse. The handler still 500s on these so a
	// genuine bug stays visible instead of degrading into an empty picker.
	Err error
}

// Options tunes the cache. The zero value is usable; New fills defaults.
type Options struct {
	TTL           time.Duration // success freshness (default 10m)
	FailureTTL    time.Duration // first retry delay after a failure (default 30s)
	FailureTTLMax time.Duration // backoff ceiling (default 5m)
	CallTimeout   time.Duration // per-discovery bound (default 20s)
	JoinWait      time.Duration // max a request waits on an in-flight call (default 1.5s)
	Concurrency   int           // warm fan-out cap (default 4)
	Now           func() time.Time
	Logf          func(format string, v ...any)
}

func (o *Options) applyDefaults() {
	if o.TTL == 0 {
		o.TTL = 10 * time.Minute
	}
	if o.FailureTTL == 0 {
		o.FailureTTL = 30 * time.Second
	}
	if o.FailureTTLMax == 0 {
		o.FailureTTLMax = 5 * time.Minute
	}
	if o.CallTimeout == 0 {
		o.CallTimeout = 20 * time.Second
	}
	if o.JoinWait == 0 {
		o.JoinWait = 1500 * time.Millisecond
	}
	if o.Concurrency == 0 {
		o.Concurrency = 4
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Logf == nil {
		o.Logf = log.Printf
	}
}

// call is one in-flight discovery, so N concurrent readers of a cold entry
// produce one spawn rather than N.
type call struct {
	done chan struct{}
}

type entryState struct {
	Entry
	failures int
	inflight *call
}

// Catalog caches discovered models per provider name.
type Catalog struct {
	reg  *provider.Registry
	opts Options

	mu      sync.Mutex
	entries map[string]*entryState
	// base is the long-lived context discovery runs on, captured by Start.
	// Discovery must NOT run on an HTTP request's context: a user
	// navigating away would kill a half-finished spawn and poison the
	// entry for everyone. A request context bounds only how long that
	// request is willing to WAIT.
	base context.Context
}

func New(reg *provider.Registry, opts Options) *Catalog {
	opts.applyDefaults()
	return &Catalog{reg: reg, opts: opts, entries: map[string]*entryState{}, base: context.Background()}
}

// Start records the base context and kicks the initial warm. It never
// blocks: boot must not be gated on spawning every harness, and a machine
// with a broken agent binary must still serve immediately.
func (c *Catalog) Start(ctx context.Context) {
	c.mu.Lock()
	c.base = ctx
	c.mu.Unlock()
	go c.warm(ctx)
}

// warm discovers every provider that declares working model discovery,
// capped at Concurrency. Providers whose matrix says the behavior does not
// work are skipped entirely — no entry, no spawn.
func (c *Catalog) warm(ctx context.Context) {
	var wg sync.WaitGroup
	sem := make(chan struct{}, c.opts.Concurrency)
	for _, name := range c.reg.Names() {
		if _, ok := c.discoverer(name); !ok {
			continue
		}
		wg.Add(1)
		go func(name string) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-sem }()
			started := c.opts.Now()
			e := c.refresh(ctx, name)
			if e.State == StateUnavailable {
				c.opts.Logf("router: model discovery %s unavailable: %s", name, e.Detail)
				return
			}
			if e.Err != nil {
				c.opts.Logf("router: model discovery %s failed: %v", name, e.Err)
				return
			}
			c.opts.Logf("router: model discovery %s: %d models in %s", name, len(e.Models), c.opts.Now().Sub(started).Round(time.Millisecond))
		}(name)
	}
	wg.Wait()
}

// discoverer resolves a provider that both implements ModelDiscoverer and
// declares the behavior as working. The matrix check is what keeps the
// cache from creating entries for providers that honestly said they can't.
func (c *Catalog) discoverer(name string) (provider.ModelDiscoverer, bool) {
	p, err := c.reg.Get(name)
	if err != nil {
		return nil, false
	}
	if !p.Matrix()[provider.BehaviorModelDiscovery].Works() {
		return nil, false
	}
	md, ok := p.(provider.ModelDiscoverer)
	return md, ok
}

// Lookup returns whatever is cached right now. It never spawns and never
// blocks — the contract session-defaults resolution needs, where a stale
// or missing answer is a tolerated degradation but a 2s agent spawn on the
// create-session path is not.
func (c *Catalog) Lookup(name string) Entry {
	c.mu.Lock()
	defer c.mu.Unlock()
	if st, ok := c.entries[name]; ok {
		return st.Entry
	}
	return Entry{State: StatePending}
}

// Get serves a fresh entry. A stale one is returned immediately while a
// refresh runs behind it (stale-while-revalidate), so a request pays the
// spawn cost only when nothing is cached at all — and even then waits at
// most JoinWait before answering with whatever it has.
func (c *Catalog) Get(ctx context.Context, name string) Entry {
	c.mu.Lock()
	st, known := c.entries[name]
	if known && !c.staleLocked(st) {
		e := st.Entry
		c.mu.Unlock()
		return e
	}
	hasData := known && st.State == StateReady
	c.mu.Unlock()

	if _, ok := c.discoverer(name); !ok {
		return Entry{State: StatePending}
	}

	done := c.startRefresh(name)
	if hasData {
		// Stale but usable: answer now, let the refresh land for the next
		// caller rather than making this one wait on a subprocess.
		return c.Lookup(name)
	}
	select {
	case <-done:
	case <-time.After(c.opts.JoinWait):
	case <-ctx.Done():
	}
	e := c.Lookup(name)
	if e.State == StatePending {
		// The join window expired before the first answer landed.
		e.State = StateRefreshing
	}
	return e
}

// Refresh forces a discovery regardless of TTL and waits for it, bounded
// only by ctx. This is the explicit "I just upgraded the CLI / just logged
// in" affordance — the honest answer to catalogs that change for reasons
// no heuristic can observe (goose's list is scoped to its currently
// configured provider and changes under `goose configure`).
func (c *Catalog) Refresh(ctx context.Context, name string) Entry {
	if _, ok := c.discoverer(name); !ok {
		return Entry{State: StatePending}
	}
	c.mu.Lock()
	if st, ok := c.entries[name]; ok {
		st.FetchedAt = time.Time{} // force staleness
	}
	c.mu.Unlock()
	done := c.startRefresh(name)
	select {
	case <-done:
	case <-ctx.Done():
	}
	return c.Lookup(name)
}

// startRefresh joins the in-flight discovery for name, or begins one.
func (c *Catalog) startRefresh(name string) <-chan struct{} {
	c.mu.Lock()
	st, ok := c.entries[name]
	if !ok {
		st = &entryState{Entry: Entry{State: StatePending}}
		c.entries[name] = st
	}
	if st.inflight != nil {
		ch := st.inflight.done
		c.mu.Unlock()
		return ch
	}
	cl := &call{done: make(chan struct{})}
	st.inflight = cl
	if st.State == StatePending {
		st.State = StateRefreshing
	}
	base := c.base
	c.mu.Unlock()

	go func() {
		defer close(cl.done)
		c.refresh(base, name)
	}()
	return cl.done
}

// refresh performs one discovery and stores the outcome. It is safe to
// call directly (the warm does) or through startRefresh.
func (c *Catalog) refresh(ctx context.Context, name string) Entry {
	md, ok := c.discoverer(name)
	if !ok {
		return Entry{State: StatePending}
	}
	callCtx, cancel := context.WithTimeout(ctx, c.opts.CallTimeout)
	defer cancel()
	models, err := md.Models(callCtx)

	c.mu.Lock()
	defer c.mu.Unlock()
	st, exists := c.entries[name]
	if !exists {
		st = &entryState{}
		c.entries[name] = st
	}
	st.inflight = nil
	st.FetchedAt = c.opts.Now()

	switch {
	case err == nil:
		if models == nil {
			models = []provider.Model{}
		}
		st.Models, st.Detail, st.Err, st.State, st.failures = models, "", nil, StateReady, 0
	case errors.Is(err, provider.ErrDiscoveryUnavailable):
		// Expected: not installed, not logged in, too old. Empty picker
		// plus a reason, and back off before trying again.
		st.Models, st.Detail, st.Err, st.State = []provider.Model{}, err.Error(), nil, StateUnavailable
		st.failures++
	default:
		// Unexpected: keep it a hard error so the bug stays visible, but
		// back off all the same so it cannot become an unbounded spawn
		// loop driven by page refreshes.
		st.Models, st.Detail, st.Err, st.State = []provider.Model{}, "", err, StateUnavailable
		st.failures++
	}
	return st.Entry
}

// staleLocked reports whether st is due for a refresh. Failures use their
// own doubling backoff so the common install — one harness present, two
// absent — does not re-pay two failed spawns on every picker mount.
func (c *Catalog) staleLocked(st *entryState) bool {
	if st.FetchedAt.IsZero() {
		return true
	}
	ttl := c.opts.TTL
	if st.failures > 0 {
		ttl = c.opts.FailureTTL << (st.failures - 1)
		if ttl > c.opts.FailureTTLMax || ttl <= 0 {
			ttl = c.opts.FailureTTLMax
		}
	}
	return c.opts.Now().Sub(st.FetchedAt) >= ttl
}
