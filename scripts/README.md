# Installer & uninstall

`install.sh` is the curl-to-shell installer (spec §14.7): it detects the platform
(linux/darwin, amd64/arm64), picks an install prefix (`~/.local/bin`, or
`/usr/local/bin` when run as root), and places a `mads` binary there.

```sh
curl -fsSL https://raw.githubusercontent.com/drewkidwell/mads/main/scripts/install.sh | sh
```

No release has been cut yet, so the download path is wired but untested — for
local builds and CI, point it at a binary you already built instead:

```sh
make build
MADS_INSTALL_SOURCE=./bin sh scripts/install.sh   # bin/ contains "mads"
```

Env overrides: `MADS_INSTALL_SOURCE` (local binary or directory, skips the
download), `MADS_INSTALL_PREFIX` (install directory), `MADS_VERSION` (release
tag, only used without `MADS_INSTALL_SOURCE`).

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

The binary is always removed. The data directory is only removed after
explicit confirmation (an interactive `y`, or `--yes`); declining, or passing
`--keep-data`, leaves it untouched. It always prints the exact paths it's
about to delete before deleting anything.

## Update

```sh
mads update check                          # asks the Router to check the release feed
mads update apply -yes                     # download, verify, swap, restart, health-check
```

Both subcommands talk to a running Router (`-router http://localhost:8080` by
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

The target platform matrix is linux and darwin (spec §14.10). This
environment only has a linux runner, so the darwin row is skipped with an
explicit "pending CI" message rather than silently passing — it needs a
macOS GitHub Actions runner wired into `.github/workflows/ci.yml` before it
can run for real (tracked for whichever work item stands up the release
build matrix, spec §14 Notes / W7.7).

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

The build matrix gives this gate darwin **build** coverage — it does not
close the darwin **execution** gap noted above, which still needs an actual
macOS runner.
