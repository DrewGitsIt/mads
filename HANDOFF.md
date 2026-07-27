# HANDOFF: Live orchestrate materialization for leader links (tsk_31cb053c8769c3f4)

## What this does

A leader ("orchestrate") link created onto an already-running session used to
sit silently inert until the session bounced — `setupOrchestrate` only ran at
child boot. This change makes the orchestrate MCP grant materialize (or
degrade) live, per what the session's provider actually supports, and makes
the link record + rail UI show an explicit `pending_restart` state whenever
live materialization genuinely can't happen for that provider. Teardown on
link removal is symmetric.

### Provider capability check

- `claudecode`: `BehaviorMCPServers: Native` in its matrix — supports passing
  `--mcp-config` at process launch, which we exploit by cycling the transport
  with a resumable ref (no user-visible restart: same conversation, brief
  process replace).
- `kirocli`: `BehaviorMCPServers: Unsupported` — no mid-session MCP config
  path at all. Link status is set (and stays) `unsupported`; UI shows a
  "NO TOOLS" pill instead of pretending it'll fix itself on restart.

### Status lifecycle (`contracts.OrchestrateStatus`)

New column on `links`, defaulted whenever the row carries the `orchestrate`
grant:
- `pending_restart` — set on create/grant-added-via-update, and left in place
  if the provider can't refresh live yet (e.g. no resumable ref exists yet).
- `active` — set once `RefreshOrchestrate` successfully cycles MCP servers in
  for that session.
- `unsupported` — set when the provider's matrix declares
  `BehaviorMCPServers: Unsupported`.
- `""` (empty) — link has no orchestrate grant at all; not shown in UI.

Store-level invariant: the status is *always* store-defaulted from the grant
set, never left to the caller to remember to set — see
`services/data/internal/store/links.go` (`Create`/`Update`) and
`SetOrchestrateStatus` (validates the grant is present and the value is
known).

### Runtime path

- Router: on link create/update/delete where an `orchestrate` grant is
  gained, lost, or a subject session is otherwise affected, it fires a
  fire-and-forget `notifyOrchestrateRefresh` POST to the affected child's own
  `/orchestrate/refresh` route (own goroutine, 30s timeout, never blocks the
  caller — a lost notification just leaves the link at `pending_restart`,
  which is the acceptable degraded state, not silent nothing).
- Session (child): `RefreshOrchestrate` (in `capability.go`) recomputes the
  MCP server set from current leader grants and, if it differs from what's
  currently running, cycles the transport via the shared `swapTransport`
  primitive. If a turn is in flight, the refresh is deferred
  (`orchestrateRefreshPending`) and applied at the next turn boundary
  (`onTurnEnd`), same as the existing `pending []pendingInjection` drain
  discipline, so it never interrupts a running turn.
- After a successful cycle (or an `Unsupported`/no-resume-ref outcome), the
  runner writes the link's `OrchestrateStatus` back via the data API, which
  emits `link.updated` — the rail UI's `useSessionLinks` refetches
  automatically off that event, no polling needed.

### UI

`SessionRail` shows a small pill next to the existing `LINKED` badge on a
leader's subject row: "TOOLS ON RESTART" (pending, `--warn-*` tokens) or
"NO TOOLS" (unsupported, `--bad-*` tokens). Nothing renders once status is
`active` — the existing `LINKED` badge alone communicates the working state.

## What was NOT done

- No change to how `setupOrchestrate` behaves at boot time itself — this
  change is additive (a live-refresh path that boot-time setup didn't have),
  not a replacement.
- No new UI affordance for creating a **follower** ("led by") link at create
  time — the task explicitly frames that as unblocked by this work, not
  delivered by it.
- No retry/backoff if the router's fire-and-forget notify is lost (e.g. child
  crashes mid-request) — the link simply stays `pending_restart` until
  something else triggers a refresh (e.g. another grant-affecting update).
  This matches the acceptance criteria's "explicit pending state" bar; an
  active reconciliation loop was judged out of scope for this task's size.

## Reviewer should scrutinize

1. **`services/session/internal/runner/runner.go` — `swapTransport`'s new
   `requireResumable` parameter.** This is a shared primitive also used by
   the pre-existing `SwitchTransport` (mode toggling, e.g. PTY discovery via
   locate-mode). While extracting `RefreshOrchestrate`'s cycle logic into the
   shared function, I initially added a universal
   `if resumeRef == "" { return ErrNoResumeRef }` guard, which broke 3
   pre-existing tests (`TestTerminalOutputReplaysOnAttach`,
   `TestRefLocatePTYDiscoversRefFromScriptedLocator`,
   `TestRefLocateNoSessionLocatorNeverHangs`,
   `TestSystemPromptPreservedAcrossSwitchTransport`) because
   `SwitchTransport` intentionally tolerates an empty ref (starts fresh,
   relies on `locateArmed`/SessionLocator to discover the ref afterward — a
   documented, accepted gap). Fixed by threading a `requireResumable bool`
   through `swapTransport`: `SwitchTransport` passes `false` (old behavior
   preserved), `RefreshOrchestrate` passes `true` (new guard — cycling into a
   fresh, unrelated session on an orchestrate refresh would risk silently
   discarding conversation history, which is a real regression risk this
   guard exists to prevent). Please double check this split is correct and
   that no other `swapTransport` caller exists that I missed.
2. **`services/data/internal/store/migrate_test.go`** — `TestMaxVersion`'s
   hardcoded literal changed `20` → `21` for the new migration
   (`0021_link_orchestrate_status`). This is the test's own documented
   update-on-migration convention, not a logic change.
3. Router-level coverage (`services/router/internal/api/links_test.go`) uses
   `newTestServerWithRealSession` (spawns the actual Session Service binary)
   for the positive materialize/upgrade/teardown-then-recreate paths, and the
   lighter `newTestServer` (testchild) only for the negative
   "monitor-only edit never notifies" case. Worth confirming this split in
   fixture weight is the right call rather than over-relying on the
   heavier real-session tests.
4. Automated coverage requested by the acceptance criteria:
   - claudecode-like live path: `TestRefreshOrchestrateMaterializesLive`,
     `TestRefreshOrchestrateDefersWhileBusy`,
     `TestRefreshOrchestrateTeardownOnGrantRemoval` (runner package,
     `orchestrate_test.go`), plus router e2e in
     `TestLinkCreateNotifiesRunningLeaderMaterializesOrchestrate`,
     `TestLinkUpdateNotifiesOnGrantAddition`,
     `TestLinkDeleteThenRecreateOrchestrateStillMaterializes`.
   - kirocli-style degrade: `TestRefreshOrchestrateUnsupportedProviderDegradesLinkStatus`,
     using a purpose-built `noMCPProvider` test wrapper (declares
     `BehaviorMCPServers: Unsupported`, directly modeling kirocli's real
     matrix declaration).
   - Store-level grant/status invariants: 8 tests in
     `services/data/internal/store/links_test.go`.
   - UI pill rendering + link→row wiring: `railLinks.test.ts`,
     `SessionRail.test.tsx`.

## Verification performed

- `go build ./...` — clean.
- `go vet ./...` — clean.
- `go test ./...` — all packages pass (full repo).
- `npx tsc --noEmit -p ui/tsconfig.json` — clean.
- `npx vitest run` (ui/) — 220 files / 1312 tests, all pass.
