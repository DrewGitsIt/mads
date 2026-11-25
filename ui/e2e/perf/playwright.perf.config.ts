import { defineConfig } from "@playwright/test";

/**
 * The perf harness (tools/perf/docs/PLAN.md): its own config, so `make
 * ui-smoke` never sees it. Run through `make ui-perf`, which builds each ref,
 * seeds the workload and writes the plan this reads (PERF_PLAN). Serial and
 * single-worker: every test boots its own stack, and parallel runs would
 * measure each other.
 */
export default defineConfig({
  testDir: ".",
  testMatch: "*.perf.ts",
  globalTeardown: "./globalTeardown.ts",
  fullyParallel: false,
  workers: 1,
  retries: 0,
  // No maxFailures: a failed repetition is reported and the run goes on.
  // A dead worker is different — harness.perf.ts sees its leftover pid
  // registry and fails every later test at once, so nothing is measured
  // beside what it left running.
  reporter: [["list"]],
  // PERF_TEST_TIMEOUT_MS: scenario 8 at a large PERF_LONGLIVED_N needs longer.
  timeout: Number(process.env.PERF_TEST_TIMEOUT_MS ?? 10 * 60_000),
  use: { trace: "off", screenshot: "off", video: "off" },
  // PERF_PW_OUTPUT (run.sh, real data: realism or a real CHAT_SRC): failure
  // context goes to the run's temp tree, deleted with it.
  outputDir: process.env.PERF_PW_OUTPUT || "test-results",
  // Each repetition launches its own browser (measure.ts: full Chromium,
  // background networking off), so nothing here launches one.
  projects: [{ name: "perf" }],
});
