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
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/drewkidwell/mads/pkg/agentenv"
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
	// Env carries mads-injected KEY=VALUE pairs — resolved bound mads
	// credentials plus any other config-time values — added on top of the
	// base environment (see EnvMode) for every invocation.
	Env []string
	// EnvMode controls how much of the process environment the spawned
	// claude binary sees (pkg/agentenv). Empty behaves as inherit.
	EnvMode agentenv.Mode
	// EnvPassthrough is the list of parent env var NAMES let through in
	// EnvMode isolated, on top of the base allowlist. Ignored otherwise.
	EnvPassthrough []string
	// TranscriptDir is where claude-code keeps project transcripts
	// (default ~/.claude/projects).
	TranscriptDir string
	// HistoryFile is claude-code's own append-only prompt-submission index
	// (default ~/.claude/history.jsonl) — LocateSession's rung 1 (locate.go).
	// A history scan is far cheaper than walking every transcript file, so
	// it runs first whenever the index has a usable entry; rung 2 (a direct
	// transcript content scan) is the fallback when it doesn't.
	HistoryFile string
	// Models overrides the pinned discovery list.
	Models []provider.Model
}

// DefaultModels is the pinned model list — claude-code has no discovery
// call, hence model_discovery: emulated in the matrix.
var DefaultModels = []provider.Model{
	{ID: "claude-sonnet-5", Name: "Claude Sonnet 5", Default: true},
	{ID: "claude-opus-4-8", Name: "Claude Opus 4.8"},
	{ID: "claude-haiku-4-5-20251001", Name: "Claude Haiku 4.5"},
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
	if cfg.HistoryFile == "" {
		if home, err := os.UserHomeDir(); err == nil {
			cfg.HistoryFile = filepath.Join(home, ".claude", "history.jsonl")
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
		// --disallowedTools removes tools from the model's palette entirely
		// (confirmed against `claude --help`) — the same flag on both
		// transports, see README.
		provider.BehaviorTrustLimited:  provider.Native,
		provider.BehaviorTrustReadOnly: provider.Native,
		// --mcp-config + --strict-mcp-config threads a real MCP server into
		// the launch on both transports — see mcp.go and README.
		provider.BehaviorMCPServers: provider.Native,
		// OnEvent set ⇒ --output-format stream-json --verbose, the same
		// wire format the interactive transport already parses.
		provider.BehaviorOneShotStream: provider.Native,
		// The structured transport's system/init line announces the ref at
		// connect (session.go). The terminal transport prints nothing, and a
		// structured session's own announcement can itself be dropped or
		// delayed past its window — either way, LocateSession (locate.go)
		// backstops it by content-correlating the Runner's exact first sent
		// message against claude-code's own local session index
		// (history.jsonl, rung 1) or its on-disk transcripts directly
		// (rung 2). Weaker and slower than the wire announcement, hence
		// Emulated as the one-row summary across both transports. See README.
		provider.BehaviorRefAnnounce: provider.Emulated,
	}
}

// trustArgs maps a trust level to the CLI flag(s) that enforce it. Limited
// and ReadOnly use --disallowedTools to remove tools from claude's palette
// entirely, so the interactive ask-first prompt they'd otherwise trigger
// never fires for a tool that's already gone. TrustFull (and the zero value)
// used to add nothing, leaving claude's default ask-first permission mode in
// effect — which stalls forever on a transport with nobody to answer the
// prompt (chat/ACP has no such surface; the pipeline confirmed this in
// terminal mode too). --permission-mode bypassPermissions fixes that; it's
// deliberately not --dangerously-skip-permissions, which stays reserved for
// the separate pipeline engine's own autonomous-agent invocation
// (cmd/mads/run.go's -claude-args) — see README.
func trustArgs(trust provider.Trust) []string {
	switch trust {
	case provider.TrustLimited:
		return []string{"--disallowedTools", "Bash"}
	case provider.TrustReadOnly:
		return []string{"--disallowedTools", "Bash,Write,Edit,NotebookEdit"}
	default:
		return []string{"--permission-mode", "bypassPermissions"}
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
	if req.OnEvent != nil {
		return p.oneShotStream(ctx, req)
	}
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

// oneShotStream runs the same one-shot invocation on the stream-json wire
// format so req.OnEvent sees text deltas and tool use as they arrive. The
// final result — parsed from the stream's terminal "result" line via the
// same parseOneShot used by the non-streaming path — is unchanged either way.
func (p *Provider) oneShotStream(ctx context.Context, req provider.OneShotRequest) (*provider.OneShotResult, error) {
	args := []string{"-p", req.Prompt, "--output-format", "stream-json", "--verbose"}
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
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %s: %w", p.cfg.Bin, err)
	}

	var resultLine []byte
	var refAnnounced bool
	r := bufio.NewReaderSize(stdout, 64*1024)
	for {
		raw, rerr := r.ReadBytes('\n')
		if len(raw) > 0 {
			var l wireLine
			if json.Unmarshal(bytes.TrimSpace(raw), &l) == nil {
				// session_id rides every wire line; announce it once, as
				// soon as it's known, so callers get exact transcript
				// attribution before the invocation finishes.
				if !refAnnounced && l.SessionID != "" {
					refAnnounced = true
					req.OnEvent(provider.Event{Kind: provider.EventSessionRef, Ref: l.SessionID})
				}
				switch l.Type {
				case "assistant":
					for _, ev := range l.events(false) {
						req.OnEvent(ev)
					}
				case "result":
					resultLine = append([]byte{}, bytes.TrimSpace(raw)...)
				}
			}
		}
		if rerr != nil {
			break
		}
	}
	if err := cmd.Wait(); err != nil {
		if rle := rateLimit(stderr.String()); rle != nil {
			return nil, rle
		}
		detail := strings.TrimSpace(stderr.String())
		if len(detail) > 600 {
			detail = detail[:600] + " …"
		}
		return nil, fmt.Errorf("claude -p: %w: %s", err, detail)
	}
	if resultLine == nil {
		return nil, fmt.Errorf("claude -p: no result line in stream-json output")
	}
	return parseOneShot(resultLine)
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
	// total_cost_usd is a top-level result-line field, independent of whether
	// the nested usage object is present — surface it unconditionally so a
	// response with cost but no token breakdown still feeds cost metrics.
	res := &provider.OneShotResult{Text: l.Result, Ref: l.SessionID, Usage: provider.Usage{CostUSD: l.TotalCostUSD}}
	if l.Usage != nil {
		res.Usage.InputTokens = l.Usage.InputTokens
		res.Usage.OutputTokens = l.Usage.OutputTokens
		res.Usage.CacheCreationInputTokens = l.Usage.CacheCreationInputTokens
		res.Usage.CacheReadInputTokens = l.Usage.CacheReadInputTokens
		res.Usage.Model = l.model()
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
