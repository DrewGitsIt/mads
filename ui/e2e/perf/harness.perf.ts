import { mkdirSync, writeFileSync } from "node:fs";
import path from "node:path";
import { test, expect } from "@playwright/test";
import { abortActive, loadPlan, runOne } from "./measure";
import { scenarioById } from "./scenarios";

/**
 * One test per (scenario, repetition, build), in ABAB order within each
 * scenario so machine drift lands on both builds alike. Each test boots its
 * own stack on a fresh copy of the run's one seeded database, in a fresh
 * browser context (or a fresh Electron app), and writes its raw result to
 * <runDir>/raw/. globalTeardown turns those into the report.
 */
const plan = loadPlan();

// Not serial mode: one failed repetition must not skip the rest. Order is
// still declaration order (one worker, fullyParallel off).
// A repetition that times out never reaches runOne's own teardown.
test.afterEach(async () => {
  await abortActive();
});

if (!plan) {
  test("perf harness needs a plan", () => {
    test.skip(true, "run through `make ui-perf` (PERF_PLAN unset)");
  });
} else {
  for (const id of plan.scenarios) {
    const scenario = scenarioById(id);
    for (let rep = 1; rep <= plan.n; rep++) {
      for (const build of plan.builds) {
        test(`${scenario.id} · ${build.label} · rep ${rep}`, async ({ browser }) => {
          const result = await runOne({ plan, build, scenario, rep, browser });
          const dir = path.join(plan.runDir, "raw", scenario.id);
          mkdirSync(dir, { recursive: true });
          writeFileSync(path.join(dir, `${build.label}-${rep}.json`), JSON.stringify(result, null, 2));
          const legs = result.legs.map((l) => `${l.name} ${l.settledMs}ms`).join(", ");
          console.log(
            `${scenario.id} ${build.label}#${rep}: events ${(result.det.events_stream_bytes / 1e6).toFixed(1)} MB` +
              ` / ${result.det.events_stream_frames} frames, ${result.det.requests_short} requests, ` +
              `peak sockets ${result.det.peak_sockets_proxy}, blocked p95 ${result.timing.blocked_p95_ms}ms; ${legs}` +
              (result.error ? ` ERROR ${result.error}` : ""),
          );
          expect(result.error, "scenario failed").toBeUndefined();
        });
      }
    }
  }
}
