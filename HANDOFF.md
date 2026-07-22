# HANDOFF: tsk_caddcfa377e7be14 — Land gate runs make check, bounded mechanical lint-autofix

## What I did

This is a re-review fix-up. The prior attempt (e9ab5db) shipped the land
gate's `classifyLintFailure`/mechanical-autofix engine mechanism and its
tests, but `code-reviewer` correctly caught that nothing upstream actually
*invoked* `make check` in production: the seeded `tpl_system_default`
(`services/data/internal/store/migrations/0006_templates.up.sql`) still ran
`command: make build && make test` for its `build` stage and
`command: make test` for `verify`. Since that row is the fallback every
workspace binds to, the 672a6bc lint-red-trunk scenario this whole task
exists to close was still reachable in practice.

**Fix**: added migration `0017_default_template_make_check` that rewrites
`tpl_system_default`'s embedded YAML source in place via `REPLACE()` on the
JSON `data` column — `command: make build && make test` →
`command: make build && make check`, `command: make test` (verify) →
`command: make check`. `.down.sql` reverses both substitutions in the
mirrored order so `TestProductionMigrations`' up→down→up round trip stays
clean. Bumped `TestMaxVersion`'s literal 16→17 (that test has a comment
telling you to do this) and added
`TestMigrationDefaultTemplateUsesMakeCheck`, which migrates a fresh DB,
unmarshals the seeded row as `contracts.Template`, and asserts the build
stage no longer contains the old `make test` invocation and both stages
now reference `make check`.

Fixing the seed data broke three test fixtures that fake out a workspace's
`Makefile` for the default template's command stages (`build`/`test`
targets only, no `check`): `services/pipeline/engine/engine_test.go`'s
`newHarness`, `cmd/mads/main_test.go`'s dogfood-rehearsal end-to-end test,
and `scripts/e2esmoke/main_test.go`'s `setupWorkspace`. Added a trivial
`check:\n\t@true\n` target to each so the default template's build stage
still succeeds against them — no behavioral change to what those tests are
actually exercising.

## Verification

- `go build ./...` — clean.
- `go test ./services/pipeline/... ./services/data/... ./pkg/... ./services/router/...` — clean.
- `make check` — full target, green (Go build/lint/test + UI lint/test).

## What I did NOT do / scrutinize

- I did not touch the engine mechanism itself (`classifyLintFailure`,
  `lintautofix.go`, the stage gate wiring) — that was reviewed and accepted
  in the prior pass; this fix-up is scoped to the migration gap called out
  in the review.
- I did not add a migration-level test asserting the *build* stage's exact
  command string is `make build && make check` via YAML/template parsing
  (only via substring `strings.Contains` on the raw source) — a
  `pkg/template.Check` round-trip would be a stronger assertion but felt
  like scope creep for a one-line seed-data fix; worth a second look if the
  reviewer wants tighter coverage here.
- Did not re-verify the integration test criteria from the original task
  spec (real-worktree autofix test, errcheck-class no-autofix test, bounded
  single-attempt test) beyond confirming `go test ./services/pipeline/engine/...`
  is green — those were already covered by `lintautofix_test.go` from the
  prior attempt and are unchanged here.
