# providers/hermescli

The Hermes Agent adapter: Nous Research's hermes, driven through its native
ACP server `hermes-acp` on the generic engine at `providers/acprpc`, with
history read from hermes's own SQLite store. Registry name `hermes`. Design
and decisions: [`docs/HERMES-ADAPTER-DESIGN.md`](../../docs/HERMES-ADAPTER-DESIGN.md).
Evidence: [`testdata/captures/`](testdata/captures/README.md) (hermes 0.22.2).

| File | What |
|---|---|
| `hermescli.go` | Provider, Config, knob values, Matrix, IntentMatrix, Start/OneShot/Models |
| `session.go` | the session wrapper: per-turn usage, the post-tool break, synthetic ask ids |
| `transcript.go` | TranscriptReader over `$HERMES_HOME/state.db`, plus the resume guard |
| `fakehermes_test.go` | the scripted stand-in hermes-acp (writes a real state.db) |
| `smoke_test.go` | the real-binary smoke, `MADS_HERMES_SMOKE=1` |

## Matrix, row by row

| Behavior | Support | Why |
|---|---|---|
| interactive, acp | Native | hermes-acp is an ACP v1 server |
| interrupt | Native | `session/cancel` resolves `stopReason: "cancelled"` in 40-225 ms (runc1 lines 321-323) |
| resume | Native | `session/load` replays and keeps context; guarded (below) |
| one_shot, one_shot_stream, one_shot_resume | Emulated | the engine's ACP one-shot, run through this binding's Start |
| model_discovery | Native | ACP's `models` selector on session/new (66 `provider:model` ids on Copilot) |
| cost_reporting | Native | prompt-result usage, cumulative per process — differenced per turn |
| ref_announce | Native | session/new returns the id synchronously |
| transcript | Native | state.db, hermes's own store; incremental by `message_uid` (`TranscriptSince`) |
| stable_keys | **Emulated** | hermes stores none of its live ids; live items carry provisional keys and the readers match stored rows to them by position while the child lives (below) |
| stored_thinking | **Native** | reasoning columns, as keyed `thinking` rows from `KeyedTranscript`, aliased like text ("Transcript" below) |
| trust_limited, trust_read_only, trust_auto_review | **Unsupported** | full trust only (below) |
| mcp_servers | Native | ACP-provided stdio servers are registered on the session and the model calls them as `mcp__<server>__<tool>` (probe-mcp; the smoke re-proves it) |
| compaction, compaction_focus | Unsupported | hermes has `/compress`, but no capture shows what a pass does to state.db and the session's ids |
| pty | Unsupported | no adapter for hermes's TUI |
| branching, cwd_reroot, delegate_* | Unsupported | no mads surface / nothing on the wire |

IntentMatrix: every intent Unsupported, `require_approval` included —
hermes decides for itself what to ask about.

## Full trust only

hermes asks `session/request_permission` only for file edits and for
commands its own reviewer flags as dangerous; it auto-approved `rm -rf`
without asking (run9 lines 23-24). Its asks also carry synthetic tool ids
(`edit-approval-1`, `perm-check-N`) that match no live call. Answering asks
therefore cannot enforce limited or read-only, and there is no reviewer
mode mads could hand auto-review to. So `Start` refuses every level but
full with `provider.ErrUnsupported` ("hermes runs at full trust only")
before spawning anything. It refuses rather than clamps: running a limited
session at full would loosen what the user chose without telling them. The
UI disables those levels for hermes and Settings says so.

A session that never chose a level is not refused: wherever trust is
defaulted (the Router's session defaults, the Launchpad and `/new`
payloads) a provider whose matrix declares every restricted level
unsupported (`provider.FullTrustOnly`) gets Full, and the trust pill shows
"Hermes can't enforce approvals; it runs at full trust". The configured
default trust is left alone for every other provider.

At full trust the engine sets hermes's `dont_ask` mode and allows whatever
hermes still asks.

## Model selection

hermes-acp has no `--provider`/`-m` flags (`acp_adapter/entry.py:94-104`).
`StartOptions.Model` (a `provider:model` id from discovery) rides ACP
`session/set_model`, which hermes stores on the session row and never
writes to `config.yaml` (`server.py:1022-1056`, "ACP never persists";
`probe-setmodel` shows config.yaml's sha256 and mtime unchanged; a reload
restores the session's model, `probe-load`). Sessions therefore run
against the user's real `HERMES_HOME`, their auth and their state.db. An
empty model is hermes's own configured default.

`Config.Home` pins a different `HERMES_HOME` (tests, the smoke). Empty
follows the child's environment, then `~/.hermes` (+`HERMES_DATA_DIR_SUFFIX`).

## The session wrapper

Four hermes facts corrected on the way through, no engine knobs:

1. Usage is cumulative per hermes-acp process (`agent/turn_finalizer.py:
   33-36,708`); turn ends carry the difference, a cancelled turn zero.
2. hermes prepends `"\n\n"` to the first visible text after a tool
   (`agent/stream_delivery.py:305-310`); the store doesn't have it, so the
   wrapper strips it.
3. A tool call's name: the engine names a kind-based call by its title,
   `"<tool>: <preview>"` (`acp_adapter/tools.py` `build_tool_title`); the
   wrapper cuts it back to the tool name hermes stores.
4. The `tool_call_update` hermes sends for a synthetic ask id becomes no
   result event (the real call's result follows under its live id).

And two for conversation keys (next section): it records what each turn
streamed, and it replaces a tool result's live text with the stored one.

The wrapper does not offer the runner's live `Transcript()`: history comes
from state.db, not the engine's in-memory buffer.

**No live transcript mid-turn.** The adapter serves only state.db, and
hermes writes an assistant row when a model call finishes (10–200 ms after
its last chunk, always before the prompt response). So a transcript
refetch during a turn shows the finished model calls only; the reply in
progress reaches the UI on the live event stream and lands in the store
by turn end. The chat-stream redesign's unsettled tail (C2,
docs/CHAT-STREAM-DESIGN.md) replays exactly that tail, keyed as below.

## Conversation keys (emulated)

docs/CHAT-STREAM-DESIGN.md §1 wants every item keyed the same live and
stored. hermes makes that impossible natively:

- the live `messageId` (a UUID4 per model call, `agent/events.py:185-236`)
  is **never stored**, and load replays chunks without one;
- the stored `message_uid` exists only once its row is written — the
  assistant row at the end of its model call, or just before its tools run
  — so no live chunk can carry it;
- live tool ids are `tc-<hex>` (`acp_adapter/tools.py:67`), stored ones the
  model's `call_…`.

So (keys.go):

1. **Live**: provisional keys from the live ids — the engine's
   `KeyModeMessageRun` (`<messageId>:<run>` for text, `tc-…` for tools).
2. **A per-ref ledger** in the provider, filled by the session wrapper: for
   each prompt, the index its user row will have (the store's user-row
   count taken just before the prompt goes out), the text sent, and the
   live text and tool keys in order.
3. **The readers match by position**, as hermes writes rows: the turn's
   user row is that index (checked against the text sent — persona and
   correction marker removed); within the turn, the k-th assistant row with
   text is the k-th live text item, and the stored tool calls, in order
   across the turn's rows (tool-only rows and parallel calls included), are
   the live tool calls in order. A matched row reads with the **live key
   while that session child lives**; every other row — and every row once
   the child has exited (restart, no child) — with its own `message_uid`
   (user rows `user:<message_uid>`) or stored call id.
4. **Text equality**: live text is normalised to the stored text — the
   `"\n\n"` strip above — and a tool result's live text is *replaced* by the
   stored one: hermes renders a result for ACP (`"terminal result\n-
   **output:** …"`) but stores the tool's own output (the JSON envelope)
   and sends that on the wire only for unstructured results, so the
   rendering can't be reversed. The wrapper waits up to 500 ms for the
   tool row (it lands 3-23 ms after the frame) and emits the stored text;
   past that, the rendering goes out unchanged.

Limits, each an honest one-sided key rather than a wrong one:

- When the child exits, its rows switch to their native keys; a viewer
  holding live keys must re-read (C2's tail dies with the child anyway).
- A turn whose user row doesn't read back as the text sent (a hermes
  rewrite not yet seen) isn't matched.
- Text is matched by count, not content: a model call whose text hermes
  streams but doesn't store (or the reverse) would shift that turn's later
  text keys. None seen in the survey or smokes.
- hermes can write a tool-call row a few ms *before* the `tool_call` frame
  (Copilot, −5..−15 ms). `Transcript` and `TranscriptSince` both hold back
  the in-flight turn's rows from the first one with nothing live to match
  yet, so while the child lives no reader sees a row under two keys; those
  rows just land a few ms later.
- `failed_turn` rows are never streamed: they are `Notice` rows keyed by
  their `message_uid`.

**Incremental reads.** Row ids are no cursor — `session/load` inserts the
session's rows again with fresh ids and deletes the old (survey
`run2.db.jsonl`). The cursor is the last served row's `message_uid`, and
`TranscriptSince` returns the display rows after that uid's *current* row.
A uid that's gone (compaction) is `ErrCursorStale`.

**Settle hints** (`EventSettle`, only with `Keyed`): on the
`tool_call` frame (the assistant row with that call is written within
~15 ms of it) and at the prompt response (the last row lands before it).

**No KeyedSender**: hermes stores no caller id, and strips and (after a
cancel) rewrites the user's text, so the session child matches its sends
to the user rows by position.

## Transcript (state.db)

Read-only (`mode=ro`, WAL-aware; hermes-acp may be writing). Rows are
rewritten by `session/load` and archived by compaction, so the reader
follows hermes's display projection: the compression lineage root → tip;
active or compacted rows minus `model_only`; generations collapsed by
`message_uid` or display identity, at their first position. Presentation:

- **user rows**: the post-cancel rewrite `<cancelled>\n\nUser
  correction/guidance after interrupt: <new>` shows as `<new>` (the
  cancelled prompt is already its own earlier row); stored text is
  whitespace-stripped by hermes.
- **failed_turn** rows ("Your request was not processed…", typed or
  untyped): an `IsError` agent message in hermes's words, marked `Notice`.
  Never streamed live, but the model sees it next turn, so the history
  shows it.
- **interrupted partials** (`{"interrupted": true}`): the agent text the
  user saw.
- **assistant tool_calls** → `tool` messages (stored `call_…` ids, not the
  live `tc-…` ones); **tool rows** → `tool_result` (`IsError` when the JSON
  envelope's `error` is a non-empty string).
- **compaction summaries** (`_compressed_summary`) → a `compact` marker.
- **reasoning columns** are skipped by `Transcript`. `KeyedTranscript` and
  `TranscriptSince` (`stored_thinking: native`) put a `thinking` row before
  the row's text: the first non-blank of `reasoning_content` and
  `reasoning` (hermes's replay order; a lone `" "` is its thinking-mode pad),
  unstripped — `reasoning_content` is the streamed reasoning deltas joined
  (`chat_completion_helpers.py` `_finish_chat_stream`), so it equals the
  live `agent_thought_chunk`s. Keyed like text: the live `<messageId>:<run>`
  while the child lives (the k-th stored row with reasoning is the turn's
  k-th live thinking item), else `<message_uid>:thinking`. No survey model
  emitted reasoning (run7/8): proven on the stand-in from hermes's source.
  Reasoning a provider delivers through `reasoning_details` only is shown
  live from the details and stored from the plain field, which can differ.
- **the persona**: the first-send prefix is written as
  `<mads-system-prompt bytes="N">\n…\n</mads-system-prompt>` and removed
  exactly (byte count, not a pattern) from the user row it opens; a message
  that only looks like it is left alone.

The resume guard reads the same store: hermes answers `session/load` of an
unknown id with an empty success (and the next prompt with a refusal), so a
ref without a `sessions` row fails Start loudly.

## Never starts a hermes install; kills the whole tree

A source-checkout hermes keeps its Python dependencies per data root
(`<root>/installs/<sha256(checkout)[:16]>/`). Its launcher
(`~/.local/bin/hermes-acp` → `<checkout>/.hermes/bin/hermes-acp`) imports
`hermes_bootstrap` first (line 116), whose `prepare_launch`
(`hermes_cli/venv_sync.py:338-386`) syncs that environment when the root
the process runs under has none — including a root that only *borrows*
the checkout, such as a temporary HOME or HERMES_HOME
(`_prepare_borrowed_launch`, `venv_sync.py:452-484`). That is a ~600 MB
`uv sync`, run by a PM worker in hermes-acp's own process group
(`pm/client.py:77`, no new session); killing hermes-acp alone left it
running as ppid-1 orphans. (`--version` is exempt, `venv_sync.py:363`,
which is why a probe never showed it.) Two fixes:

- **The guard** (`pkg/hermesinstall`): before every spawn — Start (and so
  OneShot), Models, and preflight's version probe — mads checks the marker
  the launcher itself relies on, `<dependency root>/installs/<key>/facts.json`
  (`pm/environments.py:20-38,105-106`), for the exact environment the child
  would get (HOME, HERMES_HOME, HERMES_DATA_DIR_SUFFIX resolved as
  `get_default_hermes_root` does). Missing: fail fast with
  `hermesinstall.ErrNotInstalled`, nothing spawned; preflight reports
  hermes not installed instead of probing. It only reads files; it passes
  where hermes can't bootstrap (a packaged install, no `.git`, an
  `updateMechanism` other than `self`, `HERMES_DISABLE_LAZY_INSTALLS`).
  Chosen over setting `HERMES_DISABLE_LAZY_INSTALLS=1` on every spawn,
  which would also stop the bootstrap: hermes documents that variable as
  internal ("Do not put it in `.env`"), and it overrides the user's
  `security.allow_lazy_installs`, so real sessions would lose the tools
  hermes installs on demand.
- **The tree kill** (acprpc `OwnProcessTree`, `pkg/procreap`): hermes-acp
  starts in a process group of its own; Close — which every Start
  failure, OneShot return or cancel, and discovery go through — kills the
  group while hermes-acp is still unreaped, then reaps descendants that
  left the group (hermes's tools use `start_new_session=True`), remembered
  by pid + start time when each tool starts and at Close.

Tests: a stand-in checkout whose launcher would bootstrap and detach a
helper — Start, OneShot and Models refuse, nothing runs, no `.hermes`
appears; the stand-in agent's detached and grouped helpers die on Close,
on a cancelled one-shot, and after the agent itself is killed; the Router
boot test asserts its scratch HOME gets no `.hermes`.

The smoke borrows an install from `MADS_HERMES_SMOKE_HOME`, a hermes home
prepared beforehand by hermes itself (never `~/.hermes`: a borrowing
launch rebuilds the owner's `pm-runtime` generation in place).

## Testing

```sh
go test -race ./providers/hermescli/
MADS_HERMES_SMOKE=1 MADS_HERMES_SMOKE_HOME=/path/to/prepared/hermes-home \
  go test -run Smoke ./providers/hermescli/ -v
```

The stand-in (`fakehermes_test.go`) serves the captured shapes and writes a
real state.db built from hermes's own `CREATE TABLE` text
(`testdata/state-schema.sql`); the full `conformance.Run` suite runs against
it, stable_keys included (its `tool-text` turn writes rows by position as
hermes does: the text+tool_calls row as the tool_call frame goes out, the
tool row after the result, the last row before the response). Reader
fixtures come from the survey's real rows. `keys_test.go` replays the
survey's Copilot run (`testdata/captures/runc1.*`) through the binding —
hermes's own wire lines, and its state.db rows inserted when the capture
saw them — and checks every streamed item comes back from the store under
its live key with its live text, and under hermes's ids after Close; plus
position matching with parallel and tool-only calls, the hold-back, the
cursor across a load rewrite and compaction, and failed-turn notices.

`TestRealHermesStableKeys` (same env as the smoke) runs the stable_keys
probe against the real hermes-acp and logs both sides key by key.

The smoke spawns the real hermes-acp on GitHub Copilot `gpt-4.1` in a
scratch `HERMES_HOME` whose `config.yaml` names only the provider and
model, borrowing the dependency install of `MADS_HERMES_SMOKE_HOME`
through a symlinked `installs/`. hermes resolves the Copilot credential
from `gh auth token` itself (no secret copied). It never touches
`~/.hermes`. About a minute and six short turns, the last calling a
throwaway stdio MCP server.