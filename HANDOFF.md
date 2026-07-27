# tsk_42b9366cac3f13bf — Link cadence in the UI

## What I did

UI-only change (backend PUT /links/{id}, contracts.LinkRequest.Cadence, and
digestwatch debouncing already existed — confirmed by reading
services/router/internal/api/links.go, services/data/internal/store/links.go,
and services/router/internal/digestwatch/watcher.go before touching anything).

1. **Create-time cadence choice** (`LinkChooserDialog.tsx`): a second
   choice grid below Monitor/Leader, using the task's prescribed copy —
   "Follow intently (digest after each burst of activity)" / "Catch up on
   request (no automatic digests; the agent can ask for a summary)".
   Defaults to `per_turn`. Threaded into both the live `createLink` payload
   and the staged (`stagedHolder.onStage`) payload — every path now sends an
   explicit cadence, never omits it.
2. **Editing an existing link's cadence**: new `LinkCadenceControl.tsx`
   (Popover-based, modeled on `TrustPill.tsx`), rendered on nested subject
   rows in `SessionRail.tsx`. Calls `routerClient.updateLink(id, req)` (new
   method — added to `RouterClient`/`FetchRouterClient`/`FakeRouterClient`).
   Per `store.Links.Update`'s replace-whole-grant-set-and-reject-empty
   behavior, the control always resends the link's current `grants` (and
   `granting_session_id`/`subject_session_id`, for contract completeness —
   the data service ignores those two on PUT) alongside the new cadence.
   `railLinks.ts` now threads `cadence`/`grants`/`grantingSessionId` through
   `RailSubjectRow` and `RailRow.subjects` so the rail has what it needs.
   Live reflection of agent-initiated `set_attention` changes falls out for
   free: `useSessionLinks.ts` already refetches on `link.updated`, and that
   refetch flows back down through the same `links` prop.
3. **Honest badges/copy**: `ChatRenderer.tsx`'s `CADENCE_LABEL` no longer
   says "per turn" — it's "cadence: digest after each burst of activity" /
   "cadence: manual — no automatic digests". The new rail control's trigger
   badge reads "DEBOUNCED DIGESTS" / "MANUAL ONLY" for the same reason.
4. Fixtures/types: `ui/src/router/fixtures.ts`'s `FakeRouterClient.createLink`
   already defaulted cadence (task's cited line was already correct — no
   change needed there); added `updateLink` to the fixture. `types.ts`'s
   `Link`/`LinkRequest`/`LinkCadence` mirrors were already correct — no
   changes needed.

## Tests

- `LinkChooserDialog.test.tsx`: default-cadence assertion, cadence
  threaded into `createLink` payload (both preset paths) and into the
  staged `onStage` payload, `initialCadence` seeding on reopen.
- `LinkCadenceControl.test.tsx` (new): badge copy per cadence, opens menu
  and calls `updateLink` with resent grants + new cadence, no-op when
  reselecting the current cadence.
- `SessionRail.test.tsx`: cadence badge renders on a nested subject row
  and editing it calls `updateLink` with the right payload; manual_only
  badge renders correctly.
- `ChatRenderer.test.tsx`: updated the stale "cadence: per turn" assertion,
  added a manual_only case.
- `railLinks.test.ts`: updated all `RailSubjectRow`/`RailRow.subjects`
  shape assertions for the new `cadence`/`grants`/`grantingSessionId`
  fields.
- `Launchpad.test.tsx`: updated two staged-payload assertions that now
  include the defaulted `cadence: "per_turn"`.

`make check` passes (golangci-lint, `go test -race ./...`, `npm run lint`,
`npm run test` — 221 files / 1330 tests green). Also ran `tsc --noEmit`
directly to confirm no type errors.

## What I did not do

- No Go/backend changes — everything needed already existed.
- Did not add a cadence control to the top-level *holder* row (LEADER/MONITOR
  badge) — that badge aggregates multiple links with potentially different
  cadences, so there's no single link to edit there. The control only
  appears on nested subject rows, each of which maps to exactly one link.
- Did not change `StagedLinkChips.tsx` — it only ever displays
  preset/name, never cadence, and the task didn't ask for cadence in that
  chip's copy.
- Didn't add a cadence indicator to the chip in `StagedLinkChips.tsx` for
  staged (not-yet-created) links — the task's acceptance criteria only
  mention badges for real links, and the staged flow's own chooser dialog
  already shows the choice before staging.

## Things to scrutinize

- `LinkCadenceControl`'s trigger button reuses
  `launchpad/pills/pills.module.css` directly (cross-feature-folder import)
  rather than duplicating the CSS module — matches the task's "use existing
  design-system components" instruction, but it's a rail component pulling
  styles from launchpad's local pill CSS module rather than a shared
  `components/` location. If that's considered the wrong layering, the fix
  is to hoist `pills.module.css` (or just the trigger/menu/option classes)
  into `components/`.
- The rail badge label wording ("DEBOUNCED DIGESTS" / "MANUAL ONLY") is my
  own choice, not literally specified by the task — the task only
  prescribed exact copy for the *create-time* dialog. Worth a design pass
  if the reviewer wants tighter alignment with existing badge naming
  conventions (e.g. "LINKED"/"WATCHED"/"LEADER"/"MONITOR" are all-caps single
  words or short phrases; mine are longer).
- `FakeRouterClient.updateLink` was made lenient (synthesizes a link from
  the request if `id` isn't in its internal store) because several existing
  tests construct `Link` fixtures directly via `makeLink()` / pass `links`
  as a prop rather than going through `client.createLink()` first — the rail
  tests never touch the fixture's own store. Real `PUT /links/{id}` 404s if
  the link doesn't exist; the fixture intentionally doesn't replicate that
  404 path since no test needs it, but a reviewer wanting stricter fixture
  parity with the backend might want that added.
