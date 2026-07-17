# Executor handoff: Linked sessions L1: link records with grant sets

Task: tsk_ed37048530969b06

## What shipped

- **pkg/contracts/link.go**: `Grant` (read/inject/orchestrate), `Grants` registry,
  `LinkPreset` (monitor/hand_raiser/leader) with `PresetGrants`, `Link` (with
  `HasGrant`), `LinkRequest`. Golden producer/consumer tests + fixture.
- **pkg/contracts/event.go**: `LinkCreated`/`LinkUpdated`/`LinkDeleted` event types,
  registered in `EventTypes`.
- **pkg/contracts/session.go** + **ui/src/router/types.ts**: removed the dead
  `LinkedLeaderID`/`linked_leader_id` field (superseded by the `Link` entity; the
  architect's HANDOFF.md claimed no `ui/src` reference existed, but I found and
  removed one — it was an unused type-only field, safe to drop alongside the Go
  field).
- **services/data/internal/store**: migration `0012_links` (no FKs, by design —
  see the migration file's comment); `links.go` — CRUD + every §2 validation rule
  from the design doc, collected into one `ValidationError`: both sessions exist,
  no self-links, no duplicate/reverse-direction pair, worktree-of-same-repo
  conflict refused, `orchestrate` refused against a live pipeline-run subject,
  at most one `orchestrate` holder per subject (checked in Go over all of the
  subject's links, not a SQL LIKE on the JSON grants column). `Update` re-validates
  grant shape + the orchestrate rules only — endpoints are immutable after create.
  `sessions_test.go` gained `TestSessionDeleteDoesNotCascadeToLinks`, sitting next
  to `TestSessionDeleteCascades` with a comment explaining why links are the one
  exception to that file's cascade rule.
- **services/data/internal/api/links.go**: 5 CRUD/list routes. `resolveGrants`
  is where preset→grants resolution and the hand_raiser-refused-from-user-route
  rule live — deliberately kept out of the store so a future L3 implicit-link
  path can construct a hand_raiser link directly through the store without this
  check in its way.
- **pkg/dataclient**: `CreateLink`/`Link`/`UpdateLink`/`DeleteLink`/`LinksForSession`,
  following the existing `do(ctx, method, path, in, out)` pattern.
- **services/router/internal/api/links.go**: thin proxies, all five routes wrapped
  in `requireLinkedSessions` (checks `Config.FeatureFlags.LinkedSessions`
  per-request, not baked into the mux — flag toggles take effect without a
  restart).
- Tests at all three layers: 14 store tests (`links_test.go`), 3 Data Service API
  tests (preset resolution, hand_raiser refusal, explicit-grants round trip), 2
  Router tests (all five routes 404 with the flag off; full create/get/update/
  list/delete round trip with it on).
- README updates in `services/data/README.md` and `services/router/README.md`.
  `docs/MADS-UX.md` §4 event vocabulary and §5 open-question-3 updated.

## What I did not do

- No UI work (out of scope per the task — L1 is contracts/data/router only).
- No implicit hand_raiser link creation path (that's L3, per the design doc's
  phasing — `resolveGrants` refuses hand_raiser from the user route but nothing
  yet creates one).
- Didn't touch `route_manifest_test.go` — read it first; it only asserts
  registered routes resolve, doesn't require an exhaustive route list, and no
  `ui/src/router/client.ts` caller exists yet for `/links`, so there was nothing
  to add there.

## What the reviewer should scrutinize

- The single-orchestrate-holder and live-pipeline checks in
  `services/data/internal/store/links.go` run as separate queries inside the same
  `store.Log.WithTx` transaction as the write — worth confirming the isolation
  level is sufficient to prevent a race between two concurrent orchestrate-grant
  requests for the same subject (SQLite's default transaction locking should
  serialize this, but I didn't add a dedicated concurrency test for it).
- `worktreeConflict` uses `Session.Workspace` as the same-repo proxy signal (per
  the architect's HANDOFF.md guidance) — this is a heuristic, not a true repo
  identity check; flagged here in case a workspace can legitimately point at two
  different repos under the same key in some setup I didn't check.
- The removed `LinkedLeaderID`/`linked_leader_id` field: I made the call to
  delete the UI-side stray reference without asking, since it was unused outside
  its own type declaration. Worth a quick look to confirm nothing downstream
  depended on that field name outside the two files I touched.

## Verification

`make check` (golangci-lint + `go test -race ./...` + UI vitest suite) is green.
`go build ./...` is clean.
