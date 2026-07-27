---
name: playwright-ui-testing
description: Use this skill whenever a change touches ui/src and needs verification beyond vitest — a new flow, a modified journey, or a visual regression risk. Teaches how to run this repo's Playwright suite (ui/e2e), write a throwaway spec to exercise the change, and read failures via trace/screenshot. Covers the worker-parallelism pitfall specs must opt out of when they boot their own backend topology.
---

# Verifying UI work with Playwright

This repo has two UI test layers (`ui/README.md`'s "Test layers"): fast
vitest component tests (`make check`), and `ui/e2e/` — a Playwright journey
suite against a real, hermetic stack (real `data`/`router`/`session`
binaries, the real built UI, a fake LLM provider, no mocks). Use this skill
for the second layer: proving a UI change actually works end to end, not
just that a component renders against a fixture.

## Running the suite

From the repo root:

```
make ui-smoke
```

This builds the UI (`VITE_ROUTER_URL=""` so it's same-origin against the
Router), installs the pinned Chromium build if missing, and runs
`npm --prefix ui run test:e2e`. It is **not** part of `make check`/CI's fast
job — real processes, tens of seconds, run it explicitly when verifying UI
work.

To iterate faster once the UI is built, run Playwright directly from
`ui/e2e` against a single file:

```
npx --prefix ui playwright test --config e2e/playwright.config.ts e2e/dashboard.spec.ts
```

Playwright's own flags compose normally: `--headed` to watch it, `--debug`
for the inspector, `-g "<test name>"` to filter to one test.

## Workers, parallelism, and why first-run.spec.ts is serial

`playwright.config.ts` sets `fullyParallel: true`: by default every spec
file may run in its own worker, concurrently. Most specs (`dashboard.spec.ts`,
`sessions.spec.ts`, etc.) are safe under this because they share **one**
topology booted once in `globalSetup.ts` and read it from
`process.env.MADS_ROUTER_URL` / `MADS_DATA_URL` / `MADS_BIN` /
`MADS_WORKSPACE_ID` — they never boot their own backend.

`first-run.spec.ts` is the exception: it exercises the pre-onboarding wizard,
so it can't reuse the shared, already-onboarded topology from
`globalSetup.ts`. It boots its own `data`/`router`/`session` stack in
`test.beforeAll`/tears it down in `test.afterAll`, storing them in
module-level variables (`topology`, `fastModel`). If Playwright ran two
`test()` blocks in that file concurrently, both would race over the same
module-level state and startup/teardown would collide. That's why the file
opens with:

```ts
test.describe.configure({ mode: "serial" });
```

**The rule for a new spec:** if it reuses the shared topology (the common
case — just read the `MADS_*` env vars like `dashboard.spec.ts` does), no
serial configuration is needed; `fullyParallel` is fine. If it boots its own
topology instead (only justified for flows the shared, already-onboarded
stack can't represent, like first-run), add
`test.describe.configure({ mode: "serial" })` at the top of that file.

## Writing a throwaway spec to verify a change

Don't try to shoehorn a one-off check into an existing journey spec. Add a
new file under `ui/e2e/`, following `dashboard.spec.ts`'s shape:

```ts
import { test, expect } from "./fixtures";

test("throwaway: <describe what you're checking>", async ({ page, routerURL }) => {
  await page.goto(routerURL);
  // ... drive the UI through the changed flow ...
  await expect(page.getByText("...")).toBeVisible();
});
```

- Import `test`/`expect` from `./fixtures`, not `@playwright/test` directly
  — the fixture wraps `page` with a tripwire that fails the test on any
  unexpected console error or 4xx/5xx response, which catches wiring bugs
  (an endpoint the Router never implemented) that a passing assertion alone
  would miss. Call `page.allowStatus("<url substring>")` first for any
  request that's expected to fail as part of the journey.
- Use `process.env.MADS_ROUTER_URL` (or the `routerURL` fixture) and the
  helpers in `ui/e2e/lib/seed.ts` (`createSession`, `importTasks`,
  `runFakePipeline`, `setupWorkspace`) to set up state — don't reimplement
  session/task creation by driving the UI when an API helper already exists.
- Run just that file while iterating (see above), then run the full
  `make ui-smoke` once before calling the change verified — a spec that
  passes alone can still fail once other specs' state shares the topology.

## Reading a failure: traces and screenshots

The config already turns these on for failures only (`trace:
"retain-on-failure"`, `screenshot: "only-on-failure"`), written under
`ui/test-results/`. On a failure:

```
npx playwright show-trace ui/test-results/<test-folder>/trace.zip
```

The trace viewer gives you the DOM snapshot, network requests, and console
output at every step — almost always faster than adding `console.log` and
re-running. The screenshot (`test-failed-1.png` in the same folder) is
enough for a quick visual check without opening the trace viewer at all.

## Cleaning up

A throwaway spec is scaffolding, not a deliverable: once it's done its job
of proving the change works, delete the spec file before committing (`git
status` / `git diff` should show no new file under `ui/e2e/` unless the
journey is worth keeping as a permanent regression test — in that case, say
so explicitly and give it a real name, not "throwaway"). Playwright's own
`ui/test-results/` output is already gitignored and safe to leave behind or
delete either way.
