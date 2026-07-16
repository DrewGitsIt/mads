# HANDOFF — W7.2 Installer & uninstall

Executor stage. No architect HANDOFF preceded this one — the task's own
"Scope" section functioned as the design doc, so this file records what was
actually decided while building it, for whoever touches this next.

## What shipped

- `scripts/install.sh` — POSIX `sh` (not bash — curl-to-shell scripts get piped
  into whatever shell the user's `sh` resolves to). Detects linux/darwin,
  amd64/arm64. Picks `~/.local/bin` normally, `/usr/local/bin` when run as
  root (`id -u` == 0), overridable via `MADS_INSTALL_PREFIX`. Copies from
  `MADS_INSTALL_SOURCE` (file or directory containing `mads`) when set —
  that's the only path any test exercises, since there are no releases yet —
  else attempts a GitHub Releases download built from `go.mod`'s module path
  (`drewkidwell/mads`), which will 404 until a release job exists and fails
  with a message pointing at `MADS_INSTALL_SOURCE`. PATH guidance prints
  after install if the prefix isn't already on `$PATH`.

- `cmd/mads uninstall` (`cmd/mads/uninstall.go`) — removes the running
  binary unconditionally (the user just explicitly ran `uninstall`; that's
  already the confirmation for the binary), and removes the data directory
  only after explicit confirmation: an interactive `y` at a `[y/N]` prompt,
  or `--yes` for scripted/CI use. `--keep-data` skips the data directory
  entirely, no prompt. It always prints the exact binary and data-dir paths
  before touching anything — never guesses. Data dir resolution
  (`MADS_DATA_DIR`, else `~/.mads`) is a literal copy of
  `services/data/main.go`'s `defaultDataDir`, kept as intentional small
  duplication rather than a shared package for two functions this size.

- `cmd/mads --version` / `mads version` — prints `mads <version>`, a package
  var defaulting to `"dev"`, overridable via `-ldflags -X main.version=...`.
  No release process exists to wire that ldflag yet; it's there so
  `--version` has something real to report the moment one does.

- First run: verified rather than built. `services/data/internal/store.Open`
  already `MkdirAll`s `~/.mads` (or `$MADS_DATA_DIR`) on first start, and
  migration `0010_config.up.sql` already seeds the row `contracts.
  DefaultConfig()` serializes to. There was no gap to fill — the Data
  Service has owned "first run" since W1.2. `scripts/README.md` documents
  this instead of re-implementing it in the CLI.

- `scripts/smoketest` (`go test ./scripts/smoketest/...`, runs under `make
  check`'s `go test -race ./...`) — `TestMain` builds real `cmd/mads` and
  `services/data` binaries once, then three tests each do a fresh
  `install.sh` run into a `t.TempDir()` prefix with an isolated `HOME`,
  `mads --version`, start the built Data Service against that `HOME` and
  assert `GET /config` matches `contracts.DefaultConfig()` (first run),
  then exercise one uninstall outcome each: declined (data dir survives),
  confirmed (both gone), `--keep-data` (data dir survives, no prompt shown).
  Platform matrix is a `[]string{"linux", "darwin"}` var; `requireLocalRunner`
  skips anything not `runtime.GOOS` with an explicit "pending CI" message —
  darwin has no runner in this environment, tracked as future CI work
  (probably landing with W7.7's release build matrix).

## What was not done (explicitly out of scope per the task)

- Homebrew tap — stretch goal, skipped, noted here and in `scripts/README.md`.
- Real release artifacts / the GitHub Releases download path — untested,
  wired only. No release job exists yet.
- `-ldflags` wiring in the root `Makefile` to stamp a real version — left at
  `"dev"`; nothing currently needs a version string beyond `--version`
  reporting *something*.
- Windows — spec §14.10 defers it; nothing here assumes otherwise.

## Worth scrutinizing

- The confirmation UX: `--yes` bypasses the interactive prompt entirely. The
  spec's wording ("only with explicit confirmation") is satisfied by treating
  a flag as explicit consent, same as `apt-get -y`/`rm -i` conventions — but
  it's a judgment call, not spelled out in §14.7's text.
- `os.Remove(bin)` deletes the binary while it's the one currently running.
  This is safe on Linux/macOS (unlink of an open/executing file just drops
  the directory entry; the process keeps running off its already-open
  inode) but would need a different approach if Windows ever ships (§14.10
  already flags Windows as a later goal, so this wasn't a blocker here).
- `startDataService` in the smoke test resolves the service's actual bound
  address by scanning its stderr log line (`-addr 127.0.0.1:0`) rather than
  guessing a fixed port, to avoid port-collision flakiness — worth keeping in
  mind as the pattern if other tests need to spawn real service binaries.
