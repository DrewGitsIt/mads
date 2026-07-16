# HANDOFF — W7.5 Update lifecycle

Architect stage output. Executor: read this fully before writing code. This task spans five
existing areas (`pkg/contracts`, `services/data`, `services/router`, `cmd/mads`, `ui`) plus
one new package (`pkg/update`, this file's home). Read §1 first — it locks in the scoping
calls that make the rest tractable.

## 0. What already exists — do not rebuild these

- `services/router/internal/supervisor.Supervisor.Shutdown(ctx)` (`supervisor.go:199`) —
  terminates every tracked Session/Pipeline child gracefully (SIGTERM, grace period, SIGKILL
  backstop). This **is** "graceful agent stop... via the existing supervisor paths" from the
  task brief. Already wired into `services/router/main.go`'s SIGINT/SIGTERM handler
  (`main.go:142-150`). Nothing new needed for the drain step itself — only a way to *trigger*
  it from outside the Router process (§6.1).
- `services/data/internal/store.Migrate/Rollback/Version` (`migrate.go`) — full up **and**
  down migration support, already tested (`migrate_test.go`), already has down.sql for all 11
  migrations. This is the "existing migration code" the task tells you to read first. **Keep
  as-is.** §4 explains the one thing this task adds on top.
- `services/data/internal/store/config.go` — `Config` is a single JSON blob column (`data
  TEXT`), not a normal-form table. Adding a new field to `contracts.Config` is a **zero-
  migration** change — no new `.sql` files needed for §2.
- Frontend: `ui/src/features/settings/panels/UpdatesAboutPanel.tsx` already exists (W6.10)
  with the right two-column layout and a disabled "Check for updates" button with a "coming
  in W7.5" tooltip — this task's frontend piece is making that button real, not building new
  layout. `ui/src/features/settings/state/usePreflight.ts` is the hook shape to mirror, with
  one deliberate difference (§8).
- `pkg/preflight` (W7.3) is the closest sibling in style to what this task builds: a small
  `Deps`-constructed seam interface, real + fake implementations, "never a hard error, always
  data" philosophy. Follow the same shape for `pkg/update`.

## 1. Scope decisions — read this before writing any code

**The only artifact the installer actually ships today is the `mads` binary.**
`scripts/install.sh` downloads and places exactly one file (`BIN_NAME="mads"`,
`mads_${platform}.tar.gz`). `router`/`data`/`session`/`pipeline` are built by `make build`
into a local `bin/` for dev/test/CI only — there is no release job, no packaging, and no
process-supervisor (systemd/launchd) story for them yet (confirmed: `ui/electron/main.ts`
doesn't spawn any backend process; `scripts/app.sh`'s own comment says "until W7 packaging
does this properly"). W7.2's own HANDOFF says the same thing explicitly (§"What was not
done": "Real release artifacts... untested, wired only. No release job exists yet.").

Given that, this task deliberately does **not** attempt to build a five-binary coordinated
release/restart pipeline — that's genuinely unbuilt infrastructure belonging to whichever
future work item finishes packaging (W7.2's follow-on or W7.7's release-gate work), not a
silent scope expansion here. Instead:

1. **`pkg/update`** is a fully general, binary-agnostic "check a feed, download, verify,
   swap one executable, restart, health-check, roll back on failure" library. It knows
   nothing about Router/Data/Session/Pipeline specifically — it operates on a `Target{
   BinaryPath, StagingDir}` and a `Hooks{Stop, Start, HealthCheck}` triple. This is the piece
   that is fully, rigorously testable with fake binaries in a temp dir per the task brief,
   and it's what the forced-failure smoke test (done-when criterion) exercises directly.
2. **`mads update apply`** wires that library to the one artifact that's real today: the
   running `mads` binary itself. Its `Stop`/`Start` hooks make a **best-effort** attempt to
   gracefully drain/restart a locally-running Router if one is found (§6.1) — this is real,
   working code, not a stub — but it degrades to "log guidance, continue" rather than failing
   the update when no Router is running or no sibling binary is found, because for a plain
   `mads` CLI install there usually isn't one. Flag this honestly in the PR description: full
   multi-service coordinated updates remain an open item pending real packaging.
3. **The user-initiated version check** (spec requirement 3, §14.3) is wired for real, fully
   end to end: Config → Router → UI. This has no scoping ambiguity and is pure value — do
   this one completely, no shortcuts.
4. **Data migration backward-compatibility** (§14.4-5) is a small, self-contained addition to
   `services/data` independent of the CLI/binary-swap question — do this one for real too
   (§4).
5. **Session resume** is verification-only — write the test, don't build persistence that
   already exists (§7).

If, having read this, the executor judges the `mads update apply` real-Router-wiring in §6.1
is more than the acceptance criteria actually require, it's fine to trim it to "always
best-effort, log-and-continue, never touch a real Router" and lean entirely on the fake-
binary smoke test for the done-when proof — the acceptance criteria is explicitly scoped to
"stub feed server + fake binaries in a temp dir," not real service processes. Don't let §6.1
block on getting real Router discovery perfect.

## 2. `contracts.Config.UpdateFeedURL` (`pkg/contracts/config.go`)

Add one field, empty default (spec's own "no releases exist yet" — an empty feed URL is the
honest starting state, mirroring `pkg/preflight/compat.go`'s "seeded honestly" precedent):

```go
// Config, add near the other operational fields:
UpdateFeedURL string `json:"update_feed_url,omitempty"`
```

`DefaultConfig()`: don't set it (zero value `""`). `ValidateConfig`: empty is valid (feature
off); non-empty must parse as an absolute `http`/`https` URL:

```go
if c.UpdateFeedURL != "" {
    u, err := url.Parse(c.UpdateFeedURL)
    if err != nil || !u.IsAbs() || (u.Scheme != "http" && u.Scheme != "https") {
        v.Add("update_feed_url", "must be an absolute http(s) URL")
    }
}
```

Add to `config_test.go`'s validation table. Not a restart-required field (`ui/src/features/
settings/restartRequired.ts`'s `RESTART_REQUIRED_FIELDS`) — it's read fresh on every check,
nothing to restart.

## 3. `pkg/update` — the core library (new package)

### 3.1 Manifest (`pkg/update/manifest.go`)

```go
package update

// PlatformArtifact is one platform's downloadable release, keyed by
// "<GOOS>_<GOARCH>" (e.g. "linux_amd64") — the same platform string
// scripts/install.sh's detect_platform already produces, so a manifest built
// for the installer's release tarballs and this package's checks agree on
// format for free.
type PlatformArtifact struct {
    URL    string `json:"url"`
    SHA256 string `json:"sha256"`
}

// Manifest is the feed's response shape. Notes is release-notes markdown/
// plaintext, shown verbatim in Settings.
type Manifest struct {
    Version   string                      `json:"version"`
    Notes     string                      `json:"notes"`
    Platforms map[string]PlatformArtifact `json:"platforms"`
}

// CurrentPlatform returns this process's "<GOOS>_<GOARCH>" key.
func CurrentPlatform() string { return runtime.GOOS + "_" + runtime.GOARCH }
```

### 3.2 Feed (`pkg/update/feed.go`)

```go
// Feed fetches the release manifest. A separate interface from the concrete
// HTTP client (same pattern as pkg/preflight's DataHealth/ProviderLister) so
// Check is unit-testable against a fixture without a real HTTP round trip,
// while HTTPFeed is what production and the local-stub-server tests both use
// (the stub server is a real httptest.Server serving fixture JSON over real
// HTTP — closer to production than a hand-rolled fake, and what the task
// brief's "tests run against a local stub server serving a fixture feed"
// literally asks for).
type Feed interface {
    Fetch(ctx context.Context) (Manifest, error)
}

type HTTPFeed struct {
    URL string
    HTTP *http.Client // nil uses a 10s-timeout default
}

func (f HTTPFeed) Fetch(ctx context.Context) (Manifest, error) {
    client := f.HTTP
    if client == nil {
        client = &http.Client{Timeout: 10 * time.Second}
    }
    req, err := http.NewRequestWithContext(ctx, http.MethodGet, f.URL, nil)
    if err != nil {
        return Manifest{}, err
    }
    resp, err := client.Do(req)
    if err != nil {
        return Manifest{}, fmt.Errorf("update: fetch feed: %w", err)
    }
    defer resp.Body.Close()
    if resp.StatusCode != http.StatusOK {
        return Manifest{}, fmt.Errorf("update: feed %s returned %d", f.URL, resp.StatusCode)
    }
    var m Manifest
    if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
        return Manifest{}, fmt.Errorf("update: decode feed response: %w", err)
    }
    return m, nil
}
```

### 3.3 Check (`pkg/update/check.go`) — spec §14.3, "user-initiated only"

```go
type CheckResult struct {
    CurrentVersion  string `json:"current_version"`
    LatestVersion   string `json:"latest_version,omitempty"`
    UpdateAvailable bool   `json:"update_available"`
    Notes           string `json:"notes,omitempty"`
    Detail          string `json:"detail,omitempty"` // e.g. "no update feed configured"
}

// Check fetches the feed exactly once and compares versions. It is a plain
// request/response function — deliberately no polling loop, no ticker, no
// "start background checker" entry point anywhere in this package. The only
// caller in this codebase is the Router's handleUpdateCheck (§5.2), invoked
// exactly once per HTTP request, which is exactly once per user click. This
// is the mechanism that satisfies "no auto-phone-home" — there is simply
// nothing in this package capable of firing on its own.
func Check(ctx context.Context, feed Feed, currentVersion string) (CheckResult, error) {
    m, err := feed.Fetch(ctx)
    if err != nil {
        return CheckResult{}, err
    }
    result := CheckResult{CurrentVersion: currentVersion, LatestVersion: m.Version, Notes: m.Notes}
    if compareSemver(m.Version, currentVersion) > 0 {
        result.UpdateAvailable = true
    }
    return result, nil
}
```

`compareSemver` (`pkg/update/semver.go`): copy `pkg/preflight/semver.go`'s `compareSemver`
verbatim (unexported there, ~15 lines, dotted-numeric comparison). This is intentional small
duplication, not an oversight — matches this repo's own precedent (`cmd/mads/uninstall.go`'s
doc comment: "kept as intentional small duplication rather than a shared package for two
functions this size"). If a reviewer wants it deduplicated, the honest fix is promoting it to
an exported `pkg/semver` (or similar) leaf package both `preflight` and `update` import — not
in scope for this task, note it as a follow-up if raised.

### 3.4 Apply (`pkg/update/apply.go`) — spec §14.4, the done-when mechanism

```go
// Target is the single binary being updated.
type Target struct {
    BinaryPath string // absolute path to the currently-installed executable
    StagingDir string // scratch directory for the download; caller's responsibility to clean up
}

// Hooks are the three system-specific actions Apply orchestrates but does not
// implement itself — this package has no idea what "the system" is (a bare
// CLI, a CLI plus a locally-running Router, a whole desktop app). Production
// callers (cmd/mads) wire real behavior (§6.1); tests wire fakes/fake
// binaries. All three are required — pass no-op funcs explicitly rather than
// leaving Hooks zero-valued, so a missing wire-up is a visible bug, not a
// silent no-op.
type Hooks struct {
    Stop        func(ctx context.Context) error
    Start       func(ctx context.Context) error
    HealthCheck func(ctx context.Context) error
}

// Result reports what Apply actually did.
type Result struct {
    RolledBack bool
    Version    string
}

// Apply runs the full lifecycle: stop -> download -> checksum -> swap ->
// restart -> health-check -> (rollback on failure). It never leaves the
// system in a half-swapped state: every failure path after the backup is
// taken restores the backup before returning.
func Apply(ctx context.Context, art PlatformArtifact, target Target, hooks Hooks) (Result, error) {
    if err := hooks.Stop(ctx); err != nil {
        return Result{}, fmt.Errorf("update: stop: %w", err)
    }

    staged := filepath.Join(target.StagingDir, "update.download")
    if err := downloadTo(ctx, art.URL, staged); err != nil {
        _ = hooks.Start(ctx) // best-effort: put the still-untouched original back in service
        return Result{}, fmt.Errorf("update: download: %w", err)
    }
    if err := verifyChecksum(staged, art.SHA256); err != nil {
        _ = hooks.Start(ctx)
        return Result{}, fmt.Errorf("update: checksum: %w", err)
    }
    if err := os.Chmod(staged, 0o755); err != nil {
        _ = hooks.Start(ctx)
        return Result{}, fmt.Errorf("update: chmod staged binary: %w", err)
    }

    backup := target.BinaryPath + ".bak"
    if err := os.Rename(target.BinaryPath, backup); err != nil {
        _ = hooks.Start(ctx)
        return Result{}, fmt.Errorf("update: backup current binary: %w", err)
    }
    if err := os.Rename(staged, target.BinaryPath); err != nil {
        // Backup is intact, current binary path is empty — restore before giving up.
        _ = os.Rename(backup, target.BinaryPath)
        _ = hooks.Start(ctx)
        return Result{}, fmt.Errorf("update: install new binary: %w", err)
    }

    if err := hooks.Start(ctx); err != nil {
        return rollback(ctx, target, backup, hooks, fmt.Errorf("start: %w", err))
    }
    if err := hooks.HealthCheck(ctx); err != nil {
        return rollback(ctx, target, backup, hooks, fmt.Errorf("health check: %w", err))
    }

    _ = os.Remove(backup) // success: no side-by-side versions (spec requirement 6)
    return Result{RolledBack: false, Version: art.URL}, nil
}

// rollback restores backup over the (broken) new binary and restarts the old
// one. Every step is best-effort past this point — this is the failure
// path's failure path, and Apply's own error (not a secondary error here)
// is what the caller needs to see.
func rollback(ctx context.Context, target Target, backup string, hooks Hooks, cause error) (Result, error) {
    _ = hooks.Stop(ctx)
    _ = os.Rename(backup, target.BinaryPath)
    _ = hooks.Start(ctx)
    return Result{RolledBack: true}, fmt.Errorf("update: %w — rolled back to previous binary", cause)
}
```

`downloadTo`/`verifyChecksum` are small unexported helpers (`io.Copy` to a file;
`sha256.Sum256` over the staged file compared via `hex.EncodeToString`, case-insensitive
compare against `art.SHA256`) — straightforward, no surprises, write directly.

**Judgment call**: `HealthCheck` gets no explicit retry/backoff in this sketch — add a small
bounded retry (e.g. 5 attempts, 500ms apart) inside the default health-check hook you wire in
`cmd/mads` (§6.1), not inside `Apply` itself, so `Apply`'s own tests can use a
zero-delay fake health check without waiting on real timing.

### 3.5 Tests (`pkg/update/apply_test.go`) — this is the done-when proof

Build two tiny fake "binaries" as shell scripts in a `t.TempDir()`:

```sh
# good binary: exits 0 whatever args it gets
#!/bin/sh
exit 0
```
```sh
# broken binary: exits 1 (simulates a crash-on-health-check new build)
#!/bin/sh
exit 1
```

Serve one of them from an `httptest.Server` (the "download" step fetches real bytes over real
HTTP, proving the download path end-to-end, not just checksum math on an in-memory buffer).
Compute its real sha256 for `PlatformArtifact.SHA256`.

```go
func TestApply_HealthySwap(t *testing.T) {
    // good-binary bytes served by httptest.Server, real sha256, Target pointing at a
    // t.TempDir() "current" binary (any placeholder file), Hooks.Stop/Start recording
    // call order, HealthCheck running the *new* binary at target.BinaryPath with
    // exec.Command(target.BinaryPath).Run() and asserting exit 0.
    // Assert: Result.RolledBack == false, target.BinaryPath now IS the good binary's
    // bytes, no ".bak" file left behind, Stop called before Start, Start called before
    // HealthCheck.
}

func TestApply_ForcedFailureRollsBack(t *testing.T) {
    // Same shape but the served/staged binary is the broken one (exits 1).
    // HealthCheck hook: exec.Command(target.BinaryPath).Run(), returns that error.
    // Assert: err != nil, Result.RolledBack == true, target.BinaryPath's bytes are
    // BYTE-IDENTICAL to the original ("current") binary's bytes (proves restore, not
    // just "some file exists"), no ".bak" file left behind, Stop/Start each called
    // twice (once around the failed attempt, once — Stop inside rollback, Start inside
    // rollback — restoring the old one to a running state).
}

func TestApply_ChecksumMismatchNeverSwaps(t *testing.T) {
    // Wrong SHA256 in the artifact. Assert: target.BinaryPath is untouched (still the
    // original bytes), hooks.Start was called once (best-effort put-back, since Stop
    // already ran), Stop/Start hooks otherwise never see a broken binary at all.
}

func TestApply_DataDirUntouched(t *testing.T) {
    // The literal "no session data lost" / "Data Service contents are byte-identical"
    // acceptance criterion, proven directly and cheaply: create a second file elsewhere
    // in t.TempDir() representing "the Data Service's mads.db" with arbitrary bytes,
    // hash it, run TestApply_ForcedFailureRollsBack's forced-failure cycle, hash the
    // "data file" again, assert identical. This is a deliberate, explicit proof that
    // Apply's entire footprint is target.BinaryPath + target.StagingDir — it never
    // touches any other path — rather than an implicit "well it never mentions the data
    // dir so it's probably fine."
}
```

Also unit-test `Check` (`check_test.go`) against a `httptest.Server` fixture (this is
literally "the stub feed server serving a fixture feed" from the task brief — put the
fixture JSON in `pkg/update/testdata/feed.json`): one case with a newer version
(`UpdateAvailable: true`), one with an equal/older version (`false`), one with the server
returning 500 (error propagates, not swallowed).

## 4. Data Service backward-compat (spec §14.4-5)

Per §0, `Migrate`/`Rollback`/`Version` already fully exist and are tested. Two more things,
both small:

### 4.1 `store.MaxVersion` (`services/data/internal/store/migrate.go`, new exported func)

```go
// MaxVersion returns the highest migration version this binary's embedded
// migration set knows about — the schema version this binary is "at home"
// with. Used at boot (services/data/main.go) to detect a binary older than
// the on-disk schema, distinct from Version(db) which reads what's actually
// applied.
func MaxVersion(fsys fs.FS) (int, error) {
    ms, err := loadMigrations(fsys)
    if err != nil {
        return 0, err
    }
    if len(ms) == 0 {
        return 0, nil
    }
    return ms[len(ms)-1].version, nil // loadMigrations returns them sorted ascending
}
```

Test: assert it returns `11` against the real embedded `Migrations()` today (update the
literal if migrations are added later — a small, honest coupling, same as `checker_test.go`'s
"CI runners always have git" assumption elsewhere in this repo) and returns `0` against an
empty fixture `fs.FS`.

### 4.2 The version-gate (`services/data/main.go`)

This is the piece this task actually adds — a boot-time refusal, not an automatic rollback:

```go
// after store.Migrate(db, store.Migrations()) succeeds and version, err := store.Version(db):
maxKnown, err := store.MaxVersion(store.Migrations())
if err != nil {
    log.Fatalf("data: %v", err)
}
if version > maxKnown {
    log.Fatalf("data: on-disk schema is v%d but this binary only knows up to v%d — "+
        "this binary is older than the data it's pointed at; install a newer mads build "+
        "before starting the Data Service against this data directory", version, maxKnown)
}
```

**Why a version-gate and not auto-invoking `Rollback` here**: `Rollback`'s down.sql files are
inherently destructive (several `DROP COLUMN`/`DROP TABLE`, see e.g. `0003_tasks.down.sql`).
Auto-running them the moment an older binary notices a newer schema would silently discard
whatever the newer version's migrations added, with no operator confirmation — the opposite
of "no data lost." The safe default is to refuse to start and say why; `Rollback` remains
available as an explicit, human-invoked recovery primitive (already fully built, already
tested) for whoever operates an actual downgrade, not something this task wires to fire on
its own. This directly answers the task brief's "a DOWN-migration path or a version-gate...
whichever the existing migration code supports" — both exist in the code after this task;
only the gate is automatic, by design.

Test (`services/data/main_test.go` — check whether this file exists; if `main` has no test
file yet, add one, or put this logic in a small testable helper function rather than inline
in `main()` so it's unit-testable without spawning the real binary): seed a temp sqlite file
with `schema_migrations` containing a fake future version row (e.g. `9999`), assert the
gate function returns an error/refusal rather than proceeding. Structure the gate as its own
function (e.g. `checkSchemaCompatible(db *sql.DB, fsys fs.FS) error`) precisely so this is
testable without `log.Fatalf` in the test path.

## 5. Router wiring — version + user-initiated check

### 5.1 `GET /version` (spec MADS-UX §3.7)

Every service binary needs its own version string, same pattern as `cmd/mads/version.go`:

```go
// services/router/version.go (new, mirrors cmd/mads/version.go exactly)
package main

var version = "dev"
```

Handler (`services/router/internal/api/version.go`, new):

```go
type VersionInfo struct {
    Version string `json:"version"`
}

func (s *server) handleVersion(w http.ResponseWriter, r *http.Request) {
    writeJSON(w, http.StatusOK, VersionInfo{Version: s.version})
}
```

`server` gains a `version string` field; `api.New`'s signature gains one more param
(`version string`) — every existing test call site (`api.New(data, sup, jobs, providers,
creds, pf)`) needs one more arg. Grep `api.New(` across `_test.go` files first (same
mechanical update W7.3's HANDOFF §5 walked through for the `pf` param) — pass `"test"` or
similar in tests, doesn't matter what.

`main.go` passes the package `version` var through: `api.New(data, sup, jobs, providers,
creds, pf, version)`.

### 5.2 `POST /update/check` (spec §14.3 — the only user-initiated network call in this task)

```go
// server gains: updateFeed update.Feed  (constructed once at startup from Config's
// UpdateFeedURL — see below on why it's re-read per request, not cached at startup)

func (s *server) handleUpdateCheck(w http.ResponseWriter, r *http.Request) {
    cfg, err := s.data.Config(r.Context())
    if err != nil {
        writeErrMsg(w, http.StatusInternalServerError, "update check: ", err)
        return
    }
    if cfg.UpdateFeedURL == "" {
        writeJSON(w, http.StatusOK, update.CheckResult{
            CurrentVersion: s.version, Detail: "no update feed configured",
        })
        return
    }
    result, err := update.Check(r.Context(), update.HTTPFeed{URL: cfg.UpdateFeedURL}, s.version)
    if err != nil {
        // A feed that's unreachable/misconfigured is data, not a 500 — same
        // "never a hard error" philosophy as pkg/preflight (§0).
        writeJSON(w, http.StatusOK, update.CheckResult{
            CurrentVersion: s.version, Detail: "could not reach update feed: " + err.Error(),
        })
        return
    }
    writeJSON(w, http.StatusOK, result)
}
```

Reading `cfg.UpdateFeedURL` fresh on every request (rather than caching the `Feed` at
startup) means a user who edits the feed URL in Settings' Advanced panel doesn't need to
restart the Router for a subsequent check to use it — cheap correctness, worth it. No new
`server` field for the feed itself; `s.data` (already present) is enough.

Register both routes in `api.go`'s `New`:
```go
mux.HandleFunc("GET /version", s.handleVersion)
mux.HandleFunc("POST /update/check", s.handleUpdateCheck)
```

### 5.3 Tests

`version_test.go`: trivial, asserts the JSON shape. `update_test.go`: table-driven over
`handleUpdateCheck` with a fake `dataclient`-shaped config source if the existing test
harness fakes `s.data` that way (check `preflight_test.go`'s pattern at the router-api layer
first — it may already stub `s.data` via a real `datatest.Start` or a lightweight fake); cover
empty-feed-URL, unreachable-feed, and update-available cases using a local `httptest.Server`
for the feed itself (same fixture as §3.5's `check_test.go`, reused).

## 6. `cmd/mads update` — the CLI surface

### 6.1 `mads update check`

```
mads update check [-data URL]
```

Talks to the Router (not the Data Service directly — `mads run`/`seed`/`tasks` talk to Data
Service because they drive pipeline runs against it directly, but the update-check logic and
the Config it reads live behind the Router, same as `/preflight`). Add a `-router` flag
(default `http://localhost:8080`, matching Router's own default `-addr`) since this is the
first `cmd/mads` subcommand that needs the Router rather than the Data Service. Print current
version, and either "up to date" or the new version + notes. Exit 0 either way (checking is
never a failure condition) — non-zero only on a genuine transport error talking to the
Router itself (distinct from "feed unreachable," which the Router already turns into a
`Detail` string per §5.2, not an HTTP error).

`pkg/dataclient` is Data-Service-shaped (`Client.base` is documented as the Data Service URL
throughout — see its package doc, §0). Don't repurpose it for the Router. Either add a
tiny, separate `routerclient` (a handful of lines: `POST /update/check` → decode
`update.CheckResult`, `GET /version` → decode `VersionInfo`) or, if a Router HTTP client
already exists somewhere in `ui`/other Go call sites by the time you're building this (grep
first — none was found during this design pass), reuse it. A few lines of direct
`http.Client`/`json.Decode` inline in `cmd/mads/update.go` is also fine here; this doesn't
need to be a reusable package for one CLI subcommand's two calls.

### 6.2 `mads update apply`

```
mads update apply [-feed-url URL] [-router http://localhost:8080] [-yes]
```

Orchestrates `pkg/update.Apply` against `Target{BinaryPath: <resolved running mads
executable, via os.Executable()+EvalSymlinks — copy uninstall.go's exact pattern>, StagingDir:
os.TempDir()}`. `-feed-url` overrides Config's stored URL for a one-off apply without needing
Settings open (useful for the smoke test/CI too). Print the manifest's version + notes,
confirm unless `-yes` (mirrors `uninstall.go`'s confirmation pattern exactly — read it again
before writing this).

Real `Hooks`, best-effort per §1.2:

- **Stop**: look for a Router PID file at `$MADS_HOME/router.pid` (new — §6.3 below is the
  one small addition needed to make this real rather than fake). If present and the process
  is alive, send `syscall.SIGTERM` (the exact signal `services/router/main.go`'s own
  `signal.NotifyContext` already handles via `sup.Shutdown`, per §0 — this triggers the real,
  existing graceful-drain path, nothing new on the Router side) and poll until the PID's
  process exits or a bounded timeout elapses. If no PID file, or the process is already gone,
  log "no running router detected — nothing to drain" and return nil (not an error: a bare
  `mads` CLI install with no Router running is the common case, and updating the CLI binary
  alone is still fully meaningful).
- **Start**: if a Router was stopped in `Stop`, attempt to relaunch it by looking for a
  `router` binary next to the (now-updated) `mads` binary (same sibling-binary lookup as
  `services/router/main.go`'s own `defaultChildCmd`, reused/copied — small, deliberate
  duplication, or export a shared tiny helper if you'd rather; either is fine, it's ~10
  lines). If found, spawn it detached (`exec.Command` with `Setsid`/`Start()`, not `Run()` —
  it needs to outlive `mads update apply`'s own process) and write a fresh PID file. If not
  found, log clear guidance ("router binary not found alongside mads — restart it manually")
  and return nil — not fatal, per §1.2.
- **HealthCheck**: run `exec.CommandContext(ctx, target.BinaryPath, "--version").Run()` with
  a 5-attempt/500ms-apart retry (§3.4's judgment call) — reuses the CLI's own existing
  `--version` handling (`cmd/mads/main.go`), no new flag needed. A deliberately broken "new"
  binary for the forced-failure smoke test is simply a fixture that exits non-zero on
  `--version` (or any args) — exactly matching the task brief's own phrasing ("exits non-zero
  on health check").

### 6.3 Router PID file (small, new — `services/router/main.go`)

```go
// after ln, err := net.Listen(...): best-effort, never fatal if it fails
if pidPath := filepath.Join(*madsHome, "router.pid"); pidPath != "" {
    _ = os.WriteFile(pidPath, []byte(strconv.Itoa(os.Getpid())), 0o644)
    defer os.Remove(pidPath)
}
```

Placed right after the listener binds, removed via `defer` on the same clean-shutdown path
that already runs (`main()`'s existing `select`/`srv.Shutdown`/`sup.Shutdown` sequence) — a
crash leaves a stale PID file behind, which `mads update apply`'s `Stop` step already handles
correctly (checks the process is actually alive, not just that the file exists, before
signaling).

### 6.4 Tests (`cmd/mads/update_test.go`)

Unlike `pkg/update`'s tests (§3.5, which don't touch a real Router at all), this file can
stay thin: a test that `mads update check` against a `httptest.Server` standing in for the
Router (serving a fixture `/update/check` response) prints the expected lines, and a test
that `mads update apply` with a fake `MADS_HOME` and **no** PID file present completes
without error (proving the "no Router running" path is a no-op, not a failure) using a real
good/broken fixture "mads" binary for the swap+health-check, same fixtures §3.5 built. Don't
re-test `pkg/update.Apply`'s internals here — that's already fully covered; this file only
proves the CLI wiring and the Stop/Start/HealthCheck hooks compile and behave against real
files.

## 7. Session resume — verification only (spec §14.4's "sessions resume")

No new persistence. Add one test (a good home: `services/router/internal/supervisor`, next
to the existing `supervisor_test.go`, since that's exactly the layer that already proves
"child exit releases leases / sets Activity" today) that walks through what an update's
graceful-stop-then-restart actually does to a session end to end:

1. Start a real Data Service (`datatest.Start`), create a session + a task, spawn a Session
   Service child under it (`Supervisor.SpawnSession` — real supervisor, real child, same as
   `supervisor_test.go`'s existing tests).
2. Call `Supervisor.TerminateSession` (the exact call `Supervisor.Shutdown` makes internally
   for every tracked child) — the "graceful agent stop" step.
3. Assert via the Data Service client: the session's `Activity` is `Inactive`, the task and
   its worktree-relevant fields are unchanged from before termination (byte/field comparison
   against what was read before step 2).
4. `Supervisor.SpawnSession` again under the same session ID (simulating "restart services").
5. Assert the new child comes up (non-empty `Addr`, matching `SessionPID`/`SessionAddr`
   lookups the existing tests already use) and the Data Service still reports the same task
   state — nothing was lost or reset by the stop/restart cycle.

This is the whole deliverable for this section — if it passes, "sessions resume" is proven
using only APIs that exist today (§0), which is exactly what the task brief asks for
("verify resume works rather than building new persistence").

## 8. Frontend — `UpdatesAboutPanel` goes real

### 8.1 `ui/src/router/types.ts` (new types, mirror `PreflightCheck`'s comment style)

```ts
// mirrors services/router/internal/api/version.go VersionInfo
export interface VersionInfo {
  version: string;
}

// mirrors pkg/update.CheckResult
export interface UpdateCheckResult {
  current_version: string;
  latest_version?: string;
  update_available: boolean;
  notes?: string;
  detail?: string;
}
```

### 8.2 `ui/src/router/client.ts` (new methods, same shape as `getPreflightChecks`)

```ts
getVersion(): Promise<VersionInfo>;
checkForUpdate(): Promise<UpdateCheckResult>;
// impl:
async getVersion(): Promise<VersionInfo> { return routerGet(this.baseUrl, "/version"); }
async checkForUpdate(): Promise<UpdateCheckResult> {
  return routerPost(this.baseUrl, "/update/check", {});
}
```

Check whichever `routerPost`-equivalent helper this file already uses for other POST-with-
no-meaningful-body actions (e.g. `handleStartRetro`'s client method) and match it exactly.

### 8.3 `ui/src/router/fixtures.ts`

```ts
async getVersion(): Promise<VersionInfo> { return { version: "0.1.0-dev" }; }
async checkForUpdate(): Promise<UpdateCheckResult> { return fixtureUpdateCheckResult; }

export const fixtureUpdateCheckResult: UpdateCheckResult = {
  current_version: "0.1.0-dev",
  update_available: false,
  detail: "no update feed configured",
};
```

### 8.4 `ui/src/features/settings/state/useUpdateCheck.ts` (new)

**Deliberately does not mirror `usePreflight`'s auto-fetch-on-mount behavior** — that's the
one difference called out in §0. `getVersion()` is safe to call on mount (it's a local,
in-process value, no external network call — no phone-home concern). `checkForUpdate()` must
only ever fire from a direct user action (button `onClick`), never from a `useEffect`. Keep
these as two separate pieces of state precisely so a future refactor can't accidentally
collapse them into one auto-firing hook:

```ts
export interface UseUpdateCheck {
  version: VersionInfo | null;
  result: UpdateCheckResult | null;
  checking: boolean;
  check: () => void; // user-triggered only
}

export function useUpdateCheck(): UseUpdateCheck {
  const [version, setVersion] = useState<VersionInfo | null>(null);
  const [result, setResult] = useState<UpdateCheckResult | null>(null);
  const [checking, setChecking] = useState(false);

  useEffect(() => {
    router.getVersion().then(setVersion); // local info only — not a feed check
  }, []);

  const check = useCallback(() => {
    setChecking(true);
    router.checkForUpdate().then((r) => {
      setResult(r);
      setChecking(false);
    });
  }, []);

  return { version, result, checking, check };
}
```

Test (`useUpdateCheck.test.ts`, mirroring `usePreflight.test.ts`'s harness): assert
`checkForUpdate` is **not** called until `check()` is invoked — this is the test that actually
proves the "user-initiated only" acceptance criterion at the frontend layer; don't skip it.

### 8.5 `UpdatesAboutPanel.tsx`

Replace the hardcoded `VERSION` const and disabled button with `useUpdateCheck()`: render
`version?.version` where the hardcoded string was, enable the button (`onClick={check}`,
`disabled={checking}`), and render `result` once present — "You're on the latest version" when
`!result.update_available`, or the new version + `notes` when it is, or `result.detail` when
present (covers both "no feed configured" and "feed unreachable" without the UI needing to
distinguish them). Update `UpdatesAboutPanel.test.tsx` accordingly (assert click → loading →
result rendering, using the fixture client).

## 9. Testing plan → acceptance criteria map

- "Update smoke... check → download → checksum → swap → healthy restart passes" →
  `pkg/update/apply_test.go`'s `TestApply_HealthySwap` (§3.5) is the core proof; `cmd/mads/
  update_test.go` (§6.4) proves the CLI wiring compiles and runs against real fixture files.
- "a forced mid-update failure rolls back cleanly and the Data Service contents are byte-
  identical after rollback" → `TestApply_ForcedFailureRollsBack` + `TestApply_DataDirUntouched`
  (§3.5), verbatim the done-when criterion.
- "Version check only fires on explicit user action" → `useUpdateCheck.test.ts`'s explicit
  assertion (§8.4) at the frontend layer, plus `Check`'s own design (§3.3) having no
  background-firing code path to begin with at the backend layer — both layers, not just one.
- "make check passes" → `go test -race ./...` (new `pkg/update`, `services/data` gate test,
  router handler tests, `cmd/mads` tests), `golangci-lint run ./...`, `npm run lint && npm run
  test` (new/updated frontend tests). Every new Go file gets a sibling `_test.go`.

## 10. README updates

- `pkg/update` has no README (matches `pkg/preflight` — HANDOFF.md is enough for a leaf
  package); this file stays here as the reference for anyone touching it later.
- `services/router/README.md`'s "API contract" section: add `GET /version` and `POST
  /update/check` bullets near the existing `GET /preflight` one (same "environment/meta"
  grouping W7.3 used) — same "only add this task's own routes" discipline W7.3's HANDOFF
  documented (don't backfill other pre-existing missing routes while you're in this file).
- `services/data/README.md`: add a short note on the boot-time version-gate (§4.2) near
  wherever migrations are already documented, and confirm/point at `Rollback` as the existing
  explicit-recovery primitive.
- `cmd/mads`'s own usage banner (`main.go`'s `usage()` func) and package doc comment: add the
  `mads update check`/`mads update apply` lines, same list style as the existing `mads
  uninstall` entry.
- `scripts/README.md`: if it documents `install.sh`'s `MADS_VERSION`/release-tag mechanics,
  add a sentence noting `mads update apply` is the in-place-upgrade path once a binary is
  already installed, distinct from re-running `install.sh`.
- Root `README.md`: add `mads update check`/`apply` to wherever the CLI subcommand list is
  documented (check its "mads builds mads" section first).

## 11. Risks / judgment calls for human review

1. **`mads update apply`'s Router discovery (PID file + sibling binary) is best-effort by
   design** (§1, §6.2) — for the common "just the `mads` CLI is installed" case it degrades to
   updating the CLI binary alone with clear log guidance, not a full graceful multi-service
   restart. This is an honest reflection of what's actually shippable today (no release
   packaging exists for the other services yet — confirmed via W7.2's own HANDOFF), not a
   shortcut taken carelessly. Flag it explicitly in the PR description so a reviewer doesn't
   assume more was built than was.
2. **`pkg/update`'s `Feed`/`Check`/`Apply` core is fully real and fully tested** — no risk
   there; this is the part that actually satisfies the forced-failure/rollback done-when
   criterion, independent of judgment call #1.
3. **Version-gate vs. auto-rollback for schema compatibility** (§4.2) is a deliberate safety-
   first choice, documented inline — worth a second pair of eyes given it's a "refuse to
   start" behavior change to a service that currently always starts.
4. **`compareSemver` duplicated from `pkg/preflight`** (§3.3) — same repo-precedented small
   duplication, flagged in case a reviewer wants it deduplicated into a shared leaf package
   instead; not done here to avoid an unrequested refactor of `pkg/preflight`.
5. **No code signing / no platform packaging** — explicitly out of scope per the task brief;
   `verifyChecksum` (§3.4) is the only integrity check, matching spec requirement 4's actual
   text ("Downloads are checksum-verified") rather than a broader supply-chain story.

## 12. Executor notes (W7.5 implementation pass)

Implemented all of §0-§11 as designed, with one deliberate deviation from §7's literal
wording, documented below. `make check` is green (`go test -race ./...`,
`golangci-lint run ./...`, `npm run lint`, `npm run test`).

**What was built, matching the plan directly:**

- `pkg/update` (§3): `manifest.go`, `feed.go`, `check.go`, `apply.go` — Feed/Check/Apply are
  fully real, tested against `httptest.Server` fixtures and real shell-script fake binaries
  with real sha256 checksums (no mocking of the download/verify/swap/health-check/rollback
  path). `TestApply_ForcedFailureRollsBack` and `TestApply_DataDirUntouched` are the
  done-when tests: a broken binary's failed health check triggers rollback, and a
  co-located "Data Service" file's bytes are untouched throughout.
- `contracts.Config.UpdateFeedURL` (§2) — empty by default, validated as an absolute
  http(s) URL when set.
- Data Service backward-compat (§4) — `store.MaxVersion` + `checkSchemaCompatible`
  (`services/data/compat.go`), wired into `main.go` right after `store.Migrate`. Refuses to
  boot (not an auto-rollback) when the on-disk schema is ahead of what the binary knows —
  per §4's explicit safety rationale (down.sql migrations are destructive).
- Router wiring (§5) — `GET /version`, `POST /update/check`, both following the
  "never a hard error, always data" convention `/preflight` set: a missing/unreachable feed
  comes back 200 with a `Detail` string, never a 5xx.
- `cmd/mads update check`/`apply` (§6) — Router-PID-file-based best-effort coordination,
  exactly as scoped in §1/§11 judgment call #1: with no Router running, `apply` degrades to
  updating the CLI binary alone with an explicit log line, not a failure.
- Frontend (§8) — `useUpdateCheck.ts` fetches `GET /version` on mount (safe, local) and
  only calls `POST /update/check` from `check()`, i.e. in response to the button click.
  `useUpdateCheck.test.ts` asserts `/update/check` is never called until `check()` runs —
  the frontend-layer proof of "user-initiated only" the acceptance criteria ask for.

**Deviation from §7: the session-resume test does not assert `Activity == Inactive`.**

§7 step 3 says to assert the session's `Activity` flips to `Inactive` after
`Supervisor.TerminateSession`, using the same `testChildBin` harness as the rest of
`supervisor_test.go`. That assertion doesn't hold with `testChildBin`: flipping `Activity`
on a graceful exit is `services/session/internal/runner.Shutdown`'s job, not the
Supervisor's — `onSessionExit` in `supervisor.go` only sets `Activity = Inactive` itself for
an *unexpected* (crash) exit; a expected/graceful `TerminateSession` exit relies entirely on
the real Session Service child having set it before exiting, and `testChildBin` (a bare fake
spawn target, per `internal/testchild/main.go`) never does.

I considered building and spawning the real `services/session` binary for this one test
instead (mirroring `TestMain`'s `testChildBin` build). That doesn't work either: the
`fake` provider's session history (`providers/fake.Fake.histories`) lives in that process's
memory, so a second, freshly-spawned `services/session` process can never resume the first
process's `ProviderRef` — `Runner.Start` fails before it even opens its listener
(`resume: unknown session ref`), which blocks `SpawnSession` entirely. Real cross-process
provider resume only exists for providers with their own on-disk resume state
(claudecode/kirocli via the real CLI), not the in-memory test double, so proving it this way
would require standing up a real provider CLI in this test — out of scope for W7.5 and a
capability gap in `providers/fake`, not a bug in this feature.

`TestSessionResumeAfterGracefulStopRestart` (`services/router/internal/supervisor/
supervisor_test.go`) therefore proves what the Supervisor + Data Service layer actually
guarantee across a `TerminateSession` → `SpawnSession` cycle — task state and session
worktree fields survive untouched, and the Supervisor can retrack a session under the same
ID — with a comment pointing at `TestShutdownReleasesLeases`
(`services/session/internal/runner/runner_test.go`) as where the `Activity` flip is actually
covered, since that's a real Session Service behavior, not a Supervisor one. **Reviewer:
please double check this call** — an alternative would be a dedicated end-to-end test that
spawns a real `claude-code`/`kiro-cli` session with resume state, but that's a much heavier
test (needs a real provider CLI or an extension to `providers/fake` to persist history
across processes) and felt out of proportion to what "verify resume works... using only
APIs that exist today" (§7's own words) was asking for.

**Other small deviations, all previously flagged in §11 and unchanged by this pass:**
`compareSemver` duplicated from `pkg/preflight` (§11.4), best-effort Router coordination
(§11.1), no code signing/platform packaging (§11.5).
