# Installer & uninstall

`install.sh` sets mads up from a source checkout. There's no hosted
`curl | sh` install yet: no release has been cut, and the script's header
marks that as a later goal (spec §14.7).

```sh
sh scripts/install.sh
```

From the checkout, it:

1. **Preflights** `node`, `npm`, `go` and `make`, and stops if any is missing.
2. **Builds** everything with `make build` (which runs `npm ci`).
3. **Installs the Electron binary** via `scripts/ensure-electron.sh`.
   Electron downloads it lazily, so this fetches it up front. It then
   checks for a working renderer sandbox. Where the OS blocks unprivileged
   user namespaces (e.g. Ubuntu 24.04's AppArmor restriction), it tries the
   setuid `chrome-sandbox` fix with sudo, and otherwise falls back to
   `ELECTRON_DISABLE_SANDBOX=1` with a warning. If `bash` isn't on `PATH`,
   this step is skipped.
4. **Writes a launcher** at `bin/mads-app` that runs `scripts/app.sh`
   (exporting `ELECTRON_DISABLE_SANDBOX=1` when step 3 needed it). Note that
   `bin/mads` is the Go CLI; `bin/mads-app` is the desktop app.
5. **Smoke-tests** the Electron binary with `--version`.

Every step checks for its result before doing work, so re-running is cheap
and safe. When it finishes, launch the app with `bin/mads-app`.

### Binary-only install (CI and smoke tests)

Setting `MADS_INSTALL_SOURCE` skips all of the above and just places a
prebuilt `mads` CLI binary. It builds nothing, installs no Electron, and
writes nothing outside the prefix:

```sh
make build
MADS_INSTALL_SOURCE=./bin sh scripts/install.sh   # bin/ contains "mads"
```

`MADS_INSTALL_SOURCE` is a binary or a directory containing one.
`MADS_INSTALL_PREFIX` sets the install directory (default `~/.local/bin`,
or `/usr/local/bin` as root). `scripts/smoketest` drives this path.

## First run

The installed `mads` binary is a thin client (`cmd/mads`) — it's the Data
Service that owns `~/.mads`: `store.Open` creates the directory and the
SQLite file on first start, and migration `0010_config` seeds the default
config row (`contracts.DefaultConfig()`). There's nothing else to bootstrap
here; `scripts/smoketest` exercises this by starting a built Data Service
against an isolated `$HOME` and asserting the seeded config comes back over
`GET /config`.

## Uninstall

```sh
mads uninstall               # prompts before touching ~/.mads
mads uninstall --keep-data   # binary only, no prompt
mads uninstall --yes         # scripted/CI use — answers the prompt yes
```

The binary is always removed. The data directory (`~/.mads`) and the
shadow-store root (`$XDG_DATA_HOME/mads/shadow`, default
`~/.local/share/mads/shadow`: per-session diff history, usually the largest
thing mads writes) are only removed after explicit confirmation (an
interactive `y`, or `--yes`); declining, or passing `--keep-data`, leaves both
untouched. It always prints the exact paths it's
about to delete before deleting anything.

## Update

```sh
mads update check                          # asks the Router to check the release feed
mads update apply -yes                     # download, verify, swap, restart, health-check
```

Both subcommands talk to a running Router (`-router http://localhost:17373` by
default). `check` proxies `POST /update/check` (`pkg/update.Check`, spec
§14.3): it never fires on its own — no polling, no phone-home — only in
response to this explicit command, hitting `Config.UpdateFeedURL` (empty by
default; no release feed exists yet). `apply` fetches the manifest itself,
confirms the platform artifact and its version unless `-yes`, gracefully
stops a locally-running Router via its PID file
(`$MADS_HOME/router.pid`), then calls `pkg/update.Apply`: download to a
staging dir, verify the sha256 from the feed, swap the binary in place,
restart, and run a post-swap health check (`mads --version`). A failed
health check rolls the previous binary back automatically and reports it —
nothing is left half-swapped.

## Smoke test

`scripts/smoketest` (`go test ./scripts/smoketest/...`, part of `make check`)
builds real `mads` and Data Service binaries, runs `install.sh` into a temp
prefix with an isolated `HOME`, verifies `mads --version` and first-run data
dir creation, then drives `mads uninstall` through all three outcomes
(declined, confirmed, `--keep-data`).

The target platform matrix is linux and darwin (spec §14.10), and the smoke
runs on both: linux in every CI run, darwin in CI's `macos` job (on push to
`main` and `workflow_dispatch` only, never on pull requests — macOS minutes
cost about 10x Linux; work item B3 in
[docs/MACOS-PORT-DESIGN.md](../docs/MACOS-PORT-DESIGN.md)) and in
`make check` on a Mac. Any other GOOS skips with an explicit message.

## Release gate

`scripts/release-gate.sh` (`make release-gate`) is the pre-release check: it
cross-builds `linux/amd64`, `linux/arm64`, `darwin/amd64`, `darwin/arm64`
(build-only for every combo except the runner's native platform), health-
checks the native candidate (`mads --version` must exit 0 — the same
convention `pkg/update.Apply` uses to decide rollback), then runs this
directory's install/first-run/uninstall smoke and `cmd/mads`'s update-apply
smoke against that candidate.

```sh
make release-gate
```

`MADS_RELEASE_CANDIDATE_DIR=<dir>` skips the build matrix and tests `<dir>`
instead — `scripts/smoketest`'s `TestMain` picks up prebuilt `mads`/`data`
binaries from that directory when it's set, falling back to its normal self-
build otherwise. This is what lets `scripts/releasegate`'s negative-proof
test (a deliberately broken `mads` binary that fails the health check) run
in milliseconds instead of repeating a multi-minute cross-compile.

The build matrix gives this gate darwin **build** coverage only; darwin
**execution** coverage comes from CI's `macos` job noted above. Run on a Mac,
the native candidate is darwin, so the health check and the install/uninstall
smoke both run for real there.
