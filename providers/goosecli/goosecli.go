// Package goosecli is the third provider adapter (spec §11, proposed
// W7.8): Block's open-source goose agent, driven through its first-class
// ACP server (`goose acp`, JSON-RPC 2.0 over newline-delimited stdio) for
// interactive sessions.
//
// Unlike kirocli, every wire claim behind this package is verified against
// a live binary: goose 1.48.0 captures are committed, sanitized, under
// testdata/captures/ (see its README for provenance), and the design —
// docs/GOOSE-ADAPTER-DESIGN.md — cross-checks them against the goose
// source. The ACP v1 spec (agentclientprotocol.com, pinned by the official
// agent-client-protocol-schema 1.5.0 crate) is normative: wire.go's types
// derive from the spec schema, the captures are evidence of goose's
// behavior within it, and any capture/spec disagreement is an upstream
// goose bug to report — never a shape this adapter bakes in.
//
// This file carries the core plumbing (Config, spawn env, process
// shutdown) and the interactive ACP transport entry point. One-shot, PTY,
// transcript normalization, model discovery, and the capability matrix +
// conformance run land with their own tasks.
//
// Everything here runs against a scripted stand-in binary in CI (see
// fakegoose_test.go) — the suite passes with no goose installed.
package goosecli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"time"

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

// Start launches (or with opts.Resume, reattaches to) an interactive ACP
// session. ModePTY (`goose session` under a PTY) lands with the PTY task —
// until then it is honestly unsupported rather than half-wired.
func (p *Provider) Start(ctx context.Context, opts provider.StartOptions) (provider.Session, error) {
	switch opts.Mode {
	case provider.ModeACP:
		return p.startACP(ctx, opts)
	default:
		return nil, fmt.Errorf("mode %q: %w", opts.Mode, provider.ErrUnsupported)
	}
}

// env builds the child environment: base (inherited or isolated per
// EnvMode) + config + per-session, via the shared agentenv helper so both
// modes' semantics are defined in exactly one place.
func (p *Provider) env(extra []string) []string {
	return agentenv.Build(p.cfg.EnvMode, os.Environ(), p.cfg.Env, p.cfg.EnvPassthrough, extra)
}

// closeWait shuts a process down gently: signal by closing its stdin, give
// it graceMs to exit, then kill.
func closeWait(cmd *exec.Cmd, grace time.Duration) {
	done := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(grace):
		_ = cmd.Process.Kill()
		<-done
	}
}
