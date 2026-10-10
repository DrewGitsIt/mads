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

## Model *(#1, built 2026-10-08)*

Decisions for #1, the server-side record and lifecycle *(agreed)*:
- A dedicated `delegates` table (option C), built in its own worktree to
  keep regression risk low.
- No `idle` state: a finished delegate isn't warm, because mads has no notion
  of a KV-cache TTL.
- Stops cascade, which is Claude Code's default; `perTaskStopAffordance`
  stays unset.
- The sidebar count fix is part of #1.
- Terminal (PTY) sessions are out of scope.

### Delegate record (`pkg/contracts/delegate.go`)

One row per (session, harness id) in the Data Service (`delegates` table,
migration 0028). The session runner is the only writer: it sends the whole
record with `PUT /sessions/{id}/delegates/{harness_id}`. The store keeps the
`dlg_` id and `created_at`, and announces each real change as
`session.delegate_changed` in the same transaction. Writing an unchanged
record is a silent no-op, so retries are safe. Deleting a session deletes
its delegates. Reads: `GET /sessions/{id}/delegates`, plus
`GET /delegates?state=` (proxied by the Router) for the sidebar's single
cross-session cold read.

| Field | Meaning |
|---|---|
| `harness_id` | the key. Claude Code's `task_id` = `agentId`, stable across resumes |
| `tool_use_id` | the spawning call. First seen wins: a relay re-entry reports the SendMessage call's id and must not move the anchor |
| `depth`, `type`, `description`, `backgrounded` | harness metadata; `backgrounded` is sticky (a re-entry is always backgrounded) |
| `state`, `reason`, `inferred` | the state, its evidence (the harness's status word or mads's inference), and whether mads concluded it |
| `runs`, `started_at`, `ended_at` | the run count (re-entries increment it), and the latest run's start and end. Arrival time: Claude Code's system task lines carry no timestamp |
| `transcript_path`, totals | from the notification |

Not in the record yet: nesting parent, cwd/worktree, capabilities (#3).

### Lifecycle: one owner (`pkg/delegates`)

```
running → completed | failed | stopped | orphaned
   ↑__________________________________|
   re-entry: task_started for a known id (a Claude Code relay)
```

A pure tracker that the runner feeds and writes through (with
agent_state-style retry), in `runner/delegates.go`:

- **Harness reports win.** A `task_updated` / `task_notification` status
  settles the state: completed; failed (including unrecognised words);
  stopped (killed/stopped/cancelled). States mads infers are marked
  `inferred`, and a later report overrides them.
- **The parent's turn ending does not end a backgrounded delegate.** It does
  end a *foreground* one still marked running (`turn_end`): the parent waits
  on those, so a turn that ended without the delegate's report means an
  interrupt.
- **Leaving the roster is not an ending.** Claude Code drops a task from
  `background_tasks_changed` just *before* reporting how it ended (both real
  captures show this). A roster absence that outlasts a 5s grace period with
  no report concludes `stopped` (`roster`), which is how a cascading kill
  that goes unreported is caught.
- **Process exit** (shutdown, crash, transport swap) stops every running
  delegate (`process_exit`). A swap concludes them before starting the new
  process, so the old stream winding down can't stop the new process's
  delegates.
- **Runner start** loads stored records (so resumed delegates keep their
  identity) and marks any still `running` as `orphaned` (`runner_restart`).
- **An ending with no start is not yet a delegate.** After a restart Claude
  Code reports, by id alone, every background task an earlier process left
  behind: Monitor and Bash calls as well as subagents. The provider passes
  such an id through only if it has a subagent file, and the tracker holds
  the ending until the runner's inspection finds the delegate in the
  harness's store.
- **Tool status:** the turn-end sweep (`toolstatus.TurnEndExcept`) leaves a
  live delegate's in-flight calls alone. They close out (`EndParent`) when
  the delegate ends. This fixes the stuck-⊘ bug.
- **Thinking ticks between turns** no longer start a "working" state. Claude
  Code doesn't tag a subagent's `thinking_tokens` lines, so a tick between
  turns only relabels a turn already in flight.

The sidebar mirrors `session.delegate_changed` and no longer clears delegates
when the main agent goes idle; only sleep and crash clear them. A session
whose turn is over but whose delegates aren't reads
**"N subagents running"** ("Done · …" when the turn end hasn't been seen
yet), with the working spinner as its status mark, counted under Working.
(It led with "Ready · " until main retired Ready for Idle; "Idle · N
subagents running" would contradict itself.)

### Transcript source *(B, built 2026-10-09)*

Where the harness keeps a delegate transcript, mads reads it. That is the
same principle as for sessions: mads points at provider storage and never
copies it (REBUILD-SPEC §1.9). For Claude Code that means
`~/.claude/projects/<proj>/<session>/subagents/agent-<id>.jsonl` and its
`.meta.json`.

- **Route.** `GET /sessions/{id}/delegates/{harnessId}/transcript` →
  `{available, messages}`, read by the router through the session's provider
  (`DelegateInspector.DelegateTranscript`), never proxied to the child: the
  harness writes the file as the delegate runs. The harness id comes from
  the path, so the provider checks it before it reaches a glob.
- **Where to look.** Under `ProviderRef`, then `ImportedFrom`: continuing an
  import forks it, and the original's delegates stay in the original's
  folder. (Drew is inclined to drop fork-on-resume for imports; a separate
  change.)
- **The view.** `DelegateView` reads the file, and re-reads it 1s after the
  last live event or record change. No file, or not yet: the event log, as
  before. The header says which.
- **The two sources differ** (measured on a live mads database and 30 real
  files, 2026-10-09):

  | | Live (`session.subagent_event`) | File |
  |---|---|---|
  | Tool calls and results | yes | yes |
  | The delegate's own text, final report included | **no** (0 events) | yes |
  | Its prompt, and messages relayed to it | no | yes |
  | Thinking | text (83 events) | **signature only, no text** (495 blocks) |
  | Timestamps | arrival time | the harness's, per line |
  | Bound | none, read in pages (below) | none |

  So the file view drops thinking text the live stream had. Not merged:
  rare, and the order of the two can only be matched by timestamp.
- **The conversation's dock** reads the file too *(2026-10-09)*: the
  subagent's task id is its file's name, so this works for subagents that
  ran before delegate records existed. No file: the event log, read back
  only then.
- **The event log is read in pages** *(2026-10-09)*. `GET /events` answers
  500 envelopes per call, oldest first, and the conversation, the delegate
  view and the worktree cards each read one page and stopped: in a session
  with 5,600 replayable events, everything after the first 9% was missing on
  reopen (empty docks, card counters and statuses, thinking, tool status
  changes). All three now page (`getAllEvents`). The conversation's replay
  leaves out `session.subagent_event`, two-thirds of that session's events
  and nearly all of its 11 MB, since the dock reads the file instead; it's
  read back in full only for a dock with no file.

### Imported sessions *(built 2026-10-09)*

An import records its subagents as ordinary delegate records — the same
table, shape and API as a live session's, so they show in the rail, the
conversation and the full-pane view, and a later re-run under a live runner
picks them up (Seed) with no special case. `DelegateInspector.DelegateHistory`
reads them from the harness's store; `delegates.FromHistory` maps them under
the live rules; the router records them at import
(`RecordImportedDelegates`) and backfills older imports at start
(`BackfillImportedDelegates` — sessions with no records only, so nothing a
runner learned since is overwritten).

How each ended comes from the **parent's** file (on 194 real subagents: 193
ended, 1 had no ending; 13 ran more than once):

| | Where the ending is |
|---|---|
| Foreground | The Agent call's `toolUseResult` (status, duration, tokens, tool count) |
| Background | A `<task-notification>` (task id, status, usage). Its Agent result only says `async_launched` |

A background notice is written when queued (`queue-operation` enqueue —
always) and again if delivered: a user line when the parent was idle, an
`attachment` (`queued_command`) mid-turn. The enqueues are the record (every
delivery had one; 80 enqueues had no delivery); deliveries count only for
an id with no enqueue. Each one is a run — a subagent the parent messaged
again ends again — and the last says how it ended. No ending anywhere:
`orphaned`, `reason: no_ending`, concluded by mads.

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

### Run-time checks *(#3, first pass built 2026-10-09)*

- **Defaults: the provider matrix.** The matrix has four new optional rows:
  `delegate_lifecycle` (C5), `delegate_transcript` (C1),
  `delegate_steer` (C2b: Native = direct, Emulated = relay through the
  parent) and `delegate_stop` (C3a). Claude Code declares native / native /
  emulated / unsupported (`stop_task` isn't wired). Every other adapter
  declares all four unsupported. Conformance checks the transcript row
  structurally (declared ⇔ `DelegateInspector` implemented). The other three
  need a live subagent, which a scripted run doesn't have.
- **Evidence: `provider.DelegateInspector`.** Claude Code's adapter reads
  `<projects>/*/<ref>/subagents/agent-<id>.{jsonl,meta.json}`: transcript,
  type, depth, worktree, background. The runner inspects 1s after a delegate
  starts (its files trail the first line) and again when it ends.
- **Resolved per delegate (`Delegate.capabilities`):**
  - `transcript` is true only on evidence.
  - `continuable` = transcript + the harness can fork (BehaviorBranching).
  - `steer` and `stop` come from the matrix.
  - `stops_with_parent` = true: cascading stops are mads's choice.
- **Correction from #1:** the notification's `output_file` is a task output
  under `/tmp/claude-*/…/tasks/`, not the conversation. It no longer becomes
  `transcript_path`; only inspection sets that.
- **UI:** the delegate view states the resolved capabilities as facts (own
  transcript / reachable through its parent / stops with its parent / could
  continue as a session / worktree). There are no controls yet: relay, stop
  and continue-as-session are the next actions to build on these.

Not yet: C6 (permission routing) and C7 (nesting parent) aren't resolved; a
delegate's `cwd` without a worktree isn't recorded; the ACP RFD's per-child
`capabilities` (the natural run-time source for ACP harnesses) awaits an
ACP adapter that announces children.

### Harness matrix

✓ = yes, ✗ = no, ~ = partial, ? = unverified. The *Run time* column lists
what to read live instead of trusting the default.

| | Claude Code | Codex | Kiro | Goose | Gemini CLI | ACP RFD (draft) |
|---|---|---|---|---|---|---|
| C1 Transcript | ✓ `subagents/agent-<id>.jsonl` (kept 30 days) | ✓ stored thread | ? | ✗ (probably in goose's store, not exposed) | ✗ | ~ replay via the parent's `session/load` |
| C2a Resumable | ✓ (Explore too: it exposes `task_id` and forks; Plan untested) | ✓ | ? | ✗ | ✗ | ✓ `idle` state |
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
- Stream-json follow-up (same day, mads's own argv minus MCP, hooks and
  the permission prompt tool): **no contradictions.**
  - The synthetic fork recalls for foreground, backgrounded and Explore
    subagents. The earlier claim that Explore returns no agent id was wrong.
  - A cross-folder fork works only if the synthetic file is placed in the
    *new* cwd's projdir; `--fork-session` then rewrites `cwd` on every line.
  - Still untested: PTY, permission control requests, interrupts during a
    subagent, concurrent and depth ≥ 2 spawns.
- Lifecycle consequence: a delegate can be **re-entered**, going from
  completed back to running under the same id, and the re-entry runs in
  the background across parent turns. The state machine must allow
  `completed → running`.
- **Key on `task_id`, never `tool_use_id`.** A relay re-entry's
  `task_started` and `task_notification` carry the *SendMessage*
  `tool_use_id`, while the delegate's own lines keep the original spawn id
  as `parent_tool_use_id`.
- **Claude Code reports a list of running background tasks:**
  `system/background_tasks_changed {tasks:[{task_id,task_type,description}]}`
  fires on every change. It is an authoritative snapshot to reconcile
  against; mads currently drops it as bookkeeping (`wire.go:437`).
- A late finish's `result` carries
  `origin:{kind:"task-notification",producer:"session-task"}`.
- Nested `local_bash` tasks run by a delegate carry `owned_by_subagent: true`.
- Import consequence: continuing an imported conversation forks it, so the
  parent's old delegates can't be relayed to afterwards. They can still be
  read and synthetically forked.

## Sidebar and views *(#2, built 2026-10-09; defaults await review)*

- **Conversation.** The tab loads its session's delegate records and follows
  `session.delegate_changed`. `mergeDelegates` makes a record the authority
  on whether a delegate is running and how it ended, over the task-line
  fold. The card, strip and dock render failed and stopped properly; an
  inferred ending says so on hover. While a delegate runs, its duration is a
  live 1s clock from its start (it used to move only when a subagent tool
  finished). Only the main agent's own frames open the live turn phase.
- **Sidebar rows.** A session's delegates nest under its card, before any
  linked subjects. Each row is one line: name, live elapsed, and a spinner
  or end glyph. The type is in the row's hover title. The connector is a
  thin **dashed** trunk with plain elbows and no arrowhead, deliberately
  unlike a link's solid curve.
- **Settings.** General → "Expose subagents in session list"
  (`Config.hide_delegates_in_session_list`, negated, default shown). It hides
  the rows, not the count on the status line.
- **Full-pane view.** Clicking a row, or the dock's "Open as page ↗", shows
  the delegate in the main pane in place of its parent's workspace. It has a
  breadcrumb back, the record's state and counters, and the delegate's
  stream (from the event log, live over the shared control-plane
  connection). It is read-only: steering waits on the C2 capabilities. Any
  card click leaves the view.

- **Fade** (Drew, 2026-10-09): Settings → "Fade out subagent cards after
  inactivity": 0 min, **10 min** (default), Never — counted from when a
  subagent finished; a running one never fades. The rail's cold read follows
  it (`?state=running`, `?ended_after=`, or everything), so finished rows
  survive a reload. They stay under a sleeping session's card too. Rows order
  running first, then most recently finished, so under Never the oldest fold
  behind "+N more".

Defaults chosen without a ruling (all one-line changes):
- **3 rows**, then "+N more" (`DELEGATE_ROWS_SHOWN`).
- The status-line wording ("N subagents running") awaits Drew's look at it
  live.

Not done: a nested subject's trunk runs past its delegates block; the
delegate view doesn't surface the delegate's permission asks (the parent's
conversation does).

## Phasing (proposal)

0. **Model (#1, done except as noted).** Delegate record and lifecycle in
   the session service, plus the sidebar count. Not done here: switching
   the conversation, panel and card over to the record (#2); per-line wire
   timestamps (#2, they only matter for in-stream durations); reading
   Claude Code subagent transcripts (#2/#3). `perTaskStopAffordance` is
   deliberately left unset (cascading stops).
1. **Sidebar and view.** Delegate rows, the parent's new status state, the
   full-pane delegate view, and the settings toggle.
2. **Control.** Stop, Relay, and Continue-as-session (after the probe).
3. **More harnesses.** ACP children when the RFD lands (opt in with
   `clientCapabilities.subagents`); Codex once a provider exists; Kiro.

## Known bugs to re-check after phase 0

Tags mark what #1 addressed; every item still needs the post-increment
check.

Found by reading the code on 2026-10-08; none reproduced yet. #1 and #7
were spot-checked in the code.

1. **[#1: fixed, TurnEndExcept/EndParent]** `toolstatus.TurnEnd` marks a background delegate's in-flight inner calls
   `interrupted`, and the UI's `statusOf` lets that override beat the later
   result, so the row stays ⊘ for good.
2. **[#2: fixed — the record's state]** A `failed` or `killed` notification renders a green ✓.
   `task_updated{status:"completed"}` is ignored.
3. **[#2: overridden by the record where one exists]** `finishRunningTasks` stamps live background tasks `interrupted`. Later
   progress sets `running` again, but the status stays `interrupted`.
4. **[#2: overridden by the record where one exists]** `resumeRunningTasks` revives only tasks spawned in the current turn, so a
   background task from an earlier turn replays as interrupted.
5. **[#2: fixed]** Tagged or task frames arriving between turns set the conversation's
   phase to live, and nothing ever closes it.
6. **[#1: the thinking-tick half fixed; nested-spawn tagging open]** Claude Code system lines are never parent-tagged. As a result
   `SubagentTask.ParentToolUseID` is always empty, and a subagent's
   `thinking_tokens` can flip `agent_state` to working outside a turn (the
   a2256c3 stall class).
7. **[B: moot for the full-pane view, which reads the file's own timestamps]** `wireLine` drops Claude Code's per-line `timestamp`.
8. **[#1: cascading kept by decision; the record now concludes the stops]** Interrupting the parent kills every background task
   (`perTaskStopAffordance` is unset), and `handleInterrupt` retires them
   all in the UI regardless.
9. **[B: fixed — the full-pane view reads the file; imports record their subagents]** Subagent history exists only in the mads event log; imported
   conversations have none.
10. **[#1: fixed]** The sidebar shows "Ready" while a background subagent runs.
11. The digest counts a delegate's tool uses as main-turn work.
12. **[#1 server + #2 UI: fixed, first anchor wins]** A relay re-entry re-anchors the task to the SendMessage call:
    `applyTaskEvent` overwrites `toolUseId` from every phase, and
    `tasksByToolUse` keys on it (found by the stream-json probe).
13. **[#1: fixed, roster consumed]** `background_tasks_changed` is dropped, so mads never reconciles against
    Claude Code's own list of running tasks.
14. (Unrelated, found along the way) Deleting a link holder leaves a
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
