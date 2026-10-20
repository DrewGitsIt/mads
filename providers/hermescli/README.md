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
| transcript | Native | state.db, hermes's own store |
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

Three hermes facts corrected on the way through, no engine knobs:

1. Usage is cumulative per hermes-acp process (`agent/turn_finalizer.py:
   33-36,708`); turn ends carry the difference, a cancelled turn zero.
2. hermes prepends `"\n\n"` to the first visible text after a tool
   (`agent/stream_delivery.py:305-310`); the store doesn't have it, so the
   wrapper strips it.
3. The `tool_call_update` hermes sends for a synthetic ask id becomes no
   result event (the real call's result follows under its live id).

The wrapper does not offer the runner's live `Transcript()`: history comes
from state.db, not the engine's in-memory buffer.

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
  untyped): an `IsError` agent message in hermes's words. Never streamed
  live, but the model sees it next turn, so the history shows it.
- **interrupted partials** (`{"interrupted": true}`): the agent text the
  user saw.
- **assistant tool_calls** → `tool` messages (stored `call_…` ids, not the
  live `tc-…` ones); **tool rows** → `tool_result` (`IsError` when the JSON
  envelope's `error` is a non-empty string).
- **compaction summaries** (`_compressed_summary`) → a `compact` marker.
- **reasoning columns** are skipped, as every adapter skips thinking.

The resume guard reads the same store: hermes answers `session/load` of an
unknown id with an empty success (and the next prompt with a refusal), so a
ref without a `sessions` row fails Start loudly.

## Testing

```sh
go test -race ./providers/hermescli/
MADS_HERMES_SMOKE=1 go test -run Smoke ./providers/hermescli/ -v
```

The stand-in (`fakehermes_test.go`) serves the captured shapes and writes a
real state.db built from hermes's own `CREATE TABLE` text
(`testdata/state-schema.sql`); the full `conformance.Run` suite runs against
it. Reader fixtures come from the survey's real rows.

The smoke spawns the real hermes-acp on GitHub Copilot `gpt-4.1` in a
scratch `HERMES_HOME` whose `config.yaml` names only the provider and
model; hermes resolves the Copilot credential from `gh auth token` itself
(no secret copied). It never touches `~/.hermes`. About 35 s and six short
turns, the last calling a throwaway stdio MCP server.
