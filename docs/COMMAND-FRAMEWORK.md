# Command framework (/ # @) — design and as-built reference

Status: first pass shipped on branch `session-commands`. This documents the
system as implemented; parked work is listed at the end. Visual references:
`docs/mockups/command-framework.html` (namespaces, card kit),
`docs/mockups/session-commands.html` (the session command set; `/usage` is
layout B, "Ledger"). Task graphs: `docs/session-commands-tasks.yaml`
(this pass), `docs/command-framework-tasks.yaml` (parked remainder).

**Supersedes** MADS-UX.md's "provider-native slash commands exist only inside
terminal-mode passthrough". `/` is now mads's own command registry, usable in
every chat session on every harness; harness-native slashes are not listed or
specially handled (see Namespaces).

## Namespaces

| Trigger | Means | Backing source |
|---|---|---|
| `/` | mads **commands** | the descriptor registry (`COMMANDS` in `commands/registry.ts`); nothing else |
| `#` | **skills** (agent capabilities) | `GET /capabilities` (`type: skill`), filtered by the agent's bindings, health-badged |
| `@` | **references** | `GET /mentionables` — skill reference docs, session summaries, workspace steering files |

All three are mads-level and provider-independent. `@` and `#` splice a
plain-text token into the message and resolve to injected content at send time;
`/` runs something. Detection and menus live in `useMentionResolver.ts`
(`detectTrigger`): `@` and `#` tolerate no whitespace after the trigger; `/`
tolerates exactly one interior space so `/model sonnet` drives a two-level menu
(command, then its options).

**Unknown `/words` pass through.** On send, `parseCommandLine` + `findCommand`
look up the typed name (or alias) among *visible* descriptors. No match means
the text is sent to the harness as an ordinary message, unchanged. mads does
not keep a list of harness-native commands; a harness that understands
`/clear` gets it as text.

## Emulation rule

A mads command is **harness-agnostic**. When an action is something a harness
can't do natively, mads emulates it with a **user-sent message** (a normal
turn the user could have typed) that the UI **shows as a system message**
rather than as the user's own words.

**One exception: compaction is never emulated.** A summary mads wrote itself
would be a different conversation from the harness's own, so where the
provider matrix says compaction is unsupported, every Compact affordance
(`/compact`, the + menu's "Compact now", ContextRim's link, the `/usage`
card's link) is hidden or disabled with a reason. Where compaction works but
`compaction_focus` is unsupported, the focus text is **refused, never
silently dropped**.

As built, no first-pass command needs the emulation path (each is handled by
mads itself or by native compaction); it is the rule a future command follows.

## Descriptors: adding a command

A command is one `CommandDescriptor` (`commands/types.ts`) in its own file,
plus one line in `COMMANDS` (menu order). Nothing else changes; the menu,
autocomplete, aliases, availability and dispatch all read the table.

| Field | Purpose |
|---|---|
| `name`, `aliases?` | Without the slash. Aliases resolve and match in the menu (`/status` -> `/usage`); a row matched only by alias says so. An exact name/alias match sorts first, so `/new` + Enter is `/new`, not `/new-leader`. |
| `description`, `argHint?` | Hint shown after the name (`<title>`, `[focus]`); may be a function of the context. Having a hint means picking the row inserts `/name ` for typing args instead of executing. |
| `argRequired?` | Message shown inline when invoked with no args; nothing runs. |
| `argError?(args, ctx)` | Blocks a run with a message and keeps the typed text. |
| `options?(ctx)` | Second-level menu rows (`/model ›`, `/agent ›`); `{id, label, detail}`. |
| `visible?(ctx)` | False hides the command entirely: not in the menu, not recognized when typed (so it passes through as text). Used for feature flags. |
| `available?(ctx)` | `{ok:true}` or `{ok:false, reason}` (below). |
| `runsWhenUnavailable?` | Unavailable still disables the menu row, but typed + Enter runs `execute` anyway, for commands that render their own blocked state. |
| `execute(invocation, api)` | `invocation = {args, option?}`. |

**`CommandContext`** is what a descriptor can see, built by the Composer each
render: `routerClient`, `sessionId`, `agentId`, `provider`, `compaction`
(`CompactionSupport`), `session`, `linkedSessions`, `leader`,
`pipelineRunning`. A command needing more adds an optional field and the
Composer prop that fills it.

**`CommandApi`** is what `execute` can do; the Composer has already cleared
the command text from the input, and nothing here writes to the transcript:
`updateSession(patch)` (the dock pickers' commit path; resolves true when
queued behind a running turn), `notify(msg, action?)`, `fail(msg)`,
`openCard(render)`, `openTask?(id)`, `compact?(instructions?)` (the "Compact
now" button's path; absent when the host can't).

## Availability and disabled-with-reason

`available(ctx)` returns `{ok:false, reason}` to disable a command. In the `/`
menu the row renders disabled with the reason inline and cannot be selected.
Typing the command and pressing Enter shows the reason in a notice
(`CommandNotice`) and **sends nothing**, so a refused command never leaks as
literal text. The same notice carries `argRequired` and `argError` messages.
Distinct from `visible`: a hidden command is absent and passes through;
an unavailable one is present, explained, and refused.

## Cards

`CommandCardHost` renders a command's card directly above the composer: **no
header or footer chrome**, dismissed by **Esc** or a **click-away**
(mousedown outside; listener deferred a tick so the opening click doesn't
close it). Focus returns to the composer. The host takes no focus itself; a
card autofocuses its own control. One card at a time (`openCard` replaces).
**Cards are UI only and never written to the transcript**, which also avoids
the old render-then-vanish flash of sending `/context` as literal text.

## Commands

### /task `<title>`
Creates an empty task bound to this session (`createTask({title, session_id,
source:"user"})`). Nothing goes to the agent. Success toast "Task created: ..."
with an "Open task" action (when the host can navigate); a Router error is a
failure toast. No title: inline "/task needs a title".

### /model `<name>` and /agent `<name>`
Second-level menus: `/model` lists the session provider's models
(`listModels(provider)`), `/agent` lists registry agents. Picking, or typing an
exact id/label and Enter (`pickOption`), calls `updateSession({model|agent})`
and toasts "Model set to X". If the change queued behind a running turn the
toast adds "applies when the current turn finishes". An unmatched name is a
failure toast, nothing changes. `/model` is unavailable until the session has
a provider.

### /compact `[focus]`
Runs the harness's own compaction through `api.compact(focus?)` — exactly the
"Compact now" path (progress, errors and the transcript marker come from
there; `POST /sessions/{id}/compact`, optional `{"instructions"}` body, ->
`provider.Compactor.Compact(ctx, instructions)`; auto-compaction passes `""`).
Gated by `CompactionSupport {compacts, takesFocus}`, derived from the matrix
rows `compaction` and `compaction_focus` (`ui/src/router/compaction.ts`):
- not `compacts`: unavailable, reason "<provider> can't compact".
- `compacts` but not `takesFocus`: the `[focus]` hint disappears, and
  `/compact <text>` is blocked with "<provider> can't take compaction
  instructions — send /compact alone". A `Compactor` that can't take focus
  returns `ErrUnsupported` for non-empty text rather than dropping it.
- Matrix invariant: `compaction_focus` working requires `compaction` working.
An unknown provider or matrix reads as supported (only an explicit
declaration hides Compact); the Router's own 400 backstops a wrong guess.
Today: claude-code and fake native/native; goose-cli native/unsupported;
kiro-cli unsupported/unsupported; ACP providers (acprpc) native only where a
probe found a compact command, else unsupported.

### /usage (aliases /status, /context)
Opens the **Ledger** card: one narrow column — Health; Context (meter, plus
"Compact now" when allowed); Tokens · session (in/out, from cache,
"↳ delegates", turns); then Plan limits **or** Spend. Absent data means an
absent section. Input is one read, `GET /sessions/{id}/report`
(`contracts.SessionReport`, assembled by `contracts.BuildSessionReport`); the
card refreshes live on turn-end / agent-state events while open. UI only.
- **Health** (always present): state `working|idle|sleeping|crashed`, harness,
  version and preflight verdict (preflight memoized 5 min); process start and
  last event only when a process runs (child `GET /status`, 2 s timeout).
- **Context**: `used` from last usage (`context_tokens`, else input + cache),
  `window` when the provider reported one (else the UI's static per-model
  table), `can_compact` = the matrix's `compaction` works for the provider.
  Meter tone: green < 60%, amber < 80%, red above.
- **Billing mode** `plan | usage | unknown`: only claude-code reports it
  (`apiKeySource`); every other provider is `unknown`.
  - `plan` (subscription): **limits** only — account-wide 5-hour/Weekly windows
    (`used_pct`, `resets_at`); windows already past their reset are dropped.
    No cost.
  - `usage` (API key): **cost** only — `{usd, basis:"list_price"}`, shown when
    > 0. No limits.
  - `unknown`: neither. A cost an ACP harness reported is **dropped**, since
    nobody can say whether it is money spent.
- Tokens come from persisted `UsageTotals` (scoped `POST
  /sessions/{id}/usage`), delegates' share included and broken out.

### /new `[goal]`
Opens the new-session card: a goal field prefilled from the args and the
shared Launchpad pills (folder, agent, model, trust, isolation) **prefilled
from this session** and editable (`stagedFromSession`). Enter starts the
session in the background, sends the goal (queued) and stays, with an "Open"
toast; Cmd+Enter creates it, hands it to the Sessions surface and navigates.
Start needs a non-empty goal. Every pill change is dry-run against `POST
/sessions/validate` (250 ms debounce); any blocking error disables both Start
actions with the server's message.

### /new-leader, /new-follower, /new-monitor `[goal]`
The same card plus a **relationship row** and a **cadence** switch. The link
is created atomically with the session as a staged link on `POST /sessions`
(one endpoint is the `$new` placeholder). Hidden entirely unless
`feature_flags.linked_sessions` is on (so typed, they pass through as text).

| Command | Preset | Holder (grants) | Subject | Row reads |
|---|---|---|---|---|
| `/new-leader` | leader | the new session | this session | new ── leads ──▶ this |
| `/new-follower` | leader | this session | the new session | this ── leads ──▶ new |
| `/new-monitor` | monitor | the new session | this session | new ── watches ──▶ this |

The arrow carries a prose tooltip of what the link allows (leader: read, send
messages, direct via orchestrate; monitor: digest only, can't message).
**Cadence** (`on_signal` default, `per_turn`, `manual_only`; labelled "on
signal / per turn / manual") is applied to the link and explained by a
tooltip.
- **Validation:** the dry run applies `resolveSessionDefaults`, directory and
  provider checks, staged-link planning and the Data Service link rules
  without creating anything. It always answers 200 with `errors` (field
  errors); the UI re-implements none of it.
- **Worktree join:** when the counterpart is worktree-isolated in the same
  repo, the response resolves `dir` to its worktree, `isolation:
  "current_dir"`, and `joined_worktree_of {session_id, name}`. The card locks
  the Isolation pill ("shares <name>'s worktree", tooltip) and shows an info
  banner.
- **One leader per session:** `/new-leader` is disabled in the menu with
  "<this> is already led by <name>" when this session has an orchestrate
  holder (from the session's links), or "pipeline runs can't be led" when a
  pipeline run is live. It sets `runsWhenUnavailable`, so typing it opens the
  card in a blocked state (banner, Start disabled) rather than a bare notice.
  The server remains authoritative for anything the client state misses.

### /export `[md|json]`
Downloads the whole conversation (`GET /sessions/{id}/export?format=md|json`,
`Content-Disposition: attachment`, filename from session name and date).
Always complete: tool calls/results, thinking, system and link messages, and
every delegate's own transcript. JSON is the wrapper `{schema:
"mads.export/v1", exported_at, session, messages, delegates}`; Markdown is a
rendering of it (tool calls folded in `<details>`). Format resolution:
1. an argument (`md`/`json`) overrides for this export only; anything else is a
   failure toast;
2. else `Config.export_format` (`""` = ask, `md`, `json`; editable in Settings
   -> Conversation) downloads immediately;
3. else a small card offers Markdown / JSON, "Always use this format", and
   Download; remembering writes `export_format` (read-modify-write, since
   `PUT /config` replaces the row).
The server requires an explicit format (400 otherwise); the UI resolves the
preference.

## Parked

From `docs/command-framework-tasks.yaml`, not built:
- `/help` (command list), `/diff`, `/rewind`, `/remember`.
- `Provider.Commands()` manifest (name, description, widget, source, exec
  `mads-handled|passthrough|terminal-only`, owner, lifecycle) as mads-authored
  per-provider adapters, passthrough cards for harness-native commands, and
  the owner collision rule (`owner:mads` shadows a same-named provider entry).
  Today `/` lists only mads commands; the passthrough fix for harness output
  flashing then vanishing is moot until a native command is surfaced.
- The rest of the widget kit (select-menu, info-panel, form, confirm
  archetypes; transient vs pinned lifecycle). Only the host and the bespoke
  Ledger / new-session / export cards exist.
