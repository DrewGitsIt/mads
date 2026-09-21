package acprpc

import (
	"context"
	"fmt"
	"os"

	"github.com/drewkidwell/mads/providers/provider"
)

// DiscoverModels probes the agent for its selectable models — the engine
// helper any binding's ModelDiscoverer delegates to: spawn, initialize,
// session/new in a throwaway temp dir, read the ModelConfigOptionID
// configOption's options and currentValue (→ Default) off the session
// result, then Close (which already makes the best-effort, capability-gated
// session/close call). No LLM call is involved.
func (e *Engine) DiscoverModels(ctx context.Context) ([]provider.Model, error) {
	if e.cfg.ModelConfigOptionID == "" {
		// The agent has no model surface at all — mirrors Start's stance on
		// a requested model with the knob empty.
		return nil, fmt.Errorf("agent has no model config option: %w", provider.ErrUnsupported)
	}
	tmp, err := os.MkdirTemp("", "mads-acprpc-discover-")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(tmp) }()

	// ANY Start failure — missing binary, exec failure, initialize failure —
	// is the "expected runtime condition" class provider.ErrDiscoveryUnavailable
	// marks (the kirocli Models precedent), never masked by a static fallback.
	sess, err := e.Start(ctx, provider.StartOptions{Mode: provider.ModeACP, Cwd: tmp, Trust: provider.TrustFull})
	if err != nil {
		return nil, fmt.Errorf("%s acp model discovery: %w: %w", e.cfg.Bin, err, provider.ErrDiscoveryUnavailable)
	}
	defer func() { _ = sess.Close() }()

	ss, ok := sess.(*streamSession)
	if !ok {
		return nil, fmt.Errorf("%s acp model discovery: unexpected session type %T", e.cfg.Bin, sess)
	}
	ss.mu.Lock()
	options := ss.configOptions
	ss.mu.Unlock()
	for _, opt := range options {
		if opt.ID != e.cfg.ModelConfigOptionID {
			continue
		}
		models := make([]provider.Model, 0, len(opt.Options))
		for _, c := range opt.Options {
			models = append(models, provider.Model{ID: c.Value, Name: c.Name, Default: c.Value == opt.CurrentValue})
		}
		return models, nil
	}
	// The agent conformed and offered nothing (option absent or empty):
	// an empty list, not a discovery outage.
	return nil, nil
}
