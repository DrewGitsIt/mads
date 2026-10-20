package api_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/providers/fake"
	"github.com/drewkidwell/mads/providers/provider"
	"github.com/drewkidwell/mads/services/data/datatest"
	"github.com/drewkidwell/mads/services/router/internal/api"
	"github.com/drewkidwell/mads/services/router/internal/supervisor"
)

// fullOnly is the fake with hermes's trust stance: every restricted level
// declared unsupported (provider.FullTrustOnly). Built on the fake so no
// real harness is ever spawned for discovery.
type fullOnly struct{ *fake.Fake }

func (fullOnly) Name() string { return "full-only" }

func (f fullOnly) Matrix() provider.Matrix {
	m := f.Fake.Matrix()
	m[provider.BehaviorTrustLimited] = provider.Unsupported
	m[provider.BehaviorTrustReadOnly] = provider.Unsupported
	m[provider.BehaviorTrustAutoReview] = provider.Unsupported
	return m
}

func newFullOnlyServer(t *testing.T) *httptest.Server {
	t.Helper()
	base := datatest.Start(t)
	data := dataclient.New(base, "router")
	sup := supervisor.New(data, []string{testChildBin}, []string{testChildBin}, nil)
	t.Cleanup(func() { sup.Shutdown(context.Background()) })
	opts := testOptions(t, data, sup)
	reg := provider.NewRegistry()
	for _, p := range []provider.Provider{fake.New(), fullOnly{fake.New()}} {
		if err := reg.Register(p); err != nil {
			t.Fatal(err)
		}
	}
	opts.Providers = reg
	srv := httptest.NewServer(api.New(opts))
	t.Cleanup(srv.Close)
	return srv
}

// A session that never chose a trust level runs at full on a provider that
// can run nothing else — whether that provider is Config's default or the
// one picked for this session — while the configured default (limited)
// still applies to every other provider and stays untouched in Config.
func TestSessionDefaultsFullTrustOnlyProvider(t *testing.T) {
	srv := newFullOnlyServer(t)
	updateConfig(t, srv.URL, func(c *contracts.Config) {
		c.DefaultProvider = "fake"
		c.DefaultTrust = contracts.TrustLimited
	})
	full := contracts.ResolvedSessionField{Value: string(contracts.TrustFull), Source: contracts.SessionDefaultSourceProviderDefault}
	limited := contracts.ResolvedSessionField{Value: string(contracts.TrustLimited), Source: contracts.SessionDefaultSourceConfig}

	if got := getDefaults(t, srv.URL, nil).Trust; got != limited {
		t.Errorf("fake (config default): trust = %+v, want %+v", got, limited)
	}
	if got := getDefaults(t, srv.URL, url.Values{"provider": {"full-only"}}).Trust; got != full {
		t.Errorf("full-only picked: trust = %+v, want %+v", got, full)
	}
	updateConfig(t, srv.URL, func(c *contracts.Config) { c.DefaultProvider = "full-only" })
	if got := getDefaults(t, srv.URL, nil).Trust; got != full {
		t.Errorf("full-only as config default: trust = %+v, want %+v", got, full)
	}

	// The create path resolves the same way, and Config's default is not
	// rewritten by it.
	resp := postJSON(t, srv.URL+"/sessions", map[string]string{"name": "x", "provider": "full-only", "dir": t.TempDir()})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create: status %d", resp.StatusCode)
	}
	created := decodeJSON[struct {
		Session contracts.Session `json:"session"`
	}](t, resp)
	if created.Session.Trust != contracts.TrustFull {
		t.Errorf("created trust = %q, want full", created.Session.Trust)
	}
	cfgResp, err := http.Get(srv.URL + "/config")
	if err != nil {
		t.Fatal(err)
	}
	if cfg := decodeJSON[contracts.Config](t, cfgResp); cfg.DefaultTrust != contracts.TrustLimited {
		t.Errorf("config default_trust = %q, want limited left alone", cfg.DefaultTrust)
	}

	// An explicit choice is never raised: the provider refuses it at Start,
	// visibly, rather than the Router quietly loosening it.
	resp = postJSON(t, srv.URL+"/sessions", map[string]string{"name": "y", "provider": "full-only", "trust": "limited", "dir": t.TempDir()})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("explicit create: status %d", resp.StatusCode)
	}
	explicit := decodeJSON[struct {
		Session contracts.Session `json:"session"`
	}](t, resp)
	if explicit.Session.Trust != contracts.TrustLimited {
		t.Errorf("explicit limited recorded as %q, want limited", explicit.Session.Trust)
	}
}
