# Dashboard tab — HANDOFF (W6.9, M7, MADS-UX §3.6)

Architect deliverable for the executor stage. Read this in full before writing
code — it documents a backend gap that must be closed as part of this work
item, and a fixture strategy that must be followed exactly (not invented).

## §0 Coordination risk

W6.9 depends only on **W6.1** (kit/tokens) and **W5.7** (metrics query views) —
there is no formal dependency on **W6.6 Pipeline tab**, despite the "run
history → drill-back to the session's Pipeline tab" requirement. Checked: the
Pipeline tab (`ui/src/pipeline/`) already landed (see recent merge commits).
Its `PipelineClient`/`PipelineTab` aren't imported by this module — drill-back
is implemented as a callback contract (§7), not a direct import of Pipeline
tab internals, so this doesn't create a real ordering dependency. If Pipeline
tab's module path or props shape changes later, only §7's callback signature
needs revisiting.

No shell-level cross-surface router exists yet (see §8) — every landed tab
(Conversation, Tasks, Files, Pipeline) is still reachable only via its own
standalone demo mount, not through `App.tsx`. Dashboard is the exception:
because it's a top-level `IconRail` destination (peer of Sessions/Retro/
Library), not a per-session sub-tab, it genuinely can get real one-line shell
registration today. Don't let that fact tempt you into also wiring Pipeline
tab into the shell as a drive-by — out of scope, do it only if a future work
item asks.

## §1 Backend reality check (MADS-UX spec gap — case (a), cheap, in scope)

MADS-UX §3.6 lists the Dashboard's reads as `GET /metrics/*` and
`GET /pipeline-runs`. Verified against the real Go code:

- `GET /pipeline-runs` doesn't exist under that name anywhere. The real route
  is `GET /metrics/pipelines` (Data Service).
- **The Router has zero `/metrics/*` routes.** Confirmed by reading
  `services/router/internal/api/api.go`'s full `mux.HandleFunc` list — only
  the Data Service (`services/data/internal/api/metrics.go`) serves them:
  `GET /metrics/cost`, `/metrics/agents`, `/metrics/tasks`,
  `/metrics/capabilities`, `/metrics/pipelines`.
- **This is the cheapest possible version of the gap.** `pkg/dataclient.Client`
  already has every method the Router needs to proxy —
  `MetricsCost(ctx, sessionID)`, `MetricsAgentLeaderboard(ctx)`,
  `MetricsTaskOutcomes(ctx, sessionID)`, `MetricsCapabilityUsage(ctx)`,
  `MetricsPipelineRuns(ctx, sessionID)` — all confirmed at
  `pkg/dataclient/dataclient.go:466-511`, all exercised by
  `pkg/dataclient/metrics_test.go`. So closing the gap is *five thin handlers
  that each call one existing client method*, not new client or store work.

**In scope for this work item:** add `services/router/internal/api/metrics.go`
with 5 handlers, following `handleRetryTask`'s exact shape
(`services/router/internal/api/pipelines.go:45-59` — look up via `s.data`,
`writeError`/`writeStoreError` on failure, `writeJSON` on success), e.g.:

```go
func (s *server) handleMetricsCost(w http.ResponseWriter, r *http.Request) {
	m, err := s.data.MetricsCost(r.Context(), r.URL.Query().Get("session_id"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, m)
}
```

Repeat for the other four, nil-slice-normalizing exactly like the Data
Service's own handlers do (`services/data/internal/api/metrics.go` — copy its
nil-checks verbatim). Add 5 `mux.HandleFunc` lines to `api.go`'s route list
(`GET /metrics/cost`, `GET /metrics/agents`, `GET /metrics/tasks`,
`GET /metrics/capabilities`, `GET /metrics/pipelines`). Add
`services/router/internal/api/metrics_test.go` mirroring the style of
`pipelines_test.go`/`tasks_test.go` (fake/stub `s.data`, assert the handler
forwards query params and the response body unchanged). This is Go work, not
UI work — flag it clearly to whoever picks up the executor role so it isn't
missed as "just a frontend task."

No other spec gap exists for this work item. `docs/MADS-UX.md`'s "Subscribes:
`session.*`, `pipeline.*` for the live widget only" and "Actions: none" match
reality — `GET /events` (already Router-proxied) is the only subscription
surface needed, and there are no write actions on this tab besides
navigation.

## §2 Data model & fetch strategy

Dashboard is **system-level**, not session-scoped (MADS-UX §3.6: "System-level
only, data-not-interpretation"). Every metrics call below passes `session_id`
as empty/omitted — confirmed this is meaningful, not just "leave it blank and
hope": `pkg/dataclient/metrics_test.go`'s `TestMetricsCost` calls
`c.MetricsCost(ctx, "")` and gets the aggregate **across both seeded
sessions**, not an error or an empty result. So an empty `session_id` is the
documented "all sessions" query, not an edge case to special-case.

Five independent fetches, no cross-dependency between them:

| View | Client call | Depends on date preset? |
|---|---|---|
| Cost by model | `metricsCost()` → `CostMetrics` | Yes — filter `by_day` client-side to the selected range; `by_session`/`by_pipeline_run` are all-time totals, not day-bucketed, so presets don't affect them |
| Agent leaderboard | `metricsAgentLeaderboard()` → `AgentLeaderboardEntry[]` | No — the endpoint has no time filter; don't invent client-side date filtering the backend doesn't support |
| Task outcomes | `metricsTaskOutcomes()` → `TaskOutcomes` | No, same reason |
| Capability usage | `metricsCapabilityUsage()` → `CapabilityUsage[]` | No, same reason |
| Run history | `metricsPipelineRuns()` → `PipelineRun[]` | Yes — filter client-side on `started_at` |

Only `CostPoint.day` and `PipelineRun.started_at` carry a timestamp to filter
on; the other three views have no time axis in their response shape at all.
**Don't build a five-way "date preset changes everything" abstraction** — only
the cost chart and run history table actually react to the preset. Filtering
happens client-side, in memory, on the one already-fetched response; the date
preset does not trigger a new network request (there's no `since`/`until`
query param on any of these five routes to send one to).

"Charts refresh on view" (per the task's acceptance text) = refetch all five
on mount, no polling. Add a manual refresh affordance only if you find one
already established elsewhere in this repo's convention (there isn't one in
Pipeline/Tasks/Files) — don't invent a new UI convention for this alone.

## §3 Module layout

```
ui/src/dashboard/
  HANDOFF.md
  DashboardTab.tsx              # composes everything below; owns date-preset state
  DashboardTab.module.css
  DashboardTab.test.tsx
  DashboardDemo.tsx              # demo-mount wrapper, fake client + recorded fixtures
  DashboardDemo.module.css
  dashboard-main.tsx             # createRoot(...).render(<DashboardDemo />)
  client/
    metricsClient.ts             # MetricsClient interface + FetchMetricsClient
    metricsClient.test.ts
    fixtures.ts                  # recorded golden values, see §5
  DatePresets/
    DatePresets.tsx               # preset chips: 7d / 30d / 90d / all
    DatePresets.module.css
    DatePresets.test.tsx
  StatTiles/
    StatTiles.tsx                 # small headline numbers derived from the 5 responses
    StatTiles.module.css
    StatTiles.test.tsx
  CostChart/
    CostByModelChart.tsx          # stacked bar, legend, per-column tooltip, table toggle
    CostByModelChart.module.css
    CostByModelChart.test.tsx
  AgentLeaderboard/
    AgentLeaderboardTable.tsx
    AgentLeaderboardTable.module.css
    AgentLeaderboardTable.test.tsx
  TaskOutcomes/
    TaskOutcomesPanel.tsx         # status-distribution bar + table toggle
    TaskOutcomesPanel.module.css
    TaskOutcomesPanel.test.tsx
  CapabilityUsage/
    CapabilityUsageTable.tsx      # table only; "never used" Chip when invocations === 0
    CapabilityUsageTable.module.css
    CapabilityUsageTable.test.tsx
  RunHistory/
    RunHistoryTable.tsx           # table with a drill-back action per row
    RunHistoryTable.module.css
    RunHistoryTable.test.tsx
  LiveWidget/
    LiveWidget.tsx                # active sessions + running pipelines, live
    LiveWidget.module.css
    LiveWidget.test.tsx
  state/
    useDashboardMetrics.ts        # fetch-on-mount for all 5 views; exposes loading/error per view
    useDashboardMetrics.test.ts
    useLiveActivity.ts             # refetch-on-event for the live widget only
    useLiveActivity.test.ts
```

Each leaf component takes plain data props (`CostMetrics`, `PipelineRun[]`,
etc.) and has no client/fetch code of its own — `DashboardTab` is the only
component that touches `useDashboardMetrics`/`useLiveActivity`. This mirrors
Pipeline tab's `PipelineTab` → `RunHeader`/`StageBoard`/`BlockedPanel` split
(HANDOFF §2 there), not a new pattern.

## §4 Client design

Follow Pipeline tab's precedent exactly: a **dedicated client interface**
scoped to this tab, reusing the shared low-level `routerGet` helper — not an
extension of the shared `RouterClient` (`ui/src/router/client.ts`), which is
already documented as its own separate idiom (`ui/README.md`: "unifying them
is follow-up work").

```ts
// client/metricsClient.ts
export interface MetricsClient {
  metricsCost(): Promise<CostMetrics>;
  metricsAgentLeaderboard(): Promise<AgentLeaderboardEntry[]>;
  metricsTaskOutcomes(): Promise<TaskOutcomes>;
  metricsCapabilityUsage(): Promise<CapabilityUsage[]>;
  metricsPipelineRuns(): Promise<PipelineRun[]>;
  listSessions(): Promise<Session[]>;              // for the live widget (§7)
}

export class FetchMetricsClient implements MetricsClient {
  async metricsCost() { return routerGet<CostMetrics>("/metrics/cost"); }
  async metricsAgentLeaderboard() { return routerGet<AgentLeaderboardEntry[]>("/metrics/agents"); }
  async metricsTaskOutcomes() { return routerGet<TaskOutcomes>("/metrics/tasks"); }
  async metricsCapabilityUsage() { return routerGet<CapabilityUsage[]>("/metrics/capabilities"); }
  async metricsPipelineRuns() { return routerGet<PipelineRun[]>("/metrics/pipelines"); }
  async listSessions() { return routerGet<Session[]>("/sessions"); }
}
```

No `session_id` param on any of the five — per §2, this tab always queries
system-wide. `listSessions()` reuses the real, already-proxied `GET /sessions`
route (`useSessionsList.ts`'s own call) — don't add a session-scoping param
here either, the Dashboard needs the full list to derive an active-session
count.

`CostMetrics`, `AgentLeaderboardEntry`, `TaskOutcomes`, `CapabilityUsage`,
`PipelineRun` need TS mirrors of `pkg/contracts/metrics.go` added to
`ui/src/router/types.ts` (shared, since they're generic Data Service
contracts, not Dashboard-private) — field names/casing exactly as the Go
`json:` tags say (e.g. `input_tokens`, `success_rate`, `run_seq`,
`tasks_blocked`). `TaskOutcomes.counts` is `Record<TaskStatus, number>` —
reuse the existing `TaskStatus` union in `types.ts`, don't redeclare it.

## §5 Fixture / golden-data strategy — do not invent numbers

Per this repo's established convention (Pipeline tab's `recordedRun.ts`:
"a one-time, checked-in recording, not a live generator"), `client/fixtures.ts`
must contain **literal values copied from the real Go golden tests**, not
hand-invented numbers. The exact source for each view:

- Cost: `pkg/dataclient/metrics_test.go:TestMetricsCost` (lines 27-124) — 2
  sessions, 2 models (`model-a`, `model-b`), 2 days (2026-07-10, 2026-07-11),
  one pipeline run wrapping one usage event. Concrete `by_day` values: day1
  model-a `{in:100, out:50, cost:0.10}`, day1 model-b
  `{in:300, out:150, cost:0.30}`, day2 model-a (aggregated)
  `{in:250, out:125, cost:0.25}`. `by_session` totals: sess1 `0.30`, sess2
  `0.35`. `by_pipeline_run`: one entry, `cost:0.05`.
- Agent leaderboard: `TestMetricsAgentLeaderboard` (line 128).
- Task outcomes: `TestMetricsTaskOutcomes` (line 186).
- Capability usage: `TestMetricsCapabilityUsage` (line 241) — make sure at
  least one fixture entry has `invocations: 0` so the "never-used" flag has
  something to render; `pkg/contracts/testdata/capability_usage.json`
  (`{"capability":"git-review","type":"skill","invocations":0}`) already is
  exactly that case if the Go test doesn't cover it.
- Run history: `TestMetricsPipelineRuns` (line 290);
  `pkg/contracts/testdata/pipeline_run.json` is a single golden instance
  (`session_id: ses_01hq3k`, `run_seq: 3`, `outcome: completed`,
  `cost_usd: 0.42`, `agents: [architect, executor]`) usable as one row.

Read the four test functions not yet read in this session
(`TestMetricsAgentLeaderboard`/`TestMetricsTaskOutcomes`/
`TestMetricsCapabilityUsage`/`TestMetricsPipelineRuns`, all in
`pkg/dataclient/metrics_test.go`) before writing `fixtures.ts` — copy their
seeded inputs' resulting aggregates verbatim, the same way `recordedRun.ts`
copied `TestDefaultTemplateReviewLoopBack`'s envelope sequence. **"Golden
aggregates from W5.7 fixtures" in the acceptance criteria means these five
tests' expected outputs specifically — not data you make up that merely looks
plausible.**

`DashboardDemo.tsx` uses a fake `MetricsClient` returning these fixtures
synchronously (resolved promises), same shape as `PipelineDemo.tsx`'s fake
client.

## §6 Chart implementation — cost-by-model stacked bar

Consulted the `dataviz` skill for the mark-level spec; the repo's own
`tokens.css`/`tokens.test.ts` already supplies and pins the palette (§6a). Do
not build a new "palette validator" — none exists in this repo (confirmed by
grep across `ui/`), and the pre-existing pinning tests already satisfy the
acceptance criterion "chart passes the palette validator in both themes."

**Marks:** stacked bars, one bar per day (`by_day`, grouped by `day`, stacked
by `model`), `--chart-1`/`--chart-2`/`--chart-3` assigned to models in a fixed
order (first-seen order across the fixture, not re-sorted by value — "color
follows the entity, never its rank"). A 4th+ model folds into an "Other"
bucket rather than cycling a 4th hue — there are only 3 chart tokens, this is
a hard ceiling, not a style choice. **2px surface-color gap between stacked
segments** (matches the dataviz skill's own default: "a 2px surface gap
between fills — stacked segments and adjacent bars alike") — this is smaller
than `--space-1` (4px), so it's a literal `2px` in the component's CSS, not a
spacing-token reference; don't invent a `--space-0` token for one usage.

**Legend:** always present (≥2 series triggers the skill's "legend always
present" rule; this chart has up to 3+Other). Legend swatches read
`var(--chart-1)` etc. directly — never a hex literal anywhere in this
component. A targeted test (`CostByModelChart.test.tsx`) should assert no
component output contains a raw hex string, so a regression can't slip a
hardcoded color back in.

**Tooltip:** "per-column tooltips" = one hover tooltip per day-column showing
that day's per-model breakdown (not one tooltip per individual segment) —
matches the dataviz skill's bar-hover-layer default. Text inside the tooltip
uses text tokens (`--text`/`--text2`), never the series color, for the
numbers/labels; only the swatch dot carries the series color.

**Table toggle:** a visible toggle switches the same data into a plain
`<table>` — one row per day, one column per model plus a total column. This
is the accessible fallback the acceptance criteria requires ("every chart has
... the table toggle"); it is not optional or a "nice to have."

**Form check:** cost-over-time-by-model is unambiguously a stacked-bar job
(magnitude + composition over time, one axis) — no dual-axis temptation here
since there's only one measure (cost/tokens), consistent with the skill's
"one axis" non-negotiable.

Task outcomes (`TaskOutcomesPanel`) is a status distribution — apply the same
legend + table-toggle rule, but it does not draw from `--chart-*` (those are
reserved for categorical *identity*, not task *status*); reuse the existing
semantic status tokens (`--ok`/`--warn`/`--bad`/`--info`) the same way
`Chip`'s `ChipTone` already does, since task status is a status encoding, not
a categorical one — don't put `--chart-*` tokens on a chart where the
semantic status tokens are the correct fit instead.

## §7 Live widget & drill-back

**Live widget** ("active sessions + running pipelines (live)", MADS-UX §3.6):
refetch-on-event, exactly `useSessionsList.ts`'s idiom, not a payload-folding
reducer — there's a real GET to refetch for both numbers, so this doesn't
qualify for the fold-into-reducer idiom (Pipeline tab HANDOFF §2's
distinction). `useLiveActivity.ts`:
- fetches `client.listSessions()` and `client.metricsPipelineRuns()` on mount
- active-session count = `sessions.filter(s => s.activity === "active").length`
- running-pipeline count = `runs.filter(r => r.outcome === "running").length`
- subscribes to `session.*` and `pipeline.*` via `subscribeToEvents` (already
  proxied, `ui/src/router/events.ts`) and refetches both on any matching event
- **not** date-preset-scoped — this is a live "right now" count, independent
  of the cost chart's date range

**Drill-back:** there is no cross-surface router in this codebase yet (see
§0/§8 — every tab is its own standalone Vite entry point). "Drill-back
navigation from run history is wired" therefore means: `RunHistoryTable`
takes an `onDrillBack?: (params: { sessionId: string; runSeq: number }) =>
void` prop and calls it from each row's action; `DashboardTab` forwards its
own `onDrillBack` prop through unchanged. Test that clicking a row invokes the
callback with the right `sessionId`/`runSeq` — that satisfies "wired." Actual
navigation behavior (opening `pipeline.html?session=...`, or a future in-app
router) is the caller's decision and out of scope for this module; document
this explicitly as a residual gap, don't half-implement a `window.location`
hack to make it feel more "done" than it is.

## §8 Demo mount & shell registration

Standard demo mount, same as Conversation/Tasks/Files/Pipeline:
- `ui/dashboard.html` (copy `pipeline.html`'s shape, point at
  `/src/dashboard/dashboard-main.tsx`)
- `vite.config.ts`: add `dashboard: resolve(__dirname, "dashboard.html")` to
  `rollupOptions.input`
- `package.json`: add `"dashboard": "vite --open /dashboard.html"` script
- `ui/README.md`: new "## Dashboard tab" section, same shape as the existing
  tab sections (ownership blurb, `npm run dashboard`, fixture/architecture
  notes)

**Additionally, real shell registration** (genuinely in scope here, unlike
prior tabs — see §0): `App.tsx`'s `RAIL_ITEMS`/`SURFACES` currently only has
`sessions` (confirmed by reading `App.tsx` — `retro`/`library` are rail
buttons with no `SURFACES` entry yet, i.e. they render the "no surfaces wired
up" empty state). Add one `RAIL_ITEMS` entry (`{ id: "dashboard", label:
"Dashboard", icon: "D" }`) and one `SURFACES` entry (`dashboard:
DashboardTab`) — literally the two-line registration the task brief calls
"one-line shell registration." `DashboardTab` in this position mounts a real
`FetchMetricsClient`, not the demo's fake one.

## §9 Test plan → acceptance criteria map

vitest component tests only, no Playwright (explicit in the task brief).

| Acceptance criterion | Test |
|---|---|
| Every view renders golden aggregates from W5.7 fixtures | One `.test.tsx` per leaf component, rendering against `client/fixtures.ts`'s recorded values, asserting the exact numbers appear |
| Chart uses `var(--chart-*)` exclusively, no hex | `CostByModelChart.test.tsx` regex-checks rendered output/inline styles for raw hex literals (fail if found) |
| Chart offers the table toggle | `CostByModelChart.test.tsx` toggles it and asserts the table's cells match the chart's underlying data |
| Drill-back navigation from run history is wired | `RunHistoryTable.test.tsx` clicks a row's drill-back action, asserts `onDrillBack` fires with the right `{sessionId, runSeq}` |
| Live widget updates on `session.*`/`pipeline.*` | `useLiveActivity.test.ts`, same shape as `useSessionsList.test.ts` — fire a fake event via `testFakeEventSource.ts`, assert a refetch happened |
| Date presets affect cost chart + run history only | `DashboardTab.test.tsx` changes the preset, asserts cost/run-history re-slice client-side while leaderboard/outcomes/capability counts stay unchanged |
| `make check` passes | run it before calling this done — Go build (the new Router proxy handlers) is part of that, not just `npm run lint`/`test` |

## §10 Non-goals

- Not building a generic cross-surface router/deep-linking system — out of
  scope, and no other tab has one yet either (§7).
- Not adding `since`/`until` query params to any of the 5 Data Service
  routes — the date presets are a client-side view over already-fetched data
  (§2), not a new backend capability.
- Not adding a manual "refresh" button — no other landed tab has established
  that convention; "charts refresh on view" means on mount, not on-demand.
  If a future work item wants manual refresh across the whole UI, that's a
  shared-convention change, not a Dashboard-specific one.
- Not touching Pipeline tab's own files — drill-back is a callback contract,
  not an import (§0/§7).
- Not adding a new shared `Table`/`Tabs` component to `ui/src/components/` —
  build the table-toggle and preset-chip UI as Dashboard-local components
  (`RunHistoryTable`, `DatePresets`, etc.), matching how Pipeline tab built
  its own `StageBoard`/`TaskCard` rather than generalizing into the shared
  kit prematurely.

---

## §11 Executor notes

Built in the order §3/§0 lays out: `services/router/internal/api/metrics.go`
(§1, 5 thin proxy handlers + `metrics_test.go` mirroring `pipelines_test.go`'s
style), `ui/src/router/types.ts`'s 5 metrics types + `TaskOutcomes.counts:
Record<TaskStatus, number>`, `client/metricsClient.ts` + `client/fixtures.ts`
(golden values copied from `pkg/dataclient/metrics_test.go`), the two state
hooks (`useDashboardMetrics`, `useLiveActivity`), then all 8 leaf components
in the §3 order, then `DashboardTab.tsx` + `DashboardDemo.tsx` +
`dashboard-main.tsx` + `dashboard.html` + shell/build registration +
`ui/README.md`. Root `make check` passes clean: Go tests (`-race`, all
packages including the new `services/router/internal/api/metrics_test.go`),
`eslint .`, and `vitest run` (89 files / 437 tests, 12 files / 27 tests under
`src/dashboard/`). `npx tsc --noEmit` and a full `vite build` (all 7 entry
points including `dashboard`) also pass clean.

**Deviations from this doc, flag for review:**

- **"Other" bucket color for `CostByModelChart`**: §6 specifies the 3
  `--chart-*` tokens are a hard ceiling with a 4th+ model folding into
  "Other," but doesn't say what fills that bucket's segment/swatch. Chose
  `var(--text3)` (a muted text token, not a chart or hex value) as the most
  neutral non-identity-claiming choice available. Every golden fixture only
  ever has 2 models, so this branch is not exercised by any golden-aggregate
  assertion — it's an untested judgment call. Scrutinize whether a
  dedicated neutral token would be preferable to reusing a text token for a
  fill.
- **Status→semantic-token mapping for `TaskOutcomesPanel`**: §6 says to
  reuse `--ok`/`--warn`/`--bad`/`--info` "the same way `Chip`'s `ChipTone`
  already does" but doesn't give an exact per-status table, and there are 6
  `TaskStatus` values against only 4 semantic tokens. Mapped `done`→ok,
  `in_progress`/`ready`→info, `needs_review`/`wont_do`→warn, `blocked`→bad.
  Two token/status pairs are non-unique (info and warn each cover two
  statuses), which is why every legend/table/segment entry also carries a
  visible text label — the dataviz skill's "identity never rests on color
  alone" rule made this acceptable, but it's a judgment call not explicitly
  specified. Scrutinize the specific pairings if task-status semantics
  matter more finely than this.
- **`DashboardTab.test.tsx`'s date-preset test uses `vi.spyOn(Date, "now")`**
  rather than `vi.useFakeTimers()`, since fake timers interfered with
  `@testing-library/react`'s internal `waitFor` polling. This only pins
  `Date.now()`; real timers still run underneath.

**Not done / residual risk:**

- **Drill-back is a callback contract only, exactly as §7 specifies as
  in-scope** — `RunHistoryTable`'s `onDrillBack` prop is wired through
  `DashboardTab` and tested (`DashboardTab.test.tsx` asserts it fires with
  the right `{sessionId, runSeq}`), but no actual navigation to
  `pipeline.html?session=...` or any in-app router exists. This is by
  design per §7/§10, not an oversight — flagging again here so it isn't
  mistaken for unfinished work when reviewing the shell registration.
  `DashboardDemo.tsx` passes an `onDrillBack` that just `console.log`s the
  params, as a visible demo affordance for the wiring.
- **No browser/dev-server visual smoke test of `dashboard.html`.** This
  environment has no browser-automation tool available (consistent with the
  Pipeline tab precedent, `ui/src/pipeline/HANDOFF.md` §10). Verified
  instead via a clean `vite build` of all 7 entry points (output discarded
  after the check) plus the full component/hook test suite — the demo has
  not been visually confirmed to render correctly in an actual browser.
  Whoever reviews this should run `npm run dashboard` and eyeball the chart,
  tooltip, and table-toggle once.
- Everything in §10 (Non-goals) remains undone by design: no cross-surface
  router, no `since`/`until` backend query params, no manual refresh
  button, `pipeline/` untouched, no new shared `Table`/`Tabs` kit component.
