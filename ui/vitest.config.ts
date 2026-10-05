import { defineConfig } from "vitest/config";
import react from "@vitejs/plugin-react";

export default defineConfig({
  plugins: [react()],
  test: {
    environment: "happy-dom",
    // electron/**/*.test.ts covers the pure, dependency-free helpers in
    // ui/electron/pty/ (shellEnv, resolveCwd) — they don't touch node-pty or
    // any Electron API, so (unlike *.realtest.ts, see vitest.electron.config.ts)
    // there's no reason to keep them out of the default `make check` gate.
    include: ["src/**/*.test.{ts,tsx}", "electron/**/*.test.ts"],
    exclude: ["e2e/**", "node_modules/**"],
    setupFiles: ["./src/setupTests.ts"],
  },
});
