# HANDOFF: Session defaults — empty provider must inherit, never crash the child

## What shipped

Implemented pieces 1 and 2 of the architect's plan (this task's HANDOFF is preserved in
git history at `f084ed8..HEAD~1` — see `git log -p` for the original design doc if you
need the full rationale; it's reproduced in condensed form below since the file itself
is being overwritten).

- **`services/router/internal/api/sessiondefaults.go` (new)**: `resolveSessionDefaults`
  fills empty `Provider`/`Agent`/`Isolation`/`Trust`/`Model` on a `contracts.Session` from,
  in order: the session itself, the named workspace's `Defaults` (workspace resolved via
  `sess.Workspace`, falling back to `cfg.DefaultWorkspace`), then global `Config`. Workspace
  lookup is best-effort: a stale `DefaultWorkspace` or a `sess.Workspace` that no longer
  resolves degrades to "no workspace defaults", not a failed create (matches
  `dataclient.ErrNotFound` specifically; any other error still propagates). Model is
  resolved last, from the *already-resolved* provider's `ModelDiscoverer.Models` (the
  `Default: true` entry) — `cfg.DefaultModelTier` is not consulted, documented as a
  simplification inline and in the README.
- **`sessions.go`/`retros.go`**: both `handleCreateSession` and
  `handleRetroFollowUpSession` now call `resolveSessionDefaults`, then
  `s.providers.Get(sess.Provider)` as a pre-spawn validation gate — an unknown/empty
  provider returns 400 (`writeErrMsg`, reusing `Registry.Get`'s own `unknown provider %q`
  message verbatim) **before** `s.data.CreateSession` runs, so a rejected create never
  gets a DB row or a spawn attempt.
- **`services/router/README.md`**: documented the new pre-spawn resolution/validation
  step under `POST /sessions`, and added four "Critical paths" entries: the model-tier
  simplification, provider-before-model resolution order, why the pre-check is
  trustworthy (same registry instance the child uses), and the known bounce-path gap
  (see "Not done" below).

## Tests

`services/router/internal/api/sessions_test.go` (new):
- `TestCreateSessionResolvesConfigDefaults` — all-empty create, `Config.DefaultProvider`
  set to `"fake"` first; asserts resolved `provider`/`model`/`isolation`/`trust` on the
  response *and* independently via `GET /sessions` (proving persistence, not just echo).
- `TestCreateSessionWorkspaceDefaultsOverrideConfig` — a workspace with
  `Defaults: {Model: "fake-smart", Isolation: "current_dir"}` overrides Config; a second
  create with an explicit `model` on top of the same workspace proves session-level
  still wins over workspace-level.
- `TestCreateSessionUnknownProviderNoOrphan` — unknown provider → 400 naming it; asserts
  the session list is unchanged before/after (no orphaned record). The no-orphaned-*child*
  half of this is covered for free by `TestMain`'s `testleakcheck.Verify`, which already
  fails the package if any spawned `testchild` survives — nothing new needed there.

`config_test.go` gained `setDefaultProvider(t, baseURL, provider)`, a small GET/mutate/PUT
helper — reused by the two new tests above and by `retros_test.go`'s
`TestRetroFollowUpSession`/`TestRetroFollowUpSessionExcludesRejectedDPAs`, which now set
`DefaultProvider: "fake"` before hitting the follow-up-session endpoint (they previously
relied on `testChildBin` ignoring session data entirely to get a 201 with no provider set
at all — that stopped working once the pre-check landed).

**Existing-test fallout, fixed**: every `POST /sessions` call in this package that omitted
`provider` and asserted `201` needed `"provider": "fake"` added, since a fresh `datatest`
Config has no `DefaultProvider` seeded. Touched: `TestCreateTerminateSession`,
`TestBounceSession` (`api_test.go`), `TestListSessions` (`reads_test.go`),
`TestSessionOutputForwarding` (`stream_forward_test.go`). Did **not** touch
`newTestServer`'s shared Config setup to compensate — `TestConfigRoutesProxied`
(`config_test.go`) asserts a fresh config equals `contracts.DefaultConfig()` exactly, and
each test gets its own `datatest` instance, so mutating one test's config via
`setDefaultProvider` doesn't leak into others.

## Not done — piece 3 (optional, per the architect's own handoff)

The architect's HANDOFF called piece 3 "genuinely optional/best-effort" and explicitly
said pieces 1+2 alone satisfy every acceptance criterion. I left it out:

- No `provider.ErrUnknownProvider` sentinel export in `providers/provider/registry.go`.
- No last-stderr-line capture in `internal/child/child.go`.
- `handleBounceSession` still re-spawns an existing session record without re-running
  resolution or the provider pre-check, and a spawn failure there is still a flat 502 —
  documented as a known gap in the README's "Critical paths" rather than fixed.

Reasoning: this task's acceptance criteria (all-empty create resolves; unknown provider
on *create* is 4xx; `make check` green) don't touch the bounce path, and threading a
sentinel through `errors.Is`-compatible wrapping across `child.Spawn` →
`Supervisor.SpawnSession`/`BounceSession` → the two handlers is real, multi-layer plumbing
for a corner case (a provider that was valid at session-create time going bad before a
later bounce). If this gap starts mattering in practice, the original HANDOFF (recoverable
via `git log -p -- services/router/HANDOFF.md` on this branch before this overwrite, or
ask the architect stage to re-derive it) lays out the exact approach.

## Verification

- `go build ./...` clean.
- `go test ./services/router/...` — all green, including the 3 new tests and the 4 fixed
  ones.
- `make check` — clean exit (golangci-lint, `go test -race ./...`, `npm run lint`,
  `npm run test`), no failures, no new warnings.

## Reviewer should scrutinize

- The `errors.Is(err, dataclient.ErrNotFound)` branch in `resolveSessionDefaults`'s
  workspace lookup — confirm `dataclient.Client.Workspace` really does wrap 404s in
  `ErrNotFound` (it does, via the shared `do` helper — `pkg/dataclient/dataclient.go`) and
  that no other error class gets silently swallowed there.
- Whether "no `Provider` field on `WorkspaceDefaults`" (so a workspace can't override
  provider, only Config can) matches the spec's actual intent for §10, or whether that's
  a gap worth a follow-up task — it was called explicitly out of scope in the original
  design and I left it that way.
