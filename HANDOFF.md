# HANDOFF: tsk_c23ea1a4faca9002 — bound the monitor digest push path

## Problem
A monitor-role agent got bombarded with per-turn digests from a busy subject, reading each one and doing nothing. Owner intent: a monitor should be able to choose "follow intently" (`per_turn`) vs "catch up when requested" (`manual_only`), and the push path itself should never let a burst amplify.

## What was built (all four items from the architect handoff)

1. **Pull tools for read grants** — `setupOrchestrate` (`services/session/internal/runner/capability.go`) now materializes the orchestrate MCP server for a session holding *either* an orchestrate or a read grant. A read-only holder gets `MADS_ORCHESTRATE_READONLY=1`, which drops `send_message` from `tools/list` and rejects it if called anyway (`pkg/orchestratemcp/orchestratemcp.go`, `tools.go`). Router-side `findOrchestrateLink`/`handleOrchestrateSubjects` (`services/router/internal/api/orchestrate.go`) now accept either grant for the pull routes (`get_summary`, `get_turns`), keeping `send_message` orchestrate-only.

2. **Agent-selectable attention** — new `set_attention` tool (`pkg/orchestratemcp`) and Router route `POST /sessions/{id}/orchestrate/attention` (`handleOrchestrateSetAttention`) let a session flip its own held link's `Cadence` between `per_turn`/`manual_only` at runtime. Strictly scoped: rejects (403) a `link_id` the caller isn't the granting side of. `link.updated` refresh is automatic via the existing store layer (`Links.Update`), not new code here.

3. **Debounce + coalesce the push path**:
   - `Config.DigestDebounceMS` (`pkg/contracts/config.go`, default 2000ms, mirrors `SynthesisDebounceMS`).
   - `digestwatch.Watcher` (`services/router/internal/digestwatch/watcher.go`) now buffers a subject's turn deltas per-session and debounces via `time.AfterFunc`, mirroring `synthwatch`'s `sessionBuffer` precedent exactly. `job.GenerateDigest` signature changed from `(sessionID, delta string)` to `(sessionID string, deltas []string)` to carry a whole burst, mirroring `job.SynthesizeSession`.
   - `runner.drainNextLocked` (`services/session/internal/runner/runner.go`) coalesces a *contiguous run* of same-link digest-attributed queued injections into one forced turn at `onTurnEnd`. Non-digest or different-link queued injections still deliver individually, in order, unchanged.

4. **Loop-hazard guard** — `contracts.Attribution.DigestAttributed` (`pkg/contracts/transcript.go`) marks a digest push end-to-end: `deliverDigest` sets it → `pendingInjection` carries it → `digestAccumulator.recordUser`/`renderAndReset` (`services/session/internal/runner/digest.go`) track whether *every* recordUser call this turn was digest-attributed and report `digestOnly` → `pushTurnEnded`'s envelope payload carries `"digest_only"` → `digestwatch.Watcher.Run` skips buffering that turn's delta as a digest trigger when `digest_only` is true. A monitor's own forced turn can no longer re-trigger digestwatch.

## Tests added (acceptance criteria)
- `pkg/orchestratemcp/orchestratemcp_test.go`, `services/router/internal/api/orchestrate_test.go`, `services/session/internal/runner/orchestrate_test.go`: read-grant-only sessions get the 4-tool read-only slice, never `send_message`; orchestrate holders keep all 5; `set_attention` flips the caller's own link and is rejected for links it doesn't hold.
- `services/router/internal/digestwatch/watcher_test.go`: `TestWatcherDebouncesTurnEndedBurstIntoOneDigest` (burst of 5 turns → exactly one `GenerateDigest` job/delivery), `TestWatcherSkipsDigestOnlyTurn` (a `digest_only: true` envelope spawns zero digest jobs).
- `services/session/internal/runner/runner_test.go`: `TestInjectCoalescesQueuedDigestInjections` (3 queued same-link digest injections drain as one combined forced turn; a queued non-digest injection from a different link still delivers its own turn afterward, in order).
- All existing tests in the touched packages still pass; `make check` (lint + `go test -race ./...` + `npm test`) is green.

## Bug found and fixed outside the four items above
`pkg/capability/mcp.go`'s `EnsureStarted` spawned every Command-based capability process with `cmd.Stdin` unset, which Go resolves to `/dev/null` — an immediate EOF. This is harmless for capabilities like `sleep 30`/`true` (existing tests), but fatal for `orchestrate-mcp`, which is a genuine stdio JSON-RPC read loop (`os.Stdin`): it exited almost immediately after every spawn, including the very first materialization at session `Start`. This surfaced as a flaky-looking (actually deterministic, just timing-sensitive) failure in `TestRefreshOrchestrateDowngradesToReadOnlyOnGrantRemoval` under `-race`.

**Round 1 fix (reverted by review):** gave the spawned process a `blockingReader{}` stdin — a bare custom `io.Reader` whose `Read` never returns — instead of the default `/dev/null`. Review caught that this is wrong: when `cmd.Stdin` is anything other than an `*os.File`, `exec.Cmd` allocates its own internal pipe and copies from `cmd.Stdin` to it on a background goroutine. That goroutine's `Read` call blocks forever by construction, so `cmd.Wait()` — which waits for that copy goroutine to finish, not just the process — never returns, even after `Stop()` SIGTERM/SIGKILLs the child. Every `EnsureStarted`/`Stop` cycle leaked the `go func(){ cmd.Wait() }()` reaper goroutine and the internal pipe's write-end FD.

**Round 2 fix (current):** `Registry` now lazily creates one real `os.Pipe()` per registry (`blockingStdin`, `stdinR`/`stdinW` fields, guarded by `stdinMu`) and hands every spawned process the *read end* (`*os.File`) as `Stdin`, reused across all `EnsureStarted` calls. Because it's a genuine `*os.File`, `exec.Cmd` dups it directly into the child with no internal copy goroutine, so `cmd.Wait()` returns as soon as the child dies. The write end is held open on the `Registry` for its own lifetime (never written, never closed) so the read end never sees EOF — same "block forever, no EOF" property as before, just backed by real FDs instead of a Go-level `io.Reader`.

Added `pkg/capability/mcp_test.go`'s `TestEnsureStartedStopDoesNotLeakGoroutine`: starts a capability, stops it, and asserts `runtime.NumGoroutine()` settles back to its pre-start baseline within 2s. Verified this test fails (goroutine count stays elevated) against the round-1 `blockingReader` code and passes against the round-2 fix.

## What was NOT done (deliberately out of scope)
- `OrchestrateStatus` (`pending_restart`/`active`/`unsupported`) was **not** extended to read-only holders — the handoff didn't ask for it, and `RefreshOrchestrate`'s `markOrchestrateLinks` still only marks links with the orchestrate grant.
- `job.SessionTitleKind`/`GenerateSessionTitle` spawning is still per-envelope and undebounced, and still fires even for `digest_only` turns — out of the acceptance criteria's scope.

## What the reviewer should scrutinize
- **Digest debounce buffering is keyed per subject session**, not per link — one burst produces one `GenerateDigest` call that internally fans out to every `per_turn` monitor link (existing fan-out logic, unchanged).
- **`drainNextLocked` only coalesces a contiguous run** at the front of `r.pending`, not all matching items scattered anywhere in the queue — deliberate, to preserve FIFO ordering for interleaved non-digest/different-link items. The digestwatch-level debounce should make non-contiguous duplicates rare in practice, but this is a judgment call, not something the handoff specified.
- **Coalesced digest texts are joined with `"\n\n"`** — my own formatting choice, not specified by the handoff.
- **The `pkg/capability/mcp.go` stdin fix** changes behavior for *every* Command-based capability process, not just `orchestrate-mcp` — intentional (it's the correct general fix for the Registry's "stays running until explicitly stopped" contract), but it's a broader-reaching change than the four build items technically called for, so it's worth a second look.
- **`Registry.blockingStdin`'s pipe is shared across every capability the Registry spawns**, not one pipe per process — deliberate (avoids an unbounded number of held-open write-end FDs, one per `EnsureStarted` call, over a long-lived Registry), and safe since nothing ever writes to it, but it does mean every spawned child's stdin fd 0 is a dup of the *same* underlying pipe. `stdinR`/`stdinW` are never closed for the Registry's lifetime — intentional leak, bounded by one Registry instance, not one per spawn.
