// Package goosecli is the goose provider adapter (spec §11, W7.8):
// Block's open-source goose agent, driven through the generic ACP
// engine at providers/acprpc. This binding is the engine's first
// instantiation (docs/DECLARATIVE-PROVIDERS-DESIGN.md §"providers/acprpc"):
// it supplies the capture-verified goose knob values — spawn args, the
// trust→mode table, the tool-class table, the system-prompt extension
// method — while every wire behavior stays in the engine. The goose 1.48.0
// evidence base lives under testdata/captures/ (see its README for
// provenance) and also feeds the engine's replay tests.
//
// The PTY transport (`goose session` with banner ref discovery) lives in
// pty.go, TranscriptReader (`session export`) in transcript.go, and the
// SessionLocator backstop (`session list` + export verification) in
// locate.go. Not yet wired, its own chained task: registration.
package goosecli

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/drewkidwell/mads/pkg/agentenv"
	"github.com/drewkidwell/mads/pkg/binlocate"
	"github.com/drewkidwell/mads/providers/acprpc"
	"github.com/drewkidwell/mads/providers/provider"
)

// Config adapts the binary to its surroundings. The zero value targets a
// stock install.
type Config struct {
	// Bin is the goose executable. Empty resolves it per invocation via
	// pkg/binlocate — PATH, then the CLI bundled inside goose Desktop — so
	// a goose installed after the process started is picked up, and
	// preflight's verdict matches what actually gets spawned. Tests point
	// this at a scripted stand-in.
	Bin string
	// ExtraArgs are appended to every agent-spawn invocation (`acp`,
	// `session` REPL). Bookkeeping subcommands (session export/list) omit
	// them — they are spawn flags and would break those invocations.
	ExtraArgs []string
	// Env carries mads-injected KEY=VALUE pairs — resolved bound mads
	// credentials plus any other config-time values — added on top of the
	// base environment (see EnvMode) for every invocation.
	Env []string
	// EnvMode controls how much of the process environment the spawned
	// goose binary sees (pkg/agentenv). Empty behaves as inherit. goose
	// needs HOME/XDG_CONFIG_HOME to find ~/.config/goose/config.yaml —
	// HOME is on the isolated-mode base allowlist, but XDG_CONFIG_HOME is
	// not and must ride EnvPassthrough (the service registration sites do
	// this).
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
	if cfg.Builtins == nil {
		cfg.Builtins = []string{"developer"}
	}
	return &Provider{cfg: cfg}
}

func (p *Provider) Name() string { return "goose" }

// bin is the executable every invocation runs: Config.Bin if set, else
// binlocate.Resolve's answer (which honours $MADS_AGENT_BIN_DIR).
func (p *Provider) bin() string {
	if p.cfg.Bin != "" {
		return p.cfg.Bin
	}
	return binlocate.Resolve("goose")
}

// gooseToolClassTable classifies goose's _meta tool identities onto the
// ACP kind taxonomy for the engine's trust gate; anything unlisted is
// unknown and denies below full trust. Audited at goose tag v1.48.0 (the
// captures' exact version); rows are additive across goose versions — a
// name goose no longer emits is a harmless row, an unlisted one denies.
// ACP names are BARE per _meta ("shell", not "developer__shell"): the
// extension identity rides the separate extensionName field
// (capture-pinned, testdata/captures/acp-transcript.jsonl line 43).
var gooseToolClassTable = []acprpc.ToolClassRule{
	{Extension: "developer", Name: "shell", Class: "execute"},
	// Pre-1.48 goose vocabulary: one text_editor tool whose rawInput
	// command splits reading ("view") from editing — no bare Class, so a
	// command outside the map is unknown and denies below full trust.
	{Extension: "developer", Name: "text_editor", ByCommand: map[string]string{
		"view": "read", "write": "edit", "str_replace": "edit", "insert": "edit",
	}},
	// The v1.48.0 developer builtin's actual tools (crates/goose/src/
	// agents/platform_extensions/developer/mod.rs, get_tools): write, edit,
	// shell, tree, read_image.
	{Extension: "developer", Name: "write", Class: "edit"},
	{Extension: "developer", Name: "edit", Class: "edit"},
	{Extension: "developer", Name: "tree", Class: "read"},
	{Extension: "developer", Name: "read_image", Class: "read"},
	// v1.48.0 platform_extensions/analyze/mod.rs:25,220 — extension and
	// tool are both named "analyze".
	{Extension: "analyze", Name: "analyze", Class: "read"},
	// COMPOSITE name: the bare-name rule holds for developer, but todo
	// arrives as todo__todo_write (capture-pinned, acp-transcript.jsonl
	// line 33; source: todo.rs TODO_WRITE_TOOL_NAME_COMPLETE).
	{Extension: "todo", Name: "todo__todo_write", Class: "think"},
	// Guess-grade: task-prescribed, but no fetch builtin exists at v1.48.0
	// (only test fixtures use the name). A harmless additive row.
	{Extension: "fetch", Name: "fetch", Class: "fetch"},
}

// engineConfig materializes this binding's acprpc knob values — the
// production home of the facts acprpc's gooseConfig test fixture pins
// (providers/acprpc/session_test.go; an acprpc test cannot import this
// package without a cycle, so that fixture duplicates them deliberately).
// Built per call so os.Environ() is read at spawn time, matching every
// other provider's env composition.
func (p *Provider) engineConfig() acprpc.Config {
	// --with-builtin is clap value_delimiter=',' (goose v1.48.0
	// crates/goose-cli/src/cli.rs:196-203): one comma-joined flag carries
	// multiple builtins, matching the captured spawn `goose acp
	// --with-builtin developer`. An explicit empty Builtins slice omits the
	// flag entirely (Config: "an explicit empty slice loads none").
	args := []string{"acp"}
	if len(p.cfg.Builtins) > 0 {
		args = append(args, "--with-builtin", strings.Join(p.cfg.Builtins, ","))
	}
	args = append(args, p.cfg.ExtraArgs...)
	return acprpc.Config{
		Bin:       p.bin(),
		SpawnArgs: args,
		// The engine appends Env after BaseEnv and StartOptions.Env after
		// that, so the final composition is byte-identical to the direct
		// agentenv.Build(mode, os.Environ(), cfg.Env, passthrough, extra)
		// every non-engine provider uses.
		BaseEnv: agentenv.Build(p.cfg.EnvMode, os.Environ(), nil, p.cfg.EnvPassthrough, nil),
		Env:     p.cfg.Env,
		// Full trust sets "auto" EXPLICITLY so a user config.yaml default
		// of "approve" can't stall a headless session (design doc "Trust
		// enforcement"). limited/read_only ride "approve" — goose asks for
		// every tool call — and the engine answers: read_only by its class
		// gate alone; limited by forwarding the ask to the user
		// (StartOptions.Permissions), safe classes excepted
		// (PermissionAsksEveryCall below). "smart_approve" is
		// auto_review's mode, not limited's: past read-only-annotated tools
		// it decides by an extra LLM call (permission_inspector.rs
		// detect_read_only_requests) — goose's model judging risk,
		// nondeterministic and token-spending, which is exactly the
		// reviewer auto_review asks for — and asks only for what that call
		// judges not read-only. The engine forwards every such ask to the
		// user unfiltered. README "Trust: approve, and smart_approve for
		// auto-review".
		TrustModes: map[provider.Trust]string{
			provider.TrustFull:       "auto",
			provider.TrustAutoReview: "smart_approve",
			provider.TrustLimited:    "approve",
			provider.TrustReadOnly:   "approve",
		},
		SendModeAtSessionNew: true,
		// Flat string value — pinned, testdata/captures/acp-transcript.jsonl
		// lines 10-12.
		ModelConfigOptionID: "model",
		SystemPrompt: acprpc.SystemPromptStrategy{
			Mode:   acprpc.SystemPromptExtensionMethod,
			Method: "_goose/unstable/session/system-prompt/set",
			Params: func(sessionID, text string) any {
				return map[string]string{"sessionId": sessionID, "mode": "append", "key": "mads-agent", "text": text}
			},
		},
		ToolIdentity:   acprpc.ToolIdentity{Mode: acprpc.ToolIdentityMetaNamespace, Namespace: "goose"},
		ToolClassTable: gooseToolClassTable,
		// approve mode asks before EVERY call, reads included (goose_mode.rs
		// "Ask before every tool call"; acp-transcript.jsonl line 34 asks
		// for todo__todo_write): the engine allows read/search/think/fetch
		// itself so the user's cards are the edits, commands and unknowns.
		PermissionAsksEveryCall: true,
		// goose advertises a builtin "compact" command (no input hint) and
		// a "/compact" prompt really compacts — context 6339 → 172 tokens —
		// but its summary ignores text after the command. Both pinned by a
		// goose 1.50.1 probe, acprpc testdata/captures/goose-acp-compact.jsonl.
		CompactCommand: "/compact",
	}
}

func (p *Provider) engine() *acprpc.Engine { return acprpc.New(p.engineConfig()) }

// Matrix is the honest §11 capability declaration — final: every row is
// proven by the conformance run against the scripted stand-in
// (TestConformanceAgainstScriptedBinary, no goose binary needed), with the
// per-row evidence catalogued in this package's README. Registration
// remains its own chained task.
func (p *Provider) Matrix() provider.Matrix {
	return provider.Matrix{
		// Pure engine behavior, capture-verified; MCPServers ride
		// session/new's mcpServers[] (ACP) and --with-extension (PTY,
		// pty.go ptyArgs).
		provider.BehaviorInteractive: provider.Native,
		provider.BehaviorInterrupt:   provider.Native,
		provider.BehaviorResume:      provider.Native,
		provider.BehaviorACP:         provider.Native,
		provider.BehaviorMCPServers:  provider.Native,
		// Emulated via the engine's ACP path (supersedes the design doc's
		// `goose run` plan): genuinely incremental OnEvent, and the ref is
		// Native-strength — session/new returns it synchronously.
		provider.BehaviorOneShot:       provider.Emulated,
		provider.BehaviorOneShotStream: provider.Emulated,
		// The engine's one-shot loads the ref with session/load (goose
		// advertises loadSession; acp-load-replay capture) instead of
		// session/new — Emulated like the one-shot it rides.
		provider.BehaviorOneShotResume: provider.Emulated,
		// The engine's DiscoverModels probe reads the live agent's model
		// configOption — no LLM call, no static list.
		provider.BehaviorModelDiscovery: provider.Native,
		// session/prompt results carry usage{totalTokens,inputTokens,
		// outputTokens} (pinned: testdata/captures/acp-transcript.jsonl
		// line 85) → Usage on EventTurnEnd and OneShot results.
		provider.BehaviorCostReporting: provider.Native,
		// approve mode + the engine's answer to each ask (design doc
		// "Trust enforcement"): read_only class-gated, limited asking the
		// user for everything but reads — not a harness-level block.
		provider.BehaviorTrustLimited:  provider.Emulated,
		provider.BehaviorTrustReadOnly: provider.Emulated,
		// smart_approve is goose's own reviewer: read-only-annotated tools
		// run, an extra LLM call judges the rest, and only what it judges
		// not read-only asks — forwarded to the user unfiltered. Native on
		// ACP; the PTY transport sets no mode for any trust level (pty.go),
		// as with the rows above.
		provider.BehaviorTrustAutoReview: provider.Native,
		// ACP's ref is synchronous at Start-return (Native-strength); the
		// PTY's rides a banner parse (pty.go) that a cosmetic goose change
		// could break. Emulated is the honest one-row summary across both
		// transports (design doc).
		provider.BehaviorRefAnnounce: provider.Emulated,
		// goose's own "/compact" over ACP (engineConfig's CompactCommand):
		// a real compaction pass, but text after it doesn't reach the
		// summary — so no focus. Probe: acprpc testdata/captures/
		// goose-acp-compact.jsonl and its README.
		provider.BehaviorCompaction:      provider.Native,
		provider.BehaviorCompactionFocus: provider.Unsupported,
		// Delegates (docs/DELEGATES-DESIGN.md): no subagent surface reaches
		// this adapter's wire.
		provider.BehaviorDelegateLifecycle:  provider.Unsupported,
		provider.BehaviorDelegateTranscript: provider.Unsupported,
		provider.BehaviorDelegateSteer:      provider.Unsupported,
		provider.BehaviorDelegateStop:       provider.Unsupported,
		// `goose session` under a pty (pty.go).
		provider.BehaviorPTY: provider.Native,
		// `session export --format json` (transcript.go) — richer than
		// kirocli's session/load-replay approach: timestamps, no ACP spawn
		// (design doc matrix row).
		provider.BehaviorTranscript: provider.Native,
		// No provider-layer branching surface exists yet (goose's
		// session/fork is real — flip when mads grows the surface); no cwd
		// reroot mechanism at all.
		provider.BehaviorBranching: provider.Unsupported,
		provider.BehaviorCwdReroot: provider.Unsupported,
	}
}

// IntentMatrix declares this provider's enforcement of the portable
// permission intents (pkg/contracts.PermissionIntent). require_approval is
// Emulated: on the ACP transport goose's approve mode asks before every
// call and the engine forwards each ask to the user through
// StartOptions.Permissions — but it answers the safe read/search/think/
// fetch classes itself, and on the PTY transport no mode is set at all
// (pty.go — the user's own goose config decides whether the human at the
// terminal is asked). The rest are honestly unenforced: goose has no
// command blocklist, no path-scoped write restriction, and its read-only
// trust emulation (the engine's class gate) is a coarser mechanism than
// the read_only intent claims.
func (p *Provider) IntentMatrix() provider.IntentMatrix {
	return provider.IntentMatrix{
		provider.IntentDenyCommands:    provider.Unsupported,
		provider.IntentPathScope:       provider.Unsupported,
		provider.IntentReadOnly:        provider.Unsupported,
		provider.IntentRequireApproval: provider.Emulated,
	}
}

func (p *Provider) Start(ctx context.Context, opts provider.StartOptions) (provider.Session, error) {
	// BehaviorBranching is Unsupported: refuse rather than resume in place
	// (the PTY transport would otherwise append to the original).
	if opts.Fork && opts.Resume != "" {
		return nil, fmt.Errorf("fork on resume: %w", provider.ErrUnsupported)
	}
	switch opts.Mode {
	case provider.ModeACP:
		return p.engine().Start(ctx, opts)
	case provider.ModePTY:
		return p.startPTY(opts)
	default:
		return nil, fmt.Errorf("mode %q: %w", opts.Mode, provider.ErrUnsupported)
	}
}

// OneShot delegates to the engine's ACP emulation — req.System and
// req.Model ride the same session setup an interactive Start uses, and
// OnEvent streams genuinely incrementally (the Emulated
// OneShot/OneShotStream rows).
func (p *Provider) OneShot(ctx context.Context, req provider.OneShotRequest) (*provider.OneShotResult, error) {
	res, err := p.engine().OneShot(ctx, req)
	if err != nil {
		if rle := rateLimit(err.Error()); rle != nil {
			return nil, rle
		}
		return nil, err
	}
	return res, nil
}

// rateLimit turns a rate-limited failure into the typed error (§11.9).
// Guess-grade: no captured goose rate-limit message exists to pin against
// — what escapes goose is a generic -32603 stream-error string (goose
// retries internally first), so the phrase list stays conservative and
// deliberately excludes kirocli's "quota exceeded"/"try again later":
// credits exhaustion is a distinct failure the engine already maps to its
// own message (acprpc rpcError.text). RetryAfter stays zero — goose never
// says.
func rateLimit(msg string) *provider.RateLimitError {
	low := strings.ToLower(msg)
	limited := strings.Contains(low, "rate limit") ||
		strings.Contains(low, "rate-limit") ||
		strings.Contains(low, "too many requests")
	if !limited {
		return nil
	}
	return &provider.RateLimitError{Message: strings.TrimSpace(msg)}
}

// Models probes the live agent through the engine's DiscoverModels helper
// (spawn, initialize, session/new in a temp dir, read the model
// configOption's options and currentValue, close). Failures wrap
// provider.ErrDiscoveryUnavailable — never a silent static fallback.
func (p *Provider) Models(ctx context.Context) ([]provider.Model, error) {
	models, err := p.engine().DiscoverModels(ctx)
	if err != nil {
		return nil, fmt.Errorf("goose acp: %w", err)
	}
	return models, nil
}
