import { mkdirSync, writeFileSync } from "node:fs";
import path from "node:path";
import { test } from "@playwright/test";
import { abortActive, loadPlan, runOne, type Result } from "./measure";
import { scenarioById } from "./scenarios";

/**
 * One test per (scenario, delay mode, repetition, build). Within a scenario
 * and mode the builds alternate ABBA (A B, B A, A B, ...) so drift lands on
 * both alike. Each test boots its own stack on a fresh copy of the run's one
 * seeded database, in its own browser (or Electron app), and writes its raw
 * result to <runDir>/raw/. A scenario that fails records ok=false and the
 * run goes on; a crash, a timeout or a live-port hit fails the test, and the
 * config's maxFailures: 1 then stops the whole run. globalTeardown writes
 * the report, failures and missing repetitions included.
 */
const plan = loadPlan();

// A repetition that times out never reaches runOne's own teardown.
test.afterEach(async () => {
  await abortActive();
});

function save(r: Result): void {
  const dir = path.join(plan!.runDir, "raw", r.scenario, r.mode);
  mkdirSync(dir, { recursive: true });
  writeFileSync(path.join(dir, `${r.build}-${r.rep}.json`), JSON.stringify(r, null, 2));
}

if (!plan) {
  test("perf harness needs a plan", () => {
    test.skip(true, "run through `make ui-perf` (PERF_PLAN unset)");
  });
} else {
  for (const id of plan.scenarios) {
    const scenario = scenarioById(id);
    for (const mode of plan.modes) {
      for (let rep = 1; rep <= plan.n; rep++) {
        const order = rep % 2 === 1 ? plan.builds : [...plan.builds].reverse();
        for (const build of order) {
          test(`${scenario.id} · delays ${mode} · ${build.label} · rep ${rep}`, async () => {
            let result: Result;
            try {
              result = await runOne({ plan, build, scenario, rep, mode });
            } catch (err) {
              const error = `FATAL: ${err instanceof Error ? err.message : String(err)}`;
              save({ scenario: scenario.id, build: build.label, mode, rep, browser: plan.browser, ok: false, error } as Result);
              throw err;
            }
            save(result);
            const legs = result.legs.map((l) => `${l.name} ${l.settledMs}ms`).join(", ");
            console.log(
              `${scenario.id} ${mode} ${build.label}#${rep}: ` +
                (result.ok
                  ? `events ${(result.det.events_stream_bytes_proxy / 1e6).toFixed(1)} MB, all-6-busy ${result.timing.all_sockets_busy_ms}ms, ` +
                    `queued total ${result.timing.queued_total_ms}ms (max ${result.timing.queued_max_ms}); ${legs}`
                  : `FAILED ${result.error}`),
            );
          });
        }
      }
    }
  }
}
