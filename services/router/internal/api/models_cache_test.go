package api_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/providers/fake"
	"github.com/drewkidwell/mads/providers/provider"
	"github.com/drewkidwell/mads/services/data/datatest"
	"github.com/drewkidwell/mads/services/router/internal/api"
	"github.com/drewkidwell/mads/services/router/internal/modelcatalog"
	"github.com/drewkidwell/mads/services/router/internal/supervisor"
)

// countingModels is a provider whose discovery is observable: these tests
// care how many times the harness would actually be spawned.
type countingModels struct {
	*fake.Fake
	name   string
	calls  atomic.Int32
	models []provider.Model
	err    error
}

func (c *countingModels) Name() string { return c.name }

func (c *countingModels) Models(context.Context) ([]provider.Model, error) {
	c.calls.Add(1)
	return c.models, c.err
}

// newTestServerWithCatalog registers p and wires a warmed model catalog —
// the production configuration, as opposed to the nil-catalog fallback the
// older reads_test.go cases exercise.
func newTestServerWithCatalog(t *testing.T, p provider.Provider) *httptest.Server {
	t.Helper()
	base := datatest.Start(t)
	data := dataclient.New(base, "router")
	sup := supervisor.New(data, []string{testChildBin}, []string{testChildBin}, nil)
	t.Cleanup(func() { sup.Shutdown(context.Background()) })
	reg := provider.NewRegistry()
	if err := reg.Register(p); err != nil {
		t.Fatal(err)
	}
	opts := testOptions(t, data, sup)
	opts.Providers = reg
	cat := modelcatalog.New(reg, modelcatalog.Options{Logf: func(string, ...any) {}})
	cat.Start(context.Background())
	opts.Models = cat
	srv := httptest.NewServer(api.New(opts))
	t.Cleanup(srv.Close)
	return srv
}

// Repeated reads must be served from memory — the per-mount agent spawn
// (five UI components mount the model hook) is the whole reason the cache
// exists.
func TestListProviderModelsThroughCatalog(t *testing.T) {
	p := &countingModels{Fake: fake.New(), name: "cached", models: []provider.Model{{ID: "m1", Name: "M1", Default: true}}}
	srv := newTestServerWithCatalog(t, p)

	for i := 0; i < 5; i++ {
		resp, err := http.Get(srv.URL + "/providers/cached/models")
		if err != nil {
			t.Fatal(err)
		}
		out := decodeJSON[contracts.ModelsResponse](t, resp)
		if len(out.Models) != 1 || out.Models[0].ID != "m1" {
			t.Fatalf("Models = %+v, want the discovered m1", out.Models)
		}
		if out.State != "ready" {
			t.Errorf("State = %q, want ready", out.State)
		}
		if out.FetchedAt == "" {
			t.Error("FetchedAt is empty; the UI needs it to show catalog age")
		}
	}
	if n := p.calls.Load(); n != 1 {
		t.Errorf("harness discovery ran %d times across 5 reads, want 1", n)
	}
}

// The cached path must preserve the pre-cache wire contract exactly: an
// expected failure is 200 with an empty list and a reason, never a 5xx.
func TestListProviderModelsUnavailableThroughCatalog(t *testing.T) {
	p := &countingModels{
		Fake: fake.New(), name: "gone",
		err: fmt.Errorf("not installed: %w", provider.ErrDiscoveryUnavailable),
	}
	srv := newTestServerWithCatalog(t, p)

	resp, err := http.Get(srv.URL + "/providers/gone/models")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	out := decodeJSON[contracts.ModelsResponse](t, resp)
	if out.State != "unavailable" || out.Detail == "" {
		t.Errorf("out = %+v, want unavailable with a Detail", out)
	}
	if out.Models == nil {
		t.Error("Models is null; the contract requires an empty array")
	}
}

// An unexpected error keeps its 500 even behind the cache, so a genuine
// bug does not degrade into a silently empty picker.
func TestListProviderModelsInternalErrorThroughCatalog(t *testing.T) {
	p := &countingModels{Fake: fake.New(), name: "broken", err: errors.New("boom")}
	srv := newTestServerWithCatalog(t, p)

	resp, err := http.Get(srv.URL + "/providers/broken/models")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode < 500 {
		t.Fatalf("status = %d, want 5xx for an unexpected discovery error", resp.StatusCode)
	}
}

// The refresh route is the answer to catalogs that change for reasons the
// Router cannot observe (a CLI upgrade, a fresh login, `goose configure`
// switching the backing provider and with it the whole list).
func TestRefreshProviderModelsReReads(t *testing.T) {
	p := &countingModels{Fake: fake.New(), name: "cached", models: []provider.Model{{ID: "m1"}}}
	srv := newTestServerWithCatalog(t, p)

	resp, err := http.Get(srv.URL + "/providers/cached/models")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	before := p.calls.Load()

	resp, err = http.Post(srv.URL+"/providers/cached/models/refresh", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("refresh status = %d, want 200", resp.StatusCode)
	}
	if got := p.calls.Load(); got <= before {
		t.Errorf("discovery ran %d times, want more than the %d before refresh", got, before)
	}
}
