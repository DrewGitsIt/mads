# Declarative providers — attach any agent (proposed W7.9)

Implements spec §11 requirement 8 — *"adding support for a new agent harness
should be a configuration/registration task, not a code change to mads's
core"* — the way goose implements its long tail of LLM providers: a small
set of code **engines** driven by declarative **descriptors**, with a user
drop-in directory so an agent already installed on the machine can be
attached without touching mads source at all. Where this document and §11
disagree, this document wins (it doesn't; it narrows §11.8 into a concrete
mechanism).

The evidence base is the goose checkout at `~/repo/goose` (September 2026
tree; file:line citations are to that checkout and will drift) plus the
mads provider layer as it exists today. Everything cited from mads code is
verified against HEAD; goose citations were read from source this pass.

## What goose does, and what we lift

goose faces the mirror image of our problem: it is an agent that must speak
to ~80 *LLM backends*; mads is a platform that must speak to N *agent
harnesses*. Its provider layer solves the long tail with patterns that
transfer almost verbatim:

| goose pattern | Where (in `~/repo/goose`) | What we lift |
|---|---|---|
| Two required trait methods (`get_name`, `stream`), ~20 defaulted capability hooks | `crates/goose-provider-types/src/base.rs:476-723` | Already ours: `provider.Provider` is 4 methods + optional strategy interfaces by type assertion (`providers/provider/provider.go:140,178-284`). No change needed. |
| Descriptor metadata: display name, config keys (required/secret/default), default model, install/setup steps | `ProviderMetadata`/`ConfigKey`, `base.rs:26-54,154-239` | The **descriptor file** below — today this data is scattered across Go literals in `pkg/preflight` and per-adapter `Config` defaults. |
| Type-erased constructor registry: typed defs and config instances land in the same map | `provider_registry.rs:12-26,89-315`, `init.rs:57-222` | One `providers/load` builder replacing four hand-maintained registration sites. |
| **Declarative tier**: ~45 providers are pure JSON over 3 wire "engines" (`openai`/`anthropic`/`ollama`-compatible), with `${VAR}` interpolation resolved lazily against config | `goose-providers/src/declarative.rs:95-115,147-194`, `declarative/definitions/*.json` | The core idea. Our engine axis is **transport**, not HTTP dialect: `acp`, `pty`, later `streamjson`. |
| User drop-in dir with the same schema as bundled definitions, hot-reloadable | `~/.config/goose/custom_providers/<id>.json`, `crates/goose/src/config/declarative_providers.rs:170-360` | `$MADS_HOME/providers.d/*.yaml`. |
| Safe-default capability declarations, never optimistic probing (unknown cache semantics → strictest) | `cache_semantics.rs:23-46` | Matrix rows for descriptor providers **derive from the engine**, defaulting Unsupported; a descriptor cannot claim what its engine can't deliver. |
| Live smoke: send one real prompt, require only the first chunk; doctor walks fallbacks with human-readable errors | `provider_test.rs:7`, `doctor.rs:151-241` | `mads attach --verify`: a one-turn echo smoke through the real binary at attach time. |
| Config-hash-keyed inventory cache (SHA-256 over public+secret inputs invalidates discovered models) | `inventory/mod.rs:81-145` | Future work — v1 keeps `ModelDiscoverer` uncached as today. |

One disanalogy keeps us honest. goose's declarative tier works because HTTP
LLM APIs converged on three dialects; agent harness wire surfaces have
*not* converged — claude-code's stream-json is bespoke, and even two ACP
implementations diverge in their extensions (see
`docs/GOOSE-ADAPTER-DESIGN.md`, "ACP v1 spec is normative"). Two things
rescue the analogy:

1. **ACP is becoming the convergence point.** goose itself now ships ACP
   *adapters for other agents* — `claude_acp`, `codex_acp`, `copilot_acp`,
   `amp_acp` (`crates/goose/src/providers/`), all speaking the same
   spec-normative protocol we already committed to treating as gospel.
2. **PTY is the universal floor** — any interactive CLI agent runs under
   a PTY, and mads already treats PTY as a first-class transport (§11.3).
   A descriptor-only PTY engine could give every agent a terminal-mode
   home with zero code, but the 2026-09 landscape survey found almost
   nothing left that *needs* it, so that engine is deferred (see
   `providers/ptycli` below); ACP coverage carries the promise in v1.

## The gap in mads today

The abstraction above registration is done and generic: registry-driven
Router API (`services/router/internal/api/api.go:185-187`), matrix-driven
UI (`ProvidersSection.tsx`, `Launchpad.tsx`), name-driven session runner
(`services/session/internal/runner/runner.go:285`), conformance suite,
preflight probes. What is hardcoded:

| Site | Hardcoding |
|---|---|
| `services/router/main.go:163-177` | `claudecode.New`, `kirocli.New`, `fake.New` literals |
| `services/session/main.go:98-115` | same three + env plumbing |
| `services/pipeline/main.go:70-92` | same three + autonomous-mode `ExtraArgs` (`--dangerously-skip-permissions`, `--trust-all-tools`) |
| `cmd/mads/run.go:32-89` | literal `-provider` switch + per-harness `-claude-args`/`-kiro-args` flags |
| `pkg/preflight/providers.go`, `compat.go` | `knownProviderBinaries` / `compatMatrix` Go maps |
| `services/router/main.go:112`, `internal/api/config.go:48` | `"claude-code"` default-provider fallback strings |

Adding kiro-cli touched all of these; the goose adapter's executor
checklist (`GOOSE-ADAPTER-DESIGN.md` §"Registration & plumbing") repeats
the tour. That is the §11.8 violation this design removes.

## Design overview

Two artifacts:

- **Engines** — Go packages implementing `provider.Provider` generically
  for one transport family, parameterized by a descriptor. v1 ships one:
  `providers/acprpc` (the extraction already flagged as follow-up in
  `GOOSE-ADAPTER-DESIGN.md` §"Future work"). A `providers/ptycli` floor
  engine is **deferred** (decision 2026-09-09, Drew) — the landscape
  evidence below found almost no PTY-only constituency left.
- **Descriptors** — one YAML file per provider: identity, binary + version
  probe + compat range, spawn/resume/one-shot argv templates, trust
  mapping, quirk knobs. Bundled descriptors are `go:embed`ded in
  `providers/load`; user descriptors live in `$MADS_HOME/providers.d/`.

The support ladder, cheapest first (mirroring goose's four tiers):

| Tier | What it takes | What you get |
|---|---|---|
| 1. ACP descriptor | YAML only | Full chat-mode: streaming events, interrupt, resume, trust gating, MCP wiring, engine-emulated one-shots — for any ACP v1 agent. |
| 2. Descriptor over a bespoke engine | YAML + an engine that already exists | e.g. future `streamjson` engine retrofitting claude-code. |
| 3. Native Go adapter | today's path | Harnesses whose surface fits no engine. Still first-class; gains a descriptor for metadata/preflight only. |
| (deferred) PTY descriptor | YAML only, once `ptycli` exists | Terminal-mode sessions + templated text one-shots for any interactive CLI agent — deferred with the ptycli engine. |

**Decision: descriptors are files, not Data Service registry rows (v1).**
The registry is built at process start in four binaries including
`cmd/mads run`, which must work without a running Data Service. goose made
the same call (`custom_providers/` dir, not its config DB). A Settings
CRUD UI that *writes* these files is future work; v1 Settings renders them
read-only through the existing `/providers` endpoints, which need no
changes.

**Decision: every provider gets a descriptor, including native adapters.**
`claude-code`, `kiro-cli`, `goose`, and `fake` ship bundled descriptors
with `engine: native`. For those, construction stays in Go (a single
engine→constructor table inside `providers/load`), but identity, binary,
probe, compat range, install hint, and autonomous-args all move into the
descriptor — so `pkg/preflight` and the four main.go sites read one source
of truth and §11.8 holds uniformly.

## The descriptor

```yaml
# $MADS_HOME/providers.d/goose.yaml (bundled example, facts per
# docs/GOOSE-ADAPTER-DESIGN.md + providers/goosecli/testdata/captures/)
name: goose                  # registry name; [a-z0-9-], unique
display_name: Goose
engine: acp                  # acp | pty | native
binary: goose                # resolved via exec.LookPath
install_hint: "https://block.github.io/goose/docs/getting-started/installation"
version:
  probe_args: ["--version"]  # output " 1.48.0" — parseSemverish tolerates it
  compat: { min: "1.46.0", max: "1.49.0" }   # max exclusive; empty = warn-never
spawn:
  acp_args: ["acp", "--with-builtin", "developer"]
  pty_args: ["session"]      # optional; enables the PTY transport too
  autonomous_args: []        # appended by the pipeline context only
env:
  passthrough: [HOME, XDG_CONFIG_HOME]   # union with agentenv isolated-mode base
oneshot:
  args: ["run", "-q", "--output-format", "stream-json", "-t", "{prompt}"]
  system_arg: ["--system", "{system}"]   # omitted when SystemPrompt empty
  model_arg: ["--model", "{model}"]
  output: text               # v1: `text` (join stdout) — richer parsing is engine work
resume:
  pty_args: ["session", "--resume", "--session-id", "{ref}"]
  # acp resume is engine-owned (session/load); no knob needed
ref:
  pty_banner_regex: '(\d{8}_\d+)\s+·'    # matched ANSI-stripped; optional
trust:
  acp_modes: { full: auto, limited: approve, read_only: approve }
  tools:                     # classification for permission gating (see Trust)
    - { extension: developer, name: shell, class: execute }
    - { extension: developer, name: text_editor, class: edit }
  unknown: deny              # only value allowed in v1; field exists to be greppable
quirks:
  system_prompt: extension   # extension | first-send-prefix (the fallback)
  system_prompt_method: "_goose/unstable/session/system-prompt/set"
  tool_meta_namespace: goose # where _meta.<ns>.toolCall carries {toolName, extensionName}
models:                      # optional static list; ModelDiscoverer preferred when engine supports it
  - { id: "qwen3.8", context_window: 128000 }
default_model: ""
```

Validation at load (mirroring goose's declarative tests,
`declarative.rs:465-535`): unique `name`; `engine` known; every `{var}`
placeholder is one the engine defines; `binary` and `version.probe_args`
present; quirk knobs only from the enumerated set; unknown YAML keys are a
hard error (a typo must not silently become an Unsupported behavior). A
descriptor that fails validation is reported and skipped — it never
poisons the registry (same stance as `Registry.Register` rejecting an
invalid matrix, `providers/provider/registry.go:26`).

Load order: bundled (embedded) first, then `$MADS_HOME/providers.d/*.yaml`
sorted by filename. A user file with a bundled `name` **overrides** the
bundled descriptor only for `engine: acp|pty` descriptors; shadowing a
`native` provider's descriptor is an error (a YAML file must not be able
to re-point `claude-code` at a different binary silently — that's a trust
boundary, not a convenience).

## Engines

### `providers/ptycli` (deferred, 2026-09-09)

Deferred to future work: the landscape survey (below) found the genuinely
PTY-only constituency reduced to aider (releases stalled 2026-02) and
borderline Crush — not enough to justify a v1 engine, and deferring it
also dissolves the open question of how conformance probes Required
behaviors through a raw terminal. Design notes preserved for whoever
builds it: `Start` generalizes kirocli/goosecli's `pty.go` (creack/pty,
`Events()` nil, `Send` injects a line, `Interrupt` writes `\x03`);
`OneShot` runs a templated argv collecting stdout to EOF (never stop
reading early — the goose broken-pipe lesson, `GOOSE-ADAPTER-DESIGN.md`
§"Error surfaces") and is Required, so a descriptor without a one-shot
template cannot be attached; below TrustFull, ungated PTY one-shots are
refused rather than run. The descriptor schema's `spawn.pty_args`,
`resume.pty_args`, `ref.pty_banner_regex`, and `oneshot.*` fields are
reserved for this engine; until it lands, per-harness PTY transports
(goose's banner-parsing `pty.go`) stay adapter code.

### `providers/acprpc` (extraction + generalization, v1)

The JSON-RPC 2.0 stdio client the goose design doc deferred
(§"Shape of the work" decided self-contained-for-v1 and named this
extraction as the follow-up; this is that follow-up). Spec-normative per
`GOOSE-ADAPTER-DESIGN.md` §"ACP v1 spec is normative", whose executor
rules become engine invariants: derive wire types from the ACP schema;
handle all five `stopReason`s; select permission options by `kind`, never
optionId; tolerate all twelve `sessionUpdate` variants; never advertise
`fs`/`terminal` client capabilities; quarantine `_meta.<ns>.*` behind the
tool-identity knob; gate resume/close/http-MCP on the capabilities the
agent advertised at `initialize`; always send `mcpServers` on
`session/load` (spec-legal, and required by goose 1.48).

**The knob set (walked 2026-09-09, two evidence sources).** goose itself
ships a generic ACP *client* driving five agents (claude-agent-acp,
codex-acp, copilot, amp-acp, pi-acp) through one engine with zero
per-agent branching — `AcpProviderConfig`
(`~/repo/goose/crates/goose/src/acp/provider.rs:61-76`) is nine knobs and
each per-agent file is a ~100-line config constructor. Cross-checking that
against our goose 1.48.0 captures gives the mads knob set:

| Knob | Why it exists (evidence) |
|---|---|
| spawn argv + env additions | trivially per-agent (`goose acp --with-builtin developer`; `copilot --acp`) |
| `env_remove` | claude-agent-acp misdetects a nested session unless `CLAUDECODE` is stripped (`claude_acp.rs:87`) |
| trust→mode-id map (+ send-at-session/new flag) | mode ids are pure vendor vocabulary: goose `auto`/`approve`, claude `bypassPermissions`/`default`/`acceptEdits`/`plan`, codex `agent-full-access`/`read-only`, copilot URL-shaped ids. Mode *transport* is runtime-detected, not a knob: `set_config_option` when a `category: mode` option exists, else `session/set_mode` (`provider.rs:718-747`) |
| model config-option id (empty = unsupported) | usually `"model"`; amp exposes no picker at all |
| pre-seed config options `[](id,value)` | copilot/pi want the model set at session/new, claude lazily after |
| system-prompt strategy: `none` \| `first-send-prefix` \| `extension-method{…}` | most ACP agents have **no** system-prompt surface (goose-as-client drops system prompts entirely, `provider.rs:820-826`); goose-as-agent has only `_goose/unstable/…`. mads must not silently drop agent personas, so the fallback chain is explicit |
| tool-identity strategy: `kind-based` \| `meta-namespace{ns}` \| `title-only` + classification table | ACP gives clients no canonical tool name; goose puts it in `_meta.goose.toolCall`, kiro sets `kind`, others offer only display titles. `title-only` ⇒ limited/read-only trust degrade to deny-all |

Everything else goose needed is runtime negotiation, and so is ours.
Anything a harness needs beyond this enumerated set is the signal that it
belongs in tier 4, not the signal to grow the knob set — each addition
requires a capture citation. Knob creep is how a "generic" engine becomes
two harnesses' quirks in a trench coat.

**Decision: OneShot is engine-emulated over ACP.** `OneShot` = spawn →
`initialize` → `session/new` (ref = `sessionId`, known synchronously) →
full-trust mode → optional system prompt → one streamed prompt → usage
from the result → `session/close`. Every acprpc descriptor therefore
satisfies the Required OneShot row with structured events and reliable ref
attribution — no per-harness one-shot argv, and goose's `--name`/session-
list ref fallback becomes unnecessary.

**Knob-fit verdict (2026-09-09, three live agents).** The knob set was
exercised against real captures from claude-agent-acp 0.75.1, codex-acp
1.10.0, and Hermes Agent 0.21.1 (native ACP) —
`providers/acprpc/testdata/captures/`, provenance README there. Verdict:
**every knob covered, no gaps found, and no new knobs needed.** Highlights
that would have been guesses otherwise: cancel stop-reasons genuinely
diverge (claude/hermes resolve `cancelled`, codex/goose resolve
`end_turn` — the engine's interrupted flag is confirmed load-bearing);
optionIds never mirror kinds (hermes offers only two options, no
`_always` pair — the fallback ladder is real); hermes has *no*
configOptions, so the model knob's empty state is exercised; all three
ToolIdentity strategies occur in the wild (claude sets `kind` +
`_meta.claudeCode.toolName`, codex/hermes set `kind` only, goose sets
neither); `env_remove` was verified live (`CLAUDECODE` et al. stripped —
the capture host was itself a Claude Code session); and both ACP-org
adapters tag modes with a shared `_meta.kind` taxonomy
(`standard`/`auto_review`/`full_access`) that hermes lacks — a candidate
future refinement for semi-automatic trust mapping, not a v1 knob.

**codex permission shape (source-pinned 2026-09-21).** Codex's default
`agent` mode auto-approved tool calls in every live capture, so its
`session/request_permission` shape was pinned from the installed
`codex-acp` 1.10.0 source instead (captures README §"codex permission
options"; a live capture is quota-blocked until 2026-10-09). This is the
strongest possible confirmation of the select-by-kind invariant: codex's
optionIds genuinely diverge from their kinds — a file-edit approval offers
`optionId:"allow_for_session"`/`kind:"allow_always"` and
`optionId:"cancel"`/`kind:"reject_once"`. A client keying on optionId
would mis-handle both. No new knob; the verdict above stands.

Migration: `kiro-cli` (research-grade, and non-conformant on optionId
selection per the goose doc's existing-code findings) and the goose
adapter itself become acprpc descriptors. The goose descriptor is the
engine's proving ground — its conformance run replays the committed
1.48.0 captures (`providers/goosecli/testdata/captures/`) through the
generic engine, which is a stronger test than either adapter had.

### `engine: native`

No behavior change. `providers/load` maps `name` → existing constructor
(`claudecode.New`, `fake.New`, …) and passes descriptor-sourced `Config`
fields (Bin, ExtraArgs from `autonomous_args` in the pipeline context).
The `streamjson` engine — parameterizing claude-code's stream-json dialect
for harnesses that clone it — is explicitly future work; claude-code stays
native in v1.

## Matrix derivation

**Decision: for descriptor providers, the capability matrix is computed,
never author-declared.** Each engine owns a base matrix; descriptor fields
flip individual rows only in the honest direction the engine can actually
deliver (e.g. `resume.pty_args` present → Resume Emulated; `trust.tools`
non-empty → TrustLimited/TrustReadOnly Emulated on acprpc; absent →
Unsupported). This is goose's safe-default stance (`CacheSemantics`
unknown → strictest) applied to our matrix: a YAML author — including a
user attaching a random agent — cannot claim Native anything by typing it.
`Matrix.Validate()` and the conformance suite still run unchanged on the
composed result; the IntentMatrix for descriptor providers is all
Unsupported in v1, kirocli-style.

## Trust for attached agents

The safety axis gets stricter, not looser, for user-attached agents:

- acprpc trust gating requires a `trust.tools` classification table;
  unknown tools **deny** under Limited/ReadOnly (the kirocli/goose
  precedent — never silently under-enforce). No table → both rows
  Unsupported and the Launchpad's existing degradation strings say so.
- `autonomous_args` is the one place bypass flags may live, applied only
  by the pipeline context exactly as today's hardcoded `ExtraArgs` are —
  a user descriptor may set it, but it never applies to interactive
  sessions.
- What the agent can *see* is unchanged: `agentenv` isolated-mode
  allowlist plus the descriptor's `env.passthrough` union
  (`providers/README.md` "what it can SEE" vs "what it can DO").

## Registration: `providers/load`

One new package:

```go
reg, warns, err := load.Registry(load.Options{
    Context:       load.Interactive, // or load.Autonomous (pipeline)
    MadsHome:      home,             // for providers.d/
    TranscriptDir: dir,             // fake provider plumbing
})
```

It loads bundled + user descriptors, validates, constructs per engine, and
registers. The four sites collapse to one call each; `cmd/mads run`'s
provider switch and per-harness arg flags are replaced by `-provider
<name>` + a generic `-provider-args` (append to spawn argv). The
`"claude-code"` default-provider fallback strings move to a single
`load.DefaultProvider` sourced from the bundled descriptor set.
`pkg/preflight` drops `knownProviderBinaries`/`compatMatrix` and iterates
descriptors — binary, probe args, semverish parse, compat window, install
hint are all descriptor fields now (the goose row added in `e4dfbd4`
becomes the goose descriptor's `version:` block, byte-for-byte the same
facts).

## Attach flow

v1 UX for "simply attach any existing agent":

1. `mads attach <path-to-yaml>` — validate, copy into
   `$MADS_HOME/providers.d/`, run the preflight probe (binary found,
   version parsed, compat window), print the derived matrix with
   per-row provenance ("Resume: Unsupported — no resume.pty_args").
2. `mads attach --verify` additionally runs a one-turn echo smoke through
   the real binary (goose's `test_provider_configuration` pattern: require
   the mechanics — spawn, first output, clean exit — never model content)
   and, for acprpc, a real handshake + `session/new` + interrupt.
3. The Settings Providers table and Launchpad pick the provider up through
   the existing registry-driven endpoints with zero UI changes; preflight
   surfaces unhealthy attachments exactly as it does for shipped
   providers today.

Writing a descriptor from scratch stays documented in
`providers/README.md` with the goose and (post-migration) kiro files as
worked examples.

## What "any agent" honestly means

| Agent on your machine | Tier today | Path |
|---|---|---|
| goose | 2 (acprpc) | bundled descriptor; captures already committed |
| kiro-cli | 2 (acprpc) | migrate existing adapter to a descriptor |
| claude-code | 4 (native) | unchanged; descriptor for metadata only |
| Gemini CLI, OpenCode, Cursor CLI, Copilot CLI, Cline, Qwen Code (ACP-native) | 1 | user YAML, no code |
| Codex (via `@agentclientprotocol/codex-acp`), any registry agent with an official adapter | 1 | user YAML pointing at the adapter binary |
| aider, Crush, other PTY-only stragglers | deferred | attachable when ptycli lands |

The promise this design keeps in v1: every ACP agent — native or via an
official adapter — attaches with a YAML file and gets full chat-mode
parity with shipped adapters plus engine-emulated one-shots. Bespoke wire
dialects still cost an engine, and PTY-only agents wait for ptycli — both
boundaries are stated at attach time, not hidden.

**Landscape evidence (2026-09-09 web survey).** ACP-native today: Gemini
CLI, OpenCode, Copilot CLI (public preview), Cursor CLI, Cline CLI 2.0,
Qwen Code, Kiro CLI, goose. The two holdouts, Claude Code and Codex, have
adapters maintained under the official ACP org
(`@agentclientprotocol/claude-agent-acp`, `@agentclientprotocol/codex-acp`),
and an official agent registry exists
(`github.com/agentclientprotocol/registry`, ~40 entries, JSON index on
their CDN — a natural future source for bundled descriptors; note there is
no real ACP conformance suite, registry CI only checks the handshake).
Genuinely PTY-only in real use: aider (releases stalled 2026-02) and
borderline Crush. **Decision (2026-09-09, Drew): ptycli deferred to
future work on this evidence** — the acprpc engine alone covers
essentially the whole market, and the deferral removes the
conformance-on-PTY open question entirely.

## Registration & plumbing (executor checklist)

1. `pkg/providerspec` (or `providers/spec`): descriptor types, YAML
   parsing, validation, template substitution. Pure, no exec — the
   goose-provider-types lesson (types crate with zero IO deps).
2. `providers/acprpc`: extract kirocli's RPC plumbing
   (pending-request table, single read loop, `beginRequest`/`closeWait`),
   apply the goose doc's spec-normative rules, add the knob set;
   conformance replays both harnesses' shapes (fakekiro + the goose
   captures).
3. `providers/load`: embedded bundled descriptors
   (`goose.yaml`, `kiro-cli.yaml`, `claude-code.yaml`, `fake.yaml`),
   providers.d loading, engine dispatch, `Options.Context`.
4. Swap the four registration sites + the two default-provider fallbacks;
   delete `-claude-args`/`-kiro-args`; `pkg/preflight` reads descriptors.
5. `cmd/mads attach` (+ `--verify` smoke).
6. Docs: `providers/README.md` descriptor authoring section; per-engine
   package READMEs in the house row-by-row format.
7. Wire-capture discipline holds: each bundled descriptor's facts trace to
   committed captures or are marked doc-sourced, kirocli-style; user
   attachments are exempt (they carry `--verify` evidence instead, and
   their matrix is derivation-capped anyway).

Sequencing note: this supersedes the goose plan's step 2 ("register in
four main.go files"). If the goose native adapter lands first per
`docs/goose-adapter-tasks.yaml`, it migrates to an acprpc descriptor here;
if this lands first, the goose adapter is *born* as the acprpc proving
ground and `providers/goosecli` never grows Go wire code. Prefer the
latter — the captures make it the best-evidenced target we have.

## Risks / open questions

- **Knob creep in acprpc.** The enumerated-knobs-or-tier-4 rule is stated
  above, but enforcement is review discipline. Mitigation: the knob set is
  one struct in `pkg/providerspec` with a doc comment requiring a capture
  citation per knob.
- **Template argvs invite injection-shaped bugs.** v1's descriptor argvs
  are static (model/mode/prompt all ride structured ACP params), but the
  reserved ptycli fields will substitute `{prompt}`/`{ref}`/`{model}` —
  when that engine lands, substitution must be argv-element-level only
  (never shell-joined), with values validated against conservative
  charsets. exec.Command with an argv slice keeps us out of shell quoting
  entirely.
- **Descriptor schema versioning.** goose skipped this and eats
  migrations in code. Add `schema: 1` now; reject higher majors with a
  "mads too old" message (same shape as the compat-window warning).
- **Descriptor conformance against one suite.** `conformance.Run` takes
  a Provider, so descriptor providers slot in — but the scripted stand-in
  story per descriptor needs a convention (a `testdata/` sibling per
  bundled descriptor). Executor to establish it with the goose captures
  first.

## Future work (explicitly not v1)

- `providers/ptycli` floor engine (deferred 2026-09-09 on the landscape
  evidence — revisit if a PTY-only agent people actually use appears; the
  reserved descriptor fields and the design notes in its section above are
  the starting point).
- `streamjson` engine; migrate claude-code onto it if the parameterization
  stays honest.
- Settings UI CRUD for `providers.d/` (attach wizard in the app, not just
  the CLI), riding the existing first-run preflight surfaces (W6/W7.3).
- Model-discovery caching keyed by a descriptor+binary-version hash
  (goose's `inventory_key` pattern).
- Descriptor sharing through the remote registry (W7.6) — a community
  descriptor catalog is exactly goose's bundled-definitions dir grown a
  distribution channel.
- Permission-request bridging to the platform UI (`PermissionPromptURL`)
  as an engine-level feature — flagged in the goose doc's future work;
  lands once for all acprpc descriptors instead of per adapter.
