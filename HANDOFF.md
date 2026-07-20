# HANDOFF: Profiles as full-config snapshots

## Spec gap
`docs/mockups/profiles-ux-v2.html`, cited by the architect handoff as the
approved design for this whole batch, does not exist anywhere in the repo or
its git history (checked all branches/commits). I proceeded from the written
acceptance criteria alone, which fully specify the backend/contract behavior.
Flag this to the architect — later tasks in the batch may hit the same gap.

## What was done
- `pkg/contracts/profile.go`: replaced `ProfileSettings`/`Profile.Settings`
  (fixed 9-field overlay) with `ProfileSnapshot` (`map[string]json.RawMessage`,
  keyed by `Config`'s own JSON tags) and `Profile.Snapshot`. Added
  `SnapshotConfig` (capture), `ApplySnapshot` (activation merge), and
  `ProfileDrift` (live-vs-snapshot diff) as the one shared implementation all
  three operations build on — a `Config` field added later is simply absent
  from old snapshots, no shape migration needed.
- `pkg/contracts/config.go`: added `ActiveProfileID` and
  `CredentialBindings []string` (names only) to `Config`, plus
  `ProfileExcludedFields` (`onboarding_complete`, `update_feed_url`,
  `active_profile_id`) and `RestartRequiredFields` (`concurrency_limit`,
  `log_retention_days`, `synthesis_debounce_ms`) as documented contract sets.
- `services/data/internal/store/profiles.go`: `ActivateProfile` now calls
  `ApplySnapshot` and explicitly sets `ActiveProfileID` before persisting;
  added `CaptureConfigSnapshot` (capture-path store function). Removed the old
  `mergeSettings`.
- `services/data/internal/api/config.go`: added `POST /profiles/capture`
  (Data-service only — returns a snapshot of the current Config).
- Migration `0016_profile_snapshot`: renames the `settings` JSON key to
  `snapshot` on existing profile rows (field names were already identical to
  `Config`'s own tags, so this is a pure key rename, no value transform).
  `TestMaxVersion` bumped 15→16.
- Adding `CredentialBindings []string` made `Config` non-comparable via
  `==`; switched 5 affected test assertions to `reflect.DeepEqual`
  (`scripts/smoketest/install_test.go`, `services/data/internal/store/{config,profiles}_test.go`,
  `services/data/internal/api/config_test.go`, `services/router/internal/api/config_test.go`).
- New tests cover every acceptance criterion: forward-compat (deleting a key
  from a stored snapshot to simulate a field introduced afterward),
  activation setting `active_profile_id`/restoring theme+palette+
  credential_bindings/leaving onboarding_complete+update_feed_url untouched,
  event emission, a DB/event-payload grep for a sentinel secret value that
  was never provided, `ProfileDrift` correctness, and the 0016 migration
  round-trip on a legacy-shaped row.
- Minimal UI updates to keep TypeScript consistent with the new shape:
  `ui/src/router/types.ts` (`Profile.snapshot`, `Config.active_profile_id`/
  `credential_bindings`), and mechanical `settings:` → `snapshot:` renames in
  `ProfilesPanel.tsx`, `useProfiles.test.ts`, `fixtures.ts`.

## What was deliberately NOT done
- No Router/`dataclient` method for the new `POST /profiles/capture`
  endpoint, and no UI wiring for capture/drift/restart-badge — no acceptance
  criterion exercises this, and the task description scopes this work to
  "Data/contract work." The endpoint exists in the Data service only.
- `ui/src/features/settings/restartRequired.ts` (UI's own restart-required
  field list, presentation-only per its existing comment) was left
  untouched rather than reconciled with the new `contracts.RestartRequiredFields`
  — that reconciliation belongs with the UI-focused batch task this backend
  work is presumably feeding.
- No validation added for `CredentialBindings` or `ActiveProfileID` in
  `ValidateConfig` — nothing in the acceptance criteria calls for it, and an
  arbitrary profile ID/name string can't be checked without a DB round-trip
  that would change `ValidateConfig`'s pure-function shape.

## What a reviewer should scrutinize
- The credential-bindings-are-names-only test
  (`TestCredentialBindingsAreNamesOnlyInDBAndEvents`) is a regression guard,
  not a proof: it confirms a sentinel value that was never given to the Data
  service doesn't appear in its DB/events. That's the strongest test
  possible from inside the Data service, since the actual boundary
  enforcement (never handing secret values to this service) lives in
  Router/vault, outside this task's scope.
- `ApplySnapshot`/`SnapshotConfig`/`ProfileDrift` decompose `Config` via
  marshal-to-map, which means any future `Config` field needs no snapshot
  code changes — but also means a JSON tag rename on `Config` silently
  changes snapshot key names for profiles saved under the old tag. Not a new
  risk introduced by this task, but worth knowing.
- Migration 0016 assumes every existing profile row has non-null `settings`
  (even if `{}`); `json_extract` on a genuinely absent key returns NULL,
  which `json_set` would then write as `"snapshot":null` rather than `{}`.
  Given every prior `Profile` was created via `NewProfiles()`'s registry with
  `Settings` present as a struct (never omitted), this should be moot, but
  worth a second look if any hand-inserted or pre-registry rows exist in a
  real install's DB.

## Verification
`go build ./...`, `go vet ./...`, `go test -race ./...`, `golangci-lint run
./...`, `npm run lint`, and `npm run test -- --run` are all green.
