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
	ss, ok := sess.(*streamSession)
	if !ok {
		_ = sess.Close()
		return nil, fmt.Errorf("%s acp model discovery: unexpected session type %T", e.cfg.Bin, sess)
	}
	// Delete before close: the probe's session is an artifact of asking a
	// question, not something the user started, and session/close alone
	// leaves it persisted.
	defer func() {
		ss.discard(ctx)
		_ = ss.Close()
	}()
	ss.mu.Lock()
	options := ss.configOptions
	selector := ss.models
	ss.mu.Unlock()

	// ACP's own selector wins where an agent ships both. It is the
	// standards-track surface, it is the finer-grained of the two (codex:
	// 15 entries against the configOption's 3), and it is the only one that
	// can name the live selection — so Default is honest here and
	// approximate at best from a configOption whose currentValue has been
	// flattened. modelSetter resolves the setter in the same order, which
	// is what keeps a discovered id selectable.
	if selector != nil && len(selector.AvailableModels) > 0 {
		models := make([]provider.Model, 0, len(selector.AvailableModels))
		for _, m := range selector.AvailableModels {
			name := m.Name
			if name == "" {
				name = m.ModelID
			}
			models = append(models, provider.Model{
				ID:      m.ModelID,
				Name:    name,
				Default: m.ModelID == selector.CurrentModelID,
			})
		}
		return models, nil
	}

	if e.cfg.ModelConfigOptionID != "" {
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
	}

	// Neither surface is present. That is a different fact from "offered an
	// empty list", and the caller must be able to tell them apart: an empty
	// list renders as a legitimately model-less agent, whereas this is the
	// engine being unable to ask. Returning nil, nil here is what made
	// hermes — 70 models, no configOptions — look model-less.
	if selector == nil && !hasOption(options, e.cfg.ModelConfigOptionID) {
		return nil, fmt.Errorf("%s exposes no model surface (neither session/new models nor a %q configOption): %w",
			e.cfg.Bin, e.cfg.ModelConfigOptionID, provider.ErrUnsupported)
	}
	// A surface exists and is genuinely empty: the agent conformed and
	// offered nothing.
	return nil, nil
}

func hasOption(options []configOption, id string) bool {
	if id == "" {
		return false
	}
	for _, o := range options {
		if o.ID == id {
			return true
		}
	}
	return false
}
