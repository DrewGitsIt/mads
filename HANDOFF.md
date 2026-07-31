# Encrypt credstore at rest behind the user's login keychain

## What changed

`pkg/credstore` now encrypts every value before it touches disk (AES-256-GCM),
using **envelope encryption** — architect decision (b) from the task brief:

- A single 32-byte data key is generated once and stored as **one** Secret
  Service item (`github.com/zalando/go-keyring`, service `mads-credstore`,
  user `data-key`) in the user's login keyring (gnome-keyring via D-Bus on
  the owner's COSMIC/Pop!_OS setup).
- `credentials.json` keeps its existing `{name: value}` shape; `value` is now
  `enc:v1:` + base64(nonce‖AES-GCM ciphertext) instead of plaintext. Names
  stay plaintext (never secret) — `Names()`/`Delete()` never touch the
  keyring.
- Chose (b) over (a) (one keyring item per secret) because the file is
  already opened independently by two long-lived processes (router,
  session); (b) reduces the concurrent-first-open race to *one* keyring item
  to agree on, not one per credential, and keeps every other on-disk
  mechanic (file shape, perms, atomic write) unchanged.

**Migration**: `Open()` on a pre-existing file with legacy plaintext entries
encrypts them in place before returning. The whole read-decide-write span is
guarded by `flock(2)` on `credentials.json.lock` (`flock_linux.go`, no-op
off Linux like the existing `services/pipeline/engine/workspacelock_*.go`
split) so router and session racing to `Open()` the same fresh file can't
mint two different data keys for it — whoever wins the lock migrates, the
loser finds it already done and no-ops. Writes go through a temp-file +
`os.Rename` (`writeRaw`) for atomicity, replacing the old direct
`os.WriteFile`.

**The graceful-degradation trap** (task's most subtle requirement): a name
that was never `Set` is determined *without decrypting* (plain map-key
lookup), so it's always correctly reported in `ResolveEnv`'s `missing` list
regardless of keyring state. A name that *is* present but can't be decrypted
right now (locked/unreachable keyring) is excluded from **both** `env` and
`missing` — never folded into "missing", which would read as "not
configured" to a caller (and downstream, `capability.go`'s "missing
credential: X" health message) when the truth is "keyring's locked, unlock
it". A single actionable warning is logged once per `Store` instance
(`sync.Once`) instead. `Get` on such a name returns a real, actionable
`error` (not just `ok=false`) since its signature already supports that.

`ResolveEnv`/`Get`/`Set`/`Delete`/`Names`/`Open` signatures are all
unchanged — no caller (`supervisor.go`, `runner.go`, `pkg/capability/mcp.go`,
`main.go`) needed to change.

**Key creation** only happens when the store is known to have zero existing
ciphertext (`hasCiphertext` computed fresh from the raw map at each `Set`
and inside `migrate`). If ciphertext already exists but the keyring item is
missing (e.g. deleted via Seahorse), `dataKey` refuses to silently mint a
replacement — it returns an actionable "key missing, data unrecoverable"
error instead of orphaning the existing entries.

## Testing

`pkg/credstore/credstore_test.go` covers all four required scenarios, plus
the pre-existing round-trip/Names/perms tests (updated only to seed
`go-keyring`'s mock):

- `TestSetNeverPersistsPlaintext` — reads the raw file bytes after `Set`,
  asserts the plaintext value is absent; `Get`/`ResolveEnv` round-trip.
- `TestOpenMigratesPlaintextFile` — legacy plaintext file migrates, entries
  intact, plaintext gone on disk, every on-disk value is `enc:v1:`-prefixed;
  a second `Open()` is byte-for-byte a no-op.
- `TestOpenMigratesConcurrently` — two goroutines `Open()` the same
  not-yet-migrated file concurrently; both succeed, end state is correct and
  consistent. (Skipped off Linux, same as `acquireFlock` itself.)
- `TestAcquireFlockBlocksUntilReleased` — the raw lock primitive, mirroring
  `workspacelock_test.go`'s equivalent.
- `TestResolveEnvKeyringUnavailableIsNotMissing` — the trap test: sets a
  credential, then breaks the keyring, then asserts `ResolveEnv` reports
  only the *genuinely* unset name as missing (not the undecryptable one),
  asserts the actionable log fires, and asserts `Get` on the
  present-but-locked name returns a distinct error.
- `TestOpenFailsActionablyWhenKeyringUnavailableDuringMigration` — a
  plaintext file that can't be migrated because the keyring is unreachable
  fails `Open()` with an actionable error and leaves the plaintext untouched
  (no partial/lost migration).

**Every test in this package, and everywhere else `credstore.Set` is
exercised, runs against `go-keyring`'s in-memory mock** (`gokeyring.MockInit`
/ `MockInitWithError`) — no real Secret Service/D-Bus session required. I
found and patched three other test binaries that call `credstore.Set`
(`services/router/internal/supervisor`, `services/router/internal/api`,
`services/session/internal/runner`) to call `gokeyring.MockInit()` in their
existing `TestMain`, since `go-keyring`'s mock provider is process-global
state. I verified this by running the full suite with
`DBUS_SESSION_BUS_ADDRESS` pointed at a nonexistent socket — passes clean.
(`services/router/main_test.go` only ever calls `Open` on a fresh file,
which never touches the keyring by design, so it needed no change.)

`go build ./...`, `go vet ./...`, `golangci-lint run ./...` (0 issues), and
`go test -race -count=1 ./...` all pass. I did not run the UI lint/test half
of `make check` — this task touches no UI code.

## Scope notes / what I deliberately did not do

- **Did not** touch credential *injection* semantics — still raw env vars
  into spawned processes via `ResolveEnv`, exactly as before. The
  references+resolving-wrapper redesign is explicitly a separate, later task
  series per the brief.
- **Did not** fix the pre-existing cross-process race on `Set`/`Delete`
  (router and session can still race a concurrent write to the same
  `credentials.json` outside of the one-time migration, exactly as before my
  change — only now the value is ciphertext). The task only required
  first-open migration to be race-safe; expanding the flock to cover every
  write would be a real behavior change beyond what was asked, so I left it.
- **Did not** touch `main.go` in router/session, or `supervisor.go` /
  `capability.go` / `pkg/capability/mcp.go` — the frozen public API meant no
  call site needed to change, and I kept it that way.
- Added one new direct dependency: `github.com/zalando/go-keyring` (per the
  task's own suggestion) rather than hand-rolling the Secret Service D-Bus
  protocol (session negotiation, unlock prompts, item search) — that
  protocol is considerably more surface area than the D-Bus calls already in
  `pkg/inhibit`, and go-keyring's mock is exactly the test double the
  acceptance criteria asks for.

## What a reviewer should scrutinize

- The `hasCiphertext`-gated "only create the key if nothing already depends
  on one" logic in `dataKey`/`migrate`/`Set` — this is the one piece of real
  design judgment beyond the acceptance criteria (protects against silently
  orphaning existing ciphertext if the keyring item is deleted out from
  under a live install). Worth a second look for edge cases I might have
  missed (e.g. a file with a mix of migrated and unmigrated entries, which
  shouldn't occur given atomic rename but isn't explicitly tested).
- The decision to exclude present-but-undecryptable names from *both* `env`
  and `missing` in `ResolveEnv`, rather than some other encoding, given the
  frozen signature — this is the judgment call the task asked me to make and
  document; alternatives (e.g. still reporting missing, just with a
  differently-worded log) are defensible too.
- `go-keyring`'s mock provider is package-global, so I had to hunt down and
  patch every other test binary that calls `credstore.Set` — worth
  double-checking I didn't miss one (I grepped `credstore\.` repo-wide and
  cross-checked each hit).
