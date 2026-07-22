package fastmodel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/drewkidwell/mads/providers/fake"
	"github.com/drewkidwell/mads/providers/provider"
)

// With a resolve func returning a real endpoint, NewDynamic's Runner behaves
// exactly like New(nil, cfg) with that BaseURL — the dynamic indirection is
// invisible to a configured caller.
func TestDynamicConfiguredUsesRealClient(t *testing.T) {
	t.Setenv("MADS_TEST_DYN_KEY", "sk-dyn-1")
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var req struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Model != "dyn-fast" {
			t.Errorf("model sent to endpoint: %q", req.Model)
		}
		_, _ = fmt.Fprint(w, `{"model":"dyn-fast","choices":[{"message":{"content":"hello"}}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`)
	}))
	defer srv.Close()

	r := NewDynamic(func(context.Context) (DynamicConfig, error) {
		return DynamicConfig{Model: "dyn-fast", BaseURL: srv.URL, APIKeyEnv: "MADS_TEST_DYN_KEY"}, nil
	})
	text, _, err := r.DoText(context.Background(), Job{Prompt: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	if text != "hello" {
		t.Errorf("text = %q", text)
	}
	if calls != 1 {
		t.Errorf("calls = %d, want 1", calls)
	}
}

// An unconfigured resolution (empty BaseURL — nothing set in Config or the
// -fastmodel-* flags) must skip immediately: ErrBackendUnconfigured, no
// network call, so Do doesn't waste retries and every caller's
// fastmodel.IsSkippable(err) check applies uniformly.
func TestDynamicUnconfiguredSkipsWithoutNetworkCall(t *testing.T) {
	r := NewDynamic(func(context.Context) (DynamicConfig, error) {
		return DynamicConfig{}, nil
	})
	var out classification
	_, err := r.Do(context.Background(), Job{Prompt: "classify: x"}, &out)
	if !errors.Is(err, ErrBackendUnconfigured) {
		t.Fatalf("err = %v, want ErrBackendUnconfigured", err)
	}
	if !IsSkippable(err) {
		t.Fatalf("IsSkippable(%v) = false, want true", err)
	}
}

// Do must not retry an unconfigured resolution three times over — one
// resolve call, one immediate ErrBackendUnconfigured.
func TestDynamicUnconfiguredResolvesOnce(t *testing.T) {
	var resolves int
	r := NewDynamic(func(context.Context) (DynamicConfig, error) {
		resolves++
		return DynamicConfig{}, nil
	})
	var out classification
	if _, err := r.Do(context.Background(), Job{Prompt: "classify: x"}, &out); !errors.Is(err, ErrBackendUnconfigured) {
		t.Fatalf("err = %v, want ErrBackendUnconfigured", err)
	}
	if resolves != 1 {
		t.Errorf("resolves = %d, want 1 (no retry on a transport-level skip)", resolves)
	}
}

// noCompleterProvider stands in for kirocli: a provider.Provider that does
// not implement provider.Completer.
type noCompleterProvider struct{}

func (noCompleterProvider) Name() string            { return "no-completer" }
func (noCompleterProvider) Matrix() provider.Matrix { return provider.Matrix{} }
func (noCompleterProvider) Start(context.Context, provider.StartOptions) (provider.Session, error) {
	return nil, errors.New("not implemented")
}
func (noCompleterProvider) OneShot(context.Context, provider.OneShotRequest) (*provider.OneShotResult, error) {
	return nil, errors.New("not implemented")
}

// With no explicit endpoint, a Provider implementing provider.Completer
// (e.g. claude-code, or fake standing in for it here) becomes the backend —
// resolution order's second tier: endpoint > provider > off.
func TestDynamicFallsBackToProviderCompleter(t *testing.T) {
	f := fake.New()
	r := NewDynamic(func(context.Context) (DynamicConfig, error) {
		return DynamicConfig{Provider: f}, nil
	})
	text, _, err := r.DoText(context.Background(), Job{Prompt: "echo hi"})
	if err != nil {
		t.Fatal(err)
	}
	if text != "you said hi" {
		t.Errorf("text = %q, want %q", text, "you said hi")
	}
}

// An explicit endpoint always wins over a Completer-capable Provider — no
// ambiguity when both are present in DynamicConfig.
func TestDynamicEndpointWinsOverProvider(t *testing.T) {
	var endpointCalls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		endpointCalls++
		_, _ = fmt.Fprint(w, `{"choices":[{"message":{"content":"from endpoint"}}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`)
	}))
	defer srv.Close()

	r := NewDynamic(func(context.Context) (DynamicConfig, error) {
		return DynamicConfig{BaseURL: srv.URL, Provider: fake.New()}, nil
	})
	text, _, err := r.DoText(context.Background(), Job{Prompt: "echo hi"})
	if err != nil {
		t.Fatal(err)
	}
	if text != "from endpoint" {
		t.Errorf("text = %q, want %q (endpoint must win over provider)", text, "from endpoint")
	}
	if endpointCalls != 1 {
		t.Errorf("endpointCalls = %d, want 1", endpointCalls)
	}
}

// No explicit endpoint and a Provider that doesn't implement Completer (e.g.
// kirocli) must go dormant — no silent cross-provider fallback.
func TestDynamicNoEndpointNoCompleterIsUnconfigured(t *testing.T) {
	r := NewDynamic(func(context.Context) (DynamicConfig, error) {
		return DynamicConfig{Provider: noCompleterProvider{}}, nil
	})
	_, _, err := r.DoText(context.Background(), Job{Prompt: "x"})
	if !errors.Is(err, ErrBackendUnconfigured) {
		t.Fatalf("err = %v, want ErrBackendUnconfigured", err)
	}
}

// A resolve failure (e.g. the Data Service is unreachable) is a genuine
// transport error, not "unconfigured" — it must not be mistaken for either
// skippable sentinel.
func TestDynamicResolveErrorIsNotSkip(t *testing.T) {
	want := errors.New("data service unreachable")
	r := NewDynamic(func(context.Context) (DynamicConfig, error) {
		return DynamicConfig{}, want
	})
	_, _, err := r.DoText(context.Background(), Job{Prompt: "x"})
	if !errors.Is(err, want) {
		t.Fatalf("err = %v, want %v", err, want)
	}
	if IsSkippable(err) {
		t.Fatal("resolve error must not be treated as skippable")
	}
}
