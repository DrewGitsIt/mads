# Chat/Terminal transport handoff on a shared session ref — executor handoff

## Task

Chat and Terminal UI views become two transports over ONE provider
conversation, with single-writer handoff (simultaneous mirroring explicitly
out of scope). The session child owns both transports; a switch-transport
operation is exposed via the Router with the same addr-lookup proxy pattern
as `/messages`/`/output`/`/interrupt`. Hard requirement: the old transport's
provider process must be fully dead (terminated and reaped) before the new
transport starts with `--resume <ref>`. Mid-turn switches are refused. UI:
Chat/Terminal toggle drives a real backend switch; during handoff, history
stays visible read-only with a "Switching to terminal…/chat…" indicator,
composer and terminal input disabled until the child reports the new
transport ready.

Three architect handoffs fed this work: `services/router/HANDOFF.md`,
`services/session/HANDOFF.md`, `ui/src/conversation/HANDOFF.md` (this file's
predecessor). All three were read in full before implementation and are
superseded by this single consolidated handoff.

## What was done

**`services/session`** (`internal/runner/runner.go`, `internal/api/api.go`):
- `GET /stream` renamed to `GET /output` (same SSE wire format) — the name
  no longer implies chat-only now that this channel also carries terminal
  pty output.
- `Runner.SwitchTransport(ctx, mode)`: serialized against `Shutdown` via
  `opMu`; refuses with `ErrSwitchBusy` (409) if a turn is in flight;
  idempotent no-op if already in the target mode; nils `r.sess` *before*
  releasing the lock and closing the old session, so `Send`/`Interrupt`/
  `TerminalWrite` see "no transport" for the whole switch window; blocks on
  `old.Close()` before calling `prov.Start` with `Resume: resumeRef` and the
  new mode — this ordering is what gives the "old process fully dead before
  new one starts" guarantee, not a timing assumption. On `prov.Start`
  failure, leaves `r.sess == nil` (502, `resume_failed`, `retryable: true`)
  rather than silently falling back to the old transport.
- `POST /transport` (body `{"mode": "chat"|"terminal"}`), `POST /input`
  (raw bytes → `Runner.TerminalWrite`), `POST /resize` (`{"cols","rows"}` →
  `Runner.TerminalResize`) added to the HTTP API, with 400/`ErrNotPTYMode`
  and 503/not-ready error mapping.

**`providers/fake`**: a double-attach guard makes the "old process dead
before new one starts" ordering deterministic in tests instead of relying on
timing — attaching a second session to the same fake process before the
first is closed fails loudly, so `TestSwitchTransportPreservesRef` and
`TestDoubleAttachRejected` catch ordering regressions directly rather than
being racy.

**`services/router`**: `/transport`, `/input`, `/resize` proxy routes added
using the existing `s.sup.SessionAddr(id)` → `s.proxyTo(...)`
reverse-proxy pattern (same as `/messages`/`/output`/`/interrupt`).
`route_manifest_test.go` updated to keep the manifest/`ui/src/router/
client.ts` cross-check green.

**UI** (`ui/src/router/client.ts`, `ui/src/conversation/`):
- `RouterClient` gained `switchTransport`/terminal input/resize methods and
  a `TransportSwitchError` class (carries `code`/`retryable` off the 409/502
  JSON body) plus matching `FakeRouterClient` behavior for unit tests.
- `SessionTerminalBridge` (new file,
  `ui/src/conversation/TerminalRenderer/sessionTerminalBridge.ts`) rides the
  same `SessionEventSource`/`streamSession` subscription the tab already
  holds open, rather than opening a second stream for terminal output.
- `ConversationTab.tsx` gained a `TransportState` discriminated union
  (`idle` / `switching` / `ready` / `error{code, retryable}`) driving
  render: history stays mounted and read-only with a switching indicator
  during the handoff, composer/terminal input stay disabled until `ready`,
  and an `error` state renders a `Retry` button (bumps a `retryToken` to
  re-drive the effect) instead of a dead end.

**Docs**: `services/session/README.md` documents `/output` (renamed, with
the why-note), `/transport`, `/input`, `/resize` including all status codes.
`ui/README.md` clarifies that Terminal mode is now a backend-driven
transport switch, distinct from the Electron-only Shell dock `ptyBridge`,
plus a manual smoke-test paragraph. `services/router/README.md` was already
current from prior work in this session.

**Tests**: `TestSwitchTransportPreservesRef`, `TestSwitchTransportRefusesMidTurn`
(runner-level), `TestDoubleAttachRejected` (fake provider), HTTP-level
tests for `/transport`/`/input`/`/resize` in `services/session/internal/
api/api_test.go`, router-level proxy tests in `stream_forward_test.go`,
and `ConversationTab.test.tsx` cases covering the switching-state render
(history + indicator + disabled inputs) and the error-state retry
affordance. `ui/e2e/sessions.spec.ts` was updated — see "What wasn't done"
below for what it deliberately does *not* assert.

## What wasn't done

- **Simultaneous mirroring** — explicitly out of scope per the design
  brief; not implemented, not stubbed.
- **e2e assertion on the transient "switching" indicator text itself** —
  `providers/fake`'s `Start` has no artificial delay, so against the e2e
  topology's fake provider the switch resolves near-instantly; asserting on
  the transient state would be flaky. `ui/e2e/sessions.spec.ts` instead
  asserts the switch completes (real `POST /transport` round trip:
  `[data-testid="terminal-view"]` becomes visible) and that chat history
  survives the round trip, without touching the transient indicator. The
  indicator itself is covered at the unit level in
  `ConversationTab.test.tsx`, which does not have this timing constraint.
- **xterm.js canvas/DOM content assertions in ui-smoke** — deliberately
  avoided per the architect handoff's scoping guidance; e2e only proves the
  transport switch round-trips through the real backend.
- **End-to-end exercise of the `resume_failed` (502) path** — not driven
  through a real dead-process scenario in e2e; covered at the
  `Runner`/HTTP-API level instead (`prov.Start` failure injection), plus the
  UI's error-state unit test. `providers/fake`'s session history is
  in-process memory only, so a real terminated child can never actually
  resume once its process is gone — the same pre-existing limitation
  `sessions.spec.ts`'s "close, resume affordance, delete" test already
  documents for the separate resume-a-closed-session feature.

## What the reviewer should scrutinize

- **`opMu` locking in `SwitchTransport` vs. `Shutdown`**: both take `opMu`
  before doing anything else — confirm there's no path where `Shutdown`
  can proceed (e.g. via a different mutex or an early return) while a
  switch is mid-flight, which would race the "old process reaped before
  new one starts" guarantee this task hinges on.
- **The nil-before-unlock ordering in `SwitchTransport`** (runner.go:417-431):
  `r.sess = nil` happens, then the lock releases, then `old.Close()` runs
  *outside* the lock. Worth confirming no caller can observe the nil-but-
  not-yet-closed window and take an action that assumes the old transport
  is already gone (e.g. a resource cleanup keyed on "sess is nil").
- **`resume_failed` recovery path**: once `prov.Start` fails, `r.sess` is
  left `nil` — the UI's `Retry` button re-drives the switch, but the *old*
  transport is already closed by that point (this is by design — no silent
  fallback), so a repeated `resume_failed` leaves the session with no live
  transport until a switch finally succeeds. Confirm this matches product
  expectations for a provider that's persistently failing to resume, and
  that the UI doesn't imply the old transport is still reachable during
  that window.
- **Mid-turn refusal reachability**: `TestSwitchTransportRefusesMidTurn`
  covers this at the `Runner` level — confirm (or add, if missing) an
  HTTP-level test that a `409` from `Runner` actually surfaces as `409`
  through `services/session`'s `/transport` handler and the Router's proxy,
  not just at the Go-struct level.
- **`SessionTerminalBridge` sharing the tab's existing SSE subscription**:
  confirm there's no double-subscribe or listener leak when a user switches
  Chat→Terminal→Chat→Terminal rapidly (the `retryToken`/effect re-run path
  in `ConversationTab.tsx` is the place to check).
- **`ui/e2e/sessions.spec.ts`'s scope reduction**: the fix here proves the
  switch round-trips and history persists, but intentionally stops short of
  the transient indicator and terminal content — if product wants stronger
  e2e coverage of the switching UI itself, that's a deliberate gap, not an
  oversight, and would need either a slowed-down fake provider or a
  different test strategy (e.g. intercepting the network request) to avoid
  flakiness.

## Verification

`make check` (golangci-lint, `go test -race ./...`, eslint, vitest) and
`make ui-smoke` (12 Playwright specs against real Go binaries + built UI +
`providers/fake`) both pass. `route_manifest_test.go` confirms the new
Router proxy routes match `ui/src/router/client.ts`.
