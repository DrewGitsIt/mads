# Trust enforcement: session trust must map to real provider restrictions — executor notes

Implemented the architect's design as written, all 8 sections. `make check`
and `make ui-smoke` are green.

## What changed

- `providers/provider`: `Trust` type (`full`/`limited`/`read_only`, zero
  value == `full`) on `StartOptions`; two new `LevelExpected` Matrix rows,
  `BehaviorTrustLimited`/`BehaviorTrustReadOnly`.
- `providers/fake`: `Native` on both rows. `toolAllowed` gates
  `ToolStep.Command`/file-write steps per trust level, emitting `EventError`
  + `EventTurnEnd` on refusal (turn ends cleanly, session stays open).
- `providers/claudecode`: `Native` on both rows via `--disallowedTools`
  (`Bash` for limited; `Bash,Write,Edit,NotebookEdit` for read_only), same
  flag on `startStream` and `startPTY` — no transport asymmetry. New
  `providers/claudecode/README.md`.
- `providers/kirocli`: `Emulated` on both rows. ACP transport gates
  `session/request_permission` on the tool call's `Kind`
  (limited allows read/edit/search/think/fetch, denies
  execute/delete/move/other; read_only allows only read/search/think/fetch).
  PTY transport only passes `--trust-all-tools` for full trust; otherwise
  omits it and defers to kiro-cli's own interactive confirmation prompt —
  documented as a materially weaker, human-dependent mechanism vs. ACP's
  automated gate. `OneShot`'s hardcoded `--trust-all-tools` removed.
- `providers/conformance`: `Script.AttemptWrite`/`AttemptCommand` fields,
  `checker.trust()` probe, wired into `Check()`. Unsupported rows are not
  probed for a negative (per design).
- `services/session/internal/runner/runner.go`: one-line wiring,
  `Trust: provider.Trust(sess.Trust)` in `Start()`'s `StartOptions` literal
  — `baseOpts` capture means it survives `SwitchTransport` for free.
- UI: `trustLevels.ts` is now a function of the selected provider's matrix
  (`Record<ProviderBehavior, ProviderSupport>`), deriving copy from
  native/emulated/unsupported instead of a hardcoded "not enforced yet"
  disclaimer. `TrustPill` takes an optional `matrix` prop. `Launchpad.tsx`
  looks up the matrix for `staged.provider ?? resolved?.provider?.value`
  via the existing `useProviderMatrix` hook. `ProvidersPanel.tsx` needed no
  changes — it already renders whatever behavior rows the Router returns.
- `cmd/mads/run.go` (pipeline one-shots): untouched, confirmed via grep that
  it only calls `OneShot()`, never `Start()`, and uses its own separate
  `provider.Registry`/`Config.ExtraArgs` — the interactive-only OneShot
  hardcode removal doesn't touch pipeline behavior.

## What I did not do

- Did not add a PTY-transport conformance probe for trust (design explicitly
  leaves this to README caveats — scraping terminal output for a refusal
  was judged too fragile, matching the architect's own lean).
- Did not touch `providerInfo`/`ProviderInfo` JSON shape to carry per-row
  caveat text — caveats stay in provider READMEs only, per the design.

## Reviewer scrutiny points (highest risk first)

1. **kirocli's `denyPermission` `OptionID: "reject"` is an unverified guess**
   (`providers/kirocli/session.go`), by symmetry with the pre-existing
   `autoApprove`'s `"allow"`. Neither value has ever been exercised against
   a real kiro-cli binary — `session/request_permission` was never sent by
   the fake stand-in before this change. If kiro-cli's real reject shape
   differs (a `"cancelled"` outcome, a different `optionId`), the whole
   `limited`/`read_only` ACP enforcement silently degrades to always-allow.
   Flagged in both the code comment and `providers/kirocli/README.md`;
   `MADS_KIRO_SMOKE=1` against a real binary is the only way to confirm.
2. **claude-code's `--disallowedTools` tool names** (`Bash`, `Write`,
   `Edit`, `NotebookEdit`) were confirmed against a live `claude --help` in
   this sandbox (`Bash`/`Edit` appear in the help text directly;
   `Write`/`NotebookEdit` inferred from Claude Code's own known tool set,
   not independently confirmed in the help output). Declared `Native` on
   that basis — worth a second look if this environment's `claude --help`
   output isn't representative of the target deployment's CLI version.
3. All new tests use scripted stand-ins only (`providers/fake`,
   `MADS_FAKE_CLAUDE=1`, `MADS_FAKE_KIRO=1`) — no live LLM calls anywhere in
   `make check`.

## Test coverage added

- `providers/fake/fake_test.go`: read_only refuses write, limited refuses
  command but allows write, full (and zero-value) allows everything.
- `providers/claudecode/claudecode_test.go`: `TestTrustDisallowsTools`
  (limited/read_only both refuse their respective disallowed tools).
- `providers/kirocli/kirocli_test.go`: `TestTrustDeniesToolKind`,
  `TestTrustLimitedAllowsEdit`.
- `providers/conformance`: trust probes wired into the shared suite, run by
  all three providers' existing `TestConformanceAgainstScriptedBinary`.
- `services/session/internal/runner/runner_test.go`:
  `TestTrustReachesProviderLaunch` — session-child-level proof that
  `sess.Trust` reaches the fake provider's launch and a read_only session's
  attempted write refuses end-to-end.
- UI: `TrustPill.test.tsx` covers no-matrix, native, and unsupported
  copy. Full `npm run test` (771 tests) and `make ui-smoke` (12 Playwright
  journeys) both green.

STAGE: pass
