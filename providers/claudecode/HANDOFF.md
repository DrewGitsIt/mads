# HANDOFF: session discovery by first message (history.jsonl + transcript match)

Executor stage output for `tsk_f9b143f88f1d0941`. Implements the architect's
design (this file previously held that design doc — see git history if you
need it) on top of the eager-ref feature (`f06251c`, salvaged by `24342db`).

## What was built

**`providers/provider/provider.go`** — `SessionLocator`, an optional
Provider-level strategy interface:

```go
type SessionLocator interface {
	LocateSession(ctx context.Context, cwd string, spawnedAfter time.Time, firstMessage string) (ref string, ok bool)
}
```

**`services/session/internal/runner/runner.go`** — the Runner is the only
thing that knows the exact first message, so it drives discovery, not the
provider's `Start()`. `Send()` arms a one-shot goroutine
(`locateProviderRef`) on the first turn only (`!r.locateArmed && r.lastRef
== "" && r.sentTurns == 0`), passing the `(cwd, spawnedAfter)` pair captured
in `Start()` and the turn's exact text. `locateProviderRef` no-ops if the
bound provider doesn't implement `SessionLocator`. On ACP it's bounded to
`refLocateACPWindow` (2 min — the wire announcement is already the fast
path there, so this only covers the rare miss); on PTY it runs
best-effort-forever until `r.shutdownCh` closes, mirroring the old
`ptySession` poll's lifetime. A hit funnels through the existing
`persistProviderRef`/`recordMu` path from `f06251c`/`24342db` — no new
persistence mechanism.

**`providers/claudecode`** — the concrete implementation, two rungs:

- `locate.go` (new): `Provider.LocateSession` retries both rungs every
  `locateInterval` (500ms) until `ctx` is done.
  - Rung 1, `locateFromHistory`: scans `Config.HistoryFile` (default
    `~/.claude/history.jsonl`, claude-code's own append-only prompt index)
    for the one `sessionId` whose `project`/`timestamp`/`display` match
    `(cwd, spawnedAfter, firstMessage)` — `display` match is bidirectional
    prefix to tolerate claude's pasted-content placeholder mangling. Never
    trusted alone: `verifyRef` confirms a real transcript exists under
    `TranscriptDir` with a matching `cwd` before the caller sees it.
  - Rung 2, `discoverRefByContent` (`transcript.go`): the fallback when the
    index has no usable entry. `candidateTranscripts` (refactored out of
    the old ambiguity-enforcing `discoverRef`) returns every transcript
    filtered by mtime-after-spawn and stamped cwd; `discoverRefByContent`
    narrows those to the one whose first user-role message exactly matches.
  - Both rungs decline (0 or 2+ matches) rather than guess.
- `pty.go`: deleted the superseded self-contained discovery mechanism
  (`watchRefDiscovery`/`fire`/`fireNever`/`RefAnnounced`, and the
  `spawnedAt`/`transcriptDir`/`cwd`/`announced`/`done`/`refFired` fields it
  used) — `ptySession` no longer implements `provider.RefAnnouncer` at all.
  `startPTY` now just hands back a session whose `Ref()` starts (and, absent
  `--resume`, stays) empty; the Runner discovers it via `LocateSession`.
- `claudecode.go`: `Config.HistoryFile` (+ default), `BehaviorRefAnnounce`
  Matrix comment updated to describe both rungs.

**`providers/fake`** — `Scenario.LocateRef`/`LocateDelay` and a
`LocatingFake` wrapper so `SessionLocator` can be scripted deterministically
in Runner tests without a real binary (structural interface satisfaction —
`LocatingFake` only appears to implement `SessionLocator` when the scenario
opts in, matching real providers where the interface is conditionally
present).

## Test coverage

- `services/session/internal/runner/runner_test.go`: 4 new tests covering
  the backstop's arm/fire/persist path and the ACP-window bound.
- `providers/claudecode/discover_test.go`: rewritten for the
  `candidateTranscripts`/`discoverRefByContent` split — ambiguity is no
  longer `candidateTranscripts`'s problem (it returns every survivor now);
  the decline behavior moved to `discoverRefByContent`'s tests
  (hit/narrows-among-candidates/ambiguous/message-mismatch/wrong-cwd/stale).
- `providers/claudecode/locate_test.go` (new): `locateFromHistory`
  (hit/miss/ambiguous/stale/wrong-cwd/multi-line-same-session/prefix-match/
  missing-file) and end-to-end `LocateSession` tests (rung-1 hit,
  rung-1-hit-but-unverified-falls-back-to-rung-2, rung-2-only,
  ambiguous-never-guesses, retries-until-transcript-appears).
- `providers/claudecode/claudecode_test.go`: the two tests that asserted
  against the deleted PTY `RefAnnouncer` mechanism
  (`TestPTYSessionDiscoversRefFromTranscript`,
  `TestPTYSessionAmbiguousDiscoveryNeverAnnounces`) are replaced by
  `TestPTYSessionHasNoSelfDiscovery`, which asserts the new contract: `Ref()`
  stays empty across a turn and the session no longer implements
  `RefAnnouncer` at all. The hit/ambiguity behavior those two tests used to
  cover now lives in `locate_test.go` instead, where it belongs.
- `providers/claudecode/fakeclaude_test.go`: `testProvider(t)` now sets an
  isolated (nonexistent-by-default) `HistoryFile` under the test's temp dir
  — without this it silently defaulted to the real machine's
  `~/.claude/history.jsonl`, which a test must never read.

`make check` and `make ui-smoke` both pass green.

## What was NOT done

- **`fcLoadHistory` naming** (`fakeclaude_test.go`) is misleading — it
  parses transcript files, not real `history.jsonl` — but it's an existing
  helper unrelated to this task's new code paths (it feeds pre-existing
  conversation-replay assertions). Left alone rather than renamed, to keep
  the diff to what the acceptance criteria required.
- `kirocli` was left with no `LocateSession` method at all (rather than an
  explicit method that always returns `false`) — the interface is
  optional and structural, so plain absence already produces the correct
  "unsupported" behavior. Documented in `providers/kirocli/README.md`
  instead of adding dead code.
- `SwitchTransport` does not re-arm `locateArmed` — only a fresh session's
  first message is in scope per the acceptance criteria; a mid-conversation
  transport switch already has a ref (or the session wouldn't have gotten
  that far). Noted in the field doc on `Runner.locateArmed`.

## Follow-up pass: live-session ref write-back (review fix)

The first review pass failed on exactly the gap flagged above in this file:
`Transcript()` reads `sess.Ref()` directly, and nothing wrote a
backstop-discovered ref back into the *live* `provider.Session` object — a
permanently blank transcript pane for any session discovered only through
`SessionLocator`. Fixed by adding the write-back path rather than reordering
`Transcript()`'s preference (the more complete fix: `SwitchTransport`'s
`resumeRef := old.Ref()` reads the same stale field and would have had the
identical gap the first time a backstop-discovered session tried to switch
transport).

- **`providers/provider/provider.go`** — new optional interface `RefSetter`
  (`SetRef(ref string)`), the write side of `SessionLocator`.
- **`services/session/internal/runner/runner.go`** — `persistProviderRef`
  now also does `r.sess.(provider.RefSetter)` and calls `SetRef(ref)` under
  the same `r.mu` critical section it already uses to update `r.lastRef` —
  one lock hold, so `Transcript()` (which takes the same lock) can never
  observe `r.lastRef` and `sess.Ref()` disagreeing.
- **`providers/claudecode`** — `ptySession.SetRef`/`streamSession.SetRef`:
  set `s.ref` only if currently empty (never clobber `opts.Resume` or a ref
  the wire already delivered — wire wins any race with the backstop).
- **`providers/fake`** — `fakeSession.SetRef`: the fake already knows its
  true ref from `Start` (deterministic `fake-N` generation used as the
  history-recording key from the first `Send`, unlike a real session whose
  `Ref()` genuinely starts unknown) — `announced` is what models "unknown
  until discovered," so `SetRef` only flips that visibility switch rather
  than overwriting `s.ref`, which stays a fixed, lock-free-readable value
  for the session's lifetime (`Send` and friends already read it without
  `s.mu`). Guards against sending on `refCh` when `RefAnnounceNever` already
  closed it in `Start` — an early version of this panicked exactly there
  under `TestRefLocatePTYDiscoversRefFromScriptedLocator`.

New tests: `TestPTYSessionSetRefWriteBack` /
`TestStreamSessionSetRefWriteBack` (claudecode, unit-level: sets, then
proves a second/racing `SetRef` never clobbers), and
`TestTranscriptAfterLocateBackstop` (runner_test.go) — the actual regression
test, an ACP session with `RefAnnounceNever` + a scripted `LocateRef`
matching the fake's own real generated ref, proving `Transcript()` returns
the real turn content (not empty) once the backstop lands.

Everything else — the interface shape, the Runner arm/fire wiring, both
locate rungs, the fake scripting, and the test matrix — should be
reviewable as a normal diff. `make check` and `make ui-smoke` both pass
green after this fix.
