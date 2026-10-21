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
	"github.com/drewkidwell/mads/pkg/binlocate"
	"github.com/drewkidwell/mads/providers/provider"
)

// Config adapts the binary to its surroundings. The zero value targets a
// stock install.
type Config struct {
	// Bin is the claude executable (default binlocate.Resolve("claude"),
	// which honours $MADS_AGENT_BIN_DIR). Tests point this at a scripted
	// stand-in.
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
	// LiveSessionsDir is where running claude processes register themselves,
	// one <pid>.json each (default ~/.claude/sessions) — ScanHistory skips
	// conversations whose process is still alive (history.go).
	LiveSessionsDir string
	// Models pins the model list, short-circuiting live discovery
	// (discover.go). Empty — the normal case — probes the binary instead.
	Models []provider.Model
}

type Provider struct {
	cfg Config
}

func New(cfg Config) *Provider {
	if cfg.Bin == "" {
		cfg.Bin = binlocate.Resolve("claude")
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
	if cfg.LiveSessionsDir == "" {
		if home, err := os.UserHomeDir(); err == nil {
			cfg.LiveSessionsDir = filepath.Join(home, ".claude", "sessions")
		}
	}
	return &Provider{cfg: cfg}
}

func (p *Provider) Name() string { return "claude-code" }

func (p *Provider) Matrix() provider.Matrix {
	return provider.Matrix{
		provider.BehaviorInteractive: provider.Native,
		// Interrupt rides a stream-json control_request rather than a
		// harness API call.
		provider.BehaviorInterrupt:  provider.Emulated,
		provider.BehaviorOneShot:    provider.Native,
		provider.BehaviorResume:     provider.Native,
		provider.BehaviorTranscript: provider.Native,
		// `list_models` over stream-json returns the harness's own
		// catalog — aliases included — so nothing is pinned (discover.go).
		provider.BehaviorModelDiscovery: provider.Native,
		provider.BehaviorCostReporting:  provider.Native,
		provider.BehaviorACP:            provider.Native,
		provider.BehaviorPTY:            provider.Native,
		// Fork-on-resume only: StartOptions.Fork becomes `--resume <ref>
		// --fork-session` (fork.go) on both transports. Forking at an
		// earlier turn (rewind) is not supported — claude has no scriptable
		// flag for it.
		provider.BehaviorBranching: provider.Native,
		provider.BehaviorCwdReroot: provider.Unsupported,
		// Limited is claude's own manual permission mode — claude decides
		// what is risky and asks; read-only adds --disallowedTools, which
		// removes the command and file-mutating tools from the palette
		// entirely. Same flags on both transports; on chat the prompts
		// reach the user over --permission-prompt-tool stdio, on the
		// terminal claude's own UI asks (trustArgs, README).
		provider.BehaviorTrustLimited:  provider.Native,
		provider.BehaviorTrustReadOnly: provider.Native,
		// Auto-review is claude's own auto mode: its classifier approves
		// routine calls and blocks risky ones; after a run of blocks it
		// stops judging and asks, which on chat reaches the user over the
		// same --permission-prompt-tool stdio channel (trustArgs; README
		// "Auto-review: claude's auto mode", with the probe evidence).
		provider.BehaviorTrustAutoReview: provider.Native,
		// --mcp-config + --strict-mcp-config threads a real MCP server into
		// the launch on both transports — see mcp.go and README.
		provider.BehaviorMCPServers: provider.Native,
		// OnEvent set ⇒ --output-format stream-json --verbose, the same
		// wire format the interactive transport already parses.
		provider.BehaviorOneShotStream: provider.Native,
		// `claude -p … --resume <ref>`: the interactive path's flag
		// (resumeArgs), and the same session id continues in place.
		provider.BehaviorOneShotResume: provider.Native,
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
		// "/compact [instructions]" sent as a user turn on the structured
		// transport (streamSession.Compact) runs claude's own pass, and the
		// CLI advertises the instructions argument itself ("<optional
		// custom summarization instructions>" — acprpc testdata/captures/
		// claude-agent-acp-full.jsonl). The terminal transport has no
		// Compactor; there the user types /compact into claude directly.
		provider.BehaviorCompaction:      provider.Native,
		provider.BehaviorCompactionFocus: provider.Native,
		// Delegates (docs/DELEGATES-DESIGN.md): task_*/background_tasks_changed
		// lines carry a subagent's lifecycle past its spawning call; its own
		// transcript sits under <ref>/subagents/ (InspectDelegate). Steering is
		// a relay — the parent's SendMessage — never direct (probed on
		// 2.1.285). Stopping one alone (stop_task) isn't wired yet.
		provider.BehaviorDelegateLifecycle:  provider.Native,
		provider.BehaviorDelegateTranscript: provider.Native,
		provider.BehaviorDelegateSteer:      provider.Emulated,
		provider.BehaviorDelegateStop:       provider.Unsupported,
		// Keys are claude's own ids, written the same on stdout and in the
		// JSONL: message id + block index, tool_use id, the user line's
		// uuid (stdin "uuid" through SendKeyed) — keys.go, README.
		provider.BehaviorStableKeys:     provider.Native,
		provider.BehaviorStoredThinking: provider.Unsupported, // C2: flipped with each reader's thinking rows
	}
}

// IntentMatrix declares this provider's enforcement of the portable
// permission intents (pkg/contracts.PermissionIntent): deny_commands and
// read_only are native via --disallowedTools (trustArgs' read-only set, and
// a persona's denied "shell" intent — personaTools.go); require_approval
// is native via claude's own manual permission mode, whose prompts reach
// the user over the stream-json host channel (--permission-prompt-tool
// stdio, hostprompt.go) on chat and claude's own UI on the terminal; and
// path_scope is native via the permission.go PreToolUse *path-scope* hook
// (Option A, agent-concept redesign §2) — a local POSIX sh containment
// check on Write|Edit|MultiEdit|NotebookEdit wired from
// StartOptions.PersonaAllowedPaths, independent of trust.
func (p *Provider) IntentMatrix() provider.IntentMatrix {
	return provider.IntentMatrix{
		provider.IntentDenyCommands:    provider.Native,
		provider.IntentPathScope:       provider.Native,
		provider.IntentReadOnly:        provider.Native,
		provider.IntentRequireApproval: provider.Native,
	}
}

// trustArgs maps a trust level to the CLI flag(s) that enforce it.
//
//   - TrustLimited ("the agent asks me before anything risky") runs claude's
//     own "manual" permission mode — the CLI's canonical name for its
//     normal ask-when-risky mode (the init line reports it as "default") —
//     so claude itself decides what needs asking: a Bash call, a write
//     outside the working directory. Named explicitly so a user's own
//     settings.json defaultMode can't quietly loosen it.
//   - TrustAutoReview runs claude's "auto" mode: a server-side classifier
//     judges each call that would otherwise need approval, allowing the
//     routine ones and denying the risky ones outright (the model sees "…
//     denied by the Claude Code auto mode classifier", and works around or
//     reports it). It does not ask per block: only after 3 consecutive (or
//     20 total) blocks does claude stop trusting its own reviewer and ask —
//     a can_use_tool request on the host channel, the same card limited
//     shows. Safety-check prompts the classifier never sees ask the same
//     way. Probed against 2.1.285, README "Auto-review: claude's auto
//     mode". Not every model runs it: claude silently starts a haiku
//     session in "default" (= manual) instead, which is limited — safer,
//     just chattier.
//   - TrustReadOnly is the same mode with every command and file-mutating
//     tool removed from the palette by --disallowedTools: a hard guarantee,
//     not a prompt. Whatever still asks asks the same way.
//   - TrustFull (and the zero value) is --permission-mode
//     bypassPermissions: claude never asks. Deliberately not
//     --dangerously-skip-permissions, which stays reserved for the
//     separate pipeline engine's own autonomous-agent invocation
//     (cmd/mads/run.go's -claude-args) — see README.
//
// hostAsks says who answers claude's prompts. True (the chat transport with
// a StartOptions.Permissions asker) adds --permission-prompt-tool stdio:
// each prompt then arrives as a can_use_tool control_request on stdout and
// mads answers it on stdin (hostprompt.go). False leaves claude's native
// answerer in place — a human at the terminal on the PTY transport; with
// nobody there, as on a chat session with no asker, claude refuses the
// call outright ("… but you haven't granted it yet.") rather than stalling.
func trustArgs(trust provider.Trust, hostAsks bool) []string {
	var args []string
	switch trust {
	case provider.TrustLimited:
		args = []string{"--permission-mode", "manual"}
	case provider.TrustAutoReview:
		args = []string{"--permission-mode", "auto"}
	case provider.TrustReadOnly:
		args = []string{"--permission-mode", "manual", "--disallowedTools", "Bash,Write,Edit,NotebookEdit"}
	default:
		return []string{"--permission-mode", "bypassPermissions"}
	}
	if hostAsks {
		args = append(args, "--permission-prompt-tool", "stdio")
	}
	return args
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
	args = append(args, oneShotResumeArgs(req)...)
	args = append(args, p.cfg.ExtraArgs...)
	baseline := p.resumeCostBaseline(req) // before the run appends its own cost-state

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
	res, err := parseOneShot(stdout.Bytes())
	return withCostBaseline(res, err, baseline)
}

// oneShotResumeArgs is a one-shot's --resume half: the interactive path's
// own resumeArgs, never forked. `claude -p <prompt> --resume <ref>` appends
// the prompt to that session's transcript and keeps its session id, so the
// result's session_id (OneShotResult.Ref, EventSessionRef) is req.Resume.
func oneShotResumeArgs(req provider.OneShotRequest) []string {
	args, _ := resumeArgs(provider.StartOptions{Resume: req.Resume})
	return args
}

// resumeCostBaseline is what a resumed one-shot's conversation had already
// cost: claude's total_cost_usd is the conversation's running total,
// restored from the transcript on --resume the same way a resumed
// interactive session's is (streamSession.costSeen). Read before the run,
// which appends its own cost-state as it ends. Zero for a fresh one-shot.
func (p *Provider) resumeCostBaseline(req provider.OneShotRequest) float64 {
	if req.Resume == "" {
		return 0
	}
	return p.restoredCostTotal(req.Resume)
}

// withCostBaseline makes a result's CostUSD this invocation's own cost by
// subtracting baseline (resumeCostBaseline). A total below the baseline is
// taken as a count that started over: all of it new.
func withCostBaseline(res *provider.OneShotResult, err error, baseline float64) (*provider.OneShotResult, error) {
	if err == nil && baseline > 0 && res.Usage.CostUSD >= baseline {
		res.Usage.CostUSD -= baseline
	}
	return res, err
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
	args = append(args, oneShotResumeArgs(req)...)
	args = append(args, p.cfg.ExtraArgs...)
	baseline := p.resumeCostBaseline(req) // before the run appends its own cost-state

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
	var keys liveKeys
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
					for _, ev := range l.keyedEvents(false, false, keys.line(&l)) {
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
	res, err := parseOneShot(resultLine)
	return withCostBaseline(res, err, baseline)
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
		res.Usage.ContextTokens = l.Usage.contextTokens()
		res.Usage.ContextWindow = l.contextWindowFor(l.model())
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
