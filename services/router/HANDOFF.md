# HANDOFF: Providers — model discovery must degrade, not 500

## What was done

Implemented per the architect's plan in this file (now superseded by this
executor summary) with no deviations:

1. **`providers/provider/provider.go`** — added `ErrDiscoveryUnavailable`
   sentinel next to `ErrUnsupported`.
2. **`providers/kirocli/kirocli.go`** — `Models`' `cmd.Run()` failure branch
   now wraps both the underlying error and `provider.ErrDiscoveryUnavailable`
   via a second `%w` verb. The JSON-unmarshal branch below it is untouched —
   still a plain error, still a 500.
3. **`pkg/contracts/model.go`** (new) — `Model` (duplicated, not imported,
   matching this package's existing cross-boundary-type convention) and
   `ModelsResponse{Models []Model, Detail string}`, the new wire shape for
   `GET /providers/{name}/models`.
4. **`services/router/internal/api/reads.go`** — `handleListProviderModels`
   now does one `errors.Is(err, provider.ErrDiscoveryUnavailable)` check: on
   match, 200 with `{models: [], detail: err.Error()}`; on any other error,
   unchanged `writeError` (500). The non-`ModelDiscoverer` branch and the
   success branch both now return the `ModelsResponse` envelope instead of a
   bare array.
5. **`ui/src/router/client.ts`** — `listModels` unwraps the new
   `{models, detail?}` envelope; its public `Model[]`-returning signature is
   unchanged, so no other UI file needed touching.
6. **`services/router/internal/api/reads_test.go`** — `TestListProviderModels`
   updated to decode `contracts.ModelsResponse`. Added `flakyModels` (wraps
   `*fake.Fake`, overrides `Name`/`Models` to script a failure) and
   `newTestServerWithProvider` (registers one specific provider instead of
   the standard fake). Two new tests: `TestListProviderModelsDiscoveryUnavailable`
   (sentinel-wrapped error → 200, empty `Models`, non-empty `Detail`) and
   `TestListProviderModelsInternalError` (plain `errors.New` → 500) — the
   latter is what actually proves the classification discriminates, not just
   that everything went 200.
7. **UI test fixtures** — `useProviderModels.test.ts`, `ModelField.test.tsx`,
   `ModelPill.test.tsx` mocked `GET /providers/{name}/models` as a bare JSON
   array; updated all three to the `{models: [...]}` envelope (`make check`
   caught these — they broke on the client-side unwrap change, unrelated to
   the degrade-vs-500 logic itself).

## Verification

- `go build ./...` — clean.
- `go test ./services/router/... ./providers/... ./pkg/contracts/...` — all
  pass, including the three model-discovery tests run individually with
  `-v` to confirm they execute (not skipped) and assert the right thing.
- Ad hoc scratch test (written, run, then deleted — not committed):
  `kirocli.New(Config{Bin: "definitely-not-a-real-binary-xyz"}).Models(ctx)`
  against a real (non-fake) `*kirocli.Provider` confirms `errors.Is(err,
  provider.ErrDiscoveryUnavailable)` is true for the actual missing-binary
  path, not just the test-local `flakyModels` stub.
- `make check` (root) — golangci-lint, `go test -race ./...`, `npm run
  lint`, `npm run test` (174 files / 711 tests) — exit 0, no failures.
- kiro-cli is not installed in this environment (`which kiro-cli` → not
  found), so the acceptance criterion "no 500s in the router log when
  clicking through the UI with kiro absent" was verified via the handler
  test hitting the real classification logic (`errors.Is` against the
  sentinel `kirocli.Models` actually wraps) rather than a live UI
  click-through — see risk note below.

## Things the reviewer should scrutinize

- **No live UI click-through was performed.** kiro-cli isn't installed
  here, so I could not start the full Electron/router stack and click to a
  kiro-cli model pill to observe the network tab directly. Confidence rests
  on: (a) the handler test proving the 200/500 split with a scripted
  provider, and (b) a standalone scratch test proving the *real*
  `kirocli.Provider.Models` wraps the sentinel for a missing binary exactly
  as the handler expects. If a reviewer has kiro-cli-absent conditions
  easily reproducible with the real stack, a manual click-through would
  close this gap.
- **`contracts.ModelsResponse` is a breaking wire-shape change** (bare
  `[]Model` → `{models, detail}` object). Grepped every `*.ts`/`*.tsx` for
  `/models` — `ui/src/router/client.ts`'s `listModels` is the only
  production caller, and it's updated. Worth a final independent grep
  before merging in case something new landed on `main` since this branch
  started.
- **`detail` is not surfaced in the UI.** Per the architect's plan, this was
  explicitly out of scope — `ModelPill`'s existing empty-list rendering
  (group header, no rows) already satisfies "no models" without erroring.
  Flagged, not built.
- **The sentinel wrap in `kirocli.go` is scoped to exactly one branch**
  (`cmd.Run()` failure). Confirmed the JSON-unmarshal branch below it is
  untouched — a kiro-cli binary that runs but emits malformed output still
  produces a plain (non-sentinel) error and a 500, not a silent "no models"
  degrade.
