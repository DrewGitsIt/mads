# hermes adapter — design (P2c)

A fourth provider adapter (spec §11): Nous Research's **Hermes Agent**,
driven through its native ACP server `hermes-acp` (JSON-RPC 2.0 over
newline-delimited stdio) on the generic engine at `providers/acprpc`, with
transcripts read from hermes's own SQLite store. Registry name `hermes`;
package `providers/hermescli`.

**Every claim here is verified against a live binary**: hermes **0.22.2**
(git install at `~/.hermes/hermes-agent`, `hermes-acp` at
`~/.local/bin`). Evidence comes from the 2026-10-20 provider survey
(ms-stamped ACP stdio plus a 25 ms read-only poll of `state.db`, on Ollama
and on GitHub Copilot `gpt-4.1`) and four probes written for this adapter,
all committed under `providers/hermescli/testdata/captures/` (see its
README). Source citations are to the 0.22.2 tree. Every survey run and
probe used a scratch `HERMES_HOME`; `~/.hermes` was never written.

## Decisions taken before design (Drew)

1. **Full trust only.** hermes asks permission only for edits and for
   commands its own reviewer flags as dangerous (it auto-approved
   `rm -rf`), and its permission tool ids are synthetic. The matrix
   declares the restricted trust rows Unsupported, the adapter refuses
   those levels, and Settings says hermes runs at full trust only.
2. **No new conversation keys or frames yet.** The chat-stream design is
   being revised (docs/CHAT-STREAM-DESIGN.md); this adapter does only what
   the existing provider contract asks. `message_uid` is used inside the
   reader for collapsing generations, not exposed.
3. **History after a restart comes from state.db**, not acprpc's memory.

## Shape of the work

| Piece | Where | Notes |
|---|---|---|
| Transport | acprpc engine | knob values in `engineConfig`, each cited |
| Session wrapper | `session.go` | three hermes facts the engine can't know |
| Trust gate | `Start` | refuse below full, before spawning |
| Resume guard | `transcript.go` | state.db row check before `session/load` |
| TranscriptReader | `transcript.go` | hermes's display projection over state.db |
| Model selection | engine `session/set_model` | per session; never persisted to config |
| One-shot | `OneShot` | the engine's ACP one-shot, through this binding's Start |

No SessionLocator: there is no PTY transport (hermes's TUI is not
adapted), and session/new returns the ref synchronously.

## Knob table

| Knob | Value | Evidence |
|---|---|---|
| `Bin` | `hermes-acp` via binlocate (PATH, then `~/.local/bin`) | installer's `bin_dir="$HOME/.local/bin"` (setup-hermes.sh:216) |
| `SpawnArgs` | none | hermes-acp's only flags are --version/--check/--setup* (acp_adapter/entry.py:94-104) |
| `BaseEnv`/`Env` | agentenv + `HERMES_HOME` when `Config.Home` is set | home = HERMES_HOME → `~/.hermes`+suffix (hermes_constants.py:51-58,160-169) |
| `TrustModes` | `full → dont_ask`, nothing else | modes default/accept_edits/dont_ask (runc1 line 4); `dont_ask` is the least-asking |
| `SendModeAtSessionNew` | true | set dont_ask explicitly, as goose sets `auto` |
| `ModelConfigOptionID` | "" | hermes ships the ACP `models` selector and no configOptions (runc1 line 4); the engine reads the selector |
| `SystemPrompt` | first-send prefix | no system-prompt surface; never drop a persona |
| `ToolIdentity` | kind-based | `kind` + `"<tool>: <preview>"` title, no `_meta` namespace (runc1 line 21; tools.py:20-31,192-198) |
| `PermissionAsksEveryCall` | false | hermes judges risk itself (run9 lines 13, 23-24) |
| `CompactCommand` | "" | `compress` is advertised (runc1 line 8) but unprobed |

The engine's knob set stays closed: the three corrections below are hermes
quirks, not ACP variations, so they live in a binding-side wrapper.

## Trust (full only)

What hermes asks about is its own policy: edits (outside `dont_ask`'s
auto-allow) and terminal commands its reviewer flags. A flagged `rm -rf`
was auto-approved "by smart approval" with no ask at all (run9 lines
23-24). And the ask's `toolCall.toolCallId` is synthetic
(`edit-approval-1`, run9 line 13; `perm-check-N` in source), so a mads card
couldn't even be tied to the live call. Neither limited ("asks before
commands") nor read-only ("blocks edits and commands") can be enforced by
answering asks; hermes has no reviewer mode to hand auto-review to.

`Start` therefore returns `ErrUnsupported` ("hermes runs at full trust
only…") for limited, read_only and auto_review, before spawning. The
runner hands an auto_review session over as limited
(`provider.EffectiveTrust`), which is refused the same way. **Refuse, not
clamp**: silently running a limited session at full would loosen the
user's choice. The UI follows the matrix: when a provider declares all
three restricted rows unsupported, the trust pickers disable them with
"not available on hermes, which runs at Full access only", Settings shows
one "Full trust only" tag instead of three "No … trust" tags, and a line
under the default-provider picker: "Hermes Agent runs at full trust only:
it edits files and runs commands without asking you. Sessions set to
Limited, Read-only or Auto-review won't start on it."

Pipeline stages are one-shots, which always run at full trust, so `mads
run -provider hermes` works.

## Model selection

hermes-acp has no `--provider`/`-m`. Two ways to choose a model per
process were on the table:

- **(a) ACP `session/set_model "<provider>:<model>"`** with the user's real
  `HERMES_HOME`. hermes rebuilds the session's agent through the same
  validation as `/model` and saves the choice on the session
  (`server.py:1022-1056`, `_switch_model`'s docstring: "ACP never
  persists").
- **(b) a scratch `HERMES_HOME`** per process, with its own config.yaml.

**Chosen: (a).** Verified non-persisting with the same code path against a
scratch home (`probe-setmodel`): `config.yaml` sha256 `63a7d518…` and
mtime `1792517002.716` were identical before and after `set_model
copilot:gpt-4o` plus a prompt, while the `sessions` row recorded `gpt-4o`
/ `copilot`; a fresh process loading the session reported
`currentModelId: copilot:gpt-4o` (`probe-load`). HERMES_HOME only changes
which directory that code writes, so the probe holds for `~/.hermes`;
running it against the real home was avoided on purpose (the home's
`.env` holds an OpenRouter key that must not be used, and hermes would
build an agent on the user's configured provider). With (a), sessions use
the user's auth and land in `~/.hermes/state.db` where Hermes Desktop also
sees them; (b) would split the user's history and need credentials
copied. `Config.Home` still exists for tests and the smoke.

Discovery is the engine's: session/new in a temp dir, read
`availableModels` (66 `provider:model` ids on Copilot), close. hermes
writes no `sessions` row for a session that never prompts
(`probe-emptynew`), so discovery deposits nothing in the user's store even
though hermes doesn't advertise `session/delete`.

## The session wrapper (session.go)

1. **Cumulative usage.** The prompt result's usage is
   `agent.session_<key>` (agent/turn_finalizer.py:33-36,708): totals for
   the hermes-acp process, restarting at 0 in a new one. runc1's turns
   report 9535 → 28758 → 38455 input tokens; the cancelled turn repeats the
   previous totals (line 323); run2's first turn after a load reports 0.
   The wrapper reports the difference per turn end (a total below the
   previous one counts as a restart). Context occupancy (`usage_update`)
   is absolute and passes through. The smoke checks turn 3's input is
   below turns 1+2.
2. **The post-tool break.** `_fire_stream_delta` prepends `"\n\n"` to the
   first visible delta after a tool iteration (agent/stream_delivery.py:
   305-310); the stored row lacks it (run1: 58 live chars, 56 stored). The
   wrapper strips it from the first non-blank text after a tool event,
   mirroring hermes's own rule, so live text and history agree ("rule
   (b)" in the chat-stream design).
3. **Synthetic ask ids.** hermes sends a `tool_call_update` for
   `edit-approval-1` (run9 line 15), which the engine turns into a result
   for a call never announced. The wrapper drops results whose id no
   `tool_call` introduced; the real call's result follows (line 16).

The wrapper deliberately does not implement the runner's live
`Transcript()` (decision 3), so the runner reads history through the
TranscriptReader. Cost: a mid-turn transcript refetch shows only completed
model calls; the in-flight text rides the live event stream. hermes writes
the final assistant row 10-200 ms after the last chunk and always before
the prompt response (survey §4), so at turn end the store is complete.

## Transcript normalization (transcript.go)

`$HERMES_HOME/state.db`, opened `mode=ro` with a busy timeout (not
`immutable`: the WAL must be read while hermes-acp writes). The repo's
existing driver, `modernc.org/sqlite`, is CGO-free; nothing new in go.mod.

The store is not append-only:

- `session/load` deletes and reinserts the session's active rows: new row
  ids, same `message_uid` (survey run3; acp_adapter/session.py:376-398).
- Compaction archives rows (`active=0, compacted=1`), inserts a summary
  carrier (`_compressed_summary=1`) and clones the protected tail as fresh
  live rows, every column but the id copied; the originals become rewind
  rows (`active=0, compacted=0`) that no display shows
  (hermes_state_messages.py:919-1070).
- A compression can continue in a child session whose parent ended
  `compression` (`_meta.hermes.sessionProvenance` on the wire).

So the reader reproduces hermes's own display projection rather than
reading by id: the lineage root → tip, skipping tool children and
`_branched_from`/`_delegate_from`/`_reset_from` forks
(hermes_state_compression.py:798-827; hermes_state_messages.py:1908-1927);
rows `active OR compacted` minus `display_metadata.model_only`
(DISPLAY_VISIBLE_SQL); each logical message once — a group per
`message_uid` or per hermes's display identity (role, content, timestamp,
tool ids), represented by its live-then-newest row at its first row's
position (`_dedupe_display_generations`). Row ids are never exposed.

Presentation:

| Stored | Shown |
|---|---|
| user row | `user`, as stored (hermes strips whitespace; not what was sent byte for byte) |
| `<cancelled>\n\nUser correction/guidance after interrupt: <new>` (server.py:201) | `user` `<new>` — the cancelled prompt is already its own earlier row; the last marker wins for chained cancels |
| `failed_turn` row, or the exact notice with no kind (run3 row 40, written before hermes typed it) | `agent` with `IsError`, hermes's words. Never streamed live, but the model reads it next turn (run3), so the history shows what the agent knows — the same shape acprpc gives a turn that failed |
| interrupted partial (`{"interrupted": true}`) | `agent`, the text the user saw |
| assistant `tool_calls` | `tool` per call: stored `call_…` id, name, arguments (raw if not JSON) |
| tool row | `tool_result`, `IsError` when the envelope's `error` is a non-empty string (a non-zero exit is output) |
| `_compressed_summary` carrier | `compact` marker with the summary text |
| reasoning columns | skipped (no thinking role, as in every adapter) |
| `\x00json:` content | its text parts |

Timestamps are hermes's REAL unix seconds, in UTC. A first-send system
prompt is stored inside the first user row (hermes has no other surface).

## Resume and interrupt

Resume is the engine's `session/load` (hermes advertises `loadSession`).
hermes answers `session/load` of an **unknown** id with `{"result": {}}`
and a later prompt with `stopReason: "refusal"` (`probe-loadunknown`); the
engine would report a resume that never happened. `session/resume` is
worse: it silently creates a new session (server.py:633-641). So Start (and
a resuming OneShot) first checks state.db for a `sessions` row with that
id and fails loudly without one. hermes writes that row with the first
prompt, so a never-prompted ref has nothing to resume either way.

Interrupt is `session/cancel`; hermes resolves `cancelled` in 40-225 ms.
A cancel before any output stores the failed-turn boundary; a cancel
mid-stream stores the partial. The next prompt is stored rewritten (above);
the rewrite is process state, so it doesn't survive a restart.

## Testing

- **Stand-in** (`fakehermes_test.go`): the captured initialize and
  session/new bodies, kind-based tool calls under `tc-…` ids stored as
  `call_…`, the post-tool break, the synthetic edit ask, cumulative usage,
  cancel/partial/rewrite, whitespace-stripped user rows, load's row
  rewrite and its empty success on unknown ids, lazy session rows — and a
  real state.db built from hermes's own `CREATE TABLE` text
  (`testdata/state-schema.sql`). `conformance.Run` passes every row
  against it.
- **Reader fixtures** (`transcript_test.go`): the Copilot run's rows
  verbatim, run2's cancel boundary, compaction generations, a lineage with
  a branch, structured content, tool errors, read-only open.
- **Smoke** (`MADS_HERMES_SMOKE=1`): the real hermes-acp on Copilot
  `gpt-4.1` in a scratch `HERMES_HOME` whose `config.yaml` names only the
  provider and model. hermes resolves the Copilot credential from
  `gh auth token` itself (the survey's home3 recorded a `gh_cli` pool entry
  with a fingerprint, no secret), so nothing is copied. It covers
  discovery, set_model (config.yaml unchanged), a reply, an `ls` tool turn
  (break stripped, live text matches the stored row), per-turn usage, an
  interrupt, the transcript, a recall after resume, the unknown-ref
  guard, and one call into a stdio MCP server passed on Start.

## Registration

`cmd/mads run -provider hermes`; the router, session and pipeline
registries (isolated env mode passes `HERMES_HOME` and
`HERMES_DATA_DIR_SUFFIX` through); preflight probes `hermes-acp
--version` with the vendor's install command; the compat window is
`0.22.0 ≤ v < 0.23.0`. The UI gets the "Hermes Agent" label, a
caduceus mark, and the full-trust-only surfaces above.

## MCP servers (Native)

The engine always sends `mcpServers` on session/new; hermes registers
ACP-provided servers on the session (server.py:427) and the model calls
them as `mcp__<server>__<tool>`, after a `tool_search` to find it. Proven
live, not just from source (`probe-mcp`): a throwaway stdio server whose
only tool returns a marker; Copilot `gpt-4.1` called
`mcp__madsprobe__mads_probe_ping` and replied with the marker. The smoke
repeats it. hermes doesn't advertise `mcpCapabilities.http`, so http
servers are dropped (engine rule); stdio is what mads's orchestrate
capability uses.

## Open questions

- **Compaction**: `/compress` exists with focus text. A pass may rotate
  the internal session id (provenance), which the reader follows in
  principle but no capture proves. Unsupported until probed.
- **Default trust**: the builtin default trust is `limited`, so a hermes
  session started without picking Full access is refused at Start. The UI
  marks the levels unavailable but doesn't move the default.
- **Mid-turn transcript gap** (decision 3, above).
- **The first-send prefix** lands in the first stored user row and so in
  the transcript.
- **Compaction carriers that embed a live user ask** are shown as one
  compact marker; hermes's display splits the ask out
  (`project_compaction_message_for_display`). Not captured yet.
