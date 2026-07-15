import { defineConfig } from "vitest/config";

/**
 * Separate config for the real-pty contract test. Deliberately not part of
 * `make check` — see electron/pty/ptyHost.realtest.ts and
 * ui/src/conversation/HANDOFF.md §5. Run manually via `npm run test:pty`.
 */
export default defineConfig({
  test: {
    environment: "node",
    include: ["electron/**/*.realtest.ts"],
  },
});
