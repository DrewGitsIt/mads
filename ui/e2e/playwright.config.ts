import { defineConfig, devices } from "@playwright/test";

export default defineConfig({
  testDir: ".",
  globalSetup: "./globalSetup.ts",
  // Serial, single-worker: globalSetup boots ONE shared topology (Router,
  // Data Service, workspace) for every spec, so specs mutate each other's
  // backend state — sessions, tasks and config are global. Running them in
  // parallel made the suite flaky in a way that looked like product bugs:
  // three separate red runs failed three different tests
  // (conversation-overflow, session-work's dependency edge, settings), none
  // reproducible in isolation. Per-worker topologies would allow parallelism
  // again, but that is a bigger change than this suite needs today.
  fullyParallel: false,
  workers: 1,
  retries: 0,
  reporter: [["list"]],
  timeout: 30_000,
  use: {
    trace: "retain-on-failure",
    screenshot: "only-on-failure",
  },
  projects: [
    {
      name: "chromium",
      use: { ...devices["Desktop Chrome"] },
    },
  ],
});
