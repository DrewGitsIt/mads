// Package kirocli is the second provider adapter (spec §11, work item
// W7.1): kiro-cli driven through its `acp` JSON-RPC agent for interactive
// sessions, `kiro-cli chat --no-interactive` for one-shots, `--resume-id`
// for reattach, and ACP's session/load replay for normalized history.
//
// The ACP transport is the generic providers/acprpc engine, parameterized
// by the knob values in startACP — this package carries no JSON-RPC
// plumbing of its own (the hand-rolled wire/session layer it used to have
// is superseded; README.md "Engine migration" records the two spec
// deviations that died with it). The PTY and plain-CLI strategies
// (one-shot, model discovery, rate-limit detection) remain this package's
// own.
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

	"github.com/drewkidwell/mads/pkg/agentenv"
	"github.com/drewkidwell/mads/providers/acprpc"
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
	// Env carries mads-injected KEY=VALUE pairs — resolved bound mads
	// credentials plus any other config-time values — added on top of the
	// base environment (see EnvMode) for every invocation.
	Env []string
	// EnvMode controls how much of the process environment the spawned
	// kiro-cli binary sees (pkg/agentenv). Empty behaves as inherit.
	EnvMode agentenv.Mode
	// EnvPassthrough is the list of parent env var NAMES let through in
	// EnvMode isolated, on top of the base allowlist. Ignored otherwise.
	EnvPassthrough []string
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
		// Emulated: enforcement rides the ACP session/request_permission
		// handshake (kind-based auto-deny) on the structured transport, and a
		// human's own interactive confirmation on PTY — neither is a harness
		// guarantee the way claude-code's --disallowedTools is. See README.
		provider.BehaviorTrustLimited:  provider.Emulated,
		provider.BehaviorTrustReadOnly: provider.Emulated,
		// The acprpc engine now rides descriptors on session/new (a spec
		// obligation), but no kiro doc confirms the agent actually connects
		// them — the row stays declared-unsupported until MADS_KIRO_SMOKE
		// against a real binary says otherwise. See README.md.
		provider.BehaviorMCPServers: provider.Unsupported,
		// No streaming JSON mode on `kiro-cli chat --no-interactive` — see
		// README.md. OneShot never calls OnEvent; the engine falls back to
		// a single end-of-run chunk.
		provider.BehaviorOneShotStream: provider.Unsupported,
		// ACP's session/new response carries the ref synchronously, before
		// Start even returns (the acprpc engine's Start) — no announcement
		// subscription needed there. The terminal transport prints nothing and has no
		// on-disk transcript format to discover from (unlike claude-code),
		// so a fresh PTY session's ref is never learned. Emulated as the
		// one-row summary across both transports. See README.
		provider.BehaviorRefAnnounce: provider.Emulated,
	}
}

// IntentMatrix declares this provider's enforcement of the portable
// permission intents (pkg/contracts.PermissionIntent). Unlike the
// Behavior matrix's BehaviorTrustLimited/BehaviorTrustReadOnly rows
// (Emulated — the acprpc engine's ACP kind-based auto-deny, or a human's own
// PTY confirmation), no intent here has a harness-level enforcement
// mechanism: kiro-cli never reads StartOptions.PermissionPromptURL (no
// require_approval wiring exists at all), has no command blocklist flag, no
// path-scoped write restriction, and no flag that blocks writes/commands
// outright. Every intent is declared unenforced honestly rather than
// implying otherwise; this task does not add kiro-cli enforcement.
func (p *Provider) IntentMatrix() provider.IntentMatrix {
	return provider.IntentMatrix{
		provider.IntentDenyCommands:    provider.Unsupported,
		provider.IntentPathScope:       provider.Unsupported,
		provider.IntentReadOnly:        provider.Unsupported,
		provider.IntentRequireApproval: provider.Unsupported,
	}
}

// wireModel is one entry of `kiro-cli chat --list-models --format json` —
// plain CLI output, not part of the ACP wire (which lives entirely in
// providers/acprpc).
type wireModel struct {
	ModelID   string `json:"model_id"`
	ModelName string `json:"model_name"`
	IsDefault bool   `json:"is_default"`
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

// startACP launches kiro-cli's structured transport — `kiro-cli acp`, a
// standards-compliant JSON-RPC 2.0 agent over stdio — through the generic
// providers/acprpc engine. These knob values ARE kirocli's ACP surface;
// each is doc-sourced (kiro.dev/docs/cli/acp), and where the old
// hand-rolled layer encoded a guess the spec-normative engine contradicts,
// the engine wins (README.md "Engine migration"):
//   - SpawnArgs: the documented `acp` entry point, plus ExtraArgs.
//   - BaseEnv: this package's own env() so inherit/isolated semantics stay
//     defined once (pkg/agentenv); the engine appends opts.Env per session.
//   - ToolIdentity KindBased: kiro sets the spec kind taxonomy on its tool
//     calls, so the trust gate classifies the wire kind — including the
//     identity embedded in a session/request_permission that arrives
//     before any tool_call announcement.
//   - No TrustModes, SendModeAtSessionNew false: kiro documents no session
//     mode vocabulary, and the old layer sent none; enforcement rides the
//     engine's permission-reply gate — the same Emulated stance as before.
//   - ModelConfigOptionID empty: kiro documents no model configOption, so
//     an ACP Start naming a model fails wrapping provider.ErrUnsupported
//     instead of riding the old non-spec modelId param on session/new.
func (p *Provider) startACP(ctx context.Context, opts provider.StartOptions) (provider.Session, error) {
	return acprpc.New(acprpc.Config{
		Bin:          p.cfg.Bin,
		SpawnArgs:    append([]string{"acp"}, p.cfg.ExtraArgs...),
		BaseEnv:      p.env(nil),
		ToolIdentity: acprpc.ToolIdentity{Mode: acprpc.ToolIdentityKindBased},
	}).Start(ctx, opts)
}

// OneShot runs a single non-interactive invocation. Unlike claude-code's
// `--output-format json`, kiro-cli has no documented structured one-shot
// output — stdout is captured as plain text and Usage stays zero-valued,
// consistent with CostReporting=Unsupported.
func (p *Provider) OneShot(ctx context.Context, req provider.OneShotRequest) (*provider.OneShotResult, error) {
	args := []string{"chat", "--no-interactive", req.Prompt}
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
