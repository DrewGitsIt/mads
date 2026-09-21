// Package goosecli holds the goose-specific residue of the goose adapter
// (spec §11, proposed W7.8): Block's open-source goose agent, whose ACP
// wire handling now lives in the generic engine at providers/acprpc —
// this package's former session/wire code was hoisted there and
// re-expressed as engine knobs (docs/DECLARATIVE-PROVIDERS-DESIGN.md
// §"providers/acprpc"). What remains is the capture-verified spawn
// configuration the acprpc binding task consumes next: Config, the env
// composition, and the goose 1.48.0 evidence base under
// testdata/captures/ (see its README for provenance), which also feeds
// the engine's replay tests.
package goosecli

import (
	"context"
	"fmt"
	"os"

	"github.com/drewkidwell/mads/pkg/agentenv"
	"github.com/drewkidwell/mads/providers/provider"
)

// Config adapts the binary to its surroundings. The zero value targets a
// stock install.
type Config struct {
	// Bin is the goose executable (default "goose"). Tests point this at a
	// scripted stand-in.
	Bin string
	// ExtraArgs are appended to every invocation.
	ExtraArgs []string
	// Env carries mads-injected KEY=VALUE pairs — resolved bound mads
	// credentials plus any other config-time values — added on top of the
	// base environment (see EnvMode) for every invocation.
	Env []string
	// EnvMode controls how much of the process environment the spawned
	// goose binary sees (pkg/agentenv). Empty behaves as inherit. goose
	// needs HOME/XDG to find ~/.config/goose/config.yaml — both are on the
	// isolated-mode base allowlist.
	EnvMode agentenv.Mode
	// EnvPassthrough is the list of parent env var NAMES let through in
	// EnvMode isolated, on top of the base allowlist. Ignored otherwise.
	EnvPassthrough []string
	// Builtins are goose extensions force-loaded on every spawn via
	// --with-builtin, regardless of the user's config.yaml. nil defaults to
	// ["developer"] so file/shell tools always exist (on goose ≤1.46 a
	// non-empty mcpServers param would otherwise suppress all config.yaml
	// extensions; 1.48 unions them — the explicit flag is correct on both).
	// An explicit empty slice loads none.
	Builtins []string
}

// Provider is the goose adapter. Registry name "goose"
// (docs/GOOSE-ADAPTER-DESIGN.md "Shape of the work").
type Provider struct {
	cfg Config
}

func New(cfg Config) *Provider {
	if cfg.Bin == "" {
		cfg.Bin = "goose"
	}
	if cfg.Builtins == nil {
		cfg.Builtins = []string{"developer"}
	}
	return &Provider{cfg: cfg}
}

func (p *Provider) Name() string { return "goose" }

// Start is unsupported until the acprpc binding task wires this Config
// into a providers/acprpc engine (spawn args `acp --with-builtin ...`,
// the goose knob set, trust/mode mapping) — honestly unsupported rather
// than half-wired in the meantime.
func (p *Provider) Start(_ context.Context, opts provider.StartOptions) (provider.Session, error) {
	return nil, fmt.Errorf("mode %q: %w", opts.Mode, provider.ErrUnsupported)
}

// env builds the child environment: base (inherited or isolated per
// EnvMode) + config + per-session, via the shared agentenv helper so both
// modes' semantics are defined in exactly one place.
//
//nolint:unused // forward-built for the acprpc binding task (goose-adapter-tasks.yaml), which threads it into the engine's BaseEnv/Env seams
func (p *Provider) env(extra []string) []string {
	return agentenv.Build(p.cfg.EnvMode, os.Environ(), p.cfg.Env, p.cfg.EnvPassthrough, extra)
}
