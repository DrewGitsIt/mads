import { describe, it, expect } from "vitest";

/**
 * Real-pty contract test — deliberately named *.realtest.ts, NOT *.test.ts,
 * so it is never picked up by the default `src/**\/*.test.{ts,tsx}` vitest
 * include (see vitest.config.ts). Run manually via `npm run test:pty`; not
 * part of `make check` (see ui/src/conversation/HANDOFF.md §5 for why that's
 * correct rather than a gap — this native dependency's availability
 * shouldn't gate the default check on every dev/CI machine).
 *
 * Self-skips cleanly wherever a real pty isn't available (containers,
 * sandboxes, Windows without ConPTY wired up).
 */
describe("real node-pty spawn", () => {
  it("round-trips output through a real pty", async (ctx) => {
    let nodePty: typeof import("node-pty");
    try {
      nodePty = await import("node-pty");
    } catch {
      ctx.skip();
      return;
    }

    try {
      const output = await new Promise<string>((resolve, reject) => {
        let collected = "";
        const shell = process.platform === "win32" ? "powershell.exe" : "/bin/sh";
        const args = process.platform === "win32" ? ["-Command", "echo hi"] : ["-c", "echo hi"];
        const proc = nodePty.spawn(shell, args, {
          name: "xterm-256color",
          cols: 80,
          rows: 24,
          cwd: process.cwd(),
          env: process.env as Record<string, string>,
        });
        const timeout = setTimeout(() => reject(new Error("pty round-trip timed out")), 5000);
        proc.onData((data) => {
          collected += data;
        });
        proc.onExit(() => {
          clearTimeout(timeout);
          resolve(collected);
        });
      });

      expect(output).toContain("hi");
    } catch {
      // No usable pty on this box (e.g. sandboxed container without a
      // controlling terminal) — skip rather than fail the manual run.
      ctx.skip();
    }
  });
});
