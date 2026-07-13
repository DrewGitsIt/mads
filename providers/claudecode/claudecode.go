// Package claudecode is the reference provider adapter (spec §11): Claude
// Code driven through its stream-json interface for interactive sessions,
// `claude -p` for one-shots, `--resume` for reattach, and the on-disk
// project transcripts for normalized history.
//
// Everything here runs against a scripted stand-in binary in CI (see
// fakeclaude_test.go) — the suite passes with no claude installed. Set
// MADS_CLAUDE_SMOKE=1 with a real binary on PATH for the live smoke test.
package claudecode

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/drewkidwell/mads/providers/provider"
)

// Config adapts the binary to its surroundings. The zero value targets a
// stock install.
type Config struct {
	// Bin is the claude executable (default "claude"). Tests point this at
	// a scripted stand-in.
	Bin string
	// ExtraArgs are appended to every invocation — permission flags land
	// here (agent permissions, spec §13, are configured per session by the
	// Session Service).
	ExtraArgs []string
	// Env is appended to the inherited environment for every invocation.
	Env []string
	// TranscriptDir is where claude-code keeps project transcripts
	// (default ~/.claude/projects).
	TranscriptDir string
	// Models overrides the pinned discovery list.
	Models []provider.Model
}

// DefaultModels is the pinned model list — claude-code has no discovery
// call, hence model_discovery: emulated in the matrix.
var DefaultModels = []provider.Model{
	{ID: "sonnet", Name: "Claude Sonnet", Default: true},
	{ID: "opus", Name: "Claude Opus"},
	{ID: "haiku", Name: "Claude Haiku"},
}

type Provider struct {
	cfg Config
}

func New(cfg Config) *Provider {
	if cfg.Bin == "" {
		cfg.Bin = "claude"
	}
	if cfg.TranscriptDir == "" {
		if home, err := os.UserHomeDir(); err == nil {
			cfg.TranscriptDir = filepath.Join(home, ".claude", "projects")
		}
	}
	if len(cfg.Models) == 0 {
		cfg.Models = DefaultModels
	}
	return &Provider{cfg: cfg}
}

func (p *Provider) Name() string { return "claude-code" }

func (p *Provider) Matrix() provider.Matrix {
	return provider.Matrix{
		provider.BehaviorInteractive: provider.Native,
		// Interrupt rides a stream-json control_request rather than a
		// harness API call.
		provider.BehaviorInterrupt:      provider.Emulated,
		provider.BehaviorOneShot:        provider.Native,
		provider.BehaviorResume:         provider.Native,
		provider.BehaviorTranscript:     provider.Native,
		provider.BehaviorModelDiscovery: provider.Emulated,
		provider.BehaviorCostReporting:  provider.Native,
		provider.BehaviorACP:            provider.Native,
		provider.BehaviorPTY:            provider.Native,
		provider.BehaviorBranching:      provider.Unsupported,
		provider.BehaviorCwdReroot:      provider.Unsupported,
	}
}

func (p *Provider) Models(context.Context) ([]provider.Model, error) {
	return append([]provider.Model{}, p.cfg.Models...), nil
}

func (p *Provider) Start(ctx context.Context, opts provider.StartOptions) (provider.Session, error) {
	switch opts.Mode {
	case provider.ModeACP:
		return p.startStream(ctx, opts)
	case provider.ModePTY:
		return p.startPTY(opts)
	default:
		return nil, fmt.Errorf("mode %q: %w", opts.Mode, provider.ErrUnsupported)
	}
}

func (p *Provider) OneShot(ctx context.Context, req provider.OneShotRequest) (*provider.OneShotResult, error) {
	args := []string{"-p", req.Prompt, "--output-format", "json"}
	if req.Model != "" {
		args = append(args, "--model", req.Model)
	}
	if req.System != "" {
		args = append(args, "--append-system-prompt", req.System)
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
		// Failures land in blocker notes; carry whichever stream has the
		// story (a usage-limit death writes JSON to stdout, stderr empty).
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = strings.TrimSpace(stdout.String())
		}
		if len(detail) > 600 {
			detail = detail[:600] + " …"
		}
		return nil, fmt.Errorf("claude -p: %w: %s", err, detail)
	}
	return parseOneShot(stdout.Bytes())
}

// parseOneShot decodes the --output-format json result object.
func parseOneShot(raw []byte) (*provider.OneShotResult, error) {
	var l wireLine
	if err := json.Unmarshal(bytes.TrimSpace(raw), &l); err != nil {
		return nil, fmt.Errorf("claude -p output: %w", err)
	}
	if l.IsError {
		if rle := rateLimit(l.Result); rle != nil {
			return nil, rle
		}
		return nil, fmt.Errorf("claude -p: %s", l.Result)
	}
	res := &provider.OneShotResult{Text: l.Result}
	if l.Usage != nil {
		res.Usage = provider.Usage{
			InputTokens:  l.Usage.InputTokens,
			OutputTokens: l.Usage.OutputTokens,
			Model:        l.model(),
		}
	}
	return res, nil
}

// rateLimit turns claude's rate-limit output into the typed error (§11.9).
// The phrasing varies ("rate limit", "usage limit", "session limit" — the
// Wave-4 run hit that last one untyped), so also trust an explicit 429 on a
// result line's api_error_status.
func rateLimit(out string) *provider.RateLimitError {
	low := strings.ToLower(out)
	limited := strings.Contains(low, "rate limit") ||
		strings.Contains(low, "usage limit") ||
		strings.Contains(low, "session limit") ||
		strings.Contains(out, `"api_error_status":429`)
	if !limited {
		return nil
	}
	msg := strings.TrimSpace(firstLine(out))
	// A result line's human story beats raw JSON in a blocker note.
	var line wireLine
	if json.Unmarshal([]byte(firstLine(out)), &line) == nil && line.Result != "" {
		msg = line.Result
	}
	return &provider.RateLimitError{Message: msg, RetryAfter: 0}
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
