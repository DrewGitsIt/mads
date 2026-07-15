# HANDOFF — W6.10 Settings & first-run

Architect stage output. Executor: read this fully before writing code. This
is the largest UI work item so far — it's also the first one where the
*backend* barely exists at all, not just the odd proxy route. Read §1
carefully; almost every panel needs new Go, not just new TypeScript.

## 0. Coordination risk (read first)

Depends on **W6.1 (App shell), W5.6 (capability runtime + basic credential
storage), W7.3 (preflight — explicitly not required to land first, stub it)**
per `docs/MADS-WORK-ITEMS.md`. All confirmed merged into `main` except W7.3,
which doesn't exist as code anywhere (confirmed by `git log --all --oneline`
and a full-repo grep — no `preflight` package, no route, nothing under
`services/*/internal/api` referencing it). Build the Go-side stub described
in §2.5; don't wait for it.

`App.tsx`'s `SURFACES` map and `RAIL_ITEMS` already exist (W6.2, W6.7).
Adding `settings: SettingsSurface` is the usual one-line diff. The **one
deliberate exception** to "one-line shell registration" in this task is the
first-run gate (§6) — `App.tsx` needs a few lines of mount-time config fetch
and conditional render to decide wizard-vs-shell. Flagged explicitly, kept as
small as I could make it.

Re-check `main` before writing code in case anything below has drifted, but
every claim in §1 was checked directly against the real Go tree for this
design (not against `docs/MADS-UX.md`'s route list, which — per
`[[mads-ux-spec-gaps]]` — is aspirational).

## 1. Backend reality check — this is the load-bearing section

`docs/MADS-UX.md` §3.7 lists `GET /config`, `GET /profiles`, `GET
/providers` (+matrix), `GET /credentials` (masked) as if they already exist.
**None of them do**, on either the Router or the Data Service. Here is
exactly what's real vs. missing, per panel, and what I'm designing to close
each gap:

### General / Pipeline / Advanced (needs: Config)
Nothing exists. No config file, no `pkg/config`, no `GET|PUT /config`
anywhere. `ui/src/featureFlags.ts` is literally a stub file with a comment
saying so ("Hardcoded stub — there is no /config endpoint yet... swap the
stub for a real fetch once the backend adds one"). **This task is that
backend.** Design: new `contracts.Config` + a bespoke (non-`Registry[T]`,
singleton) Data Service store + thin Router proxy. Full design in §2.1.

### Profiles (§10.5 in MADS-UX; REBUILD-SPEC §10 "Profiles vs Workspaces")
Nothing exists. But the exact machinery this needs already exists and is
reused four times over: `services/data/internal/store/registries.go`'s
generic `Registry[T]` + `registryRoutes(mux, base, registry)` (Agents,
Capabilities, Workspaces, and this task adds Profiles as the fourth). This is
a "cheap to close" thin-backend addition in the same sense W6.4/W6.7 used
that phrase — the CRUD machinery is generic, only the entity shape and
validation are new. Design in §2.2.

### Providers capability matrix (§11)
**Mostly already built, just not wired to HTTP.** `providers/provider/
matrix.go` is the real §11 capability matrix: `Behavior`, `Support`
(native/emulated/unsupported), `Level` (required/expected/optional),
`Requirement(b)`, `Behaviors()` (sorted, for iteration), and `Matrix.
Validate()`. The doc comment on `Behaviors()` literally says: *"for
iteration in tests and UI (the Settings provider table renders from this)"*
— this task is that UI. `provider.Provider.Matrix()` is a real method every
registered provider (claude-code, fake) implements. Router's existing `GET
/providers` (`services/router/internal/api/reads.go:36`) only returns
`s.providers.Names()` — a bare `[]string`. **Zero UI code consumes this
route today** (grepped the whole `ui/src` tree, confirmed empty) — safe to
widen its response shape without a compat concern. Design in §2.3.

### Credentials (§13; masked CRUD + env import + vault link)
`pkg/credstore` is fully built and already wired into the session runtime for
scoped injection (`services/session/internal/runner/capability.go`), but
**it has no HTTP surface anywhere** — grepped the whole repo, `credstore` is
referenced only inside its own package and inside `services/session`. It's
opened once, in `services/session/main.go`, at `<MADS_HOME>/
credentials.json` (env var `MADS_HOME`, default `~/.mads` — same default
family as the Data Service's `MADS_DATA_DIR`, per that file's own comment,
but a conceptually separate local file, not SQLite).

This means the Router — the process that serves the UI's HTTP API — needs to
open its own `credstore.Store` handle to expose CRUD. Design (incl. the
concurrency caveat this introduces) in §2.4.

Env-var import (§13.7) and vault link (§13.8) are formally **W7.4**'s scope
("builds on W5.6's storage", not W6.10's), but the task brief's Scope line
explicitly lists them under W6.10. Resolving that the same way W6.7 resolved
its own scope tension: env-var *scanning* has no external dependency (it's
`os.Environ()` plus a name filter) — build it for real. Vault sources
(1Password CLI, AWS Secrets Manager) are genuine external integrations with
real subprocess/API work — **stub behind an interface**, same instruction
the task brief gives explicitly for W7.3's preflight. See §2.4.

### Updates/About
No update-check backend exists (W7.5, not depended on). `GET /version`
(MADS-UX §3.7) doesn't exist either. This panel is **read-only static
content** this pass: build info baked in at compile time (a `ui/src/version.
ts` constant, mirroring how Vite typically injects `import.meta.env` — no
Go route needed) + hardcoded release-notes/About copy. "Check for updates"
button renders disabled with a "coming in W7.5" tooltip. Not stubbing a fake
network call for this — there's nothing to stub against, and a disabled
button is honest about the state.

### Preflight (§14.8, W7.3 — explicitly told to stub)
Doesn't exist. Design the seam per §2.5.

### Workspaces (needed for the wizard's "first workspace" step)
Data Service already has full CRUD (`registryRoutes(mux, "workspaces", ...)`
in `services/data/internal/api/api.go:56`). Router only proxies `GET
/workspaces` (`reads.go:18`) — no `POST`. Add the thin proxy, mirroring
`handleCreateTask`'s exact shape. Trivial, in scope.

### A vocabulary mismatch worth flagging now
REBUILD-SPEC §10's "Default trust level (manual / accept-edits / auto)"
does not match the trust vocabulary W6.2/6.3 actually shipped:
`contracts.SessionTrust` is `full | limited | read_only`
(`pkg/contracts/session.go:22-27`). **Use the real enum** for
`Config.DefaultTrust` — don't invent a second trust vocabulary that the rest
of the app doesn't speak.

## 2. Go changes

### 2.1 Config (`pkg/contracts/config.go`, new)

```go
package contracts

type SynthesisMode string

const (
	SynthesisOff   SynthesisMode = "off"
	SynthesisLight SynthesisMode = "light"
	SynthesisFull  SynthesisMode = "full"
)

type AgentSelectionMode string

const (
	AgentSelectionLight AgentSelectionMode = "light"
	AgentSelectionFull  AgentSelectionMode = "full"
)

type ModelTier string

const (
	ModelTierCheapest ModelTier = "cheapest"
	ModelTierBalanced ModelTier = "balanced"
	ModelTierBest     ModelTier = "best"
)

type ThemeMode string // dark | light | system

type FeatureFlags struct {
	MultiRepo      bool `json:"multi_repo"`
	LinkedSessions bool `json:"linked_sessions"`
	RemoteRegistry bool `json:"remote_registry"`
}

// Config is the single global settings object (spec §10). One row, no id —
// GET always returns it (creating defaults on first read), PUT always
// full-replaces it (mirrors PUT /tasks/{id}'s existing full-replace
// convention, not a partial-merge PATCH).
type Config struct {
	// Cost & Token Controls
	SynthesisMode      SynthesisMode      `json:"synthesis_mode"`
	AgentSelectionMode AgentSelectionMode `json:"agent_selection_mode"`
	DefaultModelTier   ModelTier          `json:"default_model_tier"`
	CostAlertCents     int                `json:"cost_alert_cents,omitempty"`

	// Agent Defaults
	DefaultWorkspace string       `json:"default_workspace,omitempty"`
	DefaultProvider  string       `json:"default_provider,omitempty"`
	DefaultTrust     SessionTrust `json:"default_trust"`
	DefaultTemplate  string       `json:"default_template,omitempty"`
	FastModel        string       `json:"fast_model,omitempty"`

	// UI Preferences (incl. this task's Theme & Palette panel)
	DisplayMode  string    `json:"display_mode"`  // chat | terminal
	Keybindings  string    `json:"keybindings"`   // default | vim | emacs
	Theme        ThemeMode `json:"theme"`         // dark | light | system
	Palette      string    `json:"palette"`       // clay | space | glacier
	EditorCommand string   `json:"editor_command,omitempty"`

	// System (restart-required, see ui/src/features/settings/restartRequired.ts)
	ConcurrencyLimit  int          `json:"concurrency_limit"`
	SynthesisDebounceMS int        `json:"synthesis_debounce_ms"`
	LogRetentionDays  int          `json:"log_retention_days"`
	FeatureFlags      FeatureFlags `json:"feature_flags"`

	// First-run
	OnboardingComplete bool `json:"onboarding_complete"`
}

// DefaultConfig is what a fresh data dir gets (spec §10.4 — zero-config
// usability). Fill in every field explicitly; don't rely on Go zero values
// reading as intentional (e.g. SynthesisMode's zero value must not silently
// mean "off" by accident — it does here, but say so).
func DefaultConfig() Config {
	return Config{
		SynthesisMode: SynthesisLight, AgentSelectionMode: AgentSelectionLight,
		DefaultModelTier: ModelTierBalanced, DefaultTrust: TrustLimited,
		DisplayMode: "chat", Keybindings: "default", Theme: "system", Palette: "clay",
		ConcurrencyLimit: 3, SynthesisDebounceMS: 2000, LogRetentionDays: 30,
	}
}
```

Validation (`func ValidateConfig(c *Config) *ValidationError`, called from
the store, same `.Add(field, message)` / `.Empty()` shape every other store
already uses — this is the existing validate-all mechanism, not new
machinery): enum-check every string field, range-check the ints
(`ConcurrencyLimit >= 1`, `LogRetentionDays >= 1`), and report every failure
in one pass, not fail-fast. This is what makes "validate-all reports inline
errors" true almost for free — the mechanism already exists app-wide
(`pkg/contracts/apierror.go`'s `ValidationError`), this task just uses it on
a new type.

**Data Service** (`services/data/internal/store/config.go`, new — bespoke,
not `Registry[T]`, since this is a singleton, not a named list):

```go
package store

type Config struct{ log *Log }

func NewConfig(log *Log) *Config { return &Config{log: log} }

func (s *Config) Get(ctx context.Context) (contracts.Config, error) {
	// SELECT data FROM config WHERE id = 1; if no row, INSERT the
	// DefaultConfig() row and return it. Single row, id fixed at 1.
}

func (s *Config) Update(ctx context.Context, cfg *contracts.Config) (contracts.Config, error) {
	if verr := contracts.ValidateConfig(cfg); verr != nil && !verr.Empty() {
		return contracts.Config{}, verr
	}
	// UPSERT the row inside log.WithTx — no event Append needed (Tx.Append
	// is optional per transaction; WithTx only broadcasts if t.appended is
	// true — see log.go:83-95). Settings has no live subscribers (single-
	// user local desktop app, same reasoning useRetroDetail already
	// established for not subscribing to SSE on every mutation), so skip
	// adding a config.* event type — one less thing to keep in sync.
}
```

Migration `0010_config.up.sql` / `.down.sql` (next number after `0009_
retros`):
```sql
CREATE TABLE config (
    id         INTEGER PRIMARY KEY CHECK (id = 1),
    data       TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
```

Wire into `services/data/internal/api/api.go`: `s.config =
store.NewConfig(l)` in `New`, `mux.HandleFunc("GET /config",
s.handleGetConfig)` / `PUT /config` (decode → `s.config.Update` → write 200
or the mapped validation error, exactly `handleUpdateTask`'s shape).

**Router** thin proxy (`services/router/internal/api/config.go`, new;
`pkg/dataclient` gets `Config(ctx)` / `UpdateConfig(ctx, *contracts.Config)`
mirroring `Workspaces`/`CreateCapability`'s existing shape): `GET /config`,
`PUT /config`, same `decode`/`writeError` pattern as every other Router
proxy file. `writeError` already flattens `*contracts.ValidationError` to
400 with the `{errors:[...]}` body (`api.go:99-109`) — nothing new needed
there.

### 2.2 Profiles (`pkg/contracts/registry.go`, add `Profile`)

```go
// ProfileSettings is the subset of Config a profile bundles (spec §10:
// "Profiles are just global... a convenience for adjusting multiple knobs
// at once" — Cost & Token Controls + Agent Defaults only; UI Preferences
// and System settings are not profile-bundled, they're per-machine, not
// per-persona).
type ProfileSettings struct {
	SynthesisMode      SynthesisMode      `json:"synthesis_mode,omitempty"`
	AgentSelectionMode AgentSelectionMode `json:"agent_selection_mode,omitempty"`
	DefaultModelTier   ModelTier          `json:"default_model_tier,omitempty"`
	CostAlertCents     int                `json:"cost_alert_cents,omitempty"`
	DefaultProvider    string             `json:"default_provider,omitempty"`
	DefaultTrust       SessionTrust       `json:"default_trust,omitempty"`
	FastModel          string             `json:"fast_model,omitempty"`
}

type Profile struct {
	ID          string          `json:"id"`
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Settings    ProfileSettings `json:"settings"`
	CreatedAt   time.Time       `json:"created_at"`
	UpdatedAt   time.Time       `json:"updated_at"`
}
```

Add `ProfileCreated/Updated/Deleted` to `pkg/contracts/event.go`'s
`EventTypes` list — required by `Registry[T]`'s constructor shape (every
existing registry does this; not optional plumbing, just the same 3-line
addition each of the other four made).

`services/data/internal/store/registries.go`: add `NewProfiles(log *Log)
*Registry[contracts.Profile]`, mirroring `NewWorkspaces` exactly (table
`"profiles"`, prefix `"prf"`, `system` always `false`, validate `Name`
required). Migration `0011_profiles.up/down.sql`, same 6-column shape as
`0005_registries.up.sql`'s three tables.

Wire: `registryRoutes(mux, "profiles", store.NewProfiles(l))` in Data
Service's `api.go` — one line, matching the other three registries.

**Activate** (switching profile = bulk-editing global config, spec §10) is
the one bespoke action beyond generic CRUD. Put it on the Data Service for
transactional atomicity (`POST /profiles/{id}/activate`, new handler,
bespoke — not part of `registryRoutes`): look up the profile, merge its
non-zero `ProfileSettings` fields onto the current `Config` row, validate,
write, return the updated `Config` — all inside one `log.WithTx`. Router
thin-proxies it the same way it proxies `handleRetroFollowUpSession`.

### 2.3 Providers capability matrix (§11)

Widen `handleListProviders` (`services/router/internal/api/reads.go:36`) —
safe, no consumers yet (§1):

```go
type providerInfo struct {
	Name   string                            `json:"name"`
	Matrix map[provider.Behavior]provider.Support `json:"matrix"`
}

func (s *server) handleListProviders(w http.ResponseWriter, _ *http.Request) {
	names := s.providers.Names()
	out := make([]providerInfo, 0, len(names))
	for _, n := range names {
		p, _ := s.providers.Get(n) // Names() only returns registered names, Get cannot miss
		out = append(out, providerInfo{Name: n, Matrix: p.Matrix()})
	}
	writeJSON(w, http.StatusOK, out)
}
```

Add `GET /providers/behaviors` returning `provider.Behaviors()` zipped with
`provider.Requirement(b)` — one small handler, lets the UI render the
matrix's column headers and "required" badges without hardcoding the
behavior list a second time in TypeScript (single source of truth stays in
Go, same principle as `Behaviors()`'s own doc comment intends).

No Data Service involvement — this is all in-memory registry data, Router
already holds the `*provider.Registry`.

### 2.4 Credentials (§13)

Router opens its own `credstore.Store`, same default-resolution helper
`services/session/main.go:34` already documents (`MADS_HOME` env var,
default `~/.mads`) — add the identical `defaultMadsHome()` function and a
`-mads-home` flag to `services/router/main.go`, open `credstore.Open(filepath.
Join(*madsHome, "credentials.json"))` at startup, pass the `*credstore.Store`
into `api.New` (new param — `server` struct gains a `creds *credstore.
Store` field). This touches `api.New`'s signature; update its 2-3 direct
call sites in `_test.go` files (`api_test.go`'s `newTestServer`, `agents_
test.go`, `events_test.go` each construct `api.New(...)` — add a
`testCredStore(t)` helper opening a store at `t.TempDir()`).

New `services/router/internal/api/credentials.go`:
```go
func (s *server) handleListCredentials(w http.ResponseWriter, _ *http.Request) {
	names, err := s.creds.Names()
	// -> writeJSON 200, []struct{ Name string `json:"name"` } — sorted,
	// values never touched by this handler at all, let alone serialized.
}

func (s *server) handleSetCredential(w http.ResponseWriter, r *http.Request) {
	var body struct{ Name, Value string }
	// decode -> validate Name non-empty -> s.creds.Set(body.Name, body.Value)
	// -> writeJSON 201 {"name": body.Name} — never echo Value back.
}

func (s *server) handleDeleteCredential(w http.ResponseWriter, r *http.Request) {
	// s.creds.Delete(r.PathValue("name")) -> 204
}

func (s *server) handleCredentialEnvCandidates(w http.ResponseWriter, _ *http.Request) {
	// os.Environ(), split on "=", names only (never values in the
	// response), minus names already in s.creds.Names(). This is the real
	// (non-stubbed) half of §13.7 — no external dependency.
}

func (s *server) handleListVaultSources(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, []string{}) // stub — see below
}
```

Routes: `GET /credentials`, `POST /credentials`, `DELETE /credentials/{name}`,
`GET /credentials/env-candidates`, `POST /credentials/import`, `GET
/credentials/vault-sources`. `POST /credentials/import` takes `{names:
[]string}` and, for each name, reads `os.Getenv(name)` **inside the Router
process** and writes straight into `credstore` — the value never round-trips
through an HTTP response body at any point in the import flow (see §6 step
3 for why this is the stronger design vs. a naive "GET candidates with
values, let the UI re-POST them" shape).

**Vault link is a documented stub, not a real integration** (§1): define
```go
// pkg/vault/vault.go (new, tiny)
package vault

// Source is one external secret-manager integration (1Password CLI, AWS
// Secrets Manager, spec §13.8). Zero implementations registered yet — this
// is the seam W7.4 fills in. handleListVaultSources returns an empty list
// against a zero-length registry; the UI's job this pass is to render that
// emptiness as a real, honest "no vault sources configured" state, not to
// fake data against it.
type Source interface {
	Name() string
	Healthy(ctx context.Context) (bool, string) // (healthy, detail)
}
```
No registry/wiring beyond the type — Router's `handleListVaultSources`
returns `[]string{}` directly today; swapping in a real `[]vault.Source`
iteration is a W7.4-sized follow-up, not blocked by anything here.

**Concurrency caveat (flag for review, not fixed here):** `credentials.
json` may now be opened by the Router *and* by however many session-service
child processes are alive, each with its own in-process `sync.Mutex` and no
cross-process file lock. This isn't a new risk this task introduces — the
Supervisor already spawns one session-service process per active session,
each independently opening the same file today — but adding the Router as a
third-ish writer widens the existing window. Out of scope to fix (would mean
redesigning `pkg/credstore`'s storage model, touching already-shipped W5.6
code) — noted in §12.

### 2.5 Preflight stub (W7.3)

```go
// pkg/preflight/preflight.go (new)
package preflight

type Check struct {
	Name    string `json:"name"`
	Healthy bool   `json:"healthy"`
	Detail  string `json:"detail,omitempty"`
	FixHint string `json:"fix_hint,omitempty"`
}

// Checker is the seam W7.3 implements for real (§14.8: binaries,
// dependencies, services, with fix guidance). StubChecker is the only
// implementation today.
type Checker interface {
	Run(ctx context.Context) ([]Check, error)
}

// StubChecker always reports everything healthy — enough for the first-run
// wizard and Settings' preflight surface to exercise their full UI without
// a real backend. Swap for the real implementation in
// services/router/main.go when W7.3 lands; nothing else changes.
type StubChecker struct{}

func (StubChecker) Run(ctx context.Context) ([]Check, error) {
	return []Check{
		{Name: "git", Healthy: true},
		{Name: "node-pty", Healthy: true},
		{Name: "provider: claude-code", Healthy: true},
	}, nil
}
```

Router: `GET /preflight` → `s.preflight.Run(ctx)`, wired to `preflight.
StubChecker{}` in `main.go` (one-line swap point, matching `fastmodel`'s own
"empty base URL uses providers/fake as a placeholder" pattern already in
that file). No Data Service involvement.

### 2.6 Workspaces proxy

`services/router/internal/api/sessions.go` (or a new small file) gets
`handleCreateWorkspace`, mirroring `handleCreateTask` exactly:
```go
func (s *server) handleCreateWorkspace(w http.ResponseWriter, r *http.Request) {
	var ws contracts.Workspace
	if !decode(w, r, &ws) { return }
	if err := s.data.CreateWorkspace(r.Context(), &ws); err != nil {
		writeError(w, err); return
	}
	writeJSON(w, http.StatusCreated, ws)
}
```
`pkg/dataclient` gets a `CreateWorkspace` method mirroring `CreateCapability`
line-for-line. Route: `POST /workspaces`.

## 3. Frontend module layout

```
ui/src/features/settings/
  SettingsSurface.tsx / .module.css / .test.tsx
                                    left-nav + active panel; registered
                                    SURFACES.settings
  nav/SettingsNav.tsx / .test.tsx  Appearance · General · Profiles ·
                                    Providers · Credentials · Pipeline ·
                                    Advanced · Updates/About (MADS-UX §3.7's
                                    exact left-nav order)
  restartRequired.ts / .test.ts    RESTART_REQUIRED_FIELDS: Set<keyof Config>
                                    — UI-owned classification (see §7)
  RestartBadge.tsx / .module.css / .test.tsx
  validation/
    fieldErrors.ts / .test.ts      ValidationError.errors[] ->
                                    Record<field, message> for inline display
  panels/
    AppearancePanel.tsx / .test.tsx    palette + theme picker (§5)
    GeneralPanel.tsx / .test.tsx       data dir (read-only), concurrency
                                        limit, log retention, editor command,
                                        display mode, keybindings
    ProfilesPanel.tsx / .test.tsx      list, create/edit/delete, Activate
    ProvidersPanel.tsx / .test.tsx     capability matrix table (§2.3)
    CredentialsPanel.tsx / .test.tsx   masked list, add/delete form,
                                        env-import picker, vault-sources
                                        empty state (§8 — the security test)
    PipelinePanel.tsx / .test.tsx      synthesis mode + static cost hints,
                                        agent selection mode, model tier,
                                        cost alert threshold, fast model
    AdvancedPanel.tsx / .test.tsx      3 feature flag toggles (§9)
    UpdatesAboutPanel.tsx / .test.tsx  two-column version/notes + About copy
  theme/
    applyTheme.ts / .test.ts          sets data-theme/data-palette on
                                        document.documentElement; resolves
                                        "system" via matchMedia (§5)
  state/
    useConfig.ts / .test.ts           GET/PUT /config, draft + dirty-diff +
                                        which touched fields are restart-
                                        required
    useProfiles.ts / .test.ts         CRUD + activate
    useCredentials.ts / .test.ts      list/add/delete/env-candidates/vault
    useProviderMatrix.ts / .test.ts   GET /providers + /providers/behaviors
    usePreflight.ts / .test.ts        GET /preflight (shared with wizard)
  HANDOFF.md                          (this file)

ui/src/onboarding/
  FirstRunWizard.tsx / .module.css / .test.tsx
                                    welcome -> preflight -> credential
                                    import -> first workspace -> done (§6)
  steps/
    WelcomeStep.tsx / .test.tsx
    PreflightStep.tsx / .test.tsx   reuses state/usePreflight.ts
    CredentialImportStep.tsx / .test.tsx  reuses state/useCredentials.ts
    WorkspaceStep.tsx / .test.tsx   name + primary repo path form
```

`ui/src/router/types.ts` gains `Config`, `Profile`, `ProfileSettings`,
`ProviderInfo` (mirroring `pkg/contracts` field-for-field, snake_case,
following every prior module's rule), plus `PreflightCheck`.
`ui/src/router/fixtures.ts` gains `fixtureConfig`, `fixtureProfile`,
`fixtureProviders` (claude-code + fake, a few behaviors each spanning
native/emulated/unsupported so `ProvidersPanel.test.tsx` has something real
to assert styling against), `fixturePreflightChecks`.

Uses the `routerGet`/`routerPost` (+ new `routerPut`/`routerDelete` —
`ui/src/router/client.ts` currently only exports `routerGet`/`routerPost`;
add the two missing verbs, thin wrappers over the same `fetch` call with a
different method, not a new client idiom) idiom, matching Retro's precedent
(§0 of `features/retro/HANDOFF.md`: top-level rail surface, not a
session-workspace tab, so this is the right idiom, not `RouterClient`/
`FetchRouterClient`).

## 4. State design

**`useConfig`** — fetch-once (`GET /config` on mount), holds both `saved`
(server truth) and `draft` (in-progress edits); `save()` calls `PUT /config`
with `draft`, and on a 400 `ValidationError` body returns the field-error map
from `validation/fieldErrors.ts` without touching `saved` (draft stays as
the user left it, per §10.3's "inline feedback" ethos — don't discard their
edits on a validation failure). On success, `saved = draft`, and `dirtyRestart
Fields` (computed via `restartRequired.ts` diffing `saved` against the
config as of mount) drives the badge.

**`useProfiles`** — mirrors `useTaskStore`'s fetch-once + action shape (no
live event subscription — same reasoning as `useRetroDetail`, single-user
local app, Settings mutations are refetch-on-own-action only). `activate(id)`
calls `POST /profiles/{id}/activate`, then re-fetches `/config` (the caller,
`ProfilesPanel`, should share the same `useConfig` instance or trigger its
refetch — don't duplicate config state between panels).

**`useCredentials`** — `names: string[]` only, ever. `add(name, value)`
posts and on success appends `name` to the local list — **never stores
`value` anywhere in this hook's state, not even transiently**. This is the
mechanism behind the acceptance criterion in §8.

**`useProviderMatrix`** — two parallel fetches (`/providers`, `/providers/
behaviors`), joined client-side into rows-by-behavior/columns-by-provider
for the table.

**`usePreflight`** — single `GET /preflight` call, returns `{checks,
loading, rerun()}`. Shared verbatim between `Settings > General`'s "run
preflight" affordance and the wizard's `PreflightStep`.

## 5. Theme & Palette panel — live apply + persistence

`ui/src/tokens/tokens.css` already declares all 6 `[data-theme]×[data-
palette]` combinations (confirmed — `clay`/`space`/`glacier` ×
`dark`/`light`, `clay` is the default when `[data-palette]` is absent). What
doesn't exist yet: anything that *sets* those attributes at runtime.
`index.html` hardcodes `<html data-theme="dark">` today, no `data-palette`.

`theme/applyTheme.ts`:
```ts
export function applyTheme(theme: ThemeMode, palette: PaletteName) {
  const resolved = theme === "system"
    ? (window.matchMedia("(prefers-color-scheme: dark)").matches ? "dark" : "light")
    : theme;
  document.documentElement.dataset.theme = resolved;
  document.documentElement.dataset.palette = palette;
}
```
`AppearancePanel` calls this **immediately on click** (before the `PUT
/config` round-trip resolves) — live means live, not "live after the
network confirms." On `PUT` failure, revert by re-calling `applyTheme` with
the last-saved values (mirrors the credentials/optimistic-update pattern
`useRetroDetail` already established for its accept/reject toggle).

`App.tsx`'s mount effect (§6) also calls `applyTheme(config.theme, config.
palette)` once on load, so a returning user's persisted choice actually
applies — without this, `index.html`'s hardcoded `data-theme="dark"` would
silently override whatever they picked last session.

`theme` and `palette` are **not** restart-required — they're the one
UI-Preferences pair explicitly designed to apply live.

## 6. First-run wizard

Gating mechanism: `App.tsx` fetches `GET /config` once on mount (a genuine,
small, justified exception to "one-line shell registration" — §0). While
loading, render nothing (a blank frame is fine for a sub-second local
fetch — no spinner infrastructure exists yet and this isn't the place to
invent one). Once resolved: `!config.onboarding_complete` renders
`<FirstRunWizard onComplete={...}>` in place of the icon-rail shell entirely
("wizard over empty app", MADS-UX §3.9's exact phrase); otherwise render the
normal shell, having already called `applyTheme(config.theme, config.
palette)` (§5).

**"Wizard completes on a clean data dir"** (the acceptance criterion) reads
as: point the Data Service at a fresh SQLite file (`store.Config.Get`
lazily inserts `DefaultConfig()`, which has `OnboardingComplete: false`) —
the wizard is reachable by construction on any never-configured install.
Component-test equivalent: stub `GET /config` to return
`{...fixtureConfig, onboarding_complete: false}` and drive the whole flow.

Five steps, one component each under `onboarding/steps/`, `FirstRunWizard`
owns a `step` index and forwards `next()`/`back()`:

1. **Welcome** — static copy, "Get started" button. No backend.
2. **Provider preflight** — `usePreflight()` (§4), renders each `Check` as a
   row (name, healthy/unhealthy icon, `fix_hint` if unhealthy). Against
   `StubChecker` (§2.5) everything is healthy, so this step never blocks —
   that's correct v1 behavior, not a bug: the stub's whole purpose is to let
   the wizard's happy path work end-to-end before W7.3 lands. "Continue"
   always enabled (no gating on stub data — gating on fake failures would
   be worse than not gating at all).
3. **Credential import** — reuses `CredentialImportStep` /
   `useCredentials().envCandidates` (§2.4's real, non-stubbed env scan):
   checkbox list of detected env-var names, "Import selected" calls
   `importFromEnv(names)` (§2.4's `POST /credentials/import`). The value
   each selected name resolves to is read server-side, inside the Router
   process, directly from `os.Getenv` — it never travels over HTTP to the
   renderer at all, for either the candidates list or the import call. This
   is strictly more private than the manual add form (§8), which does move
   a typed value over the wire once, locally, to be stored. Skippable
   ("Skip for now").
4. **First workspace** — name + primary repo path form, `POST /workspaces`
   (§2.6). Minimal `WorkspaceRepo{Path, Primary: true}`.
5. **Done** — `PUT /config {...config, onboarding_complete: true}`, then
   `onComplete()` — `App.tsx` re-renders into the normal shell, landing on
   Launchpad (the existing `sessions` rail item's default active state —
   nothing new needed here, `App.tsx`'s `activeId` already defaults to
   `"sessions"`).

## 7. Restart-required badges & validate-all

`restartRequired.ts`:
```ts
export const RESTART_REQUIRED_FIELDS: ReadonlySet<keyof Config> = new Set([
  "concurrency_limit", "log_retention_days", "feature_flags",
]);
```
This is a **UI-owned classification**, not server metadata — REBUILD-SPEC
§10 requirement 7 just says "the UI clearly indicates which changes require
restart," it doesn't prescribe a wire contract for it, and round-tripping
presentation metadata through Go for a single-client desktop app is more
machinery than the requirement calls for. Flagged as a judgment call in
§12 — if a future settings field needs this classification and someone
edits `Config` without touching this file, they'll silently drift.
`useConfig`'s `dirtyRestartFields` diffs `draft` against the config as
fetched at mount, filtered through this set — `RestartBadge` renders
whenever that set is non-empty, one badge per panel that owns a touched
field (`GeneralPanel` for concurrency/retention, `AdvancedPanel` for feature
flags).

Validate-all is not new machinery (§2.1) — `contracts.ValidationError`
already reports every failing field in one response; `PipelinePanel`/
`GeneralPanel`/`AdvancedPanel` all read from the same `useConfig().
fieldErrors` map (keyed by field name) and render inline, next to the
specific input, not as a toast/banner. Test: stub `PUT /config` returning a
`422`-shaped (Router flattens to 400, same body) `{errors: [{field:
"concurrency_limit", message:"..."}, {field:"log_retention_days",
message:"..."}]}` and assert **both** inline messages render from one
submit — this is the "not fail-on-first" property that's actually worth
testing, not just that errors render at all.

## 8. The credential-value-never-renders test (acceptance criterion)

`CredentialsPanel.test.tsx`, the specifically-mandated test:

```ts
it("never renders a credential value after save", async () => {
  const secret = "sk-live-do-not-render-this-value";
  stubFetch({ "POST /credentials": { status: 201, body: { name: "OPENAI_API_KEY" } } });
  render(<CredentialsPanel />);
  await userEvent.type(screen.getByLabelText(/name/i), "OPENAI_API_KEY");
  await userEvent.type(screen.getByLabelText(/value/i), secret);
  await userEvent.click(screen.getByRole("button", { name: /add/i }));
  await waitFor(() => expect(screen.getByText("OPENAI_API_KEY")).toBeInTheDocument());
  expect(document.body.innerHTML).not.toContain(secret);
  // and the form's own value field is cleared immediately on submit, not
  // just after the promise resolves — assert this synchronously, before
  // the waitFor above, so a slow network can never leave it lingering.
});
```

The stronger, synchronous half of this (clear the value field the instant
`add()` is called, not `.then()`) is the actual invariant worth building —
`CredentialsPanel`'s submit handler should read the value into a local
`const` for the POST body and clear the controlled input's state in the same
event handler tick, before `await`ing anything. `useCredentials.add()`
itself takes `value` as a plain parameter and never assigns it to any hook
state (§4) — the hook's `names: string[]` shape makes it structurally
impossible to accidentally retain a value, which is the point.

## 9. Advanced panel — feature flags

`ui/src/featureFlags.ts` currently exports a plain hardcoded `FLAGS` object
with one consumer (`ui/src/features/sessions/launchpad/pills/
IsolationPill.tsx:29`, reading `FLAGS.multiRepo` directly in render body).
Replace the stub with a real one that stays source-compatible:

```ts
export interface FeatureFlagState { multiRepo: boolean; linkedSessions: boolean; remoteRegistry: boolean; }
export const FLAGS: FeatureFlagState = { multiRepo: false, linkedSessions: false, remoteRegistry: false };
const listeners = new Set<() => void>();
export function setFlags(next: FeatureFlagState) { Object.assign(FLAGS, next); listeners.forEach((l) => l()); }
export function subscribeFlags(cb: () => void) { listeners.add(cb); return () => listeners.delete(cb); }
```
`App.tsx`'s mount effect calls `setFlags(config.feature_flags)` alongside
`applyTheme` (§5/§6) after the config fetch resolves. `AdvancedPanel`'s
toggles call `useConfig().save()` (which round-trips through `PUT /config`)
and then `setFlags` on success — `IsolationPill` keeps reading `FLAGS.
multiRepo` exactly as before (no changes needed there; it re-renders on its
own prop changes often enough that this isn't a regression — today the
value is permanently `false` and never live at all, so this is strictly an
improvement, not a new gap). Only 3 flags exist per `docs/MADS-WORK-ITEMS.md`'s
"Flagged functionality ledger": `remote-registry`, `multi-repo`,
`linked-sessions`. The latter two have **no consuming surface built yet
beyond this toggle** — that's correct and expected; the ledger explicitly
scopes "W6.10 flag toggle" as `linked-sessions`' entire job this wave, UI
deferred to a post-launch item.

## 10. `App.tsx` wiring

```ts
import { SettingsSurface } from "./features/settings/SettingsSurface";
import { FirstRunWizard } from "./onboarding/FirstRunWizard";
import { applyTheme } from "./features/settings/theme/applyTheme";
import { setFlags } from "./featureFlags";
import { routerGet } from "./router/client";
// ...
const RAIL_ITEMS: IconRailItem[] = [
  { id: "sessions", label: "Sessions", icon: "S" },
  { id: "retro", label: "Retro", icon: "R" },
  { id: "library", label: "Library", icon: "L" },
  { id: "settings", label: "Settings", icon: "⚙" },
];
const SURFACES: Partial<Record<string, ComponentType>> = {
  sessions: SessionsSurface, retro: RetroSurface, settings: SettingsSurface,
};

export function App() {
  const [config, setConfig] = useState<Config | null>(null);
  useEffect(() => {
    routerGet<Config>("/config").then((c) => {
      applyTheme(c.theme, c.palette);
      setFlags(c.feature_flags);
      setConfig(c);
    });
  }, []);
  if (!config) return null;
  if (!config.onboarding_complete) {
    return <FirstRunWizard onComplete={() => setConfig({ ...config, onboarding_complete: true })} />;
  }
  // ...existing rail + SURFACES render, unchanged
}
```
`Dashboard` (MADS-UX §1's fifth rail area, W6.9) isn't registered yet either
(not this task's job) — `RAIL_ITEMS` stays at 4 entries until then.

## 11. Non-goals (explicitly out of scope)

- **Real vault integrations** (1Password CLI, AWS Secrets Manager) — W7.4.
  Stubbed per §2.4/§11.
- **Real preflight** (binary/dependency/service checks, provider-version
  compatibility matrix) — W7.3. Stubbed per §2.5.
- **Real update checking / release notes fetch** — W7.5. Updates/About is
  static content plus a disabled button this pass (§1).
- **Dashboard rail item / Library rail item** — not this task; `RAIL_ITEMS`
  only grows by one (`settings`).
- **Linked-sessions leader/monitor UI, remote-registry browsing UI** —
  explicitly deferred per the flagged-functionality ledger; this task only
  ships their toggle.
- **Live config change notifications across multiple open windows** — no
  event type added for config/credential/profile mutations (§2.1); a second
  Electron window (if one ever exists) wouldn't see another window's config
  edit without a manual refetch. Not a regression (no multi-window support
  exists yet at all) but worth naming.
- **Per-session settings override UI** (spec §10's "Session overrides
  workspace" precedence tier) — that's Conversation tab's job (W6.3, already
  shipped, out of scope to touch here) if/when it needs one; Settings only
  edits the global layer.

## 12. Risks / judgment calls for human review

1. **Router now opens a second file handle on `credentials.json`** (§2.4) —
   no new cross-process lock added; widens an already-latent gap (multiple
   session-service children already do this with no lock). Not fixed here;
   fixing it means redesigning `pkg/credstore`'s storage model.
2. **`GET /providers`'s response shape changes** from `[]string` to
   `[]{name, matrix}` (§2.3). Confirmed zero UI consumers today, but if
   anything outside this repo (a script, a manual curl in someone's
   workflow) depends on the old shape, this breaks it silently.
3. **Restart-required-ness is UI-owned, not server metadata** (§7) — a
   deliberate scope call (avoids a wire-contract addition for pure
   presentation), but means Go and TS can drift if a field is added to
   `Config` on one side without updating `restartRequired.ts` on the other.
4. **The credential-import step's server-side env-read design** (§6 step 3)
   — `POST /credentials/import {names}` reads `os.Getenv` inside the Router
   process rather than the UI ever seeing a value, which is *more*
   restrictive than what a literal reading of "env import flow" might
   suggest (some designs would show the user the detected value before they
   confirm import). Confirm this stricter reading is the intended one before
   the executor builds a preview-then-confirm UI that the current design
   doesn't support.
5. **Trust vocabulary correction** (§1) — using the real `contracts.
   SessionTrust` (`full|limited|read_only`) instead of REBUILD-SPEC §10's
   prose (`manual|accept-edits|auto`). Low risk (the real enum is obviously
   correct), but flagging since it's a spec-vs-shipped-code divergence a
   reviewer should know was deliberate, not missed.
6. **Profile `Settings` field scope** (§2.2) — bundles Cost & Token Controls
   + a few Agent Defaults, explicitly excludes UI Preferences and System
   settings. This is my reading of REBUILD-SPEC §10's table, not something
   the table states as a field-by-field list. Confirm before the executor
   builds Activate with a different scope.
7. **App.tsx's mount-time config fetch** (§0, §6, §10) is a real (if small)
   exception to "one-line shell registration." Necessary for both the
   first-run gate and applying persisted theme/flags on load — there's no
   way to satisfy "palette/theme picker... persists" (acceptance criterion)
   without the app reading persisted config back on boot somewhere above
   the Settings surface itself.

## 13. Testing plan → acceptance criteria map

- "Changing a restart-required setting shows the badge" → `GeneralPanel.
  test.tsx` / `AdvancedPanel.test.tsx`, change `concurrency_limit` /
  `feature_flags.multi_repo` respectively, assert `RestartBadge` appears;
  change a live field (e.g. `default_provider`), assert it doesn't.
- "validate-all reports inline errors" → §7's multi-field-in-one-response
  test.
- "wizard completes against a clean data dir with the preflight stubbed" →
  `FirstRunWizard.test.tsx`, full 5-step walk against `fixtureConfig` with
  `onboarding_complete: false` and `StubChecker`-shaped fixture checks (all
  healthy) — asserts it reaches "Done" and calls `onComplete`.
- "Palette/theme picker switches live and persists" → `AppearancePanel.
  test.tsx`: click an option, assert `document.documentElement.dataset.
  theme`/`.palette` change synchronously (live), assert `PUT /config` fires
  with the new values (persists); separately, `App.test.tsx` (new, or an
  addition to an existing shell test) asserts a stubbed `GET /config`
  response with non-default `theme`/`palette` applies them on mount, before
  any user interaction (persistence survives reload).
- "a test proves credential values never appear in rendered output after
  save" → §8, verbatim.
- "make check passes" → Go: `go test -race ./...` covers the new stores/
  handlers; UI: `npm run lint && npm run test`, no Playwright anywhere in
  this package (matches every prior HANDOFF's note on this point).

Every new Go file gets a sibling `_test.go` (store-level + HTTP-level,
mirroring `retros_test.go`'s `postJSON(t, srv.URL+...)` pattern for the new
Router routes). Every new TS/TSX file gets a sibling `.test.ts(x)`.

## 14. README

Add a "Settings & first-run" section to `ui/README.md` (after "Pipeline
tab"), mirroring the Retro section's shape: owns `src/features/settings/`
and `src/onboarding/`, registered as `SURFACES.settings`, no demo mount
needed (the wizard and every panel are exercised by component tests against
stubbed `fetch`, same as Retro). Update the "Feature flags" section — the
current text describing `featureFlags.ts` as "a temporary hardcoded stub...
standing in for a real `/config` endpoint, which doesn't exist yet" is now
false; rewrite it to describe the real `GET/PUT /config`-backed
`setFlags`/`subscribeFlags` shape (§9). Update the opening paragraph's
surface list and the "Run it alone" sentence, same as every prior HANDOFF's
README instruction.

## 15. Executor notes (implementation pass)

Built per §§1–14 above; no drive-by refactors. Summary of what landed and
what a reviewer should scrutinize.

- **Full module built as specified**: all 8 panels (`AppearancePanel`,
  `GeneralPanel`, `ProfilesPanel`, `ProvidersPanel`, `CredentialsPanel`,
  `PipelinePanel`, `AdvancedPanel`, `UpdatesAboutPanel`) + `SettingsNav` +
  `RestartBadge` + `SettingsSurface`, all 5 `state/` hooks, `theme/
  applyTheme.ts`, `validation/fieldErrors.ts`, `restartRequired.ts`; the
  5-step `FirstRunWizard` under `src/onboarding/` with all 4 step
  components (`WelcomeStep`, `PreflightStep`, `CredentialImportStep`,
  `WorkspaceStep`) plus the "Done" step handled inline in
  `FirstRunWizard.tsx` (no dedicated file, matching §3's file list, which
  only names 4 step files). Every file has a sibling `.test.ts(x)`. The Go
  backend (§§1–2) landed in an earlier pass of this same session and was
  unchanged here except where noted below.
- **`App.tsx` wiring** (§10): matches the prescribed shape, with one
  correction — see the FLAGS bug below.
- **README** (§14): added the "Settings & first-run" section, rewrote
  "Feature flags", updated the opening ownership paragraph and the "Run it
  alone" sentence.
- **Real bug found and fixed, not in the doc's pseudocode**: §9's and
  §10's sample code both call `setFlags(config.feature_flags)` /
  `setFlags(nextFlags)` directly with the wire-format `FeatureFlags` object
  (`{multi_repo, linked_sessions, remote_registry}`), but `FeatureFlagState`
  (§9's own interface one paragraph earlier) uses camelCase keys
  (`{multiRepo, linkedSessions, remoteRegistry}`). Passing one where the
  other is expected silently populates `FLAGS` with the wrong key names
  (`Object.assign` doesn't overwrite `multiRepo`, it adds a stray
  `multi_repo` key) — `IsolationPill`'s `FLAGS.multiRepo` read would have
  stayed permanently `false` regardless of the real setting. Fixed in both
  `AdvancedPanel.toggle` and `App.tsx`'s mount effect by mapping the wire
  shape to `FeatureFlagState` explicitly before calling `setFlags`. Caught
  by a genuine test failure (`AdvancedPanel.test.tsx`'s `FLAGS.multiRepo`
  assertion timing out), not a doc read — worth a close look since the
  same mistake is easy to reintroduce if `App.tsx` or `AdvancedPanel` are
  touched again without noticing the key-shape mismatch.
- **Test convention correction**: every test file in this pass was
  initially written against `@testing-library/user-event` and
  `@testing-library/jest-dom`, matching common React-testing convention —
  neither is actually a dependency of this package (confirmed via
  `package.json` and `src/setupTests.ts`, which only registers `cleanup()`
  on `afterEach`). Rewrote every test to the codebase's actual convention:
  `fireEvent` from `@testing-library/react` and plain Vitest/Chai matchers
  (`.toBeTruthy()`, `.toBeNull()`, direct `.value`/`.disabled`/
  `.getAttribute(...)` reads) — matches the pre-existing
  `SessionPicker.test.tsx`/`ObservationsInput.test.tsx` precedent. No
  production code was affected by this; test-file-only.
- **`App.tsx`/`FirstRunWizard` test-fixture note**: `App.test.tsx` and
  `FirstRunWizard.test.tsx` both stub `EventSource` via the existing
  `src/router/testFakeEventSource.ts` fixture (`FakeEventSource`) rather
  than inventing a new one — `SessionsSurface`'s `useSessionsList` opens a
  real `EventSource` on mount and happy-dom doesn't implement it natively.
  `App.test.tsx`'s `fetch` stub also falls back to `[]` for any
  unstubbed route (documented inline in the test file) rather than
  enumerating every GET the default-mounted `SessionsSurface`/`Launchpad`
  pills make (`/sessions`, `/tasks`, `/workspaces`, `/agents`,
  `/providers`, `/providers/:id/models`) — that surface's own behavior is
  already covered by `SessionsSurface.test.tsx`; this file's job is only
  the config-gate/theme/flags mount wiring per §13's acceptance-criteria
  map.
- **Known gap carried forward from the doc, not re-litigated**: §12 item 4
  and `GeneralPanel`'s missing "data dir (read-only)" field — `Config` has
  no `data_dir` wire field, so it's omitted, as flagged in the doc.
- **Verification performed**: `tsc --noEmit` + `vite build` clean, `npm
  run lint` 0 issues, `npm run test` 525/525 passing (118 files, all of
  `src/features/settings/`, `src/onboarding/`, and `App.test.tsx`
  included), and `make check` from the repo root — `golangci-lint run
  ./...` 0 issues, `go test -race ./...` green across every package, UI
  lint/test green.
- **Not done, out of scope**: real vault integrations, the real §7.3
  preflight backend (stubbed per §2.5 as instructed), Dashboard rail item
  — all explicitly out of scope per §11.
