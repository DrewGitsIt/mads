# macOS port (Tahoe, Apple Silicon)

**Status:** Accepted (open questions resolved 2026-09-29, see Resolutions) ·
**Date:** 2026-09-29 · **Driver:** Drew wants
mads on a MacBook Air (macOS 26 Tahoe) alongside the two Linux machines.

## Context

macOS has been a launch requirement since the rebuild spec (REBUILD-SPEC §14.10,
"macOS and Linux are required at launch"), and the code mostly reflects that:

- `CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build ./...` is **clean today**. The
  release gate (`scripts/release-gate.sh`) already cross-builds darwin/amd64 and
  darwin/arm64.
- SQLite is `modernc.org/sqlite` (pure Go, no cgo). Process supervision uses
  `Setpgid`/`Setsid` (both POSIX, no `Pdeathsig`). The pty is `creack/pty`
  (darwin-capable). Credentials go through `zalando/go-keyring`, which uses the
  macOS Keychain via `/usr/bin/security` on darwin.
- The Electron shell already handles the darwin menu convention (`appMenu`, no
  auto-hide, stay open on `window-all-closed`, `activate`), and nearly every
  shortcut already accepts `metaKey || ctrlKey`.
- The scripts avoid bash-4-only syntax, so they should run under macOS's
  `/bin/bash` 3.2.

What has never happened is **executing** anything on a Mac. The smoke test skips
darwin with "pending CI", and every Linux-specific facility falls back to a
no-op or error on `!linux`. Those fallbacks compile, but they quietly remove
guarantees. So this spec is mostly about closing darwin **runtime** gaps. The
build already works.

## Goals / non-goals

**Goals**

1. On the Air, Drew uses mads the same way as on Linux: clone, `make build`,
   `scripts/app.sh` (or `install.sh`), dogfood `mads run`, same claude-code / goose
   providers.
2. No silent capability loss. Every `!linux` no-op either gets a real darwin
   implementation or is a documented, deliberate degradation.
3. Keep Linux green. Nothing here changes Linux behavior, except the Electron
   upgrade, which is shared by both platforms and is tested as its own item.
4. A macOS CI row, so darwin doesn't rot the way ui-smoke did.

**Non-goals (this pass)**

- A signed and notarized `.app` / `.dmg`, Homebrew tap, or auto-update for macOS.
  Running from a source checkout needs none of these: locally built binaries and
  npm-fetched Electron carry no quarantine xattr, so Gatekeeper doesn't get involved.
  This only becomes a question once anyone other than Drew installs mads.
- Intel Macs (darwin/amd64 stays in the build matrix, but nobody runs it).
- Syncing `~/.mads` state between machines. Each machine is an independent mads
  with its own data service, DB, sessions, and history. There are no plans for a
  centralized mads server (Q3).
- Windows.

## Findings and decisions

Ordered by how badly each one would hurt on day one.

### F1. Electron 33 is affected by the Tahoe WindowServer GPU bug. **Blocker.**

Electron versions before 36.9.2 / 37.6.0 / 38.2.0 override the private AppKit
`_cornerMask` API. On macOS 26 that pins WindowServer GPU load high and lags the
**whole system** while the app is open (electron/electron#48376). `ui/package.json`
pins `^33.2.1`, and 33 is EOL, so it will never get the fix. Apple also fixed this
at the OS level in 26.2, but that shouldn't be the only line of defense.

**Decision:** upgrade Electron to a currently supported major (≥ 38.2). This
change is shared with Linux, so it's its own work item with its own verification:
`make check`, `make ui-smoke`, `npm run test:pty`, plus a manual launch on both
Linux boxes.

Things to watch during the bump:

- The `engines.node <24.16` cap and the #51619 extract-zip workaround in
  `ensure-electron.sh`/`install.sh`. Re-check whether the new Electron's install
  path still hits it. The script comments say when the workaround can be deleted.
- `node-pty` is an optional native dep and there's no `electron-rebuild` step.
  Confirm how the pty host loads it today, and that the new ABI still works.
- Chromium sandbox behavior on the Ubuntu 24.04 / DGX hosts.

### F2. `ensure-electron.sh` assumes the Linux binary layout. **Blocker for `app.sh`.**

`mads_electron_ok` checks `dist/electron`. On macOS the binary is
`dist/Electron.app/Contents/MacOS/Electron` (`path.txt` is already written
correctly for Darwin). As a result, every Mac launch concludes the binary is
broken, "repairs" it, fails the post-repair check, and `app.sh` exits.

Secondary problems in the same file:

- `python3 -m zipfile` does not preserve symlinks, and `Electron.app`'s frameworks
  depend on them. On darwin, extract with `ditto -x -k` (always present).
- `mads_electron_needs_no_sandbox` finds no `unshare`, then runs `dist/electron`,
  which doesn't exist on macOS. It falls through to "disable the sandbox" and
  exports `ELECTRON_DISABLE_SANDBOX=1` for no reason. macOS's renderer sandbox is
  seatbelt and always works, so the function should just return 1 on Darwin.
- `install.sh`'s post-install smoke calls `dist/electron --version`, with the same
  path bug.

**Decision:** add a single `mads_electron_bin` helper that resolves the path by
`uname -s`. Every caller uses it; the darwin cases for extraction and sandbox are
handled as described above.

### F3. Sleep inhibition is D-Bus only and not user-controllable. **Important on a laptop.**

`pkg/inhibit/dbus.go` compiles on darwin, but no session or system bus exists, so
every `Acquire` logs a warning and does nothing. A long pipeline run on the Air
would then be killed by idle sleep.

It's also a hidden behavior today. The engine holds the inhibitor for every run.
`engine.Config.DisableSleepInhibit` exists, but nothing sets it: no flag, no
config field, no UI.

**Decision (Q2): make it a generic, OS-independent setting, then give each OS its
own backend.** Two parts:

**F3a. The setting.** Add "Prevent system sleep while a pipeline is running" to
the Pipeline settings panel (`ui/src/features/settings/sections/PipelineSection.tsx`),
**on by default**.

- **Contract.** The field is `AllowSleepDuringPipelines bool`
  (`json:"allow_sleep_during_pipelines"`) in `contracts.Config`. It's phrased
  negatively on purpose: its zero value (false) means "prevent sleep", so the
  default is correct with no migration. That covers stored configs from before
  this field, following the same zero-value-is-default idiom as
  `SynthesisAutoApprove`. A positive `PreventSleep` field would read as false on
  every existing install and silently turn the feature off. The UI checkbox is the
  positive phrasing, bound to `!allow_sleep_during_pipelines`.
- **Wiring.** Both launch paths already load config. `services/pipeline/main.go`
  (UI-launched runs) and `cmd/mads/run.go` (CLI runs) set
  `engine.Config.DisableSleepInhibit = cfg.AllowSleepDuringPipelines`. Put that in
  a shared helper next to the existing stage-timeout resolution so the two paths
  can't drift, which is exactly the bug the `PipelineStageTimeoutMinutes` comment
  describes.
- **Timing.** The setting is read once, when the run launches. Toggling it affects
  runs started afterward, not runs already in progress. Helper text under the
  toggle says so. Live re-evaluation isn't worth the plumbing for this setting.
- **Honest helper text.** The UI shows a per-OS caveat. Settings has no
  platform field today, so it comes from a small `platform` field on the Router's
  health or config response rather than being sniffed in the renderer, because
  the Router's OS is the one that matters.
  - macOS: "Closing the lid on battery still sleeps your Mac. The run pauses and
    recovers when it wakes."
  - Linux: "Closing a laptop lid may still suspend, depending on your desktop's
    lid settings."

**F3b. Per-OS backends behind the existing `inhibit.Locker`.** `NewLocker()`
becomes build-tagged; the interface and the engine call site stay the same.

| File | Backend |
|---|---|
| `dbus_linux.go` | today's logind + `org.freedesktop.ScreenSaver` code, moved verbatim |
| `caffeinate_darwin.go` | spawns `caffeinate -i -s -w <our pid>`; `Release` kills it. Ships with macOS, no cgo (release builds are `CGO_ENABLED=0`). `-w` makes the assertion die with us even after a SIGKILL. `-i` prevents idle sleep; `-s` prevents system sleep, but only on AC power |
| `noop_other.go` | everything else: a no-op `Handle` plus a single warning. This is also where a future Windows `SetThreadExecutionState` backend goes |

The "never fails, bounded ~2s" contract in `inhibit.go` applies to every backend:
if `caffeinate` can't start, log a warning and return a no-op handle.

**Caveat, surfaced by F3a's helper text rather than fixed:** on a MacBook, closing
the lid on battery sleeps the machine regardless of any assertion. A run in
progress then goes through the existing suspend/lease-loss path, and F4 makes
that recovery quick on macOS.

### F4. Process identity (`pkg/procid`) is unimplemented. **Degraded recovery.**

On darwin every method errors, so pipeline leases carry no identity and
reap-on-launch never reclaims anything. A crashed `mads run` leaves its tasks stuck
`in_progress` until the lease TTL expires. That is correct but slow.

**Decision:** add `procid_darwin.go`, using syscall-level facts with no cgo:

| Fact | Linux | darwin |
|---|---|---|
| MachineID | `/etc/machine-id` | `IOPlatformUUID` from `ioreg -rd1 -c IOPlatformExpertDevice` (read once; hardware UUID, stable across OS reinstalls) |
| BootID | `/proc/sys/kernel/random/boot_id` | `sysctl kern.bootsessionuuid` via `unix.Sysctl` (new every boot) |
| PIDStartTicks | field 22 of `/proc/<pid>/stat` | `unix.SysctlKinfoProc("kern.proc.pid", pid)` → `Proc.P_starttime` as µs since the epoch; an empty/ESRCH result maps to `os.ErrNotExist` |

The units differ by OS, but values are only ever compared on the same machine
(`Evaluate` already requires matching MachineID). The Linux-flavored field names
and docs in `contracts.LeaseIdentity` get a comment, not a rename.

### F5. `flock(2)` serialization is a no-op off Linux. **Correctness, cheap fix.**

`pkg/credstore/flock_*.go` and `services/pipeline/engine/workspacelock_*.go` gate
real locking on `linux`. `syscall.Flock` exists on darwin. The in-code rationale
("flock is a Linux guarantee") is wrong.

**Decision:** retag both as `unix` / `!unix`. With no locking, a CLI `mads run`
and a Router-spawned pipeline child could race merges into the same workspace on
the Mac.

### F6. Keychain works, but the messages are Linux-specific. **Polish.**

go-keyring's darwin backend stores the data key in the login Keychain through
`security`, with no prompts, because the item's ACL trusts `/usr/bin/security`.
The actionable errors in `credstore/crypto.go` mention D-Bus, Secret Service, and
Seahorse. Make that text platform-neutral, or give each platform its own variant
("Keychain Access" on macOS).

### F7. Tests that are Linux-only, or pass vacuously on darwin. **CI correctness.**

- `providers/goosecli/fakegoose_test.go` uses `unix.TCGETS`/`TCSETS`, so
  `go vet`/`go test` fail to compile the package on darwin. Use
  `TIOCGETA`/`TIOCSETA` through a build-tagged constant (or `golang.org/x/term`).
- `services/pipeline/engine/lease_isolated_test.go`'s `pidAlive` stats
  `/proc/<pid>`. On darwin that always reports dead, so the kill assertion
  **passes without testing anything**. Use `syscall.Kill(pid, 0)` and treat ESRCH
  as dead.
- Expect path-equality failures on first run: `t.TempDir()` lives under
  `/var/folders/…`, which is a symlink to `/private/var/…`, and `git rev-parse
  --show-toplevel` / worktree paths come back resolved. Fix by
  `filepath.EvalSymlinks` at the comparison site, not by changing production path
  handling blindly. Triage whatever the first darwin `make check` turns up.
- APFS is case-insensitive by default. Look for anything that distinguishes paths
  or branch names by case (worktree dirs named from task IDs, and `mads/<task>`
  refs). Probably nothing, but verify once.

### F8. Build and dev tooling. **Small.**

- `make ui-smoke` shells out to `flock(1)`, which macOS doesn't have, and hard-codes
  `~/.cache/ms-playwright` (on macOS it's `~/Library/Caches/ms-playwright`).
  Resolve the path through `PLAYWRIGHT_BROWSERS_PATH`, or pin it to a single
  location on both OSes. Take the install lock only when `flock` exists (the lock
  only matters for concurrent cold installs across worktrees).
- The CI Playwright cache key is already keyed on `runner.os`. The path needs the
  same fix.
- Prerequisites for the Mac: Xcode Command Line Tools (git, and the compiler for
  node-pty), Go 1.26, Node per `.nvmrc`, plus `claude` and optionally `goose`.
  Document these in the README.

### F9. Paths: keep XDG-style, not `~/Library`. **Decision, no work.**

`~/.mads` and the shadow store's `~/.local/share/mads/shadow` fallback stay as
they are on macOS. That matches how the dev tools involved (claude, goose, git)
behave, and it keeps one mental model across the three machines.
`XDG_CONFIG_HOME` pass-through for goose already works (goose reads
`~/.config/goose` on macOS too; confirm once with a real goose session).

### F10. UI polish. **Nice to have.**

- Shortcut hints in UI copy should render `⌘` on macOS instead of `Ctrl`.
  Behavior is already correct. `Ctrl+`` for the shell dock is intentionally Ctrl
  on both platforms, as in VS Code. The README's "Ctrl+R" advice becomes Cmd+R.
- Check the `--sans`/`--mono` font stacks for good macOS fallbacks (SF Pro / SF
  Mono / Menlo) so the app doesn't render in Helvetica/Courier.
- The dock badge (`app.setBadgeCount`) works on the macOS dock as is.

### F11. `localhost` resolution. **Verify only.**

The services listen on `localhost:<port>`. Go resolves that to one address
(IPv4-preferred), while macOS's `/etc/hosts` maps it to both `127.0.0.1` and
`::1`. Chromium and curl fall back across both, so this should be fine. If
anything shows connection-refused flakes on the Mac, pin to `127.0.0.1`.

## Work items

Wave A items are independent. B depends on A1–A3. C depends on B.

| # | Item | Touches | Verify |
|---|---|---|---|
| A1 | Electron ≥ 38.2 upgrade (F1) | `ui/package.json`, lockfile, `ensure-electron.sh`/`install.sh` #51619 guard, pty host | Linux: `make check`, `make ui-smoke`, `test:pty`, manual launch on both boxes |
| A2 | darwin-aware Electron repair and launch (F2) | `scripts/ensure-electron.sh`, `scripts/install.sh` | `bash scripts/ensure-electron.sh` exits 0 on the Mac without repairing; Linux unchanged |
| A3 | Test portability (F7, known items) | `fakegoose_test.go`, `lease_isolated_test.go` | `GOOS=darwin go vet ./...` clean |
| A4 | `flock` → `unix` build tags (F5) | credstore, pipeline engine | existing lock tests run on darwin |
| A5 | `procid` darwin backend (F4) | `pkg/procid/procid_darwin.go` + tests against real pids | reap-on-launch reclaims a SIGKILLed `mads run` on the Mac |
| A6 | Sleep-prevention setting (F3a), cross-platform, useful on Linux too | `pkg/contracts/config.go` (+ goldens), `services/pipeline/main.go`, `cmd/mads/run.go`, `PipelineSection.tsx`, Router platform field | engine tests: setting on → fake Locker acquired; off → not. UI: vitest for toggle inversion + per-OS helper text. Linux: `systemd-inhibit --list` shows the hold only while on |
| A7 | Per-OS inhibit backends (F3b), independent of A6 (disjoint files) | `pkg/inhibit` split: `dbus_linux.go`, `caffeinate_darwin.go`, `noop_other.go` | Mac: `pmset -g assertions` shows the hold during a run and drops it after, including after a SIGKILLed run |
| A8 | Platform-neutral keyring errors (F6) | `pkg/credstore/crypto.go` | unit test on message text |
| B1 | First real `make check` + `make e2e-smoke` on the Air; triage (F7 path/case fallout) | wherever it lands | green on darwin |
| B2 | Tooling: `ui-smoke` lock/cache path, README prerequisites (F8) | `Makefile`, `README.md`, `scripts/README.md` | `make ui-smoke` green on the Mac |
| B3 | macOS CI row (Q1): on push to `main` + `workflow_dispatch`, not PRs | `.github/workflows/ci.yml` (`macos-latest`, arm64): `go test -race ./...`, smoketest darwin row un-skipped, e2e-smoke | CI green; smoketest's "pending CI" skip removed |
| C1 | Dogfood acceptance on the Air | none | see below |
| C2 | UI polish (F10) | shortcut-hint rendering, font stacks | visual check |

**Acceptance (C1):** on the Air, from a fresh clone:
1. `make build` and `scripts/app.sh` bring up the app with no sandbox override
   and no Electron repair.
2. A claude-code session and a goose session both run a turn.
3. The shell dock opens zsh in the session's workspace.
4. `mads run` over a small seeded task set, with a fake and then a real provider,
   produces merges.
5. While the run is active, `pmset -g assertions` shows mads's hold. With the
   setting turned off, the next run takes no hold.
6. Kill the Router with SIGTERM: no orphans. Kill a pipeline with SIGKILL and
   relaunch: its lease is reclaimed with "pid N not found" evidence.

## Resolutions (2026-09-29)

- **Q1. CI cost.** The macOS row runs on pushes to `main` plus
  `workflow_dispatch`, not on PRs (B3).
- **Q2. Laptop sleep.** Drew turned this into a generic, OS-independent setting,
  on by default: "mads prevents system sleep while a pipeline is running." Each
  OS gets its own backend (F3a/F3b, work items A6/A7). The lid-close limitation is
  stated in the setting's helper text instead of a run-time warning.
- **Q3. Three machines, three databases.** Confirmed. Each machine is an
  independent mads, with no plans for a centralized mads server.
- **Q4. Distribution.** Running from source is enough for now. A double-clickable
  `.app` (electron-builder, ad-hoc signed) comes later as its own spec.
