package fastmodel

import (
	"context"

	"github.com/drewkidwell/mads/providers/provider"
)

// DynamicConfig is a single resolution of the Fast Model backend — what a
// NewDynamic Runner's resolve func returns on every call. Resolution order:
// BaseURL (explicit endpoint) wins if set; else Provider is used if it
// implements provider.Completer; else the backend is unconfigured. No
// silent cross-provider fallback — a Provider that doesn't implement
// Completer leaves Fast Model dormant, it never substitutes another
// provider on its own.
type DynamicConfig struct {
	// Model is the model name to use (empty = the endpoint's own default).
	Model string
	// BaseURL selects the OpenAI-compatible endpoint. Empty means "no
	// explicit endpoint configured" — falls through to Provider.
	BaseURL string
	// APIKeyEnv names the environment variable holding the endpoint
	// credential — names only, never values (spec §13).
	APIKeyEnv string
	// Provider is the user's default provider, used via its hermetic
	// provider.Completer capability when BaseURL is empty. Nil, or a
	// provider that doesn't implement Completer, leaves Fast Model
	// unconfigured (ErrBackendUnconfigured) rather than falling back to
	// anything else.
	Provider provider.Provider
}

// NewDynamic builds a Runner whose backend is re-resolved via resolve on
// every call, so a Settings change (or the -fastmodel-* flag fallback it
// resolves to) takes effect without a router restart — resolution happens
// per job spawn, since each job makes exactly one series of Complete calls.
func NewDynamic(resolve func(ctx context.Context) (DynamicConfig, error)) *Runner {
	return NewWithBackend(&dynamicBackend{resolve: resolve}, Config{})
}

// dynamicBackend defers backend selection to resolve on every call, rather
// than fixing it at construction time the way New/NewWithBackend do.
type dynamicBackend struct {
	resolve func(ctx context.Context) (DynamicConfig, error)
}

func (b *dynamicBackend) Complete(ctx context.Context, system, prompt, model string) (string, provider.Usage, error) {
	cfg, err := b.resolve(ctx)
	if err != nil {
		return "", provider.Usage{}, err
	}
	if model == "" {
		model = cfg.Model
	}
	if cfg.BaseURL != "" {
		backend := &openAIBackend{baseURL: cfg.BaseURL, apiKeyEnv: cfg.APIKeyEnv}
		return backend.Complete(ctx, system, prompt, model)
	}
	completer, ok := cfg.Provider.(provider.Completer)
	if !ok {
		return "", provider.Usage{}, ErrBackendUnconfigured
	}
	res, err := completer.Complete(ctx, provider.CompleteRequest{System: system, Prompt: prompt, Model: model})
	if err != nil {
		return "", provider.Usage{}, err
	}
	return res.Text, res.Usage, nil
}
