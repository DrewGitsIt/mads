import { defineConfig } from "vitest/config";

/** The perf harness's own unit tests: npx --prefix ui vitest run --config e2e/perf/vitest.perf.config.ts */
export default defineConfig({
  test: { include: ["e2e/perf/*.test.ts"], environment: "node" },
});
