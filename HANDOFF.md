# tsk_b14ca4bec15ae43e — Fast Model via the user's default provider

## What was built

- **`providers/provider/provider.go`** — `Completer` interface (`Complete(ctx, CompleteRequest) (CompleteResult, error)`), optional and detected via type assertion (like `RefSetter`/`SessionLocator`), not part of the `Matrix`. `CompleteRequest` carries `System`, `Prompt`, `MaxOutputTokens`, `JSONSchemaHint`, `Model`. Explicitly documents that JSON validation/repair never belongs per-provider.
- **`providers/fake/fake.go`** — `Complete` implementing `Completer`, for unit tests of the fastmodel layer and UI fixtures.
- **`providers/kirocli`** — deliberately does *not* implement `Completer`; `kirocli_test.go` asserts this via type assertion, covering the dormant path.
- **`providers/claudecode/complete.go`** — hermetic `Complete`: scratch `os.MkdirTemp` cwd, `--safe-mode` (rejected `--bare` — it forces API-key-only auth, breaking OAuth/keychain users), `--setting-sources ""` + a per-call `--settings` file, `--strict-mcp-config --mcp-config '{"mcpServers":{}}'`, `--tools ""`, explicit `--model` (`DefaultCompleteModel = "claude-haiku-4-5-20251001"`, overridable). Every flag verified against the installed CLI first; findings recorded in `providers/claudecode/testdata/complete_flags.md`.
- **`providers/claudecode/complete_test.go`** — `TestRealClaudeCompleteHermeticity` (opt-in, `MADS_CLAUDE_SMOKE=1`): plants a `CLAUDE.md`, a `SessionStart` hook, and a decoy `.mcp.json` in the surrounding directory; asserts none leak into the reply and `usage.input_tokens` stays within a documented overhead budget above the constructed prompt+system length.
- **`providers/claudecode/README.md`** — new "Complete: hermetic one-shot completion (Fast Model)" section documenting the verified flags, why validation/repair live in `fastmodel` only, the hermeticity test, and the latency note (spawn cost ~1-2s is fine because every Fast Model job is debounced/background; interactive consumers should use the endpoint backend).
- **`providers/fastmodel/dynamic.go`** — `DynamicConfig` resolution order: explicit `BaseURL` wins if set; else fall back to `Provider` if it implements `Completer`; else unconfigured. No silent cross-provider fallback. JSON validation + one bounded repair-retry live here (pre-existing, unchanged by this task; now exercised against both backends).
- **`pkg/contracts/config.go` / `session.go`** — `FastModelMode` (`"provider" | "endpoint" | "off"`), `Config.FastModelMode`, `Config.FastModelEffectiveMode()` (unset resolves to `endpoint` if a base URL was already configured, else the new zero-config `provider` default — preserves existing installs' behavior).
- **`services/router/main.go`** — `fastModelDynamicConfig(cfg, flagBaseURL, provider)` resolves the three-way mode into a `fastmodel.DynamicConfig`, flipping the default from dormant to a working provider-backed Fast Model for unconfigured installs.
- **`services/router/internal/api/config.go`** — `GET /fast-model/status`: returns effective mode, resolved provider name, and whether it implements `Completer` — the signal the UI needs for the no-Completer status line. Route registered in `api.go` and `route_manifest_test.go`.
- **UI (`ui/src/`)** — `FastModelMode`/`FastModelStatus` types; `RouterClient.getFastModelStatus()` in both `client.ts` and the `fixtures.ts` fake; `useFastModelStatus.ts` fetch-on-mount hook; `PipelineSection.tsx` three-way "Backend" selector (`FAST_MODEL_MODES`), `effectiveFastModelMode()`, `fastModelStatusText()` with the exact required no-Completer copy, and endpoint-only fields conditionally rendered.

## Test coverage (all passing)

- `providers/provider` — `Completer` shape covered indirectly via `fake`/`kirocli`/`claudecode` implementers.
- `providers/fake/fake_test.go` — `Complete` returns expected text/usage.
- `providers/kirocli/kirocli_test.go` — asserts kirocli does **not** implement `provider.Completer`.
- `providers/claudecode/complete_test.go` — flag construction/behavior unit tests, plus the opt-in real-CLI hermeticity smoke test (`MADS_CLAUDE_SMOKE=1`).
- `providers/fastmodel/dynamic_test.go` — resolution order: explicit endpoint wins over a `Completer`-implementing provider; provider fallback when no endpoint; unconfigured when the provider doesn't implement `Completer`; no cross-provider fallback.
- `pkg/contracts/config_test.go` — `FastModelEffectiveMode()` for unset/endpoint-configured/explicit cases.
- `services/router/main_test.go` — `fastModelDynamicConfig` resolution order table, including the flag-only-endpoint legacy path.
- `services/router/internal/api/config_test.go` — `GET /fast-model/status` for provider+Completer, provider+no-Completer, endpoint mode, off mode.
- `ui/src/features/settings/sections/PipelineSection.test.tsx` — 11 tests: default provider mode + active status, exact no-Completer status line, endpoint dormant/active, backend switch to Off, field visibility, credential picker.

`make check` (golangci-lint + `go test -race ./...` + UI lint + UI vitest, 211 files/1211 tests) passes cleanly.

## What was NOT done — explicitly out of scope

- **`make ui-smoke` is not green in this sandbox**, but the failure is pre-existing and unrelated to this task. All 12 e2e failures trace to `cmd/mads/run.go:152`'s `"run finished incomplete"` error, triggered by the fake-provider pipeline seed setup (`ui/e2e/lib/seed.ts`) producing blocked/skipped tasks. Verified via `git stash -u` back to the pre-change baseline (`6da2882`), rebuilding the UI, and re-running the same Playwright specs directly: the identical failure reproduces with zero Fast Model changes applied. This is an environmental/fake-pipeline issue in the sandbox, not a regression from this work. Restoration of the stash was confirmed complete (`git status --short` diff count matched pre-stash).
- No changes to the OpenAI-compatible endpoint backend's retry/validation logic — that was already correct from a prior task and is exercised, not modified, here.

## Reviewer should scrutinize

- The no-Completer status line copy in `PipelineSection.tsx`'s `fastModelStatusText` — verify it matches the acceptance criteria's exact wording ("Your provider doesn't support background completions — configure an endpoint or switch provider.") verbatim, since the test asserts on that exact string.
- `providers/claudecode/testdata/complete_flags.md` — the token overhead budget window used by `TestRealClaudeCompleteHermeticity`; this is an empirical figure and may drift if the CLI's own overhead changes across versions.
- `--safe-mode` vs. the originally-suggested tool-restriction flags: `--safe-mode` was chosen after `--bare` was rejected for breaking OAuth/keychain auth. Worth double-checking `--safe-mode` still disables CLAUDE.md/hooks/MCP/skills/plugins/agents on the currently-installed CLI version, per the verification note in `complete.go`/`testdata/complete_flags.md`.
- `services/router/internal/api/config.go`'s `handleFastModelStatus` and `main.go`'s `fastModelDynamicConfig` both re-implement the same legacy flag-only-endpoint fallback (`cfg.FastModelBaseURL != "" || flagBaseURL != ""` → endpoint mode) in two places; consider whether this duplication is acceptable or should be consolidated in a follow-up.
- The pre-existing `make ui-smoke` failure noted above — please confirm independently if possible, since it's the one acceptance-criteria item not demonstrably green in this environment.
