# Terminal mode: live output without remount, and real scrollback on reattach

Task: tsk_2f1a863d6d072826

## What was done

**Root cause #1 — no replay by design (backend):**
- `providers/provider/event.go`: new `EventPTYOutput` (`"pty_output"`) kind,
  distinct from `EventText` so replay/scrollback can be scoped to terminal
  bytes without touching chat-mode consumers (no query param needed).
- `services/session/internal/runner/hub.go`: the hub now keeps a bounded
  4 MiB ring buffer (`ptyReplayCap`) of raw bytes, written only when
  `broadcast` sees `EventPTYOutput` — true fixed-capacity circular buffer
  (`writePTY`/`ptySnapshot`), not append-then-truncate, so writes stay
  O(len(chunk)) instead of an O(cap) copy once full. New
  `subscribeWithPTYReplay()` snapshots the buffer and registers the new
  subscriber channel under one lock, so no byte can land in the gap between
  snapshot and attach.
- `services/session/internal/runner/runner.go`: `pumpPTY` now broadcasts
  `EventPTYOutput` instead of `EventText` for raw terminal reads. New
  `Runner.SubscribeTerminal()` wraps the hub method — added instead of
  changing `Subscribe()`'s signature, since ~20+ existing chat-mode test
  call sites depend on it unchanged.
- `services/session/internal/api/api.go`: `handleStream` (`GET /output`) now
  calls `SubscribeTerminal`, emits the replay (if non-empty) as one leading
  `pty_output` SSE frame before live events, then proceeds exactly as
  before. Chat-only sessions never write into the buffer, so replay is
  always empty for them — the scoping is structural, not conditional logic.
- `services/session/README.md`: documented the one exception to "GET
  /output is ephemeral."

**Root cause #2 — frozen live feed (frontend):**
- `ui/src/conversation/TerminalRenderer/TerminalView.tsx`: the mount effect
  had `[]` deps and closed over its `bridge` prop at mount time only. When
  `ConversationTab` reconnects its `SessionEventSource` (dead-on-arrival
  detection, `streamGeneration` bump), it rebuilds `SessionTerminalBridge`
  around the new source — but an already-mounted `TerminalView` kept
  listening on the old, now-dead bridge forever, since its effect never
  reran. Deps are now `[bridge]`; `spawnOptions` stays excluded (`ShellDock`
  passes a fresh object literal every render, which would cause unwanted
  re-effects). Confirmed via a dispatched investigation subagent that
  reproduced this exact freeze before the fix.
- `ui/src/conversation/TerminalRenderer/sessionTerminalBridge.ts`:
  `onData` now listens for `"pty_output"` instead of `"text"`, matching the
  new backend event kind.
- `ui/src/router/types.ts`: `StreamEvent` gained the `pty_output` variant.
- `ui/src/router/client.ts`: updated a stale comment describing the old
  shared-"text"-kind design.

**Root cause #3 (continuous rendering through a chat→terminal switch in the
same mount)** falls out of #2's fix: `TerminalView` already mounts fresh per
switch, and now also correctly reacts to any bridge/eventSource identity
change instead of only a full component remount.

## What was NOT done

- `TerminalView`'s effect still recreates the whole `xterm.Terminal`
  instance (and re-spawns via the bridge) whenever `bridge`'s identity
  changes, not just re-subscribes `onData`. For `SessionTerminalBridge`
  this is cheap and side-effect-free server-side (`spawn`/`kill` are no-ops
  there), and the new replay buffer means the recreated terminal
  immediately gets its scrollback back — but there's a brief visual reset
  (any not-yet-submitted local input in the xterm buffer is lost) on a
  stream reconnect while in terminal mode. A more surgical fix would split
  "terminal lifecycle" (mount/unmount) and "bridge subscription" (bridge
  identity) into two effects. Left as-is: reconnects are rare, the
  acceptance criteria only require no *page*-level remount, and the smaller
  change carries less risk.
- `ConversationTab.tsx`'s `onError` handler doesn't check `readyState`
  before deciding a connection is dead — flagged during investigation as a
  related but distinct issue. Left alone: it wasn't needed to satisfy any
  acceptance criterion (the replay buffer makes any reconnect it triggers
  harmless), and fixing it wasn't asked for.
- No change to the Router's proxying of `GET /output` — it's already
  byte-transparent, so the new `pty_output` event kind and replay frame
  pass through it unmodified; confirmed by grep, not by adding a
  Router-level test.

## What the reviewer should scrutinize

- `hub.writePTY`'s circular-buffer math (`services/session/internal/runner/hub.go`)
  — wraparound and the overflow/truncate-to-cap path when a single write is
  larger than the whole buffer. Covered by
  `TestHubPTYReplayCapBounded`/`TestHubPTYReplaySnapshotAtSubscribeTime`/
  `TestHubPTYReplayEmptyForChatEvents` in the new `hub_test.go`, but it's
  the one piece of genuinely new algorithmic code in this change.
  `broadcast` already held `h.mu` for the whole call, so `writePTY` runs
  under that same lock — no new locking was introduced.
  `subscribeWithPTYReplay`'s snapshot-then-register-under-one-lock is what
  makes replay-vs-live atomic; double check there's no path that calls
  `ptySnapshot`/`writePTY` without holding `h.mu`.
  `TestTerminalOutputReplaysOnAttach` (`services/session/internal/api/api_test.go`)
  is the end-to-end acceptance test: drives a full turn in terminal mode
  with nobody attached to `GET /output`, then attaches and checks the first
  SSE event is a `pty_output` replay containing that turn's output.
- The `TerminalView.tsx` deps-array fix is the actual bug fix for the
  reported freeze; `TerminalView.test.tsx`'s new
  "re-subscribes to a new bridge when the bridge prop's identity changes"
  test is the direct regression test for it (asserts the old bridge's data
  listener is torn down and the new bridge's is registered, without an
  unmount). It doesn't (and can't, in jsdom) assert on actual xterm-rendered
  pixel/text output, so it stops at the subscription boundary.
  `ui/src/conversation/TerminalRenderer/sessionTerminalBridge.test.ts` is
  new and directly proves `pty_output`-vs-other-kind gating in isolation.
- No test forces `ConversationTab`'s own `streamBroken`/`streamGeneration`
  reconnect path end-to-end while mounted in terminal display mode (the
  exact real-world sequence from Drew's bug report) — `FakeSessionEventSource`
  in `ui/src/router/fixtures.ts` always encodes `.data` as a JSON string, so
  the native-`Event`-shaped failure that trips `streamBroken` can't be
  simulated through it as-is. The fix is covered at the unit boundary
  (`TerminalView` re-subscribing on bridge identity change) instead of
  end-to-end; if this matters for review, extending the fake to support a
  non-string-data error simulation would be the way to close that gap.

## Verification

- `go build ./...` clean.
- `go test ./... -race` — all packages green, repo-wide (includes new
  `hub_test.go` and the new `TestTerminalOutputReplaysOnAttach` in
  `services/session/internal/api`).
- `npx tsc --noEmit -p tsconfig.json` (in `ui/`) clean.
- `npx eslint` on the touched frontend files clean.
- `npx vitest run` (in `ui/`): 200 files, 1058 tests, all green — includes
  the two new terminal-mode test files/cases above; every pre-existing
  chat-mode test is unchanged and still passing.
