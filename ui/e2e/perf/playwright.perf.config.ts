import { defineConfig, devices } from "@playwright/test";

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
  reporter: [["list"]],
  timeout: 15 * 60_000,
  use: { trace: "off", screenshot: "off", video: "off" },
  // channel "chromium": the full Chromium build (new headless), not the
  // stripped headless shell — the same browser the app's Electron embeds.
  projects: [{ name: "chromium", use: { ...devices["Desktop Chrome"], channel: "chromium" } }],
});
