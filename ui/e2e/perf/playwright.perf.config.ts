import { defineConfig } from "@playwright/test";

/**
 * The perf harness (docs/PERF-HARNESS-PLAN.md): its own config, so `make
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
  timeout: 10 * 60_000,
  use: { trace: "off", screenshot: "off", video: "off" },
  // Each repetition launches its own browser (measure.ts: full Chromium,
  // background networking off), so nothing here launches one.
  projects: [{ name: "perf" }],
});
