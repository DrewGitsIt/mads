# HANDOFF: tsk_f8fa87739259a10b — Portable permission intents

## What was done

1. **`pkg/contracts/permission.go`** — the v1 intent vocabulary
   (`IntentDenyCommands`, `IntentPathScope`, `IntentReadOnly`,
   `IntentRequireApproval`), `IntentSet` with `Has`/`Union`,
   `SessionTrust.Intents()`, `AgentPermissions.Intents()`, and
   `EffectiveIntents(trust, perms)` — the union of both compiled sets, which
   is the "most-restrictive intersection" the brief asked for (each side only
   ever *adds* a restriction, never lifts one, so intersecting what each
   side permits is exactly unioning what each side restricts).
   `permission_test.go` covers all three.

2. **`providers/provider/intent.go`** — the mirrored `Intent`/`Intents`/
   `IntentMatrix` types, following the zero-cross-import convention `Trust`
   already establishes. `IntentMatrix` reuses the existing `Support`
   (Native/Emulated/Unsupported) vocabulary rather than inventing a
   parallel one — "unenforced" in the brief maps directly to `Unsupported`.
   Declaring the capability is an **optional** `IntentEnforcer` interface
   (mirroring `ModelDiscoverer`), not a new required `Provider` method, so
   `providers/fake`, `providers/conformance`'s stub, and
   `providers/provider`'s registry test all needed zero changes.

3. **`claudecode.Provider.IntentMatrix()`** — deny_commands/read_only/
   require_approval native, path_scope unsupported. `intent_test.go`
   asserts each native row against real argument construction:
   `trustArgs()` for the disallowed-tools rows, `needsPermissionHook`/
   `permissionArgs()` for the hook row.

4. **`kirocli.Provider.IntentMatrix()`** — all four unsupported.
   `intent_test.go` asserts the honest-unenforced declaration and documents
   that kiro-cli never wires `StartOptions.PermissionPromptURL` anywhere
   (confirmed by grep — zero matches in `providers/kirocli/`).

5. **Router API** — `GET /providers`' `providerInfo` gained an
   `Intents provider.IntentMatrix` field (`omitempty`, populated via a type
   assertion against `IntentEnforcer`, same pattern as
   `handleListProviderModels`'s `ModelDiscoverer` check). No new route; no
   `route_manifest_test.go` change needed since the route already exists.
   `reads_test.go` gained two tests: a scripted provider proving the field
   round-trips, and a test proving it's omitted/empty for a provider (the
   `fake` one) that doesn't implement `IntentEnforcer`.

`make check` (lint + `go test -race ./...` + UI lint/tests) is green.

## What was deliberately not done

- **No kiro-cli enforcement mechanism was added.** The brief is explicit
  about this ("do not try to add kirocli enforcement in this task") — all
  four intents are unsupported for it, full stop.
- **No UI work.** The brief says a follow-up task adds the UX; this task
  ends at an accurate, queryable model with providers wired.
- **No changes to pipeline behavior.** `--dangerously-skip-permissions`
  in `cmd/mads/run.go` is untouched.
- **No new route in `route_manifest_test.go`.** The intent matrix rides
  the existing `GET /providers` response rather than a new endpoint — kept
  the change minimal since the brief only asks for the data to be queryable,
  not for a dedicated route.
- **No functional (live-process) conformance probes added to
  `providers/conformance`.** That shared suite's own `mcpServers()` comment
  establishes the precedent that argument-construction-specific
  verification for a declared-working behavior belongs in each adapter's
  own test, not the shared suite — I followed that same pattern for intents
  (`providers/claudecode/intent_test.go`, `providers/kirocli/intent_test.go`)
  rather than extending `conformance.Check`.

## What a reviewer should scrutinize

- **The Behavior-matrix vs. Intent-matrix inconsistency for kiro-cli is
  intentional, not a bug.** `Matrix()` already declares
  `BehaviorTrustLimited`/`BehaviorTrustReadOnly` as `Emulated` (real
  ACP kind-based auto-deny + human PTY confirmation — see `session.go`).
  The new `IntentMatrix()` declares all four intents `Unsupported`. Both are
  true simultaneously: kiro-cli's *existing* trust enforcement is a coarser,
  kind-based mechanism that doesn't map cleanly onto any single v1 intent
  (it's not a command blocklist, not a path scope, and specifically not
  `require_approval` since no `PermissionPromptURL` hook exists) — so rather
  than force an inaccurate mapping onto the new finer-grained vocabulary, I
  declared honestly per the brief's instruction ("declare unenforced where
  true and stop implying otherwise") and left the older Behavior-matrix rows
  as they were. A future task could reconcile these two matrices more
  tightly if the product wants one coherent story.
- **The task brief's stated kiro-cli bug (`--trust-all-tools` passed
  unconditionally) was already fixed before this task started**, by prior
  commit `0fe3de7` (`pty.go` already gates the flag to `TrustFull` only). I
  did not re-verify or re-fix it; I relied on reading current source.
- **`EffectiveIntents` is a union, not a literal set intersection.** The
  brief's wording ("most-restrictive intersection") is reconciled in the doc
  comment on `EffectiveIntents` — worth a second look if the product's
  mental model differs from mine.
- **`IntentMatrix` intentionally reuses `Support`** (Native/Emulated/
  Unsupported) rather than defining a separate three-value enum matching the
  brief's literal "native | emulated | unenforced" wording. I judged this
  more consistent with the existing codebase pattern than introducing a
  parallel vocabulary; flagging in case the product wants the exact spelling
  "unenforced" to appear in the API/UI rather than "unsupported".
