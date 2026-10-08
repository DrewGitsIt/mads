# Delegates — design in progress (2026-10-08)

Drew + Claude design session. **Status: draft.** Agreed points are marked
*(agreed)*; everything else is a proposal still under review. Sidebar UX
details (density, retention, fading) are deliberately deferred.

## Why

Agent harnesses (Claude Code, Codex, Kiro, …) run their own subagents. mads
shows them today as an Agent tool call with extras: an in-stream task card,
a substrip, and a read-only stream in the bottom dock. Two gaps:

1. **Discoverability.** Subagent work isn't a first-class conversation. It
   can only be reached through the parent's tool card, and the sidebar
   doesn't count it at all once the parent's turn ends (the parent reads
   "Ready" while its subagent is still working).
2. **Correctness.** Nearly every subagent bug mads has shipped comes from
   one assumption: *a subagent starts, runs and ends inside the parent turn
   that spawned it.* Backgrounded subagents (Claude Code's default) break
   that. The history:

   | Theme | Commits |
   |---|---|
   | Sidechain events leaking into the main transcript | 45d5f1e, fbd2cd3 |
   | 23h "Working…" stall from a late background notification | a2256c3, 187007d, 77d7d65 |
   | Rail stuck on "1 subagent running"; count never retired | 9a7635e |
   | Bottom cluster lost on reopen | a52ca9a |
   | Background Bash counted as a subagent | 4a81e61 |
   | Progress text retitling the card | 215caa2 |
   | Background subagent's late permission ask with no indicator | 0651d2a, 391cf7c, 1a8577f |

   Today four separate folds each derive subagent state from turn edges:
   the runner's tool-status tracker (`pkg/toolstatus`), the conversation
   (`conversationItems.ts`), the rail (`useSessionAgentState.ts`) and the
   dock. Each one retires subagents when the parent's turn ends.

Goal: make subagents first-class, and make their lifecycle a core part of
mads's model rather than a property of a tool call.

## Vocabulary *(agreed)*

- **Delegate** (noun): a unit of work a harness runs on a session's behalf,
  such as a Claude Code subagent, a Codex child thread or an ACP child
  session.
- **Delegate** (verb): what the parent agent does when it spawns one.
- **Parent**: the session (or, when nested, the delegate) that spawned it.
  Delegates form a tree. Claude Code reports `spawn_depth` and
  `parent_agent_id`.

**A delegate is not a link.** A mads link (`docs/LINKED-SESSIONS-DESIGN.md`)
is a mads-managed grant set (`read` / `inject` / `orchestrate`) that the user
creates and that carries digests and messages. Delegation is the opposite:
the harness owns the relationship, mads holds no grants over it, and it
records *origin*, not reach. The sidebar may borrow the link rows' nesting
and connector, but the two must never share a data model or code path.

## Model (proposal)

### Delegate record

One record per delegate, owned by the parent session and persisted by the
session service:

| Field | Source |
|---|---|
| `id` | harness id (Claude Code `agent_id` = `task_id`; Codex thread id; ACP child `sessionId`) |
| `session_id` | the mads session that owns the tree |
| `parent` | parent delegate id, empty for depth 1 |
| `tool_use_id` | the spawning call, which anchors the in-stream card |
| `depth`, `type`, `description` | harness metadata |
| `state` | see the lifecycle below |
| `started_at`, `ended_at` | **harness timestamps** (Claude Code stamps every line; mads drops them today) |
| `cwd` / `worktree` | spawn location (Claude Code `.meta.json` `worktreePath`) |
| `transcript` | locator for the delegate's own transcript, when one exists |
| `capabilities` | resolved per delegate (see below) |

### Lifecycle: one owner

A single server-side state machine, emitted as one event type that every
surface projects:

```
spawned → running ⇄ idle → completed | failed | stopped | orphaned
                ↑______________________________|
                re-entry: a resume (e.g. SendMessage) re-fires task_started
                for a known id, often backgrounded (seen in the probe)
```

- Transitions come **only** from harness evidence (lifecycle events,
  notifications with their `status`, a resume re-firing `task_started` for a
  known id) or from process death (→ `orphaned`).
- The **parent's turn ending is not a transition.** This one rule
  invalidates the assumption behind the bug history.
- `idle` covers resumable delegates: Claude Code can run the same `agentId`
  again via `SendMessage`, and the ACP RFD's `idle` state means the same.
- The rail, the conversation, the dock and the tool-status tracker stop
  deriving state and only read it. `toolstatus.TurnEnd` must skip calls
  that belong to a live delegate.

### Transcript source

Where the harness keeps a delegate transcript, mads reads it. That is the
same principle as for sessions: mads points at provider storage and never
copies it (REBUILD-SPEC §1.9). The mads event log stays as the fallback for
harnesses without one. For Claude Code that means
`~/.claude/projects/<proj>/<session>/subagents/agent-<id>.jsonl` and its
`.meta.json`; mads currently skips these on purpose (`history.go`,
`transcript.go`).

## Capabilities

Delegate capabilities differ **by harness and by delegate**. In Claude Code,
Explore/Plan subagents return no agent id and can't be resumed, while
general-purpose ones can. So the provider matrix supplies **defaults**, and
**what we observe at run time wins** *(agreed)*.

Drew's four, with two of them split, plus three more:

| # | Capability | Question | What the UI does with it |
|---|---|---|---|
| C1 | **Transcript** | Does the delegate have its own durable, readable transcript? | Without one: today's view built from streamed events, plus the lifecycle model. With one: a full conversation view, durable across reloads and imports. |
| C2a | **Resumable** | Can the delegate run again after it goes idle (multi-turn), or is it a one-shot? | One-shot delegates end at `completed`; resumable ones can go back to `idle`. |
| C2b | **Steer path** | Who can send it input: nobody, only the parent (relay), or the client directly? | Watch-only, "ask the parent", or a composer of its own. See *Steering* below. |
| C3a | **Stop the delegate** | Can it be stopped on its own, and what does the parent then see? | A Stop button on the delegate. The parent receives a "stopped" result and may react. |
| C3b | **Stop the parent** | Does interrupting the parent kill its delegates? | Whether Stop on the parent asks for confirmation. Claude Code kills all of them unless `perTaskStopAffordance` is set, which mads doesn't do today. |
| C4 | **Spawn location** | cwd and worktree of the delegate. | Files/diff views, plus attributing a worktree to a delegate rather than the parent. |
| C5 | **Lifecycle signals** | Does the harness report start/finish separately from the spawning tool call? | Without them, a delegate can't outlive its tool call: its state is the tool call's state. |
| C6 | **Permission routing** | Do the delegate's approval requests arrive through the parent's channel, and are they attributed to the delegate? | Where "Needs approval" shows: the delegate's row, the parent's, or both. |
| C7 | **Nesting** | Can delegates spawn delegates, and is the parent reported? | A tree, or one level of rows. |

### Harness matrix

✓ = yes, ✗ = no, ~ = partial, ? = unverified. The *Run time* column lists
what to read live instead of trusting the default.

| | Claude Code | Codex | Kiro | Goose | Gemini CLI | ACP RFD (draft) |
|---|---|---|---|---|---|---|
| C1 Transcript | ✓ `subagents/agent-<id>.jsonl` (kept 30 days) | ✓ stored thread | ? | ✗ (probably in goose's store, not exposed) | ✗ | ~ replay via the parent's `session/load` |
| C2a Resumable | ~ yes, except Explore/Plan | ✓ | ? | ✗ | ✗ | ✓ `idle` state |
| C2b Steer path | parent relay (`SendMessage`) ✓; direct ✗; fork-to-session ✓ via a synthetic fork (probe below) | direct thread is plausible; docs say steer via the parent | v2 private `_message/send`; v3 ? | ✗ | ✗ | **✗ in v1** (cancel only, per child) |
| C3a Stop the delegate | ✓ `stop_task` | ✓ `turn/interrupt` / `close_agent` | v2 terminate; v3 `session/cancel` | ✗ | ✗ | ✓ `cancel`, if advertised |
| C3b Stop the parent | kills all unless `perTaskStopAffordance` | ? | ? | n/a (foreground) | n/a | ✗ (children continue past the parent's turn) |
| C4 Spawn location | ✓ `.meta.json` `worktreePath` | ? | ? | ✗ | ✗ | ? |
| C5 Lifecycle signals | ✓ `task_started/progress/updated/notification` | ✓ `collabToolCall` plus `thread/started` | v2 roster; v3 tool activity only | ✗ tool call only | ✗ tool call only | ✓ `subagent_update` snapshots |
| C6 Permission routing | ✓ via the parent's control channel, mapped by `agent_id` | ? | ? | n/a | n/a | ✓ child-scoped events |
| C7 Nesting | ✓ `spawn_depth`, `parent_agent_id` | ✓ `ancestorThreadId` | ✓ DAG | ✗ | ✗ subagents can't spawn | ✓ tree |
| **Run time** | `.meta.json` (`requestShape`, `spawnDepth`, `worktreePath`); presence of an `agent_id`; `task_type` (`local_agent` only) | thread source kind `subAgent*` | `_kiro.dev/subagent/list_update` | — | — | per-child `capabilities` in the announcement |

mads providers today: only `claudecode` parses any of this. `acprpc`,
`goosecli` and `kirocli` leave `ParentToolUseID` empty, because none of the
captured agents send it. No Codex provider exists yet.

Confidence: the Claude Code entries are checked against SDK 0.3.280 typings
and real `.meta.json` files on disk. Codex field names have changed between
releases. The Kiro v2→v3 entries come from scraped docs. The goose
`SessionType::SubAgent` claim is from a third-party summary.

## Steering (open: the main point of disagreement)

**Drew's position:** if a delegate has a transcript on the filesystem, it
should be fair game to message. Whether it has one is a harness
implementation detail, independent of ACP adherence.

**The ACP RFD's position:** v1 defines only `cancel`, because exposing a
child as an ordinary session "would imply that Clients can call methods such
as `session/prompt` … even when the underlying runtime has no way to accept
user input for that worker." It doesn't rule out more later: per-child
`capabilities` "leave room for future controls".

**Proposed reconciliation:** a transcript makes a delegate *continuable*. It
does not by itself make it *steerable in place*. Those are two different
operations:

| Operation | Needs | Effect |
|---|---|---|
| **Steer in place** | harness support (C2b direct) | Input reaches the live delegate. The parent sees the effect in that delegate's result. |
| **Relay** | C2b parent | The user asks the parent; the parent messages the delegate (Claude Code `SendMessage`). |
| **Continue as session** | C1 plus a resumable transcript format | Fork the delegate's transcript into a **new mads session** the user owns. The original delegate and the parent's view of it stay untouched. |

"Continue as session" follows an existing mads rule: continuing someone
else's native conversation **forks** it and never appends to the original
(`docs/conversation-import-tasks.yaml`; Claude Code `--resume <id>
--fork-session`). Appending to a harness-owned delegate transcript behind
the harness's back risks two things. The parent would act on a delegate
whose history changed under it. And the harness itself could later resume
the same id over a file it doesn't recognise.

So Drew's principle holds in the form "a transcript makes it fair game to
**continue**", and in-place steering is offered wherever a harness supports
it.

### Probe: Claude Code 2.1.285, 2026-10-08

Setup: a foreground general-purpose subagent read a secret word from a file
and replied only "DONE". The secret was confirmed absent from the parent's
transcript (0 hits) and present in the subagent's (1 hit). The file was then
deleted, and every question below was asked with file and search tools
disabled. Model: haiku. Mode: `claude -p` only.

| Route | Result |
|---|---|
| `--resume <agentId>` / `agent-<agentId>` / `<jsonl path>`, with or without `--fork-session` | ✗ `--resume` takes only a top-level session UUID or title |
| **Relay**: resume the parent in place, which calls `SendMessage({to: agentId})` | ✓ The same agent id resumed: **`task_started` fired again for a known id, now `is_backgrounded: true`** although the original was foreground. It appended to the subagent's transcript. The parent's turn ended first; the reply arrived in a **second, unprompted turn**. |
| Relay from a **forked** parent (`--resume <parent> --fork-session`) | ✗ "No transcript found for agent ID". Subagent transcripts sit under the *original* session's folder, so **a forked parent loses its delegates.** |
| **Synthetic fork**: copy `subagents/agent-<id>.jsonl` to `<projdir>/<new-uuid>.jsonl`, rewriting `sessionId`, setting `isSidechain: false` and dropping `agentId`; then `--resume <new> --fork-session` | ✓ **Recalled the secret.** Control: the forked parent answered "UNKNOWN". The original subagent transcript was byte-identical afterwards. |

What this means:

- **Continue as session is feasible for Claude Code**, through a synthetic
  fork that mads owns. It never touches the harness's file, which is
  consistent with the fork-never-append rule.
- The cost: it depends on Claude Code's undocumented transcript schema
  (`sessionId`, `isSidechain`, `agentId`). It needs a version-pinned
  conformance probe, like the existing live smoke tests.
- **Direct in-place steering doesn't exist** in Claude Code. The only
  in-place path is the parent relay, and it is mediated by the model: the
  parent decides whether, and how faithfully, to pass the message on.
- Not covered yet:
  - the stream-json and PTY transports mads actually uses;
  - backgrounded originals;
  - a cross-folder fork;
  - Explore/Plan agents, which return no agent id.
- Lifecycle consequence: a delegate can be **re-entered**, going from
  completed back to running under the same id, and the re-entry runs in
  the background across parent turns. The state machine must allow
  `completed → running`.
- Import consequence: continuing an imported conversation forks it, so the
  parent's old delegates can't be relayed to afterwards. They can still be
  read and synthetically forked.

## Sidebar *(direction agreed, UX deferred)*

- Delegates appear in the session list as session-like rows nested under
  their parent, using the link rows' nesting with a distinct connector and
  caption. Volume is a UX problem to solve, not a reason to leave them out
  *(agreed)*.
- Escape hatch: a setting, **"Expose subagents in session list"** *(agreed)*.
- The parent's status line gets a state for "idle, but delegates still
  running". It is neither "Working" nor "Ready".
- Opening a delegate shows a full conversation view in the main pane, not
  the bottom dock, with controls gated by its capabilities.
- Deferred: whether finished delegates fold, fade or stay; foreground vs
  background presentation; how many rows to show before collapsing.

## Phasing (proposal)

0. **Model, no visible change.** Delegate record and lifecycle in the
   session service; the four folds become projections. Parse wire
   timestamps. Read Claude Code subagent transcripts. Set
   `perTaskStopAffordance`. Fix the bugs listed below that the model
   doesn't fix by itself.
1. **Sidebar and view.** Delegate rows, the parent's new status state, the
   full-pane delegate view, and the settings toggle.
2. **Control.** Stop, Relay, and Continue-as-session (after the probe).
3. **More harnesses.** ACP children when the RFD lands (opt in with
   `clientCapabilities.subagents`); Codex once a provider exists; Kiro.

## Known bugs to re-check after phase 0

Found by reading the code on 2026-10-08; none reproduced yet. #1 and #7
were spot-checked in the code.

1. `toolstatus.TurnEnd` marks a background delegate's in-flight inner calls
   `interrupted`, and the UI's `statusOf` lets that override beat the later
   result, so the row stays ⊘ for good.
2. A `failed` or `killed` notification renders a green ✓.
   `task_updated{status:"completed"}` is ignored.
3. `finishRunningTasks` stamps live background tasks `interrupted`. Later
   progress sets `running` again, but the status stays `interrupted`.
4. `resumeRunningTasks` revives only tasks spawned in the current turn, so a
   background task from an earlier turn replays as interrupted.
5. Tagged or task frames arriving between turns set the conversation's
   phase to live, and nothing ever closes it.
6. Claude Code system lines are never parent-tagged. As a result
   `SubagentTask.ParentToolUseID` is always empty, and a subagent's
   `thinking_tokens` can flip `agent_state` to working outside a turn (the
   a2256c3 stall class).
7. `wireLine` drops Claude Code's per-line `timestamp`.
8. Interrupting the parent kills every background task
   (`perTaskStopAffordance` is unset), and `handleInterrupt` retires them
   all in the UI regardless.
9. Subagent history exists only in the mads event log; imported
   conversations have none.
10. The sidebar shows "Ready" while a background subagent runs.
11. The digest counts a delegate's tool uses as main-turn work.
12. (Unrelated, found along the way) Deleting a link holder leaves a
    dangling link, and its subject disappears from the rail
    (`railLinks.ts` / `buildRailRows`).

## Open questions

- Capability resolution: where do the matrix defaults live (provider
  declaration, `docs/DECLARATIVE-PROVIDERS-DESIGN.md`) and how do run-time
  observations override them per delegate?
- ~~Probe: can Claude Code resume or fork a subagent transcript?~~ Not
  directly. Yes through a synthetic fork, and in place only through the
  parent relay (see *Probe* under Steering). Follow-up: repeat the probe on
  the stream-json transport and with a backgrounded original.
- Delegates with their own worktree: does mads adopt, show or ignore it?
- Retention: do finished delegates persist as records indefinitely?
  Claude Code deletes their transcripts after 30 days.
- Does a delegate's request for approval surface on the parent's row, the
  delegate's row, or both?

## Sources

- ACP "Subagent Sessions" RFD (draft): https://agentclientprotocol.com/rfds/subagents
- ACP PR #855 (competing proposal; Drew's review: not viable): https://github.com/agentclientprotocol/agent-client-protocol/pull/855
- Claude Code subagents: https://code.claude.com/docs/en/sub-agents
- Claude Agent SDK typings: https://unpkg.com/@anthropic-ai/claude-agent-sdk@0.3.280/sdk.d.ts
- Codex subagents and app-server: https://developers.openai.com/codex/subagents, https://developers.openai.com/codex/app-server.md
- Kiro: https://kiro.dev/docs/custom-agents/subagents/, https://kiro.dev/docs/cli/v3/acp-migration/
- Goose: https://goose-docs.ai/docs/guides/context-engineering/subagents
- Gemini CLI: https://geminicli.com/docs/core/subagents.md
