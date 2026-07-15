# HANDOFF — W6.8 Library

Architect stage output. Executor: read this fully before writing code. Like
W6.4 (Tasks), this task's backend gap is real but shallow for three of the
four shelves — full CRUD already exists at the Data Service, only Router
proxying and `pkg/dataclient` methods are missing. The fourth shelf
(Templates) is a true pure-proxy too, once you see `pkg/template`'s real
grammar. The one shelf with a genuine *new* capability is Agents' three
enrichment actions (describe/suggest-capabilities/refine) — everything else
here is wiring, not new business logic. Read §1 before assuming anything is
missing that isn't, or building against fixtures where a real endpoint is
one proxy method away.

## 0. Coordination check (do this first)

Re-run these checks against the current `main`/target branch before writing
code — this doc was written against a worktree where library-specific code
did not exist yet:

- **W6.2 (tab registry / one-line shell registration)**: landed. `App.tsx`'s
  `SURFACES: Partial<Record<string, ComponentType>>` map currently registers
  only `sessions`; `RAIL_ITEMS` already has a `library` entry with no
  `SURFACES` counterpart. Add `library: LibrarySurface` — genuinely one line,
  confirm this is still true before assuming it.
- **W6.4/W6.5 (Tasks/Files tabs)**: landed. Both are **session-scoped tabs**
  using Router-client idiom (a) — the `RouterClient` interface +
  `FetchRouterClient`/`FakeRouterClient` in `ui/src/router/client.ts`. Library
  is architecturally a **top-level rail surface**, not a session-scoped tab
  — like Sessions, not like Tasks/Files. **Use idiom (b) instead**
  (`routerGet`/`routerPost` + `subscribeToEvents`, also in `client.ts` /
  `events.ts`) — see `ui/src/features/sessions/state/useSessionsList.ts` for
  the exact pattern to mirror. Do not add Library methods to the
  `RouterClient` interface; that interface is idiom (a)'s, scoped to
  session-tab surfaces. Unifying the two idioms remains explicitly
  out-of-scope follow-up work (both READMEs already say so — don't be the
  one to almost-but-not-quite unify them here).
- **W5.3 (agent generation)**: landed. `POST /agents/generate` exists on the
  Router today (`services/router/internal/api/agents.go`), dispatches through
  `job.Runner`, and the result arrives via SSE as `agent.draft_generated`
  (`{draft: contracts.Agent, similar?: agentselect.Similar}`). This is the
  endpoint the NL-generate acceptance criterion needs — it already works,
  confirm it's still there before rebuilding it.
- **`pkg/agentgen`'s `Describe`/`SuggestCapabilities`/`Refine`**: implemented
  at the package level (`pkg/agentgen/agentgen.go`) but as of design time have
  **zero** job/route/event plumbing anywhere — not even a job body like
  `GenerateAgentFromTask`. This is a real gap this task closes; see §1a.
- **`remote-registry` flag**: does not exist yet. `ui/src/featureFlags.ts` is
  `export const FLAGS = { multiRepo: false }`. Add `remoteRegistry: false`.

## 1. Backend reality check — this task DOES touch Go code

Read `services/data/internal/api/{registries.go,templates.go,capability_health.go}`,
`pkg/dataclient/dataclient.go`, and `services/router/internal/api/{api.go,reads.go,agents.go}`
before assuming anything is missing.

- **The Data Service already has full CRUD for Agents, Capabilities, and
  Workspaces.** `registries.go`'s generic `registryRoutes[T any](mux, base,
  registry)` mounts `POST /{base}`, `GET /{base}`, `GET /{base}/{id}`, `PUT
  /{base}/{id}`, `DELETE /{base}/{id}` uniformly, already wired for `agents`,
  `capabilities`, `workspaces` in `services/data/internal/api/api.go`.
  Templates get the same five verbs from a dedicated `templateServer`
  (`templates.go`) plus `POST /templates/validate`. **None of this needs a
  new Data Service handler** — the gap is entirely that `pkg/dataclient` and
  the Router only wrap a fraction of it today.
- **`pkg/dataclient.Client` today only has**: `Agent`, `Agents`,
  `CreateAgent`, `Capability`, `Capabilities`, `CreateCapability`,
  `SetCapabilityHealth`, `Workspaces` (list only, no single-get/create/
  update/delete), `Template` (single get, **drops `used_by`** — see below).
  **Missing, add all of these** (same shape as the existing sibling
  methods — see `dataclient.go:104-388` for the pattern: thin `c.do(ctx,
  METHOD, path, in, out)` wrapper, `contracts.X` in/out):
  - `UpdateAgent(ctx, *contracts.Agent) error` — `PUT /agents/{id}`
  - `DeleteAgent(ctx, id string) error` — `DELETE /agents/{id}`
  - `UpdateCapability(ctx, *contracts.Capability) error` — `PUT /capabilities/{id}`
  - `DeleteCapability(ctx, id string) error` — `DELETE /capabilities/{id}`
  - `DiagnoseCapability(ctx, idOrName string) (contracts.CapabilityHealth, []string, error)` — `POST /capabilities/{id}/diagnose`, **new Data Service endpoint**, see below
  - `Workspace(ctx, idOrName string) (contracts.Workspace, error)` — `GET /workspaces/{id}`
  - `CreateWorkspace(ctx, *contracts.Workspace) error` — `POST /workspaces`
  - `UpdateWorkspace(ctx, *contracts.Workspace) error` — `PUT /workspaces/{id}`
  - `DeleteWorkspace(ctx, id string) error` — `DELETE /workspaces/{id}`
  - `Templates(ctx) ([]contracts.Template, error)` — `GET /templates`
  - `TemplateDetail(ctx, idOrName string) (contracts.Template, []string, error)` — `GET /templates/{id}`, decodes the `{template, used_by}` wrapper the handler actually returns (the existing `Template()` method presumably discards `used_by` — **leave `Template()` as-is**, it's used elsewhere; add this as a new method rather than changing `Template()`'s return shape and breaking callers)
  - `CreateTemplate(ctx, *contracts.Template) error` — `POST /templates`
  - `UpdateTemplate(ctx, *contracts.Template) error` — `PUT /templates/{id}`
  - `DeleteTemplate(ctx, id string) error` — `DELETE /templates/{id}`
  - `ValidateTemplate(ctx, source string) error` — `POST /templates/validate`, body `{"source": source}`. Returns `nil` on 200 `{"valid":true}`; on 422 `do()`'s existing generic handling **already** decodes the body into `*contracts.ValidationError` and returns it as the error (see `dataclient.go`'s `do()`, the `StatusUnprocessableEntity` case) — no special-casing needed, this method is a one-liner.
  - `DescribeAgent(ctx, prompt string) (string, error)`, `SuggestAgentCapabilities(ctx, prompt string) ([]string, error)`, `RefineAgent(ctx, prompt, instruction string) (string, error)` — **only if** you route enrichment through the Data Service; per the decision below these live on the Router instead and call `pkg/agentgen` directly, so **no dataclient methods needed for enrichment**.
- **New Data Service endpoint: `POST /capabilities/{id}/diagnose`.** No
  diagnose route exists anywhere today (confirmed by grep — zero hits for
  "diagnose" outside `pkg/capability`). Add `services/data/internal/api/capability_diagnose.go`,
  same shape as the existing `capability_health.go`:
  ```go
  func (s *server) handleDiagnoseCapability(w http.ResponseWriter, r *http.Request) {
      cap, err := s.caps.Get(r.Context(), r.PathValue("id")) // or lookup-by-name, mirror templates.go's lookup()
      if err != nil { writeStoreError(w, err); return }
      healthy, reason := capability.Health(r.Context(), cap, false) // running=false: see caveat below
      writeJSON(w, http.StatusOK, map[string]any{
          "healthy": healthy, "reason": reason,
          "suggestions": capability.Diagnose(cap, healthy, reason),
      })
  }
  ```
  Register as `mux.HandleFunc("POST /capabilities/{id}/diagnose", s.handleDiagnoseCapability)` in `api.go` alongside the existing `POST /capabilities/{id}/health`.
  **Caveat to document in the UI, not fix here**: `pkg/capability.Health`
  (`pkg/capability/skill.go`) checks skill directory existence (accurate
  anywhere) and remote MCP `Config.URL` reachability via real HTTP probe
  (accurate anywhere) — both fine from a session-independent Data Service
  call. But local-command MCP capabilities (`Config.Command` set, no `URL`)
  only know "running" from a caller-supplied bool, which only Session
  Service's live `MCPRegistry` can truthfully answer. Called from the Data
  Service with no session context, `running` is always `false`, so **local
  MCP capabilities will always diagnose as "not running."** Ship it anyway —
  the skill and remote-MCP cases are the common ones and are fully accurate
  — but the Diagnose panel copy must say something like "based on
  configuration, not a live process check" for local MCP, not present it as
  equally authoritative. Don't try to thread a running signal through from
  Session Service for this task; that's a real architectural addition
  (Data Service would need to ask Session Service, inverting the current
  dependency direction) out of scope here.
- **New synchronous Router endpoints for agent enrichment** — the resolved
  judgment call (flag for human review, see §10): full draft generation
  (`POST /agents/generate`) stays async via `job.Runner` because it's a
  5-15s Fast Model call the existing job+SSE UX already handles well.
  `Describe`/`SuggestCapabilities`/`Refine` are fast, side-effect-free,
  single-call, "click a button, fill in a form field" interactions — build
  them as **synchronous request/response**, not job+event, to avoid
  building three more domain events (`JobCompleted`'s payload is only
  `{job_id, kind}` — no result; full generation gets its result via the
  separate `agent.draft_generated` event the job body emits itself, and
  three more one-off result events for these would be pure ceremony for a
  form-fill interaction).
  - `job.Runner` (`services/router/internal/job/job.go:40-47`) holds `fm
    *fastmodel.Runner` unexported, no getter. Add one line:
    ```go
    func (r *Runner) FastModel() *fastmodel.Runner { return r.fm }
    ```
  - New handlers in `services/router/internal/api/agents.go`, alongside the
    existing `handleGenerateAgent`:
    ```
    POST /agents/describe              {prompt}                 -> agentgen.New(s.data, s.jobs.FastModel()).Describe(ctx, prompt)
    POST /agents/suggest-capabilities   {prompt}                 -> .SuggestCapabilities(ctx, prompt)
    POST /agents/refine                {prompt, instruction}    -> .Refine(ctx, prompt, instruction)
    ```
    Return the raw result JSON directly (`{"result": "..."}` or similar,
    executor's call on exact shape — check `agentgen.go`'s actual
    `Describe`/`SuggestCapabilities`/`Refine` signatures before finalizing,
    they were read as implemented but their exact return types weren't
    re-verified against this exact call shape in this design pass).
- **Router has no `/agents/{id}`, `/capabilities*`, `/workspaces/{id}`,
  `/templates*` routes at all today** — `api.go`'s `New()` mux (lines 32-63)
  only has `GET /workspaces` and `GET /agents` (list, no single-get) among
  these four shelves, wired for the session launchpad's pills, nothing else.
  Note this also means `RouterClient.getSkillCapabilities` (idiom (a),
  `client.ts`, used by the Conversation tab) calls `GET /capabilities`
  against a route that **does not exist on the Router today** — a
  pre-existing gap from an earlier task, not this one's to explain away.
  Adding `GET /capabilities` here (§1a) incidentally fixes it; don't go
  looking for more like it, just note it in the PR description.

### 1a. Router changes (new files + registration)

New/extended files in `services/router/internal/api/`, same style as
`sessions.go`/`tasks.go` — thin handlers, `decode`/`writeError`/`writeJSON`/
`mustJSON` from `api.go`, no new abstractions. Split by shelf for review
clarity, matching the existing one-file-per-domain convention:

```
# agents.go (extend existing file)
handleGetAgent            GET    /agents/{id}                    -> s.data.Agent
handleCreateAgent         POST   /agents                          -> s.data.CreateAgent
handleUpdateAgent         PUT    /agents/{id}                     -> s.data.UpdateAgent
handleDeleteAgent         DELETE /agents/{id}                     -> s.data.DeleteAgent
handleDescribeAgent       POST   /agents/describe                 -> agentgen.Describe (sync, see §1)
handleSuggestCapabilities POST   /agents/suggest-capabilities      -> agentgen.SuggestCapabilities (sync)
handleRefineAgent         POST   /agents/refine                   -> agentgen.Refine (sync)

# capabilities.go (new)
handleListCapabilities    GET    /capabilities                    -> s.data.Capabilities
handleGetCapability       GET    /capabilities/{id}                -> s.data.Capability
handleCreateCapability    POST   /capabilities                    -> s.data.CreateCapability
handleUpdateCapability    PUT    /capabilities/{id}                -> s.data.UpdateCapability
handleDeleteCapability    DELETE /capabilities/{id}                -> s.data.DeleteCapability
handleDiagnoseCapability  POST   /capabilities/{id}/diagnose       -> s.data.DiagnoseCapability

# workspaces.go (new)
handleGetWorkspace        GET    /workspaces/{id}                 -> s.data.Workspace
handleCreateWorkspace     POST   /workspaces                      -> s.data.CreateWorkspace
handleUpdateWorkspace     PUT    /workspaces/{id}                 -> s.data.UpdateWorkspace
handleDeleteWorkspace     DELETE /workspaces/{id}                 -> s.data.DeleteWorkspace

# templates.go (new)
handleListTemplates       GET    /templates                       -> s.data.Templates
handleGetTemplate         GET    /templates/{id}                  -> s.data.TemplateDetail (returns {template, used_by})
handleCreateTemplate      POST   /templates                       -> s.data.CreateTemplate
handleUpdateTemplate      PUT    /templates/{id}                  -> s.data.UpdateTemplate
handleDeleteTemplate      DELETE /templates/{id}                  -> s.data.DeleteTemplate
handleValidateTemplate    POST   /templates/validate               -> s.data.ValidateTemplate
```

Register all in `api.go`'s `New()` mux alongside the existing entries
(`GET /workspaces` and `GET /agents` already there — add the rest next to
them, keep the four new/extended handler files' registrations grouped).

Error handling: use the Router's own `writeError` (400 flat for
`*contracts.ValidationError`, 404 for `dataclient.ErrNotFound`, 409 for
`dataclient.ErrConflict`) — same convention as every other proxy route.
`writeError` already type-switches on `*contracts.ValidationError` via
`errors.As`, so `ValidateTemplate`'s `*contracts.ValidationError` return
flows straight through with zero new code in the handler.

Update `services/router/README.md`'s "API contract" section with the new
routes, one-liner-per-route style already used there.

### 1b. Non-goals in the backend

- **Do not add HTTP exposure to `pkg/credstore`.** It has zero HTTP surface
  today (confirmed by grep) and this task doesn't need one — see §5's
  credential design. Don't be tempted to "complete" credstore's API surface
  as a drive-by; it's out of scope and unreviewed here.
- **Do not thread a live `running` signal from Session Service into the new
  Diagnose endpoint.** See the caveat in §1 — ship the documented limitation.
- **Do not implement `GET /registry/*` or any remote-registry backend.** The
  Registry shelf is a flag-gated stub only (§7) — MADS-UX §3.5's "Browse
  Registry" prose describes a future capability, not this task's scope. The
  `remote-registry` flag's own ledger line in `docs/MADS-WORK-ITEMS.md` says
  "W6.8 nav entry" — i.e., this task owes the flag-gated *entry point*, not
  the feature behind it.

## 2. `ui/src/router/` additions (extend in place, don't fork)

All new calls go through the existing `routerGet`/`routerPost` helpers in
`client.ts` (idiom (b) — see §0). No new client class, no new fetch wrapper.

**One shared-file change, flag for review**: `readJSON` (the private helper
`routerGet`/`routerPost` funnel through, `client.ts:344-350`) currently
throws a plain `Error` with the raw response body text on any non-2xx
status — it does not parse `{errors:[{field,message}]}` the way idiom (a)'s
`jsonOrThrowValidation` does. The template Validate acceptance criterion
("reports all errors together") needs structured field/message pairs, not a
flattened string. Extend `readJSON` to check for a `{errors:[...]}` body on
failure and throw the existing (already-exported) `RouterValidationError`
in that case, falling back to the current plain-`Error` behavior otherwise —
mirrors `jsonOrThrowValidation`'s logic, now shared by both idioms instead
of duplicated a third time. This touches a shared helper used by Sessions
today (`useSessionsList.ts`) — low risk (additive on the error path only,
same success-path behavior) but call it out explicitly in the PR.

New functions to add to `ui/src/router/` (co-locate under
`ui/src/library/`'s own `use*.ts` hooks per shelf — see §3 — calling
`routerGet`/`routerPost` directly with the paths from §1a; no need for a
dedicated `libraryClient.ts` indirection layer beyond the per-shelf hooks).

`subscribeToEvents` (already exists, `events.ts`) covers all live-update
needs: `capability.health_changed` for the health dots, `agent.draft_generated`
for the NL-generate flow, and `agent.*`/`capability.*`/`workspace.*`/
`template.*` wildcards for list refetch-on-mutation (same "refetch, don't
trust the sparse payload" idiom as `useSessionsList.ts`).

## 3. Contract types (`ui/src/router/types.ts`)

`Workspace`/`WorkspaceRepo`/`WorkspaceDefaults` already fully match the Go
contract (`registry.go:117-154`) — no changes needed.

`Agent` (`types.ts:54-64`) is missing three fields the editor needs. Extend
in place:

```ts
export type Quadrant =
  | "autonomous_executor"
  | "technical_partner"
  | "investigator"
  | "research_analyst";

export interface AgentCapabilities {
  tools?: string[];
  mcp_servers?: string[];
  skills?: string[]; // verify exact field name against pkg/contracts/registry.go AgentCapabilities before use — captured from a partial read
}

export interface AgentPermissions {
  allowed_paths?: string[];
  denied_commands?: string[];
}

export interface AgentPreferences {
  model?: string;
}

export interface Agent {
  id: string;
  name: string;
  description?: string;
  prompt: string;
  quadrant?: Quadrant; // was `string` — narrow it, MADS-UX's quadrant tooltip needs the closed set
  tags?: string[];
  system?: boolean;
  created_at: string;
  updated_at: string;
  capabilities?: AgentCapabilities;
  permissions?: AgentPermissions;
  preferences?: AgentPreferences;
}
```

New types, mirroring `pkg/contracts/registry.go` and `pkg/agentselect/similarity.go`:

```ts
// mirrors pkg/agentselect/similarity.go Similar
export interface Similar {
  agent: string;
  score: number;
}

export type CapabilityType = "skill" | "mcp";

export interface CapabilityConfig {
  path?: string;    // skills
  command?: string; // mcp, local process
  url?: string;     // mcp, remote
}

export interface CapabilityHealth {
  healthy: boolean;
  reason?: string;
  checked_at?: string;
}

// full Capability — CapabilitySummary (already in types.ts) stays as-is for
// the / command palette; this is the Library editor's full shape
export interface Capability {
  id: string;
  name: string;
  description?: string;
  type: CapabilityType;
  config: CapabilityConfig;
  credential_env_vars?: string[]; // NAMES ONLY — see §5, never a value
  health?: CapabilityHealth;
  created_at: string;
  updated_at: string;
}

export interface Template {
  id: string;
  name: string;
  description?: string;
  source: string; // the YAML text — single source of truth, see §6
  system?: boolean;
  created_at: string;
  updated_at: string;
}
```

`pkg/template`'s parsed grammar, frontend-only (no Go JSON tags to mirror —
this is YAML the UI parses client-side for the visual view, see §6):

```ts
export interface TemplateStage {
  name: string;
  agent?: string;   // "auto" or a registry agent name; mutually exclusive with command
  command?: string;
  when?: string;
  on_fail?: { loop_back?: string; max_loops?: number; then?: string };
}

export interface ParsedTemplate {
  name: string;
  stages: TemplateStage[];
}
```

## 4. Module layout (`ui/src/library/`)

Top-level module, matching the `tasks/`/`files/`/`conversation/` convention
(flat under `src/`, not nested in `features/` — Sessions' `features/sessions/`
predates this convention and is the outlier, not the pattern to extend).

```
ui/src/library/
  HANDOFF.md                          (this file)
  LibrarySurface.tsx / .module.css / .test.tsx
                                        # owns shelf nav (4 buttons + Registry,
                                        # flag-gated) + renders active shelf;
                                        # mirrors SessionsSurface.tsx's shape

  agents/
    AgentsShelf.tsx / .test.tsx        # card grid + create button
    AgentCard.tsx / .module.css / .test.tsx
    AgentEditor.tsx / .module.css / .test.tsx
                                        # SideSheet-hosted; composes the below
    NLGenerateBox.tsx / .module.css / .test.tsx
                                        # task/description input -> POST /agents/generate,
                                        # subscribes agent.draft_generated
    PromptScaffold.tsx / .test.tsx     # static structured-prompt guidance/template text
    EnrichmentActions.tsx / .test.tsx  # Describe/Suggest capabilities/Refine buttons
    SimilarAgentWarning.tsx / .module.css / .test.tsx
                                        # renders agentselect.Similar, score, link to existing agent
    QuadrantField.tsx / .test.tsx      # select + Popover tooltip (quadrant descriptions)
    ModelField.tsx / .test.tsx         # validated dropdown from GET /providers + /providers/{name}/models
    ImportFromProviderDialog.tsx / .module.css / .test.tsx
                                        # SideSheet; fixture-only mapping preview, see §5
    useAgents.ts / .test.ts            # list + CRUD, routerGet/routerPost, refetch on agent.*
    useAgentGeneration.ts / .test.ts   # POST /agents/generate + agent.draft_generated subscription
    useAgentEnrichment.ts / .test.ts   # describe/suggest-capabilities/refine, sync POST calls

  capabilities/
    CapabilitiesShelf.tsx / .test.tsx  # card grid with HealthDot + create button
    HealthDot.tsx / .module.css / .test.tsx
                                        # green/red/gray dot, subscribes capability.health_changed
    DiagnosePanel.tsx / .module.css / .test.tsx
                                        # POST /capabilities/{id}/diagnose, shows suggestions,
                                        # local-MCP "not a live check" caveat copy (§1)
    CapabilityEditor.tsx / .module.css / .test.tsx
                                        # type switch (skill: path; mcp: command/url)
    CredentialEnvVarsField.tsx / .module.css / .test.tsx
                                        # name-list editor, see §5 — the acceptance-critical component
    useCapabilities.ts / .test.ts

  workspaces/
    WorkspacesShelf.tsx / .test.tsx
    WorkspaceEditor.tsx / .module.css / .test.tsx
    RepoList.tsx / .module.css / .test.tsx
                                        # repos + primary-repo picker (WorkspaceRepo[])
    UsedBySessions.tsx / .test.tsx     # client-side computed, see §5
    useWorkspaces.ts / .test.ts

  templates/
    TemplatesShelf.tsx / .test.tsx
    TemplateEditor.tsx / .module.css / .test.tsx
                                        # owns `source: string` state (§6), mode toggle
    VisualStageList.tsx / .module.css / .test.tsx
    StageCard.tsx / .module.css / .test.tsx
                                        # full §6 grammar fields incl. on_fail/loop_back/max_loops
    YamlSourceView.tsx / .test.tsx     # Monaco (@monaco-editor/react, already a dependency)
    templateSync.ts / .test.ts         # pure parse(source)->ParsedTemplate / serialize(ParsedTemplate)->source
    useTemplates.ts / .test.ts
    useTemplateValidation.ts / .test.ts
                                        # POST /templates/validate, holds ValidationError[] keyed
                                        # by pkg/template's field paths (stages[N].name, etc.)

  registry/
    RegistryShelfStub.tsx / .test.tsx  # flag-gated ("coming soon" + FLAGS.remoteRegistry check), see §7

  demo.tsx, library.html              # standalone review mount, mirrors tasks.html/files.html
```

`useProviderModels`-equivalent logic (provider list + model list fetch) is
already implemented inline in `ui/src/features/sessions/launchpad/pills/ModelPill.tsx`.
**Do not duplicate it.** Extract the fetch logic into
`ui/src/router/useProviderModels.ts` (new, small, cross-cutting — lives
under `router/` since both Sessions and Library depend on it, neither owns
it) and have `ModelPill` consume it too. This touches an existing W6.2 file
— flag it in the PR, keep the diff to "extract, don't change behavior."

## 5. Design notes with real gotchas

**Agents — NL-generate and draft styling.** `POST /agents/generate` takes
(per `services/router/internal/api/agents.go`'s existing
`handleGenerateAgent`) a task-based or description-based body and dispatches
async via `job.Runner`; the result lands as `agent.draft_generated`
(`{draft: contracts.Agent, similar?: Similar}`) on `GET /events` (session_id
optional — this flow has no session). `NLGenerateBox` POSTs, then
subscribes with `subscribeToEvents({types: ["agent.draft_generated",
"job.failed"]}, ...)` filtering on the response's `job_id` if the envelope
carries one (check `job.failed`'s payload shape at build time — it's
`{job_id, kind, error}`, filter client-side). Render the resulting draft
using `Draft`/`DraftChip` (`ui/src/components/Draft/`) — the kit's one
provenance treatment, already used elsewhere for generated content; don't
invent a second "draft" visual language. When `similar` is present, render
`SimilarAgentWarning` inline above the draft, not as a blocking modal (no
Modal in the kit — SideSheet or inline, per kit convention) — score and
agent name from `agentselect.Similar`, link to the existing agent's editor.

**Agents — quadrant tooltip.** `Quadrant` is a closed 4-value enum
server-side (`registry.go:7-14`) — `QuadrantField` is a `<select>` over
`contracts.Quadrants`-equivalent hardcoded list (mirror the 4 values in
TS, §3) with a `Popover` (controlled, caller owns `open`) showing each
quadrant's one-line description from REBUILD-SPEC §9's table — hardcode
that copy client-side, there's no server-side description field.

**Agents — validated model dropdown.** Reuse `useProviderModels` (§4) — same
`GET /providers` + `GET /providers/{name}/models` the launchpad already
calls successfully. "Validated" means: only list models the provider
actually reports, no free-text entry. Written to `Agent.preferences.model`.

**Agents — import-from-provider mapping preview.** No backend support for
provider-agent enumeration/import exists anywhere (not in this task's
dependency list, not elsewhere). Build `ImportFromProviderDialog` against a
**local fixture** (`ui/src/library/agents/importFixtures.ts`, a small
hardcoded list simulating what a provider might expose — e.g. Claude Code's
own subagent definitions) showing a field-mapping preview (provider field ->
`Agent` field) with no live backend call. Flag this plainly in the dialog's
empty/footer copy ("preview only — live provider import isn't built yet")
so it doesn't read as a broken feature. This is the one shelf-scope item
that's fixture-only by necessity, not by convention choice.

**Capabilities — health dots.** `HealthDot` subscribes to
`capability.health_changed` via `subscribeToEvents` and refetches (or, if
the payload is confirmed to carry the full `contracts.Capability` at build
time — likely, matching the `Created`/`Updated` sibling events' undocumented
but consistent shape — patches in place). No doc comment pins the exact
payload shape (`event.go:135` has no comment, unlike its siblings) —
**verify against the actual emitter** (`store.SetCapabilityHealth` /
wherever `capability.health_changed` is actually appended) before assuming;
worst case, refetch-on-event is always correct even if patch-in-place isn't.

**Capabilities — Diagnose panel.** Calls the new `POST
/capabilities/{id}/diagnose` (§1). Render `suggestions` as a bullet list
under the healthy/unhealthy status. For MCP-without-URL capabilities,
render the "based on configuration, not a live check" caveat (§1)
unconditionally — don't try to detect session-liveness client-side, there's
no signal available to the Library surface for that.

**Capabilities — credential env-var fields (acceptance-critical).**
`Capability.CredentialEnvVars []string` is names-only *by construction* —
the Go doc comment on the field is explicit that values live only in
`pkg/credstore`, never on this wire type (`registry.go:98-100`).
`CredentialEnvVarsField` is therefore just a string-list editor (add/remove
env var names, e.g. `ANTHROPIC_API_KEY`) — **it never fetches, displays, or
transmits a credential value**, because the `Capability` JSON this whole
shelf reads and writes structurally cannot contain one. This trivially
satisfies "credential fields display env-var names only... no value strings
appear in rendered output or requests" — the component test for this (§8)
should assert the rendered DOM and the request body contain only the name
strings the test seeds (e.g. `"GITHUB_TOKEN"`) and never a value-shaped
string, proving the component doesn't invent a display path for something
the type doesn't carry. **Do not touch `pkg/credstore`** (§1b) — there is
nothing for this UI to call there.

**Workspaces — used-by.** Templates already report `used_by` (workspaces
bound to them) server-side (`templates.go`'s `usedBy()`, filters workspaces
by `Defaults.Template == name`) — reuse via `TemplateDetail` (§1). The
reverse (which *sessions* use a given workspace) has **no server-side
equivalent** anywhere. Compute it client-side in `UsedBySessions`: fetch
`GET /sessions` (already proxied, `reads.go`) and filter by
`session.workspace === workspace.id` — same "client-side heuristic when the
backend field is missing" precedent as W6.4's Queued-display rule
(`ui/src/tasks/HANDOFF.md` §2). This is O(sessions) per workspace card;
fine at current expected scale, don't add a dedicated backend aggregate for
this task.

**Workspaces — template binding.** `WorkspaceDefaults.template` (already in
`types.ts`) is a template *name*, matching `pkg/template`'s
agent-resolution-by-name convention. The editor's template picker is a
`<select>` populated from `useTemplates()`'s list, storing the selected
template's `name` (not `id`) into `defaults.template`.

## 6. Dual-mode template editor (visual ⇄ YAML, acceptance-critical)

The Go contract already settles the "single source of truth" question:
`contracts.Template.Source` is the YAML text — "the single artifact behind
both editor views (visual and source)" per its own doc comment
(`registry.go:113-116`), and `pkg/template.Check` (parse+validate) always
operates on `[]byte` source, never a structured form. **Design accordingly**:

- `TemplateEditor` owns exactly one piece of state: `source: string`. There
  is no separate "visual model" state that can drift from it.
- Visual mode (`VisualStageList`/`StageCard`) is a **pure render+edit
  projection** of `source`: `templateSync.parse(source) -> ParsedTemplate`
  (client-side YAML parse, new `yaml` npm dependency — none exists in
  `package.json` today, confirmed) drives what `StageCard`s show; any edit
  in a `StageCard` calls `templateSync.serialize(updatedParsedTemplate) ->
  string` and replaces `source` wholesale. This is what makes "visual ⇄
  YAML stay in sync" true by construction rather than by two-way-sync logic
  — there is only ever one piece of state, two views of it, matching the
  editor pattern MADS-UX and the Go contract both already commit to.
- YAML mode (`YamlSourceView`) is a Monaco instance (`@monaco-editor/react`,
  already a dependency — no new Monaco install) bound directly to `source`.
  A parse error in YAML mode (invalid YAML, not semantic) should not crash
  the visual mode's render when switching back — `templateSync.parse`
  should return a `{parsed, error}` shape; on parse error, `VisualStageList`
  renders an inline "can't render — fix YAML syntax first" state rather
  than throwing, and the mode toggle stays enabled either direction.
- `StageCard` fields cover pkg/template's **actual, narrower** grammar —
  `Stage{Name, Agent, Command, When, OnFail{LoopBack, MaxLoops, Then}}`
  (`pkg/template/template.go:14-35`) — Agent/Command as a mutually exclusive
  radio-or-toggle (never both, matching `Validate`'s "a stage is an agent OR
  a command, not both" check), `When` free text, `OnFail` as an optional
  sub-section (loop-back target stage picker constrained to *earlier*
  stages in the list — `Validate` rejects forward/self loop-backs — plus
  `max_loops` and a `then` field that today only accepts `"block"`).
  **This is meaningfully narrower than REBUILD-SPEC §6's fuller example**
  (which shows `defaults`, `version`, `retry`, `reads`/`produces`, and a
  `gate` stage type — none of which `pkg/template.go` parses or validates).
  Build the editor against the real grammar. Flag the spec/reality gap
  explicitly in the PR description as a `pkg/template` follow-up, don't
  silently "complete" the grammar client-side with fields the backend will
  reject or silently drop.
- **Validate reports all errors together**: `POST /templates/validate` (via
  `useTemplateValidation`) sends `{source}`, gets back 200 `{valid:true}` or
  a `RouterValidationError` (after §2's `readJSON` extension) whose
  `.errors` is `contracts.ValidationError.Errors` — `[{field, message}]`
  with paths like `stages[0].name`, `stages[1].on_fail.loop_back`
  (`pkg/template/template.go:56-116`'s exact `Add()` call sites). Parse the
  `stages[N]...` prefix client-side to route each error onto the matching
  `StageCard` (highlight + inline message), with a top-level list for
  template-level errors (`name`, `stages` (empty)) that don't map to a
  specific card. This is what makes "a seeded multi-error template reports
  all errors together" true — one `Validate` call, one response, every
  error rendered at once, no per-field round trips.
- **Save never disturbs an in-flight run**: already true by construction,
  nothing to build. `Template`'s doc comment states it directly — "in-flight
  runs freeze their own copy at launch, so edits here never disturb them"
  (`registry.go:114-116`) — the Pipeline Engine reads a template once at
  launch time via its own `pkg/template.Check` call, not a live reference to
  the stored record. Document this in the editor (e.g. a passive "editing
  this won't affect runs already in progress" note near Save) rather than
  building any run-awareness into the save path — there is nothing to
  coordinate with.

## 7. Registry shelf (flag-gated stub)

MADS-UX §3.5's "Browse Registry" and the `remote-registry` flag ledger
(`W1.6 registry schema | W6.8 nav entry + W7.6 flows`) together mean this
task owes a **nav entry and a stub only** — the actual browse/import-from-
remote-registry flow is W7.6, not this task. `RegistryShelfStub` renders
only when `FLAGS.remoteRegistry` is true (add the flag per §0) as a fifth
shelf-nav button; its content is a `Panel` with placeholder copy ("Remote
registry browsing lands in a later work item") and nothing else. No fetch,
no fixture data, no real UI beyond the entry point. Don't build more here —
W7.6 owns the real design.

## 8. Test-to-acceptance-criteria map

| Acceptance criterion | Test |
|---|---|
| NL → agent flow renders a draft-styled definition from the W5.3 endpoint (providers/fake) | `agents/NLGenerateBox.test.tsx` — mock `routerPost("/agents/generate")` + `FakeEventSource.emit("agent.draft_generated", ...)`, assert the draft renders via `Draft`/`DraftChip` |
| Near-duplicate shows the similarity warning | same test file, second case — `agent.draft_generated` payload includes `similar`, assert `SimilarAgentWarning` renders with the agent name + score |
| Template editor: visual ⇄ YAML stay in sync | `templates/templateSync.test.ts` (pure parse/serialize round-trip) + `templates/TemplateEditor.test.tsx` (edit a stage in visual mode, assert the YAML view's rendered `source` reflects it, and vice versa) |
| A seeded multi-error template reports all errors together | `templates/useTemplateValidation.test.ts` — mock `routerPost("/templates/validate")` to return a `RouterValidationError` with 3+ seeded field errors (e.g. duplicate stage name, unknown agent, forward loop_back), assert all 3 render simultaneously, not just the first |
| Credential fields display env-var names only; no value strings in rendered output or requests | `capabilities/CredentialEnvVarsField.test.tsx` — seed `credential_env_vars: ["GITHUB_TOKEN"]`, assert rendered DOM contains only `"GITHUB_TOKEN"` (never a value-shaped string), and assert the `routerPost`/`routerPut` mock's captured request body has no field but the name list — this is the literal acceptance test, keep it simple and direct rather than clever |
| `make check` passes | lint + `go test -race ./...` (new Go: `job.FastModel()` getter, 4 new/extended Router handler files + tests mirroring `agents_test.go`/`retros_test.go`'s `newTestServer(t)` pattern, `capability_diagnose.go` + test) + `npm run lint` + `npm run test` (vitest, all new `.test.tsx`/`.test.ts` above) |

Also cover, not acceptance-gated but expected of every shelf per prior
precedent: `HealthDot.test.tsx` (health dot color reflects `healthy`,
updates on `capability.health_changed`), `WorkspaceEditor.test.tsx` (repo
add/remove, primary-repo toggle, template binding persists the template's
*name*), `UsedBySessions.test.tsx` (client-side filter against a seeded
session list), `LibrarySurface.test.tsx` (shelf nav switches active shelf;
Registry button absent when `FLAGS.remoteRegistry` is false).

## 9. Non-goals (explicitly out of scope, don't build)

- No Playwright/E2E — vitest component tests only, per repo-wide convention.
- No drag-and-drop stage reordering in the visual editor — up/down buttons
  or none at all (executor's call); `pkg/template`'s grammar has no
  positional semantics beyond array order and loop-back-must-be-earlier, so
  reordering is a nice-to-have, not load-bearing.
- No full Registry browse/import UI — flag-gated stub only (§7), W7.6 owns
  the real flow.
- No credential *value* handling anywhere — not display, not entry, not a
  "test credential" button. Names only, always (§5).
- No live credential-name validation against `pkg/credstore` (e.g. "does
  this env var actually resolve") — credstore has no HTTP surface and this
  task doesn't add one (§1b).
- No `pkg/template` grammar extensions (`defaults`/`version`/`retry`/
  `reads`/`produces`/`gate`) to match REBUILD-SPEC §6's fuller example —
  build against the real, narrower grammar (§6), flag the gap, don't close it.
- No live "running" signal for local MCP Diagnose — documented caveat, not
  a bug to fix here (§1).
- No wiring `LibrarySurface` into `App.tsx` beyond the one-line `SURFACES`
  registration — no changes to `RAIL_ITEMS` (the `library` entry already
  exists) and no changes to rail-item ordering/icons.

## 10. Risks / judgment calls for human review

1. **Sync vs. async for agent enrichment** (§1a): chose synchronous
   request/response for `describe`/`suggest-capabilities`/`refine` over the
   job+event pattern full generation uses. Reasonable given these are fast
   single-call form-fills, but it's a real API-shape decision a human should
   confirm before it's load-bearing elsewhere.
2. **`readJSON` extension in `client.ts`** (§2): a shared-file change
   touching Sessions' existing code path, done to unify error handling
   rather than add a third parallel error-parsing helper. Low risk
   (additive on the failure path only) but worth a second look given it's
   outside `ui/src/library/`.
3. **`ModelPill` extraction into `useProviderModels`** (§4): another
   shared-file touch outside this module, done to avoid duplicating
   provider/model fetch logic a third time (Sessions, now Library). Same
   "flag it, low risk, but not purely additive" profile as #2.
4. **Import-from-provider is fixture-only** (§5): no backend support exists
   for this at all; shipping a clearly-labeled preview-only dialog rather
   than deferring the whole feature. A human should confirm "preview only"
   framing is acceptable for this acceptance-adjacent (but not
   acceptance-gated) scope item rather than cutting it entirely.
5. **`agentgen.Describe`/`SuggestCapabilities`/`Refine`'s exact return
   shapes weren't re-verified against the new handler's response-body
   design** in this pass (noted in §1a) — confirm against
   `pkg/agentgen/agentgen.go`'s actual signatures before finalizing the
   three new Router handlers' JSON shape.
6. **`AgentCapabilities`'s exact field names** (§3, `skills` field) were
   captured from a partial read of `pkg/contracts/registry.go` — verify the
   exact JSON tag before shipping the TS type; a wrong field name here
   silently drops data on save rather than erroring.

## 11. README

Add a `## Library` section to `ui/README.md`, inserted after `## Pipeline
tab` and before `## Test it` (matching the existing per-surface section
order). Cover: `npm run library` (new script + `library.html` +
`vite.config.ts` input entry, mirroring `tasks`/`files`/`pipeline`'s
existing three-line addition each), the four shelves + flag-gated Registry
stub, the idiom-(b) Router-client choice and why (§0), and a pointer to this
HANDOFF for the backend-gap and dual-mode-editor details rather than
duplicating them in the README.

## 12. Executor notes (fill in during/after implementation)

**Status**: all four shelves (Agents, Capabilities, Workspaces, Pipeline
templates) plus the flag-gated Registry stub are built, wired into
`LibrarySurface`, and mounted via `npm run library` (`library.html` +
`demo.tsx` + `vite.config.ts`/`package.json` entries mirroring `tasks`/
`files`). `npx vitest run src/library` — 34 test files, 83 tests, all
passing. `npx tsc --noEmit -p .` — clean. Idiom (b) (`routerGet`/
`routerPost`/`routerPut`/`routerDelete` + `subscribeToEvents`) was used
exclusively throughout, per §0; no `FakeRouterClient` fixture exists for
Library's demo mount as a result (see the README's Library section for the
consequence — it hits a real Router via `VITE_ROUTER_URL` instead).

**Acceptance criteria — all four verified directly**:
- NL → agent draft styling + similarity warning: `useAgentGeneration.test.ts`
  and `SimilarAgentWarning.test.tsx` drive this against the `agent.
  draft_generated` event.
- Template editor visual↔YAML sync: `TemplateEditor.test.tsx` covers both
  directions (visual edit reflected in YAML source, YAML edit reflected back
  in visual), built to hold by construction — `TemplateEditor` owns exactly
  one state variable (`source: string`); visual mode is a pure projection.
- Multi-error validation: `useTemplateValidation.test.ts` seeds three
  simultaneous field errors (duplicate stage name, unknown agent, forward
  loop_back) and asserts all three surface together, not just the first.
- Credential env-var names only: `CredentialEnvVarsField.test.tsx` asserts
  by negative regex against `document.body.textContent` that no value-shaped
  string appears, and that `onChange` receives a plain `string[]`. This
  holds structurally, not just by test coverage — `CredentialEnvVars` is
  typed `string[]` end to end, so a value has nowhere to enter the tree.

**README anchor mismatch (§11)**: §11 says to insert the new section "after
`## Pipeline tab`," but no such section exists in `ui/README.md` — the
closest sibling sections are `## Files tab` and `## Test it`. Inserted `##
Library` between those two instead, which is almost certainly the intended
spot (§11 also lists this as one of three "mirroring tasks/files/pipeline"
demo entries, i.e. the same discrepancy — there's no `pipeline` npm script
either, only `tasks`/`files`). Reviewer: confirm this is just the HANDOFF
lagging an earlier rename rather than a section this executor missed.

**§10 risk items — reviewer attention**:
- #3 (`ModelPill` extraction into a shared `useProviderModels`) and #4
  (import-from-provider is a fixture-only preview dialog, no backend
  support) both landed as described; #4 in particular ships a UI affordance
  with zero live backend behind it — confirm that framing is still
  acceptable before this reaches real users.
- #1, #2, #5, #6 are backend/shared-file items from earlier stages in this
  same pipeline run (dataclient methods, Router proxy routes, sync
  enrichment endpoints, `client.ts`'s `readJSON` extension). This stage's
  context did not carry forward direct visibility into that work's diff, so
  they're flagged here unverified rather than re-asserted as resolved —
  worth a dedicated look at the actual Go diff before treating them as
  closed, especially #6 (`AgentCapabilities` field-name accuracy, silent
  data loss on save if wrong) and #5 (`agentgen` return-shape verification).

**Not built (confirmed non-goals per §9)**: real remote-registry browsing
(stub only, behind `FLAGS.remoteRegistry`), server-side `workspace ->
sessions` lookup (client-side filter over `GET /sessions` instead, per §5),
Template → Workspace server-side `used_by` (only the Workspace → Session
direction is in scope per the module layout).
