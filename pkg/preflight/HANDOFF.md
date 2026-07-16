# HANDOFF — W7.3 Preflight & provider compatibility

Architect stage output. Executor: read this fully before writing code. The good news:
almost all of the *surface* for this work item already exists — W6.10 built the Router
route, the DI seam, and a fully generic UI (Settings + wizard) against a `StubChecker`,
explicitly so W7.3 could land as a pure swap-in later (`pkg/preflight/preflight.go`'s own
doc comment says this). Confirmed by reading `ui/src/features/settings/HANDOFF.md` §2.5
and §11 and grepping the whole tree. **This task is almost entirely backend**, plus two
small, cheap test/fixture gaps on the frontend that are worth closing while we're here.

## 0. What already exists — do not rebuild these

- `pkg/preflight/preflight.go`: `Check{Name, Healthy, Detail, FixHint}` (JSON tags already
  match what the UI consumes), `Checker` interface, `StubChecker`. **Keep this file as-is**
  — `Check`/`Checker` are the real contract, `StubChecker` stays for tests/dev fallback.
- `services/router/internal/api/preflight.go` + `api.go`: `GET /preflight` →
  `s.preflight.Run(ctx)` → `writeJSON`. `api.New`'s `preflightChecker` param already
  defaults to `StubChecker{}` when nil — **`api.New`'s signature does not need to change**.
- Frontend, entirely built and generic (renders whatever `Check` rows come back, no
  hardcoded names): `ui/src/router/types.ts`'s `PreflightCheck`, `ui/src/router/client.ts`'s
  `getPreflightChecks`, `ui/src/features/settings/state/usePreflight.ts`,
  `ui/src/features/settings/panels/GeneralPanel.tsx`'s "Preflight" section,
  `ui/src/onboarding/steps/PreflightStep.tsx`. All render ✓/✗ per row plus `fix_hint` when
  unhealthy. **No component changes needed** — §5 below covers the two small additions that
  are worth making anyway.

## 1. Scope of the real work

1. A real `Checker` implementation in `pkg/preflight` (new files alongside the existing
   one) covering exactly the work item's checklist — **no more**:
   - git present + version
   - Data Service reachable
   - disk space at the worktree root sane
   - each *registered* provider's binary present + version, evaluated against a
     compatibility matrix
2. Wire it into `services/router/main.go` in place of the implicit `nil`→`StubChecker`.
3. A tiny `dataclient.Client.Health` method (new — reachability probe).
4. Seeded-broken-PATH test proving the done-when criterion.
5. Two small, genuinely-missing frontend tests (§5) + one stale fixture/doc-comment cleanup.
6. README: `services/router/README.md` (add the `GET /preflight` bullet — it's missing
   today, along with several other W6.10-era routes; **do not** scope-creep into fixing
   those, only add this task's own route) and `ui/README.md` (the one sentence describing
   preflight as "stubbed" needs updating).

**Deliberately dropped from the stub's original 3-row placeholder**: `node-pty`. It was
never in this work item's scope list (re-read the task brief: binaries, Data Service,
git, disk space — no mention of node-pty), and it isn't something the Go Router process
can meaningfully probe anyway — it's a native Node module bundled *inside* the Electron
UI at build time, not a separately-installed dependency on PATH like git/claude. If a
runtime PTY-health signal is ever needed, that's a session-scoped concern for a different
work item, not a global preflight row. Flagging this explicitly so it doesn't look like an
oversight.

**Deliberately out of scope**: kiro-cli. `providers/kirocli` doesn't exist yet (W7.1, not
landed — confirmed via `find`/`grep`, only `providers/README.md` and the work-items doc
mention it). Design the provider-binary check as a lookup table keyed by provider name
(§3) so adding kiro's entry later is a 4-line addition, not a redesign — that's what "kiro
if declared" in the task brief means: check it *if and when* it's registered, don't
speculatively build against a binary that doesn't exist.

## 2. `dataclient.Client.Health` (new, small)

`pkg/dataclient/dataclient.go` already has the `do` helper and every service's Data
Service exposes `GET /health` (`services/data/internal/api/api.go:71`, 200 healthy / 503
DB-unreachable). Add:

```go
// Health probes Data Service reachability (used by pkg/preflight). Returns nil on a 2xx
// response, an error otherwise — do already treats a non-2xx/404/409/422 status as an
// error via its default branch, which is exactly the "unreachable or unhealthy" signal
// this needs.
func (c *Client) Health(ctx context.Context) error {
	return c.do(ctx, http.MethodGet, "/health", nil, nil)
}
```

Test in `pkg/dataclient/dataclient_test.go`, matching the file's existing "round-trip
against a REAL Data Service (`datatest.Start`), no mocks" convention: one test asserting
`Health` returns nil against a running `datatest.Start(t)` instance, and one asserting it
returns a non-nil error against a client pointed at a closed/nonexistent port (e.g.
`dataclient.New("http://127.0.0.1:1", "test")` — connection refused).

## 3. Real `Checker` — `pkg/preflight/checker.go` (new)

Keep this decoupled from concrete Router types so it's unit-testable without spinning up
real services — two tiny interfaces, both satisfied by existing types with zero adapter
glue:

```go
package preflight

import "context"

// DataHealth is satisfied by *dataclient.Client (its new Health method, §2). A separate
// interface here — not an import of pkg/dataclient's concrete type — keeps this package
// testable with a fake and keeps the dependency direction (preflight is a leaf package,
// doesn't need dataclient's full surface).
type DataHealth interface {
	Health(ctx context.Context) error
}

// ProviderLister is satisfied by *provider.Registry (its existing Names() []string) —
// again, the minimal slice of that type's surface this package actually needs.
type ProviderLister interface {
	Names() []string
}

// Deps wires the real Checker to its live counterparts.
type Deps struct {
	Data      DataHealth
	Providers ProviderLister
	// MadsHome is the worktree/credentials root (services/router/main.go's
	// defaultMadsHome() — same value already passed to credstore.Open there).
	// Disk space is checked against this path, not the binary's own filesystem.
	MadsHome string
}

// New returns the real environment-health Checker (§14.8/14.9). Deps' zero value is
// usable in tests where a given check isn't exercised (e.g. Data == nil skips straight to
// an unhealthy "no client configured" row rather than a nil-pointer panic).
func New(deps Deps) Checker { return &realChecker{deps: deps} }

type realChecker struct{ deps Deps }

func (c *realChecker) Run(ctx context.Context) ([]Check, error) {
	checks := []Check{
		checkGit(ctx),
		checkDiskSpace(c.deps.MadsHome),
		checkDataService(ctx, c.deps.Data),
	}
	for _, name := range c.deps.Providers.Names() {
		if bin, ok := knownProviderBinaries[name]; ok {
			checks = append(checks, checkProviderBinary(ctx, name, bin))
		}
	}
	return checks, nil
}
```

`Run`'s own `error` return is reserved for "the whole run could not happen" — nothing in
this design ever hits that; every individual probe failure becomes an unhealthy `Check`
row instead (that's the entire point of the feature — a broken environment is *data*, not
a 500). Always return `nil` for the error.

### 3.1 git (`pkg/preflight/git.go` or inline in `checker.go`, executor's call)

```go
func checkGit(ctx context.Context) Check {
	path, err := exec.LookPath("git")
	if err != nil {
		return Check{
			Name: "git", Healthy: false,
			Detail:  "not found on PATH",
			FixHint: "install git (https://git-scm.com/downloads) — required for workspace and worktree operations",
		}
	}
	out, err := runVersion(ctx, path, "--version")
	if err != nil {
		return Check{Name: "git", Healthy: false, Detail: "found at " + path + " but --version failed: " + err.Error(), FixHint: "reinstall git"}
	}
	return Check{Name: "git", Healthy: true, Detail: strings.TrimSpace(out)}
}

// runVersion runs bin with args under a bounded timeout and returns combined
// stdout — every probe in this package is a quick local subprocess call, never
// network-dependent, so 5s is generous, not a real constraint.
func runVersion(ctx context.Context, bin string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, args...).CombinedOutput()
	return string(out), err
}
```

### 3.2 Disk space (worktree root)

```go
func checkDiskSpace(madsHome string) Check {
	dir := nearestExistingAncestor(madsHome)
	var stat syscall.Statfs_t
	if err := syscall.Statfs(dir, &stat); err != nil {
		return Check{Name: "disk_space", Healthy: false, Detail: "could not stat " + dir + ": " + err.Error(), FixHint: "verify " + dir + " is on a mounted, readable filesystem"}
	}
	// stat.Bavail/Bsize are int64 on darwin, uint64 on linux — explicit conversion
	// is required on at least one GOOS, so keep it on both rather than build-tag-fork.
	free := uint64(stat.Bavail) * uint64(stat.Bsize)
	const minFree = 1 << 30 // 1 GiB — isolated pipeline runs create full working-tree copies under this root (services/pipeline/engine's WorktreeRoot)
	detail := fmt.Sprintf("%.1f GiB free at %s", float64(free)/(1<<30), dir)
	if free < minFree {
		return Check{Name: "disk_space", Healthy: false, Detail: detail, FixHint: "free up disk space under " + dir + " — isolated pipeline runs need room for full working-tree copies"}
	}
	return Check{Name: "disk_space", Healthy: true, Detail: detail}
}

// nearestExistingAncestor climbs from path until it finds a directory that
// actually exists — MadsHome itself may not exist yet on a fresh install
// (the wizard runs before anything has written to it).
func nearestExistingAncestor(path string) string {
	for path != "" && path != "/" && path != "." {
		if _, err := os.Stat(path); err == nil {
			return path
		}
		path = filepath.Dir(path)
	}
	return "/"
}
```

Uses the standard-library `syscall` package (no new dependency — `golang.org/x/sys` isn't
used anywhere in the repo today and isn't needed here; `syscall.Statfs`/`Statfs_t` exist on
both `linux` and `darwin` GOOS with compatible-enough field types that `uint64(...)`
conversion works unmodified on both — this is macOS+Linux only per §14 requirement 10,
Windows explicitly not required at launch, so no build-tag fork needed).

Test: assert the row is healthy against `t.TempDir()` (CI machines have far more than 1
GiB free — don't attempt to simulate exhaustion, there's no portable, non-flaky way to do
that in a unit test). Separately test `nearestExistingAncestor` climbs correctly from a
path with non-existent leaf components.

### 3.3 Data Service reachability

```go
func checkDataService(ctx context.Context, d DataHealth) Check {
	if d == nil {
		return Check{Name: "data_service", Healthy: false, Detail: "no client configured", FixHint: "internal error — report this"}
	}
	if err := d.Health(ctx); err != nil {
		return Check{Name: "data_service", Healthy: false, Detail: err.Error(), FixHint: "start the Data Service (make -C services/data run) or check the Router's -data-url flag"}
	}
	return Check{Name: "data_service", Healthy: true}
}
```

Test with a small fake (`type fakeData struct{ err error }` implementing `DataHealth`) for
both branches — no need to spin up a real Data Service here, that round-trip is already
covered by §2's `dataclient` test.

### 3.4 Provider binaries + compatibility matrix

`pkg/preflight/providers.go` (new):

```go
// providerBinary describes how to detect and version-probe one provider's underlying
// binary. Keyed by provider.Provider.Name() ("claude-code", ...) in knownProviderBinaries
// below — only providers *actually registered* in the Router's provider.Registry are ever
// probed (the "if declared" in the task brief), and only if this package knows how.
type providerBinary struct {
	Bin          string
	VersionArgs  []string
	ParseVersion func(output string) (version string, ok bool)
	InstallHint  string
}

// knownProviderBinaries is deliberately small and honest: one entry per provider adapter
// this repo actually ships (providers/claudecode). Add kiro-cli's entry here when W7.1
// lands that adapter — nothing else in this file changes.
var knownProviderBinaries = map[string]providerBinary{
	"claude-code": {
		Bin:          "claude",
		VersionArgs:  []string{"--version"},
		ParseVersion: parseSemverish,
		InstallHint:  "install Claude Code (https://docs.claude.com/en/docs/claude-code) and ensure `claude` is on PATH",
	},
}

func checkProviderBinary(ctx context.Context, name string, spec providerBinary) Check {
	checkName := "provider:" + name
	path, err := exec.LookPath(spec.Bin)
	if err != nil {
		return Check{Name: checkName, Healthy: false, Detail: spec.Bin + " not found on PATH", FixHint: spec.InstallHint}
	}
	out, err := runVersion(ctx, path, spec.VersionArgs...)
	if err != nil {
		return Check{Name: checkName, Healthy: false, Detail: "found at " + path + " but version check failed: " + err.Error(), FixHint: spec.InstallHint}
	}
	version, ok := spec.ParseVersion(out)
	if !ok {
		return Check{Name: checkName, Healthy: true, Detail: "found at " + path + "; version could not be parsed from output"}
	}
	if compat, known := compatMatrix[name]; known {
		if ok, guidance := compat.evaluate(version); !ok {
			return Check{Name: checkName, Healthy: false, Detail: name + " v" + version + " detected", FixHint: guidance}
		}
	}
	return Check{Name: checkName, Healthy: true, Detail: name + " v" + version}
}
```

`pkg/preflight/compat.go` (new) — the versioned compatibility table (§14.9):

```go
// compatRange is one provider's proven-compatible version window against this mads
// build. Max is exclusive; an empty Min/Max means no known floor/ceiling yet (untested,
// not declared incompatible — don't manufacture a warning from absence of data).
type compatRange struct {
	Min, Max string
	Note     string
}

// compatMatrix is seeded honestly: only claude-code has actually been run through
// providers/conformance at a known version, and only the 2.1.x line has been proven.
// Add a row only once a provider/version pair has actually been exercised — this table
// is a record of what's proven, not a prediction of what will probably work. When mads
// itself starts cutting versioned releases (W7.5), this becomes keyed by mads version
// too; until then there is exactly one build to be compatible with, so provider name is
// the whole key.
var compatMatrix = map[string]compatRange{
	"claude-code": {Min: "2.1.0", Max: "2.2.0", Note: "proven against providers/conformance"},
}

func (r compatRange) evaluate(version string) (ok bool, guidance string) {
	if r.Min != "" && compareSemver(version, r.Min) < 0 {
		return false, fmt.Sprintf("mads requires >= %s, detected %s — upgrade the provider", r.Min, version)
	}
	if r.Max != "" && compareSemver(version, r.Max) >= 0 {
		return false, fmt.Sprintf("mads has only been proven against < %s, detected %s — this version may include unproven breaking changes, check release notes before relying on it", r.Max, version)
	}
	return true, ""
}
```

`pkg/preflight/semver.go` (new) — deliberately minimal, not a general semver library (no
pre-release/build-metadata handling; provider CLI `--version` output doesn't use those):

```go
var versionPattern = regexp.MustCompile(`\d+(?:\.\d+){1,2}`)

// parseSemverish extracts the first "N.N" or "N.N.N" substring from free-form --version
// output (e.g. "2.1.3 (Claude Code)" -> "2.1.3"). Real claude-code output format wasn't
// verifiable in this environment (no live binary) — this is deliberately permissive; if
// the real format differs, this still finds the version number, it just needs the
// leading text to not itself contain a digit sequence that looks like one (unlikely).
func parseSemverish(output string) (string, bool) {
	m := versionPattern.FindString(output)
	return m, m != ""
}

// compareSemver compares dotted version strings by numeric component, left to right,
// missing trailing components treated as 0 ("2.1" == "2.1.0"). Returns -1/0/1.
func compareSemver(a, b string) int {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(as) || i < len(bs); i++ {
		var an, bn int
		if i < len(as) {
			an, _ = strconv.Atoi(as[i])
		}
		if i < len(bs) {
			bn, _ = strconv.Atoi(bs[i])
		}
		if an != bn {
			if an < bn {
				return -1
			}
			return 1
		}
	}
	return 0
}
```

**Judgment call, flagging for review**: `claude --version`'s actual output format
couldn't be verified against a real binary in this environment. `parseSemverish`'s regex
is permissive enough to survive most reasonable formats, and `providers/claudecode_test.go`
already has a `MADS_CLAUDE_SMOKE=1` live-binary opt-in smoke test pattern
(`claudecode_test.go:183`) — consider adding one assertion there (or a small standalone
one gated the same way) that runs the real `claude --version` and confirms
`parseSemverish` extracts something sane, the next time someone has a real binary handy.
Not blocking this task — the seeded-broken-PATH test (§4) doesn't depend on real binary
output at all.

## 4. The seeded-broken-dependency test (done-when criterion)

`pkg/preflight/checker_test.go` (new) — this is the test that proves the work item's own
acceptance criterion, at the layer where the logic actually lives (the Router's
`handlePreflight` is a two-line passthrough already; no separate HTTP-level test is
needed to prove this — `GET /preflight` mechanically returns whatever `Run` returns,
already true since W6.10 and unchanged by this task):

```go
func TestRun_BrokenPATH(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // empty dir on PATH — git and claude both unresolvable
	c := New(Deps{
		Data:      fakeData{},                              // healthy
		Providers: fakeProviders{names: []string{"claude-code", "fake"}},
		MadsHome:  t.TempDir(),
	})
	checks, err := c.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	git := findCheck(t, checks, "git")
	if git.Healthy || git.FixHint == "" {
		t.Errorf("git check = %+v, want unhealthy with fix guidance", git)
	}
	provider := findCheck(t, checks, "provider:claude-code")
	if provider.Healthy || provider.FixHint == "" {
		t.Errorf("provider:claude-code check = %+v, want unhealthy with fix guidance", provider)
	}
	// "fake" has no knownProviderBinaries entry — no row for it, and it must not panic.
	for _, c := range checks {
		if c.Name == "provider:fake" {
			t.Errorf("unexpected row for unprobeable provider: %+v", c)
		}
	}
}
```

(`fakeData`/`fakeProviders`/`findCheck` are small test-local helpers — `fakeData.Health`
returns nil, `fakeProviders.Names()` returns its configured slice.)

Also cover, in the same file or split sensibly: the data-service-unhealthy branch (§3.3),
the disk-space-healthy branch (§3.2), and one "everything healthy" happy-path test with
git present (CI runners always have git — safe to assert on the real PATH there) to prove
the whole `Run` wiring end-to-end, not just the broken branch.

## 5. Router wiring (`services/router/main.go`)

`newMux` currently hardcodes `nil` for `api.New`'s preflight param (main.go:71). Thread
a real one through:

```go
func newMux(data *dataclient.Client, sup *supervisor.Supervisor, jobs *job.Runner, providers *provider.Registry, creds *credstore.Store, pf preflight.Checker) http.Handler {
	return api.New(data, sup, jobs, providers, creds, pf)
}
```

In `main()`, after `providers`/`madsHome` are both in scope:

```go
pf := preflight.New(preflight.Deps{Data: data, Providers: providers, MadsHome: *madsHome})
...
srv := &http.Server{Handler: newMux(data, sup, jobs, providers, creds, pf)}
```

`data` (`*dataclient.Client`) already satisfies `preflight.DataHealth` once §2 lands;
`providers` (`*provider.Registry`) already satisfies `preflight.ProviderLister` — zero
adapter code needed, both types are passed directly.

Update `main_test.go`'s one call site (`newMux(data, sup, jobs, providers, creds)` →
add `nil` as the sixth arg — that test only asserts `/health`, not `/preflight`, so the
stub fallback is fine there). `services/router/internal/api/*_test.go`'s several
`api.New(..., nil)` call sites are untouched — `api.New`'s signature doesn't change (§0).

## 6. Frontend: two small, genuinely-missing tests + one stale comment (cheap, in scope)

Confirmed by reading every preflight-adjacent test file: **no test anywhere exercises the
unhealthy-row rendering path** (`healthy: false` + `fix_hint`) — every existing test
(`PreflightStep.test.tsx`, `GeneralPanel.test.tsx`) only ever renders
`fixturePreflightChecks`, which is all-healthy. The component code already handles it
(`{!check.healthy && check.fix_hint && <span>...}` in both `PreflightStep.tsx` and
`GeneralPanel.tsx`) — it's just never been asserted. This is exactly the acceptance
criterion's "wizard shows failing checks" / "Settings shows the preflight report" — worth
actually proving now that the backend can really produce a failing row, not before.

1. `ui/src/router/fixtures.ts`: update `fixturePreflightChecks`'s stale
   `"stubbed — real check pending W7.3"` detail strings (now false — W7.3 is this task),
   and add one unhealthy row for the new tests to target, e.g.:
   ```ts
   export const fixturePreflightChecks: PreflightCheck[] = [
     { name: "git", healthy: true, detail: "git version 2.43.0" },
     { name: "data_service", healthy: true },
     { name: "provider:claude-code", healthy: false, detail: "not found on PATH", fix_hint: "install Claude Code and ensure `claude` is on PATH" },
   ];
   ```
2. `PreflightStep.test.tsx`: add a test asserting the unhealthy row renders its `fix_hint`
   text and the ✗ marker, using the updated fixture. Keep the existing "Continue always
   enabled" test as-is (that behavior is still correct — see §7).
3. `GeneralPanel.test.tsx`: add the equivalent assertion for the Settings-panel rendering.

Do **not** change `PreflightStep.tsx`/`GeneralPanel.tsx`/`usePreflight.ts` themselves —
they already do the right thing; this is test coverage catching up to real backend data,
not a behavior change.

## 7. One doc/comment cleanup, no logic change

`PreflightStep.tsx`'s doc comment ("Against the StubChecker everything reports healthy...")
and `ui/README.md`'s "against the stubbed preflight backend everything reports healthy, so
Continue is never gated" sentence are now inaccurate framing (the backend is real as of
this task) even though the *behavior* they describe (Continue is never gated on preflight
failures) is still correct and should **stay** correct — re-read §14 of REBUILD-SPEC: this
is a health check with fix guidance, not a hard gate; nothing in the spec says the wizard
should block a user who can't currently pass preflight (they may fix it after finishing
setup). Update both comments to describe *why* non-gating is still the right call now that
checks are real ("informational — a user with a real environment problem still needs to
reach Settings to see the guidance and fix it, not get stuck in the wizard"), not just
leave stale "it's a stub" reasoning in place.

## 8. README updates

- `services/router/README.md`'s "API contract" bullet list: add a `GET /preflight` entry
  near `GET /health` (they're conceptually adjacent — both are environment-probe routes),
  e.g.: `` `GET /preflight` — per-check environment health (git, Data Service reachability,
  disk space, registered provider binaries + versions against `pkg/preflight`'s
  compatibility matrix), each with `{name, healthy, detail?, fix_hint?}` (spec §14.8–9); no
  request body, always 200 unless the checker itself errors (never does, today). `` Do
  **not** attempt to backfill this file's other missing W6.10-era routes (`/config`,
  `/providers`, `/credentials`, etc.) — confirmed genuinely absent from this README today,
  but that's a pre-existing gap outside this task's scope; adding only what this task
  itself introduces keeps the diff honest about what changed here.
- `ui/README.md`: fix the one sentence in "Settings & first-run" per §7 above.

## 9. Testing plan → acceptance criteria map

- "`GET /preflight` returns per-check status + guidance" → covered mechanically once §5
  wires the real checker in; the actual logic is proven by §4's `pkg/preflight` tests
  (the handler itself has no new logic to test).
- "a test with a broken PATH yields the broken row with actionable guidance" → §4's
  `TestRun_BrokenPATH`, verbatim.
- "Settings shows the preflight report; wizard shows failing checks" → already true by
  construction (§0) for the *rendering*; §6 adds the tests that actually prove it against
  real failing data rather than only the all-healthy fixture.
- "make check passes" → `go test -race ./...` (new `pkg/preflight` tests, new
  `dataclient` test, updated `main_test.go`), `golangci-lint run ./...`, `npm run lint &&
  npm run test` for the two updated frontend test files + fixture. Every new Go file gets
  a sibling `_test.go`, matching every prior HANDOFF's convention.

## 10. Risks / judgment calls for human review

1. **`claude --version` output format unverified** (§3, end) — the regex-based parser is
   permissive but untested against a real binary in this environment. Low risk (a
   mis-parse degrades to "version could not be parsed" — still `Healthy: true`, never a
   false failure), but worth a real-binary smoke check when convenient.
2. **Compatibility matrix has exactly one row, one provider, no mads-version dimension**
   — deliberate (§3.4's compat.go comment): honest about what's actually been proven, and
   mads doesn't cut versioned releases yet (W7.5, not landed — `cmd/mads/version.go`
   still hardcodes `"dev"`). Once W7.5 lands real versioning, this table's key shape
   should probably grow a mads-version dimension; not attempting that speculatively here.
3. **`node-pty` dropped from the check set** (§1) — was in the stub's placeholder list but
   never in this work item's actual scope and isn't Go-probeable. Flagged in case a
   reviewer expected it to carry over 1:1 from the stub.
4. **Disk-space threshold (1 GiB) is a guess**, not derived from any measured worktree
   size — reasonable floor for "can a git checkout even happen," not tuned against real
   repo sizes. Cheap to adjust later; not worth over-engineering into a configurable
   setting for a first pass.

## 11. Executor notes (implementation pass)

Built exactly per §§1–8 above; no drive-by refactors, no scope creep beyond what's listed.

- **`pkg/dataclient.Client.Health`** (§2): added verbatim, plus the prescribed
  round-trip test against `datatest.Start` and a second assertion against
  `dataclient.New("http://127.0.0.1:1", "router")` (connection refused) rather than a
  nonexistent-port variant — same "unreachable" signal, avoids relying on a specific
  unbound port staying unbound across CI runs.
- **`pkg/preflight/{checker,providers,compat,semver}.go`** (§3): landed verbatim against
  the handoff's code, split across the same four files. `preflight.go`'s doc comment was
  updated (§0 implied by "keep as-is" for the struct/interface, but the package doc's
  "does not exist yet" framing was stale once this landed) to describe the real Checker
  as a plugged-in seam rather than a stub-only placeholder.
- **`checker_test.go`** (§4): `TestRun_BrokenPATH` landed verbatim (the done-when proof).
  Added the three other suggested tests (data-service-unhealthy, happy-path,
  disk-space-healthy) plus one not explicitly listed: `TestNearestExistingAncestor`,
  asserting the climb-to-existing-ancestor behavior directly (cheap, and the happy-path/
  broken-PATH tests alone don't exercise a MadsHome path with non-existent leaf
  components, only `t.TempDir()`, which always exists).
- **Router wiring** (§5): `newMux` gained the `pf preflight.Checker` parameter, `main()`
  constructs `preflight.New(preflight.Deps{...})` and threads it through, exactly as
  specified. `main_test.go`'s one call site updated to pass `nil` (stub fallback) — that
  test only asserts `/health`, unaffected.
- **Frontend** (§6/§7): `fixturePreflightChecks` updated to a real git-detail/data_service/
  one-unhealthy-provider-row shape (dropped the stale `node-pty` row along with the
  `"stubbed..."` detail strings, consistent with §1's node-pty drop). Added one new test
  per file (`PreflightStep.test.tsx`, `GeneralPanel.test.tsx`) asserting the unhealthy
  row's fix_hint text renders, using the now-realistic fixture; left the existing
  "Continue always enabled" test as-is. No component code touched, per §6's instruction.
  Updated `PreflightStep.tsx`'s doc comment and `ui/README.md`'s onboarding-steps paragraph
  to describe *why* preflight stays non-gating now that checks are real, per §7.
- **README** (§8): added the `GET /preflight` bullet to `services/router/README.md`
  verbatim from the handoff's suggested text; did not touch that file's other
  pre-existing missing-route gaps. Fixed `ui/README.md`'s one stale sentence.
- **`make check`**: full run is green — `golangci-lint run ./...`, `go test -race ./...`
  (including the new `pkg/preflight` and `pkg/dataclient` tests), `npm run lint`, and
  `npm run test` (695 tests, all passing). `ui/node_modules` did not exist in this
  worktree at task start; ran `npm install` once to unblock local `vitest`/`eslint` runs
  before `make check` re-ran its own `npm ci`.
- **Not done / out of scope, confirmed unnecessary**: no changes to `services/router/
  internal/api/preflight.go` or `api.go` (§0 — already correct), no kiro-cli entry (§1 —
  provider doesn't exist yet), no node-pty check (§1 — not Go-probeable, not in scope).
- **Nothing found worth deviating on** from the handoff's design — every code sample
  compiled and passed as given, with only the cosmetic comment-trimming described above
  (repo style prefers no comments unless the *why* is non-obvious; kept the ones that are:
  the Bavail/Bsize darwin/linux note, the 1 GiB rationale, the semverish-permissiveness
  note, the runVersion timeout rationale).
- **Worth a reviewer's attention**: the same two judgment calls the handoff already
  flagged (§10.1: `claude --version` output format unverified against a real binary;
  §10.2: single-row, single-provider compat matrix) are unchanged — nothing in this pass
  had a real `claude` binary available to verify against either.
