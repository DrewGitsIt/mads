package fastmodel

import (
	"context"

	"github.com/drewkidwell/mads/providers/provider"
)

// DynamicConfig is a single resolution of the Fast Model backend — what a
// NewDynamic Runner's resolve func returns on every call.
type DynamicConfig struct {
	// Model is the model name to use (empty = the endpoint's own default).
	Model string
	// BaseURL selects the OpenAI-compatible endpoint. Empty means "no Fast
	// Model backend configured": Complete returns ErrBackendUnconfigured
	// immediately, no network call made.
	BaseURL string
	// APIKeyEnv names the environment variable holding the endpoint
	// credential — names only, never values (spec §13).
	APIKeyEnv string
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
	if cfg.BaseURL == "" {
		return "", provider.Usage{}, ErrBackendUnconfigured
	}
	if model == "" {
		model = cfg.Model
	}
	backend := &openAIBackend{baseURL: cfg.BaseURL, apiKeyEnv: cfg.APIKeyEnv}
	return backend.Complete(ctx, system, prompt, model)
}
