// Package kirocli is the second provider adapter (spec §11, work item
// W7.1): kiro-cli driven through its `acp` JSON-RPC agent for interactive
// sessions, `kiro-cli chat --no-interactive` for one-shots, `--resume-id`
// for reattach, and ACP's session/load replay for normalized history.
//
// Every claim behind the Matrix in this package is sourced from kiro-cli's
// public docs and GitHub issues, not from running the binary — it is not
// installed on this machine. See README.md for the honest justification
// behind every row, and the risks that come with designing against docs
// instead of a live process.
//
// Everything here runs against a scripted stand-in binary in CI (see
// fakekiro_test.go) — the suite passes with no kiro-cli installed. Set
// MADS_KIRO_SMOKE=1 with a real binary on PATH for the live smoke test.
package kirocli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/drewkidwell/mads/providers/provider"
)

// Config adapts the binary to its surroundings. The zero value targets a
// stock install.
type Config struct {
	// Bin is the kiro-cli executable (default "kiro-cli"). Tests point this
	// at a scripted stand-in.
	Bin string
	// ExtraArgs are appended to every invocation.
	ExtraArgs []string
	// Env is appended to the inherited environment for every invocation.
	Env []string
	// Models is a static fallback list. Currently unused: Models() always
	// calls `--list-models` directly, because ModelDiscovery is declared
	// Native — silently falling back here on a broken discovery call would
	// make that declaration a quiet lie (the exact class of bug conformance
	// exists to catch). Kept for a future caller that wants a list without
	// invoking the binary.
	Models []provider.Model
}

type Provider struct {
	cfg Config
}

func New(cfg Config) *Provider {
	if cfg.Bin == "" {
		cfg.Bin = "kiro-cli"
	}
	return &Provider{cfg: cfg}
}

func (p *Provider) Name() string { return "kiro-cli" }

// Matrix is the honest §11 capability declaration for kiro-cli — see
// README.md for the citation behind every row. Net shape vs. claude-code:
// more capable on Interrupt/Transcript/ModelDiscovery (Native vs.
// Emulated), less capable on CostReporting (Unsupported vs. Native).
func (p *Provider) Matrix() provider.Matrix {
	return provider.Matrix{
		provider.BehaviorInteractive:    provider.Native,
		provider.BehaviorInterrupt:      provider.Native,
		provider.BehaviorOneShot:        provider.Native,
		provider.BehaviorResume:         provider.Native,
		provider.BehaviorTranscript:     provider.Native,
		provider.BehaviorModelDiscovery: provider.Native,
		provider.BehaviorCostReporting:  provider.Unsupported,
		provider.BehaviorACP:            provider.Native,
		provider.BehaviorPTY:            provider.Native,
		provider.BehaviorBranching:      provider.Unsupported,
		provider.BehaviorCwdReroot:      provider.Unsupported,
	}
}

// Models runs `kiro-cli chat --list-models --format json` and parses the
// result. A parse or exec failure is returned as an error, never masked by
// a static fallback — see Config.Models.
func (p *Provider) Models(ctx context.Context) ([]provider.Model, error) {
	args := append([]string{"chat", "--list-models", "--format", "json"}, p.cfg.ExtraArgs...)
	cmd := exec.CommandContext(ctx, p.cfg.Bin, args...)
	cmd.Env = p.env(nil)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = strings.TrimSpace(stdout.String())
		}
		return nil, fmt.Errorf("kiro-cli chat --list-models: %w: %s: %w", err, detail, provider.ErrDiscoveryUnavailable)
	}
	var wire []wireModel
	if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &wire); err != nil {
		return nil, fmt.Errorf("kiro-cli chat --list-models output: %w", err)
	}
	models := make([]provider.Model, 0, len(wire))
	for _, m := range wire {
		models = append(models, provider.Model{ID: m.ModelID, Name: m.ModelName, Default: m.IsDefault})
	}
	return models, nil
}

func (p *Provider) Start(ctx context.Context, opts provider.StartOptions) (provider.Session, error) {
	switch opts.Mode {
	case provider.ModeACP:
		return p.startACP(ctx, opts)
	case provider.ModePTY:
		return p.startPTY(opts)
	default:
		return nil, fmt.Errorf("mode %q: %w", opts.Mode, provider.ErrUnsupported)
	}
}

// OneShot runs a single non-interactive invocation. Unlike claude-code's
// `--output-format json`, kiro-cli has no documented structured one-shot
// output — stdout is captured as plain text and Usage stays zero-valued,
// consistent with CostReporting=Unsupported.
func (p *Provider) OneShot(ctx context.Context, req provider.OneShotRequest) (*provider.OneShotResult, error) {
	args := []string{"chat", "--no-interactive", "--trust-all-tools", req.Prompt}
	if req.Model != "" {
		args = append(args, "--model", req.Model)
	}
	args = append(args, p.cfg.ExtraArgs...)

	cmd := exec.CommandContext(ctx, p.cfg.Bin, args...)
	cmd.Dir = req.Cwd
	cmd.Env = p.env(nil)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		if rle := rateLimit(stderr.String() + stdout.String()); rle != nil {
			return nil, rle
		}
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = strings.TrimSpace(stdout.String())
		}
		if len(detail) > 600 {
			detail = detail[:600] + " …"
		}
		return nil, fmt.Errorf("kiro-cli chat --no-interactive: %w: %s", err, detail)
	}
	return &provider.OneShotResult{Text: strings.TrimSpace(stdout.String())}, nil
}

// rateLimit turns kiro-cli's rate-limit output into the typed error
// (§11.9). No real captured rate-limit message exists to pin against (kiro
// is not installed here) — these phrases are a best-effort guess at
// kiro-appropriate wording, exercised only by the fake's own dialect.
func rateLimit(out string) *provider.RateLimitError {
	low := strings.ToLower(out)
	limited := strings.Contains(low, "rate limit") ||
		strings.Contains(low, "quota exceeded") ||
		strings.Contains(low, "try again later")
	if !limited {
		return nil
	}
	return &provider.RateLimitError{Message: strings.TrimSpace(firstLine(out))}
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// env builds the child environment: inherited + config + per-session.
func (p *Provider) env(extra []string) []string {
	env := append(os.Environ(), p.cfg.Env...)
	return append(env, extra...)
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
