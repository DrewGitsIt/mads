# providers/

Provider adapters land here in wave 2 (docs/MADS-WORK-ITEMS.md W2.1–W2.3, W7.1):

- `pkg/` style shared abstraction: the provider interface, capability-matrix strategy
  registry, and the conformance-suite harness (spec §11, §15)
- `fake/` — **provider-fake**, the deterministic scripted test double every downstream
  suite runs against
- `claudecode/` — the reference adapter
- `kirocli/` — the second adapter that keeps the abstraction honest

Nothing here yet by design: wave 1 (Data Service) has no provider dependency.
