# Deflake TestConformance/ref_announce — executor notes

Implemented exactly as designed by the architect handoff (see git history
for the full root-cause writeup); no deviations.

## What was done

- `providers/conformance/conformance.go`'s `checker.refAnnounce()`: when
  `select` lands on the `turnDone` case, it no longer concludes failure
  immediately. It calls a new `refAnnounceAfterTurnDone`, which does one
  more non-blocking check of `RefAnnounced()` and, failing that, `Ref()`.
  Both are monotonic (a buffered value sits until read; `Ref()`'s flag,
  once set, never unsets) — this converts the racy `select` tie-break
  (which doesn't preserve which channel became ready first in real time)
  into a deterministic post-hoc read of state that a compliant provider
  has, by then, always already set.
- Extracted `verifyRefAnnounced(working, ref, ok)` so the two call sites
  (the direct `RefAnnounced()` receive, and the retry inside
  `refAnnounceAfterTurnDone`) share the same working/unsupported
  verification logic instead of duplicating it.
- Added `providers/conformance/refannounce_test.go`: four white-box unit
  tests of `refAnnounceAfterTurnDone` covering each reachable state
  (announced-but-select-picked-turnDone, ref visible via `Ref()` instead,
  genuinely never announced, and the unsupported-honest case) — these
  construct the race's outcome directly rather than trying to force
  `select`'s tie-break under real goroutine timing, so they're
  deterministic themselves.

## What was not done

- `providers/fake/fake.go`: untouched, per the architect's root-cause
  finding — `fakeSession.Send` already writes the ref to `refCh` strictly
  before spawning the goroutine that can emit `EventTurnEnd` (a real
  happens-before edge via Go's `go`-statement memory-model guarantee). The
  flake was entirely in the observer (`conformance.go`'s racy `select`),
  not in the provider's synchronization. I did first attempt a
  provider-side fix (arming `RefAnnounced()` + a blocking handoff in
  `fake.go`) before reading `HANDOFF.md`'s existing analysis; reverted it
  in favor of the architect's simpler, lower-risk design once I found the
  handoff, since a blocking send in `fake.go` risks deadlocking existing
  callers (e.g. `TestDoubleAttachRejected`, `TestCrashOnCue`) that call
  `Send` without ever reading `RefAnnounced()`.
- No changes to `providers/claudecode` or `providers/kirocli`: both declare
  `BehaviorRefAnnounce` as working and share this same conformance path, so
  they pick up the fix automatically. Spot-checked
  `providers/claudecode/session.go`'s `streamSession` — same
  synchronous-write-before-read shape as the fake (`handleLine` sets
  `s.announced <- s.ref` before any event reaches `s.events`), so no
  separate provider-side bug there either.

## Verification performed

1. `go test -count=50 -run 'TestConformance/ref_announce' ./providers/fake/` — 50/50 PASS (both `TestConformance` and `TestConformanceWithScenario` subtests).
2. `go test -race -count=10 ./providers/fake/...` and `./providers/conformance/...` — both green.
3. Artificial load: 4 concurrent `go test -race -count=20 ./providers/fake/... ./providers/conformance/...` processes — all green (~42s each for conformance).
4. New unit tests (`refannounce_test.go`) pass individually and under `-race`.
5. `make check` — green (Go lint+test across all 41 packages, UI lint/test 195 files / 1000 tests). Exit 0.

## Reviewer scrutiny

- `refAnnounceAfterTurnDone` takes a bare `<-chan string` and `func() string`
  (not `provider.RefAnnouncer` / `provider.Session`), matching the
  architect's design, specifically so it's unit-testable without a mock
  session.
- One new error string was added:
  `"declared unsupported but Ref() is non-empty after the turn completed"`
  — mirrors the existing "at Start-return" wording for the pre-turn `Ref()`
  check. Every other error string is byte-for-byte unchanged from the
  original code.
- No `time.Sleep`, no retry loop, anywhere in the fix or the new tests.
- No changes to `fake.go`, `claudecode`, or `kirocli`.
