// Package acprpc is the generic ACP v1 client engine (proposed W7.9,
// docs/DECLARATIVE-PROVIDERS-DESIGN.md §"providers/acprpc"): a JSON-RPC
// 2.0 stdio client any spec-conforming ACP agent rides, parameterized by
// the small enumerated knob set in Config. It is the extraction the goose
// adapter design flagged as follow-up — the RPC plumbing pattern comes
// from providers/kirocli, the capture-verified wire handling from the
// goose adapter WIP this package hoisted.
//
// The ACP v1 spec (agentclientprotocol.com, pinned by the official
// agent-client-protocol-schema crate) is normative: wire.go's types derive
// from the spec schema, and the committed captures — goose 1.50.1 and 1.48.0 under
// providers/goosecli/testdata/captures/, claude-agent-acp 0.75.1 /
// codex-acp 1.10.0 / hermes 0.21.1 under testdata/captures/ here — are
// evidence of individual agents' behavior within it, never shapes to bake
// in. Engine invariants (spec obligations, deliberately NOT knobs):
// initialize with protocolVersion 1 and empty client capabilities; runtime
// capability gating off the initialize result; mcpServers always present
// on session/new and session/load; one in-flight prompt per session; all
// five stopReasons handled; all twelve sessionUpdate variants tolerated;
// permission replies selected from the offered options by kind, never by
// optionId. Anything a harness needs beyond Config's enumerated knob set
// is the signal it belongs in a native adapter, not a new knob — each
// knob requires a capture or goose-as-client citation (the design doc's
// "knob creep" rule).
//
// The engine is not a provider.Provider — it has no Name, Matrix, or
// registration. Per-agent bindings (descriptors, a later task) wrap it.
// Everything here runs against a scripted stand-in binary in CI (see
// fakeacp_test.go) — the suite passes with no agent installed.
package acprpc

import (
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/drewkidwell/mads/pkg/procreap"
	"github.com/drewkidwell/mads/providers/provider"
)

// Config is the knob set adapting the engine to one agent. Every field's
// doc comment cites the evidence that made it a knob — goose-as-client
// citations are to the ~/repo/goose September 2026 tree (its generic ACP
// client drives five agents through the nine-knob AcpProviderConfig,
// crates/goose/src/acp/provider.rs:61-76), capture citations to committed
// files under testdata/captures/ here or providers/goosecli/testdata/.
type Config struct {
	// Bin is the agent executable. Trivially per-agent (design doc knob
	// table: `goose acp --with-builtin developer`, `copilot --acp`).
	Bin string
	// SpawnArgs are the argv the ACP transport needs, e.g. ["acp",
	// "--with-builtin", "developer"] for goose. Same evidence as Bin.
	SpawnArgs []string
	// BaseEnv is the environment the child starts from; nil means
	// os.Environ(). The seam for a binding's agentenv.Build composition
	// (inherit/isolated modes stay defined in pkg/agentenv, once) — the
	// base half of the same env model goose-as-client's AcpProviderConfig
	// env/env_remove pair operates on (provider.rs:61-76).
	BaseEnv []string
	// Env is appended after BaseEnv filtering — mads-injected KEY=VALUE
	// additions (resolved credentials, config values). goose-as-client's
	// per-agent env additions knob (AcpProviderConfig.env, provider.rs:64).
	Env []string
	// EnvRemove names vars stripped from BaseEnv before Env is appended
	// (explicit additions win). claude-agent-acp misdetects a nested
	// session unless CLAUDECODE is stripped (goose claude_acp.rs:87-88);
	// the live capture run verified the strip (testdata/captures/README.md,
	// "every CLAUDE* env var removed").
	EnvRemove []string
	// TrustModes maps each provider.Trust level to the agent's vendor mode
	// id. Mode vocabularies are pure vendor invention — goose auto/approve,
	// claude-agent-acp bypassPermissions/default/acceptEdits/plan, codex-acp
	// agent-full-access/read-only, copilot URL-shaped ids (goose-as-client's
	// per-agent mode_mapping tables, provider.rs:74; testdata/captures/
	// README.md "Mode vocabularies"). The mode TRANSPORT is never a knob:
	// detected at runtime from the session/new-or-load result — a
	// configOption with category "mode" means session/set_config_option,
	// else session/set_mode (goose-as-client provider.rs:718-747).
	TrustModes map[provider.Trust]string
	// SendModeAtSessionNew, when true, applies TrustModes[trust] right
	// after session/new-or-load succeeds (after PreseedConfigOptions —
	// goose-as-client's apply order, provider.rs:1578-1589). False sends no
	// mode call at session start, leaving the agent's default: goose's
	// per-agent session_mode_id (AcpProviderConfig, provider.rs:61-76) is
	// Some for claude/copilot/amp and None for codex/pi. Real trust
	// enforcement is the engine's permission-reply
	// gate either way — the mode is best-effort agent-side UX, so false
	// with limited trust is legal, not an error.
	SendModeAtSessionNew bool
	// ModelConfigOptionID is the configOption id carrying model selection,
	// usually "model" (goose capture acp-transcript.jsonl lines 10-12).
	// Empty means model switching is unsupported: hermes advertises no
	// configOptions at all (testdata/captures/README.md), and amp exposes
	// no picker (goose amp_acp.rs:85, model_config_option_id: None) — with
	// it empty, a Start or OneShot naming a model fails wrapping
	// provider.ErrUnsupported rather than silently running the default.
	ModelConfigOptionID string
	// PreseedConfigOptions are set_config_option calls applied at session
	// start, before the mode (goose-as-client session_config_options,
	// provider.rs:69: copilot/pi want the model pinned at session/new —
	// copilot_acp.rs:73 — while claude sets it lazily after).
	PreseedConfigOptions []ConfigOptionValue
	// SystemPrompt is the strategy for delivering StartOptions.SystemPrompt.
	// Most ACP agents have NO system-prompt surface — goose-as-client
	// ignores system prompts entirely (provider.rs:820-826); goose-as-agent
	// has only the unstable extension method. mads must not silently drop
	// an agent persona, so the zero value is FirstSendPrefix and the
	// extension-method strategy falls back to it on method_not_found.
	SystemPrompt SystemPromptStrategy
	// ToolIdentity is the strategy for naming/classifying tool calls for
	// trust gating. ACP gives clients no canonical tool name: claude sets
	// kind plus _meta.claudeCode.toolName, codex/hermes set kind only,
	// goose sets neither and puts identity in _meta.goose.toolCall
	// (testdata/captures/README.md "Tool identity varies"). TitleOnly (the
	// zero value) means no stable identity: limited/read_only trust degrade
	// to deny-all rather than gate on a display string.
	ToolIdentity ToolIdentity
	// ToolClassTable classifies MetaNamespace tool identities onto the ACP
	// kind taxonomy for the trust gate; unknown identities deny below full
	// trust (the kirocli unknown-deny precedent — over-denying after an
	// agent renames a tool is safe, under-enforcing is not). The need is
	// capture-pinned: goose's tool_call updates carry only
	// _meta.goose.toolCall names and no kind
	// (providers/goosecli/testdata/captures/acp-transcript.jsonl line 18),
	// so classification must be supplied, not read off the wire.
	ToolClassTable []ToolClassRule
	// PermissionAsksEveryCall declares that the agent's limited-trust mode
	// asks session/request_permission for EVERY tool call, reads included,
	// rather than judging risk itself. goose's "approve" mode is exactly
	// that ("Ask before every tool call", goose_mode.rs; permission_
	// inspector.rs sends every call without a user-config grant to
	// RequireApproval — and the capture asks even for todo__todo_write,
	// acp-transcript-1.48.0.jsonl line 34). Forwarding all of those to the user
	// would make a card of every file read, so with this set and an asker
	// wired (StartOptions.Permissions, limited trust only), the engine
	// allows the read/search/think/fetch classes itself and forwards only
	// the rest — approximating the "reads don't ask, risky things do" shape
	// claude-code's own normal mode has. False (kiro-cli: its own
	// per-request policy already trusts reads) forwards every ask the agent
	// makes, unfiltered: the harness alone decides what is worth asking.
	// Limited only: at auto_review the agent's own reviewer mode
	// (TrustModes[TrustAutoReview], e.g. goose smart_approve) has already
	// filtered, so every ask it makes is forwarded.
	PermissionAsksEveryCall bool
	// CompactCommand is the prompt text that runs the agent's own
	// compaction pass, e.g. "/compact". ACP has no compaction method;
	// agents advertise a slash command in available_commands_update
	// instead (goose, codex-acp and claude-agent-acp all list "compact",
	// hermes "compress" — testdata/captures/README.md "Native
	// compaction"). Empty means the agent has none: Compact returns
	// provider.ErrUnsupported, and the binding's matrix declares
	// BehaviorCompaction Unsupported. Set only where a live probe showed
	// the command really compacts (goose: goose-acp-compact.jsonl).
	CompactCommand string
	// CompactFocus declares that text after CompactCommand steers the
	// agent's summary (BehaviorCompactionFocus). False makes Compact with
	// instructions return provider.ErrUnsupported rather than send text the
	// agent drops — goose 1.50.1 accepts "/compact <text>" but its summary
	// ignores the text (same capture, turn 3).
	CompactFocus bool
	// OwnProcessTree starts the agent in a process group of its own and,
	// when the session ends — Close, which every Start failure, OneShot
	// return or cancel, and DiscoverModels pass through — SIGKILLs that
	// group and every descendant that left it (pkg/procreap: snapshots
	// taken while the agent lives, killed by pid + start-time identity).
	// Needed where an agent's helpers outlive it: hermes runs its
	// dependency bootstrap in children of its own group (pm/client.py:77,
	// a `uv sync` that survived as ppid-1 orphans when only hermes-acp was
	// killed) and detaches tool processes, MCP servers and kernels into
	// sessions of their own (tools/process_registry.py:1246,
	// tools/mcp_tool.py:594, tools/code_kernel.py:642). Off by default:
	// goose and kiro-cli keep the plain stdin-EOF-then-kill shutdown.
	OwnProcessTree bool
	// Keys is the strategy for Event.Key on EventText, EventThinking,
	// EventToolUse and EventToolResult (docs/CHAT-STREAM-DESIGN.md §1). ACP
	// names no item identity: agents put their own on the wire, or none.
	// goose stamps every chunk with the model call's messageId plus
	// _meta.goose.created, and the store keeps both (survey 2026-10-20,
	// goosecli testdata/captures/keys/run.wire.jsonl lines 15-16 and
	// run.store.jsonl rows 3, 6); hermes stamps a UUID4 messageId per
	// model call that it never stores (hermescli testdata/captures/
	// runc1.wire.jsonl vs runc1.db.jsonl); kiro-cli is uncaptured. The
	// zero value emits no keys — the honest reading where the wire's ids
	// are unverified.
	Keys KeyStrategy
	// Settle names where the agent's own store has likely caught up, for
	// EventSettle (emitted only for a session started with
	// StartOptions.Keyed). Store timing is per agent: goose writes a
	// tool call and the text before it only once the tool finishes
	// (survey lag.txt: 6025 ms for `sleep 6`), so its points are the tool's
	// completion and the prompt response; hermes writes the assistant row
	// with its tool calls before the tool runs (3–15 ms after the tool_call
	// frame) and its last row before the prompt response. Zero: none.
	Settle SettlePoints
}

// KeyMode selects a KeyStrategy variant.
type KeyMode string

const (
	// KeyModeNone emits no keys. The zero value: an agent whose ids aren't
	// verified live-vs-stored must not claim stable keys.
	KeyModeNone KeyMode = ""
	// KeyModeMessageRun keys text and thinking as
	// "<messageId>[@<created>]:<run>" and tool calls by toolCallId. A message
	// is the chunks sharing one messageId with no tool call, tool result or
	// turn boundary between them; run counts the switches between text and
	// thinking within it, so a thinking block and the text after it are two
	// items (0 and 1), as an agent storing one content item per run lists
	// them. created is the first chunk's _meta.<CreatedNamespace>.created:
	// later chunks of one message carry later seconds (survey: chatcmpl-17
	// spans 4), and the stored message keeps the first. A chunk with no
	// messageId opens an anonymous message keyed by a per-session counter —
	// the engine invents an id only where the agent gives none.
	KeyModeMessageRun KeyMode = "message_run"
)

// KeyStrategy configures Event.Key derivation; see the modes.
type KeyStrategy struct {
	Mode KeyMode
	// CreatedNamespace, for KeyModeMessageRun, names the _meta key whose
	// "created" stamp joins the messageId: goose's ollama ids are
	// chatcmpl-0..999 and repeat within a session (27 repeated pairs in a
	// real store, survey main-store-msgid-collisions.txt), so the id alone
	// isn't unique. Empty: the messageId alone (hermes's are UUID4s).
	CreatedNamespace string
	// ThinkingApart, for KeyModeMessageRun, numbers a message's text runs
	// and thinking runs separately: text "<id>:<n>" for its n-th text run,
	// thinking "<id>:thinking:<n>". For an agent whose store can move
	// thinking off the text's row (goose puts a model call's thinking on
	// its tool-call messages), so text keys can't count thinking.
	ThinkingApart bool
}

// SettlePoints are the wire events after which the engine emits
// EventSettle; see Config.Settle.
type SettlePoints struct {
	// ToolCall: after each tool_call announcement (Key = the tool's).
	ToolCall bool
	// ToolDone: after each terminal tool_call_update, completed or failed
	// (Key = the tool's).
	ToolDone bool
	// PromptResponse: when the session/prompt response arrives, just
	// before the turn end.
	PromptResponse bool
}

// ConfigOptionValue is one PreseedConfigOptions entry.
type ConfigOptionValue struct {
	ID    string
	Value string
}

// SystemPromptMode selects a SystemPromptStrategy variant.
type SystemPromptMode string

const (
	// SystemPromptFirstSendPrefix prefixes the first Send's text with the
	// system prompt — claude-code-era emulation, better than dropping the
	// agent definition on the floor. The zero value: the engine must never
	// silently drop an agent persona, so the default is the safe strategy.
	SystemPromptFirstSendPrefix SystemPromptMode = ""
	// SystemPromptNone drops the system prompt by declared decision — an
	// explicit opt-out, mirroring what goose-as-client does silently
	// (provider.rs:820-826).
	SystemPromptNone SystemPromptMode = "none"
	// SystemPromptExtensionMethod delivers the prompt via a vendor
	// extension method (goose's spec-sanctioned underscore-prefixed
	// _goose/unstable/session/system-prompt/set). method_not_found
	// (-32601) falls back to FirstSendPrefix for the session.
	SystemPromptExtensionMethod SystemPromptMode = "extension"
)

// SystemPromptStrategy configures system-prompt delivery; see the modes.
type SystemPromptStrategy struct {
	Mode SystemPromptMode
	// Method and Params apply to SystemPromptExtensionMethod only. Params
	// builds the vendor-specific params shape (goose's {sessionId,
	// mode:"append", key, text}) — a closure, because the shapes don't
	// generalize; the declarative expression is the descriptor layer's
	// problem, not the engine's.
	Method string
	Params func(sessionID, text string) any
}

// ToolIdentityMode selects a ToolIdentity variant.
type ToolIdentityMode string

const (
	// ToolIdentityTitleOnly: the agent offers only display titles — no
	// stable identity to gate on, so every call classifies unknown and
	// limited/read_only trust deny all tools. The zero value, because it is
	// the safe reading of an unconfigured knob.
	ToolIdentityTitleOnly ToolIdentityMode = ""
	// ToolIdentityKindBased reads the spec kind field the agent sets on
	// tool_call updates (codex/hermes do; claude additionally sets _meta).
	ToolIdentityKindBased ToolIdentityMode = "kind"
	// ToolIdentityMetaNamespace reads the stable tool name from
	// _meta.<Namespace>.toolCall{toolName, extensionName} — goose's case
	// (it never sets kind on model tool calls) — and classifies it through
	// ToolClassTable.
	ToolIdentityMetaNamespace ToolIdentityMode = "meta"
)

// ToolIdentity configures how tool calls are identified and classified.
type ToolIdentity struct {
	Mode ToolIdentityMode
	// Namespace is the _meta key for ToolIdentityMetaNamespace ("goose").
	Namespace string
}

// ToolClassRule maps one (extension, tool) identity onto a class from the
// ACP kind taxonomy (read, edit, delete, move, search, execute, think,
// fetch, switch_mode, other). ByCommand splits the class on the call's
// rawInput "command" field — goose's developer text_editor is one tool
// whose "view" command reads and whose "write"/"str_replace"/"insert"
// commands edit, and the distinction must survive classification. A
// ByCommand miss falls back to Class; an empty Class means unknown, which
// denies below full trust.
type ToolClassRule struct {
	Extension string
	Name      string
	Class     string
	ByCommand map[string]string
}

// Engine is one agent's configured ACP client. It is not a
// provider.Provider — bindings wrap it and own Name/Matrix/registration.
type Engine struct {
	cfg Config
}

func New(cfg Config) *Engine {
	return &Engine{cfg: cfg}
}

// env builds the child environment: BaseEnv (default os.Environ()) with
// EnvRemove names stripped, then cfg.Env, then per-session extra.
// EnvRemove strips from the base only — explicit additions win, so a
// binding can deliberately re-add a removed name.
func (e *Engine) env(extra []string) []string {
	base := e.cfg.BaseEnv
	if base == nil {
		base = os.Environ()
	}
	removed := make(map[string]bool, len(e.cfg.EnvRemove))
	for _, name := range e.cfg.EnvRemove {
		removed[name] = true
	}
	env := make([]string, 0, len(base)+len(e.cfg.Env)+len(extra))
	for _, kv := range base {
		if name, _, ok := strings.Cut(kv, "="); ok && removed[name] {
			continue
		}
		env = append(env, kv)
	}
	env = append(env, e.cfg.Env...)
	env = append(env, extra...)
	return env
}

// closeTree is closeWait for an OwnProcessTree agent: grace for the
// agent to exit on its own (its stdout ending), then the whole group is
// killed while the agent is still unreaped — its pid, and so the group
// id, can't have been reused yet — then the agent is waited and every
// remembered descendant that left the group is reaped.
func closeTree(cmd *exec.Cmd, grace time.Duration, readDone <-chan struct{}, reaper *procreap.Reaper) {
	select {
	case <-readDone:
	case <-time.After(grace):
	}
	procreap.KillGroup(cmd.Process.Pid)
	reaper.MarkExited()
	_ = cmd.Wait()
	reaper.Reap()
}

// ownGroup puts cmd in a process group of its own (OwnProcessTree).
func ownGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// closeWait shuts a process down gently: signal by closing its stdin, give
// it grace to exit, then kill.
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
