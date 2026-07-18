# Executor handoff: Committed files open their diffs

Task: tsk_1b1c78ee5d438a91
Stage: executor → reviewer

## What I did

Implemented per the architect's design (this file, now overwritten per
pipeline convention — see the previous commit for the original design doc):

- **Router**: new `GET /sessions/{id}/commits/{hash}/diff?path=...`
  (`services/router/internal/api/commit_diff.go`). Resolves the commit via
  `s.data.Commits`, verifies it's reachable in the recorded repo (`git
  cat-file -e`), then uses `git show <hash>^:<path>` /
  `git show <hash>:<path>` for before/after content. An added file (no
  parent copy) is the expected shape, not an error; only a missing
  modified-side, unreachable commit/repo, or missing `path` produce an
  error status (400/404/502 — never a silent 200, never a 500). Registered
  in `api.go`; route covered in `route_manifest_test.go`; behavior covered
  in `commit_diff_test.go` (added file, modified file, commit not linked to
  session, no repo recorded, unreachable repo, missing `path`).
- **Frontend**: `CommitList` file rows are now selectable buttons (same
  `composes` idiom as `ChangeList`), `DiffViewer` shows a "Diff from commit
  &lt;short hash&gt;" chip for committed-file selections, and `FilesTab`
  holds a discriminated `Selection` union so a working-change selection and
  a committed-file selection are structurally mutually exclusive (one state
  slot, not managed-by-hand resets). `client.ts`/`types.ts`/`fixtures.ts`
  got the corresponding `getCommitFileDiff`/`fixtureCommitFileDiffs`
  additions.
- **e2e**: `session-work.spec.ts`'s files-tab journey clicks the seeded
  pipeline commit's own file (`src/commit-file.ts`, added against a real
  parent commit that didn't contain it — exercises the added-file path) and
  asserts the diff editor renders with the correct commit label.
- All of the above matches the architect's design, with one deviation: the
  design doc's e2e assertion says to wait on `.monaco-editor` becoming
  visible, calling it "Monaco's own stable internal class, already used as
  an e2e selector elsewhere" — that citation doesn't hold (grepped the repo,
  no prior e2e use). In the real browser, Monaco's `DiffEditor` renders
  eight-plus nested elements carrying the `monaco-editor` class (gutters,
  panes, etc.), so `.first()` isn't reliably the visible root and the
  assertion flaked. Used `.monaco-diff-editor` instead — the `DiffEditor`
  widget's own top-level container class (confirmed in
  `node_modules/monaco-editor`), which is unique and reliably visible.

## Collateral fixes (outside the stated scope, but blocking `make ui-smoke`)

Both are pre-existing bugs, not introduced by this task's feature work, but
this task's `make ui-smoke` run was the first time either got a chance to
manifest. Landed both because "`make ui-smoke` must pass" is an explicit
acceptance criterion — flagging both here for scrutiny since they touch
shared, cross-feature code.

### 1. `MergeBackFlow` connection-cap deadlock

`MergeBackFlow` opened its own raw, unpooled `EventSource` for merge events
instead of sharing the session workspace's already-pooled subscription
(`ui/src/router/events.ts`'s `subscribeToEvents` dedupes SSE connections by
exact resulting URL). In the real e2e (Chromium, real backend), a session
workspace with the Files tab open already sits close to Chromium's
6-connections-per-origin HTTP/1.1 cap (rail's global subscriptions + this
session's other streams). `MergeBackFlow`'s own extra connection pushed it
over the cap and permanently starved an unrelated `fetch` (the diff content
GET), hanging the test on a stuck, uncancellable request (`status: -1`).

Fix: added the merge event types (`merge.attempted`, `merge.conflicted`,
`merge.completed`) to `WORKSPACE_SESSION_EVENT_TYPES`
(`ui/src/router/sessionEventTypes.ts`) and switched `MergeBackFlow` to
`subscribeToEvents` with that shared array, matching the pattern
`ConversationTab` already documents and uses. Removed the component's manual
`SessionEventSource`/ref/cleanup code in favor of the single
`subscribeToEvents` unsubscribe function. `MergeBackFlow.test.tsx` and
`FilesTab.test.tsx` needed a `FakeEventSource` global stub added (the
latter had none — `FilesTab` renders `MergeBackFlow`, which now opens an
`EventSource` on mount).

### 2. Task-event refetch storm

Once (1) freed a socket, the task-events pooled subscription (`useAllTasks`,
`useOpenTaskCounts` — both subscribe globally, unfiltered by session) could
open for the first time in this test's browser session. Its SSE stream
replays full history on connect with no `since` filtering, and every
dispatched event triggered its own uncoalesced `refetch()` → `GET /tasks`,
with no debounce. Against this dev/test data store's accumulated history
(confirmed via instrumentation: hundreds of distinct real historical task
events spanning several days of prior test runs, not a bug in this task's
seeded fixture data), this fired well over a thousand `GET /tasks` requests
within roughly 100ms, tripping Chromium's `ERR_INSUFFICIENT_RESOURCES` and
hanging the page load.

Fix: both hooks now debounce their event-triggered refetch with a 50ms
trailing timer (`ui/src/features/sessions/state/useOpenTaskCounts.ts`,
`ui/src/features/command-palette/state/useAllTasks.ts`), collapsing a burst
into a single refetch. Existing unit tests for both hooks pass unmodified —
they only assert eventual convergence via `waitFor`, which tolerates the
added delay.

I did not investigate why this dev/test data store has that much
accumulated task-event history, or whether the SSE
replay-everything-on-connect behavior should be scoped differently (e.g. a
default `since`) — that looked like a separate, likely intentional design
decision (full replay so live folds always converge from a cold start) and
is out of scope here. The debounce is the minimal fix for the actual
failure mode (many events dispatched in a tight window), not a change to
what's fetched or when the stream connects.

## What I did not do

- Did not add `GET /sessions/{id}/changes`, `GET /mentionables`, `POST
  /system/open-editor`, or `POST /sessions/{id}/merge` — explicitly out of
  scope per the architect's design doc's correction to the task brief.
- Did not investigate the backend's SSE-replay-vs-batch-query volume
  difference beyond confirming it's real historical data rather than a
  filtering bug — not needed once the debounce fix resolved the actual
  failure.

## What to scrutinize

- The 50ms debounce value in `useOpenTaskCounts`/`useAllTasks` is a judgment
  call, not derived from any spec — reasonable but arbitrary.
- The `MergeBackFlow`/`sessionEventTypes.ts` pooling change alters when/how
  merge status updates arrive (shared connection instead of an independent
  one) — behaviorally equivalent per its test coverage, but it's a change to
  a shared, cross-feature constant, worth a second look.
- The `.monaco-diff-editor` vs `.monaco-editor` e2e selector deviation from
  the design doc, noted above.

## Checks

`make check`: passes (958/958 unit tests).
`make ui-smoke`: passes (16/16 e2e tests, including the extended files-tab
journey).
