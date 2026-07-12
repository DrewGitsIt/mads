# providers/

The provider layer (spec §11, §15): everything between the platform and an
agent harness. Adapters are Go packages linked into the services that spawn
agents — not services themselves.

- `provider/` — the abstraction: the `Provider`/`Session` interfaces, optional
  strategy interfaces (`ModelDiscoverer`, `TranscriptReader`), the capability
  `Matrix` (each behavior declared native/emulated/unsupported, required rows
  enforced at registration), normalized events/usage/transcript types, and the
  `Registry` the platform resolves providers from.
- `conformance/` — the §11 capability matrix as an executable test suite.
  Every adapter's test package calls `conformance.Run(t, p, script)`; a
  mismatch in either direction (declared-working-but-broken, or
  declared-unsupported-but-quietly-works) fails. `Check` returns per-behavior
  results for programmatic use — the Settings provider preflight renders from
  the same data. Probes the suite cannot run for a given provider (e.g.
  interrupt verification on a PTY-only harness) pass with an explicit
  `Note`, never silently.
- `fake/` — **provider-fake** (W2.2), the deterministic scripted test double
  every downstream suite runs against.
- `claudecode/` — the reference adapter (W2.3).
- `kirocli/` — the second adapter that keeps the abstraction honest (W7.1).

## Test it

```sh
go test -race ./providers/...
```

The conformance suite's own correctness is proven in `conformance/` against
an in-memory stub: an honest stub passes (full and partial matrices, ACP-only
and PTY-only), and every lie class is caught (`TestMismatchesAreCaught`).

## Contract notes

- A `Session` turn: one `Send`, streamed `Event`s, closed by `EventTurnEnd`.
  The events channel closing **without** a turn end is the process-died signal
  the Router's supervision consumes.
- `Interrupt` ends the turn but not the session; conformance proves the
  session serves another turn afterward.
- Resume is `StartOptions.Resume` (a previous `Session.Ref`) — the same spawn
  path as starting, because that is how real harnesses work.
- Rate limits surface as `*provider.RateLimitError` (inform-and-wait, §11.9);
  unsupported behaviors as `provider.ErrUnsupported`.
