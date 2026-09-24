# The "Agent" concept, redesigned

Re-splits the mads concept of an **Agent** (spec §9) into two things that are *both*
user-facing and both load-bearing: **Persona** and **Harness** (the existing
provider). It does **not** add a third user-facing object. The harness and the
persona are both already first-class on the wire; this document re-homes the
decision about which one owns what, removes one field, and turns persona
selection into an automated default with a visible override.

Where this document and the other design docs disagree (most relevant:
`GOOSE-ADAPTER-DESIGN.md`'s "the system prompt is an agent-definition input,
harness-agnostic" claim, `DECLARATIVE-PROVIDERS-DESIGN.md`'s
per-provider `Config` keys, and `pkg/agentselect`/`pkg/agentgen`'s drafts),
**this document wins** for the parts it touches, and defers to them for the
parts it doesn't (provider registration, model discovery).

## Why two, not three

`pkg/contracts/profile.go` already gives us a named, switchable, per-field
config snapshot (profiles-v2). `contracts.Session` already splits `Provider`
(harness) from `Agent` (persona). The existing data model is therefore *already
two-way* — the mess is that the persona has absorbed two bits of *harness*
state: model preferences and concrete tool names. This document does not add a
third object (the `Persona`, `Runtime`, `Harness` split proposed in the
earlier design pass). The `Runtime` was a data-model seam that the UI never
needed; dropping it is correct and simplifies both the wire and the Library.

## The split (final)

### 1. Persona (what, not how) — *this document's new object*

Persona is a **registry object** (spec §9) with exactly these fields:

| Field | Type | Notes |
|---|---|---|
| `id` | `string` | immutable, kebab-case |
| `name` | `string` | display name |
| `description` | `string` | one-liner, appears in Library card and auto-pick prompt |
| `prompt` | `string` | system prompt; the persona's voice and rules |
| `allowlist` | `[]AbstractTool` | abstract tool *intents* this persona is allowed to use (see §2) |
| `denylist` | `[]AbstractTool` | abstract tool intents this persona is forbidden from using; overrides allowlist |
| `quadrant` | `quadrant` | existing: `autonomous_executor`, `technical_partner`, `investigator`, `research_analyst` |
| `tags` | `[]string` | existing: freeform, used by Library search and auto-pick scoring |
| `system` | `bool` | existing: ships with mads, editable not deletable |

**Removed from the persona (deprecations, not renames):**

- `capabilities.tools` (concrete tool names) — moved to `allowlist`/`denylist`
  as abstract intents (see §2)
- `capabilities.mcp_servers` — stays a capability-registry reference (it is
  *already* harness-agnostic: an MCP server is a named process/URL)
- `capabilities.skills` — stays a capability-registry reference (already
  harness-agnostic: a skill is a document)
- `permissions.allowed_paths` — **stays on the persona**. Path restrictions are
  a *persona* property ("this persona doesn't touch `vendor/`") — they are
  about the role, not the harness
- `preferences.model` — **removed entirely**. A persona does not know which
  model it will run on; this was the single most harness-leaky field and is
  now the model's own concern (the harness + the user at session start)
- `agentgen`'s `Prompt`-drafting prompt is preserved but its JSON shape
  (spec §9.5) drops `preferences.model`; the golden files in
  `pkg/agentgen/testdata/*.golden.json` get regenerated without that field.

The **`prompt` field is the persona's only prose**, and it is the one field the
harness adapter injects via `StartOptions.SystemPrompt` (which is already
harness-generic — `ModeACP` and `ModePTY` both consume it the same way;
verified against `packages/goosecli/goosecli.go:153-158` and the claude-code /
kiro adapters). This part of today's code **does not change.**

### 2. Harness (a provider, as today)

`providers/provider.Provider` and the capability matrix are untouched. The one
concrete addition is **tool-intent resolution**, which today is implicit and
harness-specific:

```go
// in providers/provider/provider.go, new
type ToolIntent string
const (
    IntentRead   ToolIntent = "read"
    IntentWrite  ToolIntent = "write"
    IntentEdit   ToolIntent = "edit"
    IntentShell  ToolIntent = "shell"
    IntentSearch ToolIntent = "search"
    IntentFetch  ToolIntent = "fetch"
    IntentSkill  ToolIntent = "skill"     // a capability-registry name
    IntentMCP    ToolIntent = "mcp"       // a capability-registry name
    // ... extend as needed
)
```

And the harness adapter declares, per tool intent, **one of**:

- the concrete tool name(s) it exposes for that intent (claude-code's `Read` /
  `Write` / `Edit` / `Bash` / `Grep` …; goose's `read` / `write` / `edit` /
  `shell` / …; kiro's equivalents);
- OR `ErrUnsupported` for intents this harness has no mapping for (kiro might
  not have a first-class `IntentSearch`, for example).

The resolver is the *only* place a harness-adapter knows about concrete tool
names. Every other surface (persona config, UI, runner) talks **intents only**.

This satisfies both of the two constraints this redesign has to satisfy:
1. **The persona decides** which intents are allowed/denied (owner decision:
   "this persona may not `shell`" is a *persona* decision, not a harness
   decision — because the same intent means "exec a command" on every harness
   that supports it, so the restriction is role-shape, not harness-shape).
2. **The tools themselves are provider-agnostic on the persona** (owner
   decision: no tool *name* ever appears in a persona, because "name" is a
   harness vocabulary and "intent" is not).

### 3. Session start (the only required selection the user makes)

A session still has two fields on the wire, `Provider` and the persona
reference. The user-facing surface for picking them is **a single dropdown**
where each option is a pair: `[harness] model`. Examples:

```
[Kiro]         Claude Sonnet 5
[Claude Code]  Claude Sonnet 5
[Goose]        Ollama Qwen3.8
[Goose]        Claude Sonnet 5
```

- The **harness** part is a property of the option, not an independent
  selection — this is the user's Level-1 ask.
- The **model** part is a property of the option — it can change mid-session
  and **swapping models mid-session is supported exactly as today** (the
  runner's `SetModel` path already re-bounces the provider process, verified
  against `services/session/internal/runner/model_test.go:102-157`).
- The **persona** part is **not** in this dropdown. It is auto-picked (next
  section) and overridden by a separate, always-visible UI affordance (a
  chip, not a modal).

**Explicit out of scope (owner decision):** swapping the *harness* mid-session.
It is technically possible (goose's system-prompt strategy is `mode:"append"`
with a stable key, so a re-bounce across providers *could* work), but the
duplicate-persona and capability-re-resolution failure modes (see
`GOOSE-ADAPTER-DESIGN.md`'s "Trust enforcement" section and the goose adapter's
`goosecli.go:153-158`) are not worth defending for v1. The runner treats a
harness change across `SetModel` calls as an **error**, not a swap. This keeps
one invariant hard: the persona is injected once at session start, by exactly
one harness, under a stable key.

### 4. Persona auto-pick (the user doesn't make this selection)

Instead of the user searching a persona list, the runner, **at session start
only** (not per-message), calls the Fast Model — exactly the §11.8 baseline —
in a *thinking-disabled* one-shot with a fixed small prompt. The prompt says:

> Given the user's prompt "<user prompt>" and the following candidate personas
> (id, description, allowed tool intents), return the id of the best-matching
> persona. Reply with the id alone.

Candidate set: every **non-`system`-excluded** persona from the registry, plus
the workspace's `DefaultPersona` (a field added here, see below). Scoring:
exact-id, then description-overlap, then quadrant-overlap with the request's
class (a classifier already exists in `pkg/agentselect/classify.go` — it is
currently used for *agent* matching; this re-uses it on *persona* matching,
which is the *same object after this redesign*).

**Failure and fallback:** if the Fast Model call fails (binary missing,
network, rate-limit), the runner **falls back to the workspace default persona**
(no persona at all — i.e. the harness's native persona — is a legal outcome and
the persona-chip shows `none`). The session starts; nothing is broken. This is
important: the auto-pick is an *enhancement*, not a dependency. The §11.9
rule already says "inform and wait, never silent retry" — the auto-pick
follows the same rule.

**Override:** the persona-chip in the conversation header is always visible
and editable. One click swaps it; the same persona-injection idempotency key
applies (see §5). There is no separate "pick a persona" screen. The Library
still has the full persona list — that's the *definition* surface (§6), not
the selection surface.

### 5. Persona-injection idempotency (the seam that keeps this safe)

When the runner bounces a live session onto a new **model** (mid-session
`SetModel`), the harness re-invokes `Start` with the *current* `StartOptions`
in full. Today this re-sends `SystemPrompt`, and on goose that is fine
because the strategy is `mode:"append"` with a stable key — the harness
recognizes the key and overwrites. But "fine because goose happens to have a
stable key" is exactly the kind of thing that silently breaks on the next
provider. The runner therefore:

- Sets a **`PersonaKey`** field on `StartOptions` (the harness is expected to
  respect it as the idempotency scope for the persona payload).
- Sets the persona text on `StartOptions.SystemPrompt`, as today.
- The *adapter* is *expected* to use its own keying — goose already does
  (`"mads-agent"`, `goosecli.go:156`), claude-code's `--append-system-prompt`
  is idempotent by construction (it appends to the *existing* prompt), kiro
  should be audited against its own `mode:append` semantics.

This keeps the persona stable across mid-session model swaps (the supported
case), without relying on each harness to guess it should keep it stable.

### 6. Persona definition surface (the Library)

A new (or repurposed) **Persona** tab in the Library:

- **List** — card view, `name` + `description` + `quadrant` badge + `allowed
  intents` chip row. Search by name / tag.
- **Edit / Create** — a single edit screen, four fields:
  1. **Name / Description** — text inputs (existing UX)
  2. **System prompt** — a textarea (this is the "text entry area for system
     prompt" from the owner's plan, unchanged)
  3. **Tool intents** — a chip list over the closed set in §2, with an
     `allow` / `deny` segment on each chip. Deny overrides allow. This is
     the "list of tools known to mads that can be allowlisted or denylisted"
     from the owner's plan — and it is *abstract by construction*, so the
     user can't accidentally put a harness-specific name in there.
  4. **MCP servers / Skills** — the two existing lists, unchanged (they are
     already harness-agnostic)

There is no `model` field on this screen. There is no `harness` field on this
screen. The persona is harness-free by UI *and* by data model.

### 7. Workspace defaults (the anti-fatigue)

Add to `WorkspaceDefaults` (spec §10):

| New field | Type | Default |
|---|---|---|
| `default_persona` | `string` | `""` (no persona) |
| `default_provider` | `string` | existing (global `DefaultProvider`) |
| `default_model` | `string` | `""` (harness's own default) |

A workspace with `default_persona: reviewer` and `default_provider: goose`
and `default_model: qwen3.8` means: *every session started in this workspace
starts on Goose/Qwen with the reviewer persona, auto-chosen*. The auto-pick
(§4) **respects** the workspace default as its *first-choice* candidate (it is
always in the candidate set, and if the Fast Model picks a different persona,
the user still sees the chip and can override). This is the rule that turns
"what should I be by default on this repo" from a per-session ritual into a
per-workspace property — the owner's anti-fatigue goal.

## Compatibility & migration (concrete)

| Site | Change | Notes |
|---|---|---|
| `contracts.Agent` | Split: `prompt`, `quadrant`, `tags`, `allowlist`, `denylist`, `mcp_servers`, `skills`, `permissions.allowed_paths` stay; `preferences.model`, `permissions.denied_commands`, `capabilities.tools` **removed** | `denied_commands` (a *string* list) is *replaced* by `denylist` (a *type* list) — it was a harness leak (a command string is a harness vocabulary), see §2 |
| `contracts.Session` | No wire change; `Session.Agent` now means a Persona id (it already pointed to `contracts.Agent`, which is now narrower) | `Provider` field unchanged; `Model` field unchanged |
| `services/session/internal/runner/runner.go:371` | `agent.Prompt` → `persona.Prompt`; no change in behavior | `PersonaKey` now set (see §5) |
| `pkg/agentgen` | Drop `preferences.model` from the system prompt + JSON shape; add `allowlist`/`denylist` to the drafting prompt; regenerate `testdata/*.golden.json` | The near-duplicate warning in `agentselect.FindSimilar` is unaffected (it scores on `description` + `quadrant` + `tags` + tool sets — all present on the narrower persona) |
| `pkg/agentselect` | `Select` signature unchanged; it now scores over personas instead of agents | The user-facing "auto-pick" entry point becomes a new function `AutoPick(ctx, prompt, candidates, workspace)` in this package, thin over Fast Model + the scorer |
| `providers/claudecode`, `providers/kirocli`, `providers/goosecli` | Each declares a `ToolIntentResolver` mapping | This is the *only* new code in the provider packages. The conformance suite gains a §2 assertion: every intent the harness *claims* it supports (via `Matrix`) must be present in its `ToolIntentResolver` (see `providers/conformance/conformance.go`) |
| `ui/src/library` | New Persona tab; the Agent tab's *edit* surface moves here and the Agent tab becomes a *harness* tab (providers + their models) | UX change, not data-model |
| `ui/src/conversation/ConversationTab.tsx` | Always-visible persona-chip in the header, backed by `AutoPick` at first turn and user-overridable | The existing `useProviderModels` hook becomes `useProviderPersonas` after the split |
| `cmd/mads/run.go:32-89` | The literal `-provider` switch stays; the per-harness `-claude-args` / `-kiro-args` flags become **deprecated** in favor of the workspace defaults in §7 | Migration is additive: the old flags still work, the new workspace fields are the *preferred* path |

## Open items (not blocking the design, explicit decisions)

1. **The closed set of `ToolIntent` values is currently a *proposal*, not a
   registry.** Should the runner expose it as a capability-registry *type*
   (so a new intent = a registry entry, hot-reloadable like MCP servers), or
   keep it as a *compile-time* closed set in `providers/provider`?
   **Recommendation:** compile-time set, because it's a vocabulary mads owns
   and a registry entry implies "users can invent new ones," which is
   exactly the harness-leak we're trying to remove. (Open to pushback.)

2. **`denied_commands` removal is the most aggressive change in this document.**
   The field is a string list of shell commands the persona must not run — a
   harness-shaped restriction on a persona-shaped object. The redesign
   replaces it with `denylist: [shell]` (intent-level) plus the persona's
   already-existing `allowed_paths`. If a user needs command-level denial,
   they set `allowed_paths` + `denylist: [shell]`. **This is a behavior change
   for existing personas that used `denied_commands`** — a migration task on
   the data service, not an automatic rewrite. Flagged as a decision, not a
   defect.

3. **The auto-pick prompt needs its own golden test** in `pkg/agentselect`'s
   existing test suite — a fixture Fast-Model response ("best: reviewer") and
   a failure case (timeout → workspace default → no persona). The fixture
   mechanism already exists in `providers/fastmodel/testdata/`.

## What this document deliberately does NOT do

- Does not unify the four harness adapters' tool *names* (that is the ACP
  convergence path in `GOOSE-ADAPTER-DESIGN.md`; our §2 mapping is the
  pragmatic bridge until then).
- Does not make persona selection *required* (no persona is a valid
  configuration; the chip shows `none`).
- Does not add a per-session "pick a harness" second confirmation after the
  dropdown — the dropdown *is* the harness selection, and the user only
  picks once.
- Does not add a per-session "pick a persona" screen — the chip *is* the
  persona UI.

## Acceptance

- A user can start a session on *any* currently-registered harness with a
  model of their choice, with a persona auto-picked and overridable (the
  chip). The persona's prompt, allow/deny set, and MCP/skill list flow to the
  harness through `StartOptions` exactly as today.
- A user can define a persona in the Library with (name, description, system
  prompt, allow/deny intents, MCP servers, skills) and **no other fields are
  required, no harness-specific fields exist.**
- A user can set a workspace default persona + provider + model, and *every*
  session started in that workspace inherits it.
- `make check` is green; the conformance suite includes the new
  `ToolIntentResolver` assertion; the golden files in `pkg/agentgen/testdata/`
  regenerate cleanly.

## Implementation status (2026-09-24 — full pass)

**Done and verified end-to-end.** Every item in "Not yet done (the rest of
this doc)" from the 2026-09-22 note has landed in this pass:

- **The persona split itself** — `contracts.Agent` gains `Allowlist` /
  `Denylist` over the closed `ToolIntent` vocabulary
  (`pkg/contracts/toolintent.go`); `EffectiveToolIntents()` folds the legacy
  `capabilities.tools` + `denied_commands` shapes in so existing records keep
  their enforcement (deny always beats allow). `WorkspaceDefaults` gains
  `Persona` (+`PersonaID()`/`SetPersona()`) and `Provider`.
- **The `ToolIntentResolver` (redesign §2)** — `providers/provider` defines
  the vocabulary + `ToolIntentMap` + `ToolIntentResolver` interface;
  claude-code, goose, and kiro each declare their concrete tool-name mapping;
  `providers/conformance.CheckToolIntents` pins it and each adapter's test
  package asserts it.
- **claude-code native enforcement (redesign §2)** —
  `providers/claudecode/personaTools.go` resolves the persona's
  Allowlist/Denylist into a `--disallowedTools` argument (8-case golden
  `TestPersonaDisallowedTools`); wired into `session.go`'s start path.
- **The persona auto-pick (redesign §4)** — `pkg/personapick.AutoPick`, a
  thinking-disabled Fast Model one-shot (golden-pinned prompt,
  `TestAutoPickGoldenPinsThePrompt`) over the persona registry + workspace
  default, with the documented fallback chain
  (skippable / unusable-output / hallucinated-id → workspace default /
  none). The router's `resolveAutoAgent` calls it; the two legacy
  capability-inference tests were replaced with Fast-Model pick tests
  (`TestCreateSessionAgentAutoPicksAFastModelPersona`,
  `TestCreateSessionAgentAutoFallsBackToWorkspaceDefault`).
- **The runner wiring (redesign §2/§5)** — `services/session/internal/runner`
  passes `StartOptions.ToolIntents` (Allowed+Denied) and a stable `PersonaKey`
  on every Start. For goose/kiro (no native surface), the persona's allowance
  is additionally spoken into the system prompt
  (`services/session/internal/runner/personaPrompt.go`) — an honest
  prompt-level fallback for harnesses whose `IntentMatrix` declares
  Unsupported.
- **`agentgen` shape (redesign §1)** — the draft shape drops concrete tool
  names and command strings in favor of `allowlist`/`denylist`
  over the closed vocabulary; `GenerateFromTask` still honors task-level
  required tools by mapping them to intents and folding them in. Golden
  files regenerated (`pkg/agentgen/testdata/*.golden.json`).
- **The data store (redesign §2)** — `services/data/internal/store/registries.go`
  rejects any allowlist/denylist value outside the closed vocabulary (a
  shape error, 422), so the harness-leak is structurally impossible at
  write time, not just at enforcement time.
- **CLI `mads personas` (redesign §6 + §3)** — `mads personas
  list/get/new/delete` against the data service; `-allow`/`-deny` values
  are the closed ToolIntent vocabulary (a concrete tool name is dropped
  loudly, not silently). `mads new-session -agent` now validates the
  value against the persona registry (or accepts `auto` for the
  §4 pick).
- **UI (redesign §3/§4/§6)** — `types.ts` gains `ToolIntent` +
  `Agent.allowlist/denylist` + `WorkspaceDefaults.persona/provider`;
  the Library's "Agents" shelf becomes the **"Personas"** shelf (same
  records, narrower concept); the persona editor gains the **tool-
  allowance chip list** (an allow / deny / unset segment on each chip,
  over the closed set — `ui/src/library/agents/ToolIntentChips.tsx`);
  the Launchpad's persona pill stages the persona **id** (the wire
  value) and its `Auto` option is the §4 pick. `make check` is green.

---

## (prior) Implementation status (2026-09-22 — first working pass)

**Done and verified end-to-end** (a user can now `mads new-session
-provider goose` and start a live goose session through the router):

- **CLI wiring** — `cmd/mads/seed.go`: `new-session` now accepts
  `-provider`, `-model`, and `-agent` (they persist onto the Session record,
  which the data service → router → spawn chain already honored); a new
  `providers` subcommand lists the harnesses + one model set straight from the
  router's registry.
- **The root blocker was found and fixed** — goose 1.51 rejects a *relative*
  `cwd` in `session/new` with `-32602 "cwd must be an absolute path"` (claude-code
  and kiro tolerate it). The ACP engine (`providers/acprpc/session.go`) now
  absolutizes `StartOptions.Cwd` before both `session/new` and `session/load`,
  and the goose fake profile mirrors goose's rejection (`requireAbsCwd`) so
  `TestStartRelativeCwdIsAbsolutized` pins the fix.
- **Build environment note** — Go 1.27 dropped `http.Response.Close()`; the
  new CLI code closes via `resp.Body.Close()` (a small `closeBody` helper keeps
  the linter quiet). Any future `http.Get(...).Close()` in `cmd/mads` will not
  compile here.

**Not yet done (the rest of this doc):**

- The **persona auto-pick** (Fast Model, thinking-off) and the **override
  chip** are still design only — the current `AgentPill` in the Launchpad is
  the interim persona UI and still works.
- **Persona split** (`Persona` vs. registry `Agent`, `allowlist`/`denylist`
  tool intents, `ToolIntentResolver`, `Preferences.Model` removal, the persona
  Library tab, `WorkspaceDefaults.default_persona/provider/model`) is unstarted.
- **Goose ACP assistant-reply transcript gap — closed.** The goose ACP
  *assistant* reply streamed to the UI live (`GET /output` SSE) but was not
  captured into the `GET /transcript` record (the user message was). Now fixed
  end-to-end: the `acprpc` engine's `streamSession` buffers its own running
  record — `Send` records the user message and `handleUpdate` coalesces every
  streamed `agent_message_chunk` (by `messageId`), `user_message_chunk`,
  `tool_call` and `tool_call_update` into a per-session `transcript` buffer,
  exposed via `Transcript()`; the Runner
  (`services/session/internal/runner/transcript.go`) now **prefers** a live
  `provider.Session` that implements `Transcript() []provider.Message` over
  its provider-level disk `TranscriptReader` (which goose ACP cannot serve
  mid-turn — it has no on-disk transcript). A running goose ACP session's
  assistant reply is therefore readable from `GET /transcript` in real time.
  Pinned by `TestACPLiveTranscriptCapturesAssistantReply` (acprpc) and
  `TestTranscriptPrefersLiveACPBuffer` (runner).
- **Goose's `session/new` now requires an existing absolute cwd** — the
  absolutize fix assumes the runner's cwd exists (it always does: it's the
  workspace/worktree dir). A caller passing a relative *nonexistent* dir still
  fails at `exec` `chdir`, which is the correct error.
