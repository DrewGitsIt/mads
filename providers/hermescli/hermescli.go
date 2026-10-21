// Package hermescli is the Hermes Agent provider adapter (spec §11): Nous
// Research's hermes, driven through its native ACP server `hermes-acp` on
// the generic engine at providers/acprpc. Like goosecli, this binding owns
// the knob values — every one cites a survey capture or a hermes 0.22.2
// source line (docs/HERMES-ADAPTER-DESIGN.md "Knob table") — while every
// wire behavior stays in the engine.
//
// What this binding adds over the bare engine, each forced by a hermes fact:
//   - full trust only (Start refuses anything else): hermes asks permission
//     only for edits and for commands its own reviewer flags, and its asks
//     carry synthetic tool ids, so no lower trust level can be enforced by
//     answering them (design doc "Trust");
//   - a session wrapper (session.go) that turns hermes's per-process
//     cumulative usage into per-turn usage, strips the paragraph break
//     hermes injects before the first text after a tool, drops tool
//     results for those synthetic approval ids, and records what each turn
//     streamed for the conversation keys (keys.go);
//   - a resume guard: hermes answers session/load of an unknown id with an
//     empty success, so the ref is checked against state.db first;
//   - a TranscriptReader over hermes's own SQLite store (transcript.go).
//
// The model is chosen per session with ACP session/set_model
// ("provider:model" picker ids), which hermes stores on the session row and
// never writes to config.yaml — so sessions run against the user's real
// HERMES_HOME (their auth, their state.db) without mads touching it.
package hermescli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/drewkidwell/mads/pkg/agentenv"
	"github.com/drewkidwell/mads/pkg/binlocate"
	"github.com/drewkidwell/mads/pkg/hermesinstall"
	"github.com/drewkidwell/mads/providers/acprpc"
	"github.com/drewkidwell/mads/providers/provider"
)

// Config adapts the binary to its surroundings. The zero value targets a
// stock install against the user's own ~/.hermes.
type Config struct {
	// Bin is the hermes-acp executable. Empty resolves it per invocation via
	// pkg/binlocate — PATH, then ~/.local/bin where hermes's installer puts
	// it (setup-hermes.sh:216) — so preflight's verdict matches what gets
	// spawned. Tests point this at a scripted stand-in.
	Bin string
	// ExtraArgs are appended to every hermes-acp spawn. hermes-acp has no
	// flags that shape a session (acp_adapter/entry.py:94-104: only
	// --version/--check/--setup*), so this is empty in production.
	ExtraArgs []string
	// Home, when set, is the HERMES_HOME every spawn gets and the directory
	// the TranscriptReader reads state.db from. Empty follows the child's
	// own environment — HERMES_HOME if it carries one, else hermes's
	// platform default ~/.hermes plus any HERMES_DATA_DIR_SUFFIX
	// (hermes_constants.py:51-58,160-169). Tests and
	// the smoke use a scratch home; production leaves it empty so sessions
	// land in the user's real store.
	Home string
	// Env carries mads-injected KEY=VALUE pairs added on top of the base
	// environment for every spawn.
	Env []string
	// EnvMode controls how much of the process environment hermes-acp sees
	// (pkg/agentenv). Empty behaves as inherit. HOME is on the isolated
	// base allowlist; HERMES_HOME is not and must ride EnvPassthrough (the
	// service registration sites do this).
	EnvMode agentenv.Mode
	// EnvPassthrough lists parent env var NAMES let through in isolated mode.
	EnvPassthrough []string
}

// Provider is the hermes adapter. Registry name "hermes".
type Provider struct {
	cfg Config
	// live holds each running session child's ledger of what it streamed,
	// by ref — what the readers match stored rows against (keys.go).
	live liveSessions
}

func New(cfg Config) *Provider { return &Provider{cfg: cfg} }

func (p *Provider) Name() string { return "hermes" }

// bin is the executable every spawn runs: Config.Bin if set, else
// binlocate.Resolve's answer (which honours $MADS_AGENT_BIN_DIR).
func (p *Provider) bin() string {
	if p.cfg.Bin != "" {
		return p.cfg.Bin
	}
	return binlocate.Resolve("hermes-acp")
}

// env is the mads-side additions every spawn gets: Config.Env, then
// HERMES_HOME when Config.Home pins one (last wins in the child).
func (p *Provider) env() []string {
	env := append([]string(nil), p.cfg.Env...)
	if p.cfg.Home != "" {
		env = append(env, "HERMES_HOME="+p.cfg.Home)
	}
	return env
}

// childEnv is exactly the environment the engine hands hermes-acp: the
// agentenv base, then Env (acprpc Engine.env; no EnvRemove here).
func (p *Provider) childEnv() []string {
	base := agentenv.Build(p.cfg.EnvMode, os.Environ(), nil, p.cfg.EnvPassthrough, nil)
	return append(base, p.env()...)
}

// ready refuses before any spawn when launching hermes-acp would make
// hermes install its dependencies into the home it would run under
// (pkg/hermesinstall: a ~600 MB `uv sync` that outlives a killed
// hermes-acp). Every spawn site — Start (and so OneShot) and Models —
// calls it first; preflight asks the same question.
func (p *Provider) ready() error {
	if err := hermesinstall.Check(p.bin(), p.childEnv()); err != nil {
		return fmt.Errorf("hermes-acp: %w", err)
	}
	return nil
}

// home is the HERMES_HOME the spawned hermes-acp resolves — computed from
// exactly the environment the engine hands the child (base, then Env), so
// the TranscriptReader always reads the store the agent writes.
func (p *Provider) home() string {
	var home, suffix string
	for _, kv := range p.childEnv() {
		if v, ok := strings.CutPrefix(kv, "HERMES_HOME="); ok {
			home = strings.TrimSpace(v)
		}
		if v, ok := strings.CutPrefix(kv, "HERMES_DATA_DIR_SUFFIX="); ok {
			suffix = v
		}
	}
	if home != "" {
		if rest, ok := strings.CutPrefix(home, "~/"); ok {
			if uh, err := os.UserHomeDir(); err == nil {
				home = filepath.Join(uh, rest)
			}
		}
		return home
	}
	uh, err := os.UserHomeDir()
	if err != nil {
		return ".hermes" + suffix
	}
	return filepath.Join(uh, ".hermes"+suffix)
}

// engineConfig materializes the knob values. Captures cited by run name
// ("runc1 line 4") live under testdata/captures/ (see its README); hermes
// source lines are against hermes-agent 0.22.2.
func (p *Provider) engineConfig() acprpc.Config {
	return acprpc.Config{
		Bin: p.bin(),
		// hermes-acp takes no session-shaping flags (entry.py:94-104).
		SpawnArgs: append([]string(nil), p.cfg.ExtraArgs...),
		BaseEnv:   agentenv.Build(p.cfg.EnvMode, os.Environ(), nil, p.cfg.EnvPassthrough, nil),
		Env:       p.env(),
		// Full trust only (Start refuses the rest). "dont_ask" is hermes's
		// least-asking mode — "Auto-allow file edits for this session
		// except sensitive paths" (session/new modes, runc1 line 4). It is
		// set explicitly so a session never sits in "default" (ask before
		// every edit); whatever hermes still asks (sensitive paths, commands
		// its reviewer flags) the engine allows at full trust.
		TrustModes:           map[provider.Trust]string{provider.TrustFull: "dont_ask"},
		SendModeAtSessionNew: true,
		// hermes ships ACP's models selector and no configOptions at all
		// (runc1 line 4: 66 "provider:model" ids, currentModelId
		// "copilot:gpt-4.1"); the engine reads and sets the selector itself
		// (session/set_model, server.py:1022-1056), so no option id.
		ModelConfigOptionID: "",
		// No system-prompt surface: hermes has no extension method, so the
		// engine's default first-send prefix applies — never drop a persona.
		// Start hands the engine the persona already wrapped
		// (wrapSystemPrompt), so the reader can strip exactly what was
		// prefixed from the stored user row.
		SystemPrompt: acprpc.SystemPromptStrategy{Mode: acprpc.SystemPromptFirstSendPrefix},
		// Tool calls carry the spec kind ("execute", "edit") and a
		// "<tool>: <preview>" title, no _meta namespace (runc1 line 21,
		// run9 line 12; acp_adapter/tools.py:20-31,192-198).
		ToolIdentity: acprpc.ToolIdentity{Mode: acprpc.ToolIdentityKindBased},
		// hermes judges risk itself: it asks for edits and for commands its
		// reviewer flags, and auto-approved `rm -rf` (run9 lines 13, 23-24).
		PermissionAsksEveryCall: false,
		// hermes advertises "compress" (available_commands_update, runc1
		// line 8), but no capture shows what a pass does to state.db or the
		// session's ids, so mads does not drive it yet (README).
		CompactCommand: "",
		// hermes's helpers outlive it unless the whole tree is killed: the
		// dependency bootstrap's PM worker shares its group
		// (pm/client.py:77) and tools detach into their own sessions
		// (process_registry.py:1246, mcp_tool.py:594, code_kernel.py:642).
		OwnProcessTree: true,
		// Provisional live keys from the live ids: a UUID4 messageId per
		// model call (agent/events.py:185-236; runc1 lines 9-13) and tc-…
		// tool ids (tools.py:67). Never stored, so keys.go matches them to
		// the stored rows by position.
		Keys: acprpc.KeyStrategy{Mode: acprpc.KeyModeMessageRun},
		// hermes writes the assistant row with its tool calls just as the
		// tool_call frame goes out (−15..+15 ms, survey) and the turn's
		// last row before the prompt response (10-202 ms after its last
		// chunk).
		Settle: acprpc.SettlePoints{ToolCall: true, PromptResponse: true},
	}
}

func (p *Provider) engine() *acprpc.Engine { return acprpc.New(p.engineConfig()) }

// Matrix is the honest §11 declaration; per-row evidence is in README.md.
func (p *Provider) Matrix() provider.Matrix {
	return provider.Matrix{
		provider.BehaviorInteractive: provider.Native,
		// session/cancel resolves stopReason "cancelled" in 40-225 ms
		// (survey runc1 line 323, run2).
		provider.BehaviorInterrupt: provider.Native,
		// session/load replays and keeps the conversation (run2, run3, and
		// the smoke's recall after resume); guarded against hermes's empty
		// success on unknown ids.
		provider.BehaviorResume: provider.Native,
		provider.BehaviorACP:    provider.Native,
		// No adapter for hermes's TUI.
		provider.BehaviorPTY: provider.Unsupported,
		// The engine's one-shot over ACP, through this binding's wrapper.
		provider.BehaviorOneShot:       provider.Emulated,
		provider.BehaviorOneShotStream: provider.Emulated,
		provider.BehaviorOneShotResume: provider.Emulated,
		// The models selector on session/new — no LLM call, no static list.
		provider.BehaviorModelDiscovery: provider.Native,
		// session/prompt results carry usage (runc1 line 18), cumulative per
		// process; session.go differences it per turn.
		provider.BehaviorCostReporting: provider.Native,
		// Full trust only — see Start.
		provider.BehaviorTrustLimited:    provider.Unsupported,
		provider.BehaviorTrustReadOnly:   provider.Unsupported,
		provider.BehaviorTrustAutoReview: provider.Unsupported,
		// session/new's ref is synchronous.
		provider.BehaviorRefAnnounce: provider.Native,
		// state.db, hermes's own store (transcript.go).
		provider.BehaviorTranscript: provider.Native,
		// The engine sends mcpServers on session/new; hermes registers them
		// on the session (server.py:427) and the model calls them as
		// mcp__<server>__<tool> (probe-mcp: a stdio server's marker came
		// back through the call; re-proven by the smoke).
		provider.BehaviorMCPServers: provider.Native,
		// Not driven yet (CompactCommand above).
		provider.BehaviorCompaction:      provider.Unsupported,
		provider.BehaviorCompactionFocus: provider.Unsupported,
		// hermes has background delegations, but nothing about them reaches
		// the ACP wire this adapter reads.
		provider.BehaviorDelegateLifecycle:  provider.Unsupported,
		provider.BehaviorDelegateTranscript: provider.Unsupported,
		provider.BehaviorDelegateSteer:      provider.Unsupported,
		provider.BehaviorDelegateStop:       provider.Unsupported,
		// hermes stores none of its live ids: live items carry provisional
		// keys and the readers match stored rows to them by position while
		// the child lives (keys.go, README "Conversation keys").
		provider.BehaviorStableKeys:     provider.Emulated,
		provider.BehaviorStoredThinking: provider.Unsupported, // C2: flipped with each reader's thinking rows
		// hermes has session/fork, but mads has no branching surface for
		// ACP yet; no cwd reroot at all.
		provider.BehaviorBranching: provider.Unsupported,
		provider.BehaviorCwdReroot: provider.Unsupported,
	}
}

// IntentMatrix: hermes runs at full trust only, so no portable permission
// intent is enforced — not even require_approval, since hermes decides for
// itself which calls to ask about.
func (p *Provider) IntentMatrix() provider.IntentMatrix {
	return provider.IntentMatrix{
		provider.IntentDenyCommands:    provider.Unsupported,
		provider.IntentPathScope:       provider.Unsupported,
		provider.IntentReadOnly:        provider.Unsupported,
		provider.IntentRequireApproval: provider.Unsupported,
	}
}

// fullTrustOnly refuses every level but full ("" is full by contract).
// Refused, never clamped: running a limited session at full would loosen
// what the user asked for without telling them.
func fullTrustOnly(t provider.Trust) error {
	if t == "" || t == provider.TrustFull {
		return nil
	}
	return fmt.Errorf("hermes runs at full trust only: it asks permission only for edits and commands its own reviewer flags, so trust %q cannot be enforced: %w", t, provider.ErrUnsupported)
}

func (p *Provider) Start(ctx context.Context, opts provider.StartOptions) (provider.Session, error) {
	if opts.Fork && opts.Resume != "" {
		return nil, fmt.Errorf("fork on resume: %w", provider.ErrUnsupported)
	}
	if opts.Mode != provider.ModeACP {
		return nil, fmt.Errorf("mode %q: %w", opts.Mode, provider.ErrUnsupported)
	}
	if err := fullTrustOnly(opts.Trust); err != nil {
		return nil, err
	}
	if err := p.ready(); err != nil {
		return nil, err
	}
	if opts.Resume != "" {
		if err := p.requireStoredSession(ctx, opts.Resume); err != nil {
			return nil, err
		}
	}
	if opts.SystemPrompt != "" {
		opts.SystemPrompt = wrapSystemPrompt(opts.SystemPrompt)
	}
	sess, err := p.engine().Start(ctx, opts)
	if err != nil {
		return nil, err
	}
	return wrapSession(sess, p, opts.Keyed), nil
}

// The first-send prefix lands inside hermes's stored user row (hermes has
// no other surface for it), so it is written in a wrapper the
// TranscriptReader can recognise exactly and remove (stripSystemPrompt):
//
//	<mads-system-prompt bytes="N">\n<persona, N bytes>\n</mads-system-prompt>
//
// followed by the engine's "\n\n" and the user's text. The byte count makes
// the match exact — no fuzzy pattern, and no persona content can end it
// early. The engine prefixes the first prompt of every hermes-acp process,
// so after a resume the wrapper opens that process's first user row too.
const (
	sysPromptOpen  = `<mads-system-prompt bytes="`
	sysPromptClose = "\n</mads-system-prompt>"
)

func wrapSystemPrompt(persona string) string {
	return fmt.Sprintf("%s%d\">\n%s%s", sysPromptOpen, len(persona), persona, sysPromptClose)
}

// stripSystemPrompt removes an exact wrapSystemPrompt prefix (and the
// engine's "\n\n" separator) from a stored user row, returning what the
// user typed. Anything that isn't exactly that shape — a wrong byte count,
// a missing close, no separator — is returned unchanged: a message that
// merely looks like the wrapper is the user's. hermes strips the row's
// surrounding whitespace, so a prompt with no text of its own ends right
// at the close tag and strips to "".
func stripSystemPrompt(row string) string {
	rest, ok := strings.CutPrefix(row, sysPromptOpen)
	if !ok {
		return row
	}
	digits, body, ok := strings.Cut(rest, "\">\n")
	if !ok || digits == "" || len(digits) > 9 || strings.Trim(digits, "0123456789") != "" || (len(digits) > 1 && digits[0] == '0') {
		return row
	}
	n, err := strconv.Atoi(digits)
	if err != nil || n > len(body) {
		return row
	}
	after, ok := strings.CutPrefix(body[n:], sysPromptClose)
	if !ok {
		return row
	}
	if after == "" {
		return ""
	}
	if text, ok := strings.CutPrefix(after, "\n\n"); ok {
		return text
	}
	return row
}

// OneShot is the engine's ACP emulation (spawn, session/new or guarded
// session/load, one prompt, exit), run through this binding's Start so the
// one-shot gets the same per-turn usage and text normalization as an
// interactive turn. It mirrors acprpc.Engine.OneShot step for step.
func (p *Provider) OneShot(ctx context.Context, req provider.OneShotRequest) (*provider.OneShotResult, error) {
	sess, err := p.Start(ctx, provider.StartOptions{
		Mode:         provider.ModeACP,
		Model:        req.Model,
		Cwd:          req.Cwd,
		Resume:       req.Resume,
		SystemPrompt: req.System,
		Trust:        provider.TrustFull,
	})
	if err != nil {
		return nil, err
	}
	defer func() { _ = sess.Close() }()
	emit := func(ev provider.Event) {
		if req.OnEvent != nil {
			req.OnEvent(ev)
		}
	}
	emit(provider.Event{Kind: provider.EventSessionRef, Ref: sess.Ref()})
	if err := sess.Send(ctx, req.Prompt); err != nil {
		return nil, err
	}
	var text strings.Builder
	var errText string
	for {
		select {
		case ev, ok := <-sess.Events():
			if !ok {
				return nil, fmt.Errorf("hermes-acp: agent exited before the turn ended")
			}
			emit(ev)
			switch ev.Kind {
			case provider.EventText:
				text.WriteString(ev.Text)
			case provider.EventError:
				errText = ev.Text
			case provider.EventTurnEnd:
				if errText != "" {
					if rle := rateLimit(errText); rle != nil {
						return nil, rle
					}
					return nil, fmt.Errorf("hermes-acp: %s", errText)
				}
				res := &provider.OneShotResult{Text: text.String(), Ref: sess.Ref()}
				if ev.Usage != nil {
					res.Usage = *ev.Usage
				}
				return res, nil
			}
		case <-ctx.Done():
			_ = sess.Interrupt(context.Background())
			return nil, ctx.Err()
		}
	}
}

// rateLimit turns a rate-limited failure into the typed error (§11.9).
// Guess-grade, like goosecli's: no hermes rate-limit failure has been
// captured; hermes retries and fails over internally before anything
// escapes, so the phrase list stays conservative.
func rateLimit(msg string) *provider.RateLimitError {
	low := strings.ToLower(msg)
	if strings.Contains(low, "rate limit") || strings.Contains(low, "rate-limit") || strings.Contains(low, "too many requests") {
		return &provider.RateLimitError{Message: strings.TrimSpace(msg)}
	}
	return nil
}

// Models reads the live agent's models selector through the engine (spawn,
// initialize, session/new in a temp dir, read availableModels, close).
// hermes creates no state.db row for a session that never prompts
// (probe-emptynew), so discovery leaves nothing behind in the
// user's store. Failures wrap provider.ErrDiscoveryUnavailable.
func (p *Provider) Models(ctx context.Context) ([]provider.Model, error) {
	if err := p.ready(); err != nil {
		return nil, fmt.Errorf("%w: %w", err, provider.ErrDiscoveryUnavailable)
	}
	models, err := p.engine().DiscoverModels(ctx)
	if err != nil {
		return nil, fmt.Errorf("hermes-acp: %w", err)
	}
	return models, nil
}
