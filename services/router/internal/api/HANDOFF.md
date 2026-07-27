# HANDOFF: Staged links on POST /sessions (tsk_dabe3ab30473b0dd)

Extend `handleCreateSession` (sessions.go:22) with an optional `links` list so a
session is born already linked. The server creates the links between
`data.CreateSession` and `sup.SpawnSession` — that ordering is the entire point:
`setupOrchestrate` (services/session/internal/runner/capability.go:113)
materializes the orchestrate MCP once at child boot by reading
`LinksForSession`, so a client-side createLink-after-createSession races the
spawn and a leader can silently miss list_links/get_summary/send_message on its
first turn. Design agreed with Drew 2026-07-22 (see memory
`linked-sessions-launchpad-ux`); v1 UI (follow-up tsk_419872a903b03526) only
sends leader/monitor with the new session as granting side, but the API shape is
direction-generic — follower ("led by", placeholder on the subject side) must
work here.

## Request/response shape

`POST /sessions` body gains one optional field:

```json
{ "...existing Session fields...",
  "links": [ { "granting_session_id": "$new", "subject_session_id": "ses_abc",
               "preset": "leader", "cadence": "per_turn" } ] }
```

- Decode into a local wrapper — do NOT add a field to `contracts.Session` (it's
  the persisted record):
  ```go
  var req struct {
      contracts.Session
      Links []contracts.LinkRequest `json:"links,omitempty"`
  }
  ```
  Verified: `Session` has no custom UnmarshalJSON and no `links` JSON tag, so
  embedding promotes cleanly. Pass `&req.Session` to the existing flow
  (`resolveSessionDefaults`, providers.Get, CreateSession, SpawnSession).
- Entries are plain `contracts.LinkRequest` (pkg/contracts/link.go:114) with a
  placeholder for the not-yet-created session's ID. Add to
  pkg/contracts/link.go:
  ```go
  // NewSessionPlaceholder marks the to-be-created session's slot in a staged
  // LinkRequest on POST /sessions (either endpoint). Never persisted — the
  // Router substitutes the real ID before the link reaches the Data Service.
  const NewSessionPlaceholder = "$new"
  ```
  (`$`-prefix cannot collide with real `ses_*` IDs.)
- 201 response adds the created links so the caller can see the resolved IDs
  (this is what makes the follower direction "round-trip at the API"):
  `{"session": ..., "addr": ..., "links": [contracts.Link, ...]}` — include the
  key only when staged links were sent (`omitempty` behavior is fine); existing
  clients are unaffected.

## Handler flow (sessions.go)

Keep `handleCreateSession` readable by extracting the link work into two
helpers in this package (suggested: sessions.go or a new `stagedlinks.go`):

1. **Decode + flag gate.** After decode, if `len(req.Links) > 0`, load
   `s.data.Config(ctx)` and reject with **400** (`writeErrMsg`, message naming
   the linked-sessions flag) when `!cfg.FeatureFlags.LinkedSessions`. Don't
   reuse `requireLinkedSessions` (links.go:15) — that middleware 404s the whole
   route, but POST /sessions must keep working flag-off; only the field is
   refused. A one-line comment on the asymmetry is warranted.
2. **Pre-validate staged entries** (before `data.CreateSession`, so malformed
   payloads never create a session): each entry must have
   `NewSessionPlaceholder` on **exactly one** endpoint, and the other endpoint
   non-empty. Neither-side = a link between two existing sessions (belongs on
   POST /links, not here); both-sides = self-link. 400 via a
   `contracts.ValidationError`.
   Also pre-check the worktree rule here — **the store cannot catch it for a
   staged link**: `worktreeConflict` (services/data/internal/store/links.go:381)
   short-circuits on empty `WorktreeDir`, and the new session's `WorktreeDir`
   is only written by the child at boot (runner.go:284), i.e. after link
   creation. Fetch each counterpart via `s.data.Session` (doubles as an early
   404 for a bad counterpart ID) and reject when the new session *will* be
   worktree-isolated into the counterpart's workspace:
   `req.Isolation == contracts.IsolationWorktree && counterpart.Workspace != ""
   && counterpart.Workspace == req.Workspace && counterpart.WorktreeDir != ""`
   — worktree paths are per-session (`<root>/<sessionID>`), so a fresh worktree
   in the same workspace is a guaranteed different-worktree pair. Run this
   after `resolveSessionDefaults` (it fills Isolation/Workspace). Reuse the
   store's error wording. Same known gap as the store: an unspawned counterpart
   (empty WorktreeDir) isn't caught — parity, don't over-engineer.
3. **Create links** after `data.CreateSession`, before `sup.SpawnSession`:
   for each entry, substitute the placeholder with `sess.ID` and call
   `s.data.CreateLink(ctx, entry)` (pkg/dataclient/dataclient.go:623),
   collecting the returned `contracts.Link`s. This routes through the Data
   Service's POST /links, which is where all remaining validation already
   lives — do not duplicate it in the Router: preset resolution and the
   hand_raiser/unknown-preset refusal (services/data/internal/api/links.go:125,
   so user presets stay monitor/leader only), and the store rules (both
   sessions exist, no self-link, no reverse-direction dup, one orchestrate
   holder per subject, orchestrate-vs-live-pipeline). Entries are created
   sequentially; each `Create` runs in its own tx and sees prior rows, so
   cross-entry conflicts (e.g. two orchestrate holders onto the same subject)
   fail correctly on the later entry.
4. **Rollback — never an orphan.** On any `CreateLink` error: delete the
   already-created links (`s.data.DeleteLink`) then the session
   (`s.data.DeleteSession`), both best-effort on `context.Background()`
   (mirror the existing spawn-rollback comment at sessions.go:42), then
   `writeError(w, err)`. `writeError` (api.go:243) already maps the failure
   modes to 4xx: data-service 422 arrives as `*contracts.ValidationError` → 400
   (dataclient.go:100), missing counterpart → 404, conflict → 409.
5. **Spawn rollback grows.** The existing spawn-failure branch must now also
   delete the created links before deleting the session — session deletion
   deliberately does not cascade to links (contracts/link.go:82), so skipping
   this leaves dangling rows that `LinksForSession` would still return.

Actor: the router's dataclient sends `X-Mads-Actor: router`; link.created
envelopes are emitted by the store as usual. No supervisor or runner changes —
the ordering alone delivers first-turn orchestrate tools.

## Tests (services/router/internal/api, extend links_test.go or a new sessions_links_test.go)

Harness already does everything needed: `newTestServer` (api_test.go:110) runs a
real Data Service + supervisor with a fake-provider test child;
`setLinkedSessionsFlag` and `mkSessionRouter` helpers exist in links_test.go.
Four acceptance cases:

1. **Staged leader**: flag on; create subject via `mkSessionRouter`; POST
   /sessions with `links: [{granting: "$new", subject: subjID, preset:
   "leader"}]` → 201; response `links[0].granting_session_id` == new session ID
   and grants contain orchestrate; `GET /sessions/{new}/links` returns it.
   (Persist-before-spawn is structural in the handler; the first-turn MCP
   materialization from persisted links is already covered by
   runner/orchestrate_test.go — don't re-test the runner here.)
2. **Staged follower round-trip**: `links: [{granting: existingID, subject:
   "$new", preset: "monitor"}]` → 201; response link's `subject_session_id` ==
   new ID.
3. **Invalid staged link, no orphan**: flag on; use e.g. `preset:
   "hand_raiser"` (422→400 via data API) and a nonexistent counterpart ID
   (→404 from pre-validation fetch). Assert error status AND that no session
   was created (`GET /sessions` count unchanged) and no stray links exist.
   Worth one more sub-case: second entry invalid after a valid first entry —
   proves link rollback, not just session rollback.
4. **Flag off**: default flag state; POST /sessions with any `links` payload →
   400; no session created. Plain create without `links` still 201 flag-off
   (regression guard).

`go test ./services/router/... ./pkg/contracts/...` must pass; run
`gofmt`/`go vet` as usual.

## Risks / notes

- Rollback is compensating, not transactional: a router crash mid-sequence can
  leave an unlinked session or a dangling link. Same exposure as the existing
  spawn rollback; accepted.
- Order the link-creation loop's substitution carefully: substitute into a
  copy of the entry, don't mutate `req.Links` in place while also using it for
  error reporting.
- Do not touch the UI in this task — tsk_419872a903b03526 (Launchpad split
  button) consumes this endpoint.
- If docs are updated, note the new body field in docs/LINKED-SESSIONS-DESIGN.md
  rather than MADS-UX.md (the latter is aspirational — see memory).

---

## Executor handoff (tsk_dabe3ab30473b0dd)

**Done, per the spec above:**

- `pkg/contracts/link.go`: `NewSessionPlaceholder = "$new"` const.
- `services/router/internal/api/sessions.go` (`handleCreateSession`): decodes
  the wrapper struct (embedded `contracts.Session` + `Links`); flag gate right
  after decode (400 via `writeErrMsg`, not the 404 middleware — comment
  explains the asymmetry); pre-validation after
  `resolveSessionDefaults`/`providers.Get`; link creation between
  `CreateSession` and `SpawnSession`; both the link-failure and spawn-failure
  paths roll back links then session. Response adds a `links` key only when
  staged links were sent.
- `services/router/internal/api/stagedlinks.go` (new): `validateStagedLinks`
  (placeholder on exactly one endpoint, counterpart fetch → early 404,
  worktree pre-check with the store's wording; accumulates a single
  `ValidationError` across entries per spec §10.3), `createStagedLinks`
  (substitutes into the range-value copy — `req.Links` never mutated),
  `rollbackStagedCreate` (links then session, background ctx, best-effort).
- `services/router/internal/api/sessions_links_test.go` (new): the four
  acceptance cases. The invalid-link test is table-driven with four
  sub-cases: hand_raiser refusal (session rollback), nonexistent counterpart
  (pre-validation 404), no placeholder (400), and valid-first/invalid-second
  (proves link rollback — counterpart link lists asserted empty after).
- `docs/LINKED-SESSIONS-DESIGN.md`: item 8 under Decomposition documents the
  new body field.

**Not done / out of scope:** no UI changes (tsk_419872a903b03526), no
supervisor/runner changes (ordering alone delivers first-turn tools, per
spec), no worktree-conflict test (would need real workspace + worktree
fixtures; the pre-check condition mirrors `worktreeConflict` and carries the
same unspawned-counterpart gap as the store — parity by design).

**Reviewer notes:**

- The 201 response's `session` key serializes `req.Session` by value —
  identical shape to before; no-links requests get byte-identical responses.
- Pre-validation returns the counterpart-fetch error immediately (mid-loop)
  rather than accumulating it — a 404 isn't a field error; validation-shape
  problems across all entries are still reported together.
- `go test ./services/router/... ./pkg/contracts/...`, `go vet`, `gofmt`,
  `go build ./...` all clean.

Verified: `go test ./services/router/... ./pkg/contracts/...` — all pass.
