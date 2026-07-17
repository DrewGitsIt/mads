# Executor handoff: Linked sessions L2a: queued, attributed inject path

Task: tsk_979b7e41062d7036

## What shipped

- **pkg/contracts/transcript.go**: `Attribution` (`link_id`, `source_name`),
  `InjectRequest` (`text`, `attribution`), and `TranscriptMessage.Attribution
  *Attribution`, `omitempty` so every existing transcript fixture/consumer is
  untouched for ordinary turns.
- **pkg/contracts/event.go**: `LinkMessageInjected` event type, registered
  in `EventTypes`. Payload `{link_id, source_name, text}`; envelope
  `SessionID` is the subject, `Actor` is `"link-<id>"`.
- **services/session/internal/runner/runner.go**: the core of this task.
  - `Runner` grows `pending []pendingInjection`, `sentTurns int`,
    `turnAttributions map[int]contracts.Attribution`.
  - `deliverLocked(attribution *Attribution)` factors the "mark busy, bump
    the turn ordinal, record attribution if any" step out of `Send` and
    `Inject` so both go through one path.
  - `Inject(ctx, text, attribution) error`: if the runner is busy, appends
    to `pending` and returns immediately (decision 1 — never interrupts);
    otherwise delivers straight away, identically to `Send` but attributed.
  - `onTurnEnd()` replaces the inline `busy = false` in both `pump()`'s and
    `pumpPTY()`'s `EventTurnEnd` handling: it clears `busy`, and if
    `pending` is non-empty, pops the head and delivers it — all under one
    continuous lock hold, so a concurrent `Send`/`Inject` can't steal the
    slot ahead of an already-queued message (FIFO).
  - `recordInjectionAudit` appends `LinkMessageInjected` through a
    *separate* `dataclient.Client` scoped to actor `"link-"+LinkID`, not
    `r.data` (which is bound to the subject session's own actor per spec
    §15's per-service convention) — best-effort, logged not returned, since
    a failure here must never block delivery.
  - Trust capping (decision 3) needed no new plumbing: `Inject` ends by
    calling the same `sess.Send` an ordinary user turn calls, so an
    injected turn is bound by the subject session's own trust level
    automatically — proved by `TestInjectRespectsTrustCapping`, not just
    assumed.
- **services/session/internal/runner/transcript.go**: copies
  `turnAttributions` under lock (not holding it across the provider's
  network call), then correlates it against `Role == "user"` messages by a
  1-indexed ordinal that lines up with `deliverLocked`'s `sentTurns` bump.
- **services/session/internal/api/api.go**: `POST /inject` →
  `handleInject`, decoding `InjectRequest` and calling `runner.Inject`,
  reusing `writeRunnerError` as-is.
- **services/router/internal/api/stream_forward.go**: `postJSONToChild` —
  unlike `proxyTo`'s byte-transparent reverse proxy, this builds a fresh
  outbound request from a caller-supplied body, since inject needs the
  Router to resolve `Attribution` server-side before the request reaches
  the child.
- **services/router/internal/api/links.go**: `handleLinkInject` — flag gate
  → decode → link lookup (404) → grant check (403) → subject-running check
  (404) → deliver. `source_name` resolves from the granting session's
  current `Name`, falling back to its raw ID if that session's since been
  deleted (links don't cascade, so history shouldn't 404 just because the
  source is gone). Registered as `POST /links/{id}/inject` in
  `services/router/internal/api/api.go`, alongside the other four
  `requireLinkedSessions`-gated link routes.
- **ui/src/router/types.ts**: `Attribution` interface,
  `TranscriptMessage.attribution?`.
- **ui/src/conversation/ChatRenderer/ChatRenderer.tsx** +
  `.module.css`: an attributed message renders as a `.injected` block (left-
  aligned, bordered, `Chip tone="info"` reading "⇄ from Leader ·
  <source_name>" above the prose) *before* the existing role switch, so it
  never lands in the `.user` bubble even though its wire-level `role` is
  still `"user"`.
- Tests: `TestInjectQueuesBehindInFlightTurn`,
  `TestInjectAttributionReachesTranscript`,
  `TestInjectRespectsTrustCapping` (runner, against `providers/fake`);
  `TestLinkInjectRefusedWithoutGrant`, `TestLinkInject404WhenSubjectNotRunning`,
  `TestLinkInjectDeliversWithAttribution` (Router, the last against a real
  session-service child via `newTestServerWithRealSession`); a ChatRenderer
  component test proving the chip renders and the wrapper's class is
  `.injected`, not `.user`.

## What I did not do

- No persistence for `turnAttributions` across a session bounce/restart —
  it's an in-memory map on `Runner`, lost exactly like the rest of a live
  turn's transient state. The provider's own transcript has no metadata
  channel to persist attribution into. Flagged as a known gap for L2b/L4,
  not fixed here.
- `pending` is an unbounded, non-durable slice — same no-durability posture
  `Send` already has; queued injections are lost on a crash before they're
  delivered. Not addressed, per the architect's handoff explicitly scoping
  it out.
- No bounded "did it land" response from `POST /links/{id}/inject` beyond
  202 Accepted — decision 1 wants fire-and-forget async delivery; a
  confirmation channel is future work, not this task.
- Didn't add a README line for `POST /links/{id}/inject` in
  `services/router/README.md` (the architect's handoff called this
  optional/non-blocking).

## What the reviewer should scrutinize

- `onTurnEnd`'s lock discipline: it holds `r.mu` across `deliverLocked`
  (marking the queued turn busy) but calls `sess.Send` *after* unlocking,
  from inside `pump`'s own event-reading goroutine. This relies on the
  provider's `Send` being non-blocking (fire the turn, return — the fake
  provider streams asynchronously), so this same goroutine keeps draining
  `sess.Events()` immediately after. Worth confirming a real provider's
  `Send` never blocks waiting on something only its own event-pump
  goroutine would supply, or this could deadlock in production where the
  fake provider's looseness wouldn't catch it.
- `Transcript`'s ordinal correlation (`userIdx`) assumes provider history's
  `user`-role messages appear in the exact order turns were sent, one per
  turn, no more/no less. That holds for every provider today, but it's an
  implicit assumption this code introduces a new dependency on.
- `recordInjectionAudit` is fire-and-forget/best-effort by design (a
  logged, swallowed error) — confirm that's the right call for an audit
  trail vs. surfacing the failure to the caller some other way.

## Verification

`make check` (golangci-lint + `go test -race ./...` + UI vitest suite) is
green. `make ui-smoke` (Playwright e2e against a production UI build) is
green. `go build ./...` is clean.
