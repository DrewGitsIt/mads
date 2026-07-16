# HANDOFF: W7.4 — Credentials: env import & vault sources

## What shipped

Implemented per the architect's plan in this file's original content (now
superseded by this executor summary — the plan is preserved in this task's
conversation transcript and in the git history of this branch's commit).

1. **`pkg/vault/vault.go`** — extended `Source` with `Resolve(ctx, ref)
   (string, error)`. Doc comment restates the value-boundary rule: a
   credential value may only exist in this process's own environment, the
   local credential store's file on disk, and the argv/env of a child
   process it starts — never in an HTTP response body, event payload, log
   line, or another workspace's session env.
2. **`pkg/vault/onepassword.go`**, **`awssecrets.go`** (new) — real
   `Source` implementations shelling out to `op read <ref>` and `aws
   secretsmanager get-secret-value`. Both take a `Config{Bin, Env}` so
   tests can point at a scripted stand-in; `cmd.Env =
   append(os.Environ(), cfg.Env...)` so the real environment (PATH, etc.)
   is preserved in production and only extended in tests. `Healthy` and
   `Resolve` both preflight with `exec.LookPath` and return an actionable
   error (install URL) when the CLI is absent, rather than a bare "exec:
   not found".
3. **`pkg/vault/fakevault_test.go`** (new) — `TestMain`-based fake `op`/
   `aws` binaries (`MADS_FAKE_OP=1`/`MADS_FAKE_AWS=1`, data via
   `MADS_FAKE_VAULT_DATA`), mirroring the `providers/claudecode` re-exec
   pattern, so `onepassword_test.go`/`awssecrets_test.go` exercise real
   subprocess calls without either CLI installed.
4. **`pkg/vault/refstore.go`** (new) — `RefStore`, a `credstore.Store`-
   shaped JSON file (0600/0700 perms) storing `Ref{Source, Path}` by
   credential name. A ref is a locator, not a secret — safe to persist
   and to echo back over HTTP, unlike a resolved value.
5. **`pkg/vault/resolver.go`** (new) — `Resolver{Refs, Sources}.Resolve(ctx,
   name)` looks up the ref, dispatches to the matching `Source`, and
   returns `(value, ok, err)` with the same nil-safe /
   missing-degrades-gracefully contract as `credstore.ResolveEnv`.
6. **`services/session/internal/runner/capability.go`** — `setupSkill`/
   `setupMCP` now go through a new `resolveCredentialEnv` helper: try
   `CredStore` first, then fall back to `VaultResolver` only for names
   `CredStore` didn't have. A nil `VaultResolver` (the default) makes this
   byte-for-byte the old `CredStore.ResolveEnv` behavior. Scoping is
   unchanged — vault-resolved values are injected into the same
   per-capability env slice as credstore values, so they inherit the
   existing skill-env-to-agent-process / MCP-env-to-that-MCP-process-only
   isolation, including cross-workspace isolation (each workspace's
   session has its own capability set and its own resolved env; nothing
   is shared across sessions).
7. **`services/session/main.go`** — wires a `vault.Resolver` into
   `Runner.VaultResolver` from `<madsHome>/vault-refs.json` plus the two
   real sources, nil-safe (logs and disables vault resolution if the ref
   store can't be opened, doesn't crash the service).
8. **`services/router/internal/api/`** — `credentials.go`: replaced the
   suffix-heuristic env-candidate scan with a declared allowlist
   (`knownCredentialEnvVars` + `knownCredentialEnvVarPrefixes`); added
   `GET/POST/DELETE /credentials/vault-refs` (ref CRUD — echoes `path`
   back since it's a locator, never a value); `GET /credentials/vault-
   sources` now reports a real `Error` string per source. `api.go`'s
   `New()` and `main.go`'s `newMux()` both grew trailing `vaultRefs
   *vault.RefStore, vaultSources []vault.Source` params, wired from real
   `vault.NewOnePassword`/`vault.NewAWSSecretsManager` plus an
   `OpenRefStore` call in `main()` (nil-safe on open failure, same
   pattern as (7)).

## Tests

- `pkg/vault`: `TestOnePassword*` / `TestAWSSecretsManager*` (resolve
  against fake, unknown ref, healthy against fake, missing-CLI error
  contains install URL) — all pass. `TestRefStore*` (round trip, names).
  `TestResolver*` (resolves through source, unknown source errors, no ref
  configured degrades to `ok=false`, nil receiver is safe).
- `services/router/internal/api/credentials_test.go`:
  `TestCredentialEnvCandidatesUsesAllowlistNotHeuristic` (a known name is
  surfaced, an arbitrary env var is not, even though neither is stored
  yet — proves allowlist not heuristic).
  `TestVaultSourcesReportsRealUnhealthySourceWithActionableError` (a real
  `vault.Source` pointed at a nonexistent binary reports unhealthy with
  the 1Password install URL in the response body, exercised through the
  HTTP layer, not just the `pkg/vault` unit level).
  `TestVaultRefCRUDRoundTrip` (set/list/delete a ref; confirmed path is
  echoed, since it isn't a secret).
  Pre-existing `TestCredentialValueNeverCrossesTheWireBack` /
  `TestCredentialImportNeverEchoesValues` (from W6.10, unmodified) still
  pass and continue to cover the value-never-in-response-body invariant.
- `services/session/internal/runner/capability_test.go`:
  `TestSetupCapabilitiesResolvesVaultBackedCredential` (an agent
  declaring a capability whose credential only exists as a vault ref, not
  in credstore, gets it injected via the fake vault source).
  `TestSetupCapabilitiesCrossWorkspaceIsolation` — the work item's literal
  done-when: a credential scoped to workspace A's session never appears in
  workspace B's session env, asserted against the MCP child's actually
  dumped env file (`pkg/capability/testdata/envdump`), not just against
  the runner's in-memory env slice.

## Not done

Nothing in scope was skipped. Both vault backends (1Password, AWS Secrets
Manager) are implemented against the `Source` interface with real
subprocess calls (via the fake-binary harness); neither CLI is installed
on this machine, so they're covered by the fake harness plus a
missing-CLI-produces-actionable-error test, not by a run against a live
1Password/AWS account — the task anticipated this ("neither backend
available on this machine").

## Verification

- `gofmt -l .` — clean.
- `make check` (root: `golangci-lint run ./...`, `go test -race ./...`,
  `npm run lint`, `npm run test`) — clean exit. All Go packages `ok`,
  including `pkg/vault`, `services/router/internal/api`,
  `services/session/internal/runner`. 172 UI test files / 693 UI tests
  passed.

## Reviewer should scrutinize

- **`cmd.Env` construction** in `onepassword.go`/`awssecrets.go`: must be
  `append(os.Environ(), cfg.Env...)`, not `append(cmd.Env, cfg.Env...)`
  (`cmd.Env` starts nil, so the latter would silently drop the real
  environment in production and only "work" in tests because the fake
  binary is invoked by absolute path). Caught this during implementation;
  worth a second look since it's an easy regression to reintroduce.
- **`resolveCredentialEnv`'s vault fallback only fires for names
  `CredStore.ResolveEnv` reports missing** — a name present in both
  credstore and a vault ref always resolves from credstore. This wasn't
  specified either way by the plan; seemed like the least-surprising
  precedence (local store wins) but worth confirming against intent.
- **Vault resolve errors are logged, not surfaced to the capability's
  health/error state** (`resolveCredentialEnv` treats a vault `Resolve`
  error the same as "still missing" for the caller). This matches the
  existing missing-credential-degrades-gracefully contract, but means a
  transient vault outage looks identical to a genuinely absent ref from
  the capability's perspective — only the log line distinguishes them.
- **`vault-refs.json` and `credentials.json` are separate files** with
  independent nil-safety/open-failure handling in both `services/session/
  main.go` and `services/router/main.go` — if one store's directory is
  unwritable but the other's isn't, only the affected feature degrades,
  which is intentional but doubles the number of failure-mode code paths
  a reviewer needs to check.
