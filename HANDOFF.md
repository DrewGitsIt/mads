# HANDOFF: tsk_dcf6455e8b50c166 — synthesis proposal cards

## What this task was

UI half of the background synthesizer (REBUILD-SPEC.md ~L385-392): render
synthesis proposals as approve/dismiss cards in the Context Rail (the pane
that already shows tasks and touched files), display-mode agnostic. No
architect HANDOFF.md existed for this task — the backend Proposal system
(routes, store, events) already existed from a prior task
(`tsk_e8396a6aae53304b`, commit `6ba74dc`/`998e756`); this task built the UI
against that existing contract.

## What was done

- **`ProposalCard`** (`ui/src/context-rail/ProposalCard.tsx`): renders one
  proposal's kind chip (New task/Note/Update), headline (task title, or
  target-task title for notes/updates), and a content summary. Approve/
  Dismiss call `routerClient.approveProposal`/`dismissProposal` and report
  back via `onApproved`/`onDismissed`. An already-`approved` proposal (the
  auto-approve path) renders as a static "Applied automatically" notice
  instead of action buttons. Border-pulses once on mount via a CSS
  `@keyframes` animation, disabled under `prefers-reduced-motion: reduce`
  (pure CSS, no JS `matchMedia` check).
- **`RailProposalList`**: fetches + filters to pending/approved-and-visible
  proposals via `useProposalStore` (GET /proposals + a live refetch on any
  `synthesis.proposal_*` SSE envelope — state always comes from the fetched
  record, never reconstructed from the envelope, so it survives a remount).
  Hides a card immediately on manual approve/dismiss (local optimistic
  filter) so the resolved task doesn't duplicate-flash once the Tasks list's
  own live refetch shows it.
- **`ContextRail`**: mounts `RailProposalList` above the Tasks/Changes tab
  switcher, visible regardless of active tab.
- **`tabRegistry.tsx`**: `ConversationTabView` now mounts `ContextRail` as a
  flex-row sibling to `ConversationTab` (new `ConversationTabView.module.css`),
  so the rail — and its proposal cards — are visible on the default
  "Conversation" tab, constant across the chat/terminal toggle.
- **Settings**: added an "Auto-approve synthesis" checkbox to
  `PipelineSection`, wired to `draft.synthesis_auto_approve`.
- **Router routes**: `services/router/internal/api/proposals.go` — thin
  proxies to the Data Service's existing proposals store: `GET /proposals`,
  `POST /proposals/{id}/approve`, `POST /proposals/{id}/dismiss`. Registered
  in `api.go` + `route_manifest_test.go`.
- **TS client**: `Proposal`/`ProposalType`/`ProposalStatus`/etc. types
  (`router/types.ts`), `RouterClient` methods + `FetchRouterClient` impl
  (`router/client.ts`), and `FakeRouterClient` fixture methods with realistic
  mutation semantics + fixture proposal/task data (`router/fixtures.ts`).
- **Tests**: `ProposalCard.test.tsx` (6), `RailProposalList.test.tsx` (6),
  `ProposalCard.reduced-motion.test.ts` (2, raw-CSS-string assertion per the
  acceptance criteria's "test or explicit CSS assertion" allowance —
  jsdom doesn't evaluate CSS media queries on rendered DOM), plus new/updated
  tests in `ContextRail.test.tsx`, `tabRegistry.test.tsx`, and
  `PipelineSection.test.tsx`.
- **e2e**: extended `ui/e2e/lib/fastModelFixture.ts` with a synthesis-shaped
  response branch (detected via the `"background task synthesizer"` marker,
  unique to `synthesisSystemPrompt` in `services/router/internal/job/
  synthesis_prompt.go` — does not collide with the session-level `synth`
  package's differently-worded prompt, see below). New
  `ui/e2e/synthesis-proposals.spec.ts` exercises the full acceptance-criteria
  journey against the real backend: create a `fake`-provider session, send a
  composer message containing a `synthwatch` confirmation phrase ("sounds
  good"), wait past `synthesis_debounce_ms` (2000ms default) for the
  synthesizer job to fire against the fixture and persist a Proposal, assert
  the card renders live over SSE, click Approve, assert the applied task
  appears in the rail's Tasks list without a reload.

## Verification

- `make check` (go test -race, golangci-lint, `npm run lint`, `npm run
  test`): green — 1236/1236 UI tests, all Go packages pass.
- `npx tsc --noEmit`: clean.
- `make ui-smoke`: 12 failing specs, all matching the documented pre-existing
  baseline (memory `mads_ui_smoke_baseline_failures.md`: 11 known
  fake-pipeline-seed failures — `dashboard`, `pipeline-board-scroll`,
  `retro`, `session-work` x2, `pipeline-drillin` x3, `linked-sessions`,
  `sessions` x2 — plus `library.spec.ts`'s documented flakiness) exactly by
  name. The new `synthesis-proposals.spec.ts` passed.

## What was *not* done / scope notes for the reviewer

- **Did not touch `services/session/internal/synth/engine.go`.** This is a
  second, pre-existing, *separate* synthesis mechanism (session-side, not
  router-side) that writes draft Task/Note records directly — bypassing the
  Proposal system entirely — whenever a confirmation phrase or write-tool use
  fires, independent of and in addition to the router-side `synthwatch` ->
  `job.SynthesizeSession` -> Proposal path this UI actually renders. Both
  fire on the same trigger in production today. This is a known, previously
  documented issue (see commit `6ba74dc`'s message: "Found but explicitly
  not fixed... a separate, pre-existing session-side synthesis mechanism").
  It didn't interfere with this task's e2e test (its differently-worded
  system prompt doesn't match the fixture's synthesis marker, so it silently
  gets the DPA fallback content, which parses to zero tasks/notes), but it
  means a real user session today can get *both* directly-written draft
  tasks *and* review-gated proposals from the same turn. Not in scope for
  this UI task; flagging for a future dedicated fix.
- **Did not add a dedicated card-transition/no-duplicate-flash visual test**
  beyond the unit-level assertion in `RailProposalList.test.tsx` (card hidden
  synchronously with the approve click, before any refetch) — no e2e
  screenshot/visual-diff check was added.
- **No new backend work.** The Proposal store, router-side `synthwatch`
  watcher, and synthesis job were already complete from the prior task; this
  session only added the thin router proxy routes needed for the UI to reach
  them (`proposals.go`) plus the TS client surface.

## Scrutinize

- The `synthesis-proposals.spec.ts` e2e test relies on `synthwatch`'s
  `substantive()` trigger firing on a *substring* match of a confirmation
  phrase anywhere in the rendered turn delta (not the whole message) — the
  composer message `"echo sounds good, let's go with that plan"` is
  constructed so the fake provider's deterministic `"echo "` reply behavior
  and the `"sounds good"` trigger coexist in one message. If `synthwatch`'s
  trigger heuristics or the fake provider's reply table change, this test
  will need updating in lockstep.
- `bodyFor()`'s branch ordering in `fastModelFixture.ts` matters: synthesis
  marker checked first, then digest, then the DPA default — since the
  synthesis system prompt and digest system prompt are both plausible
  substrings of unrelated content, double-check this if either prompt's
  wording changes.
