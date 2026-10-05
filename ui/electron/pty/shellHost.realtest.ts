import { describe, it, expect } from "vitest";
import type { NodePtyModule } from "./ptyHost";
import { ShellHostCore, type ShellEvent } from "./shellHostCore";

/**
 * Real-pty test of the shell host core — *.realtest.ts, so, like
 * ptyHost.realtest.ts, it runs only via `npm run test:pty` and never in
 * `make check`. Forking the actual utilityProcess needs a running Electron,
 * so this drives the same ShellHostCore the utilityProcess entry
 * (shellHost.ts) wraps, with real node-pty; the MessagePort wiring is
 * verified by hand in the app. Self-skips where no pty is available.
 */
describe("shell host core with real node-pty", () => {
  it("keeps a shell alive across detach and re-attaches to the same process", async (ctx) => {
    let nodePty: NodePtyModule;
    try {
      nodePty = (await import("node-pty")) as unknown as NodePtyModule;
    } catch {
      ctx.skip();
      return;
    }

    const core = new ShellHostCore({
      loadPty: async () => nodePty,
      shell: () => (process.platform === "win32" ? "powershell.exe" : "/bin/sh"),
    });
    const events: ShellEvent[] = [];
    core.connect(1, (e) => events.push(e));
    const seen = () =>
      events.flatMap((e) => (e.event === "data" ? [e.data] : [])).join("");
    const waitFor = async (needle: string) => {
      const deadline = Date.now() + 5000;
      while (!seen().includes(needle)) {
        if (Date.now() > deadline) throw new Error(`timed out waiting for ${needle}`);
        await new Promise((r) => setTimeout(r, 25));
      }
    };

    let shellId: string | undefined;
    try {
      const opened = (await core.handle(1, {
        type: "open",
        sessionId: "ses_real",
        cwd: process.cwd(),
        cols: 80,
        rows: 24,
      })) as { shellId: string; created: boolean };
      shellId = opened.shellId;
      expect(opened.created).toBe(true);
      const pid = core.list()[0].pid;
      expect(pid).toBeGreaterThan(0);

      await core.handle(1, { type: "write", shellId, data: "echo hi-$((1+1))\n" });
      await waitFor("hi-2");

      await core.handle(1, { type: "detach", shellId });
      expect(core.list()[0].attached).toBe(false);
      // The process is still alive and running commands while detached…
      process.kill(pid!, 0);

      const again = (await core.handle(1, { type: "open", sessionId: "ses_real" })) as {
        shellId: string;
        created: boolean;
      };
      expect(again).toMatchObject({ shellId, created: false });
      expect(core.list()[0].pid).toBe(pid);

      await core.handle(1, { type: "write", shellId, data: "echo more-$((2+2))\n" });
      await waitFor("more-4");

      // Output produced while detached comes back in the re-open's snapshot.
      await core.handle(1, { type: "write", shellId, data: "printf 'line%s\\n' 1 2 3\n" });
      await waitFor("line3");
      await core.handle(1, { type: "write", shellId, data: "sleep 0.3; echo after-detach\n" });
      await core.handle(1, { type: "detach", shellId });
      await new Promise((r) => setTimeout(r, 800));
      const restored = (await core.handle(1, {
        type: "open",
        sessionId: "ses_real",
        cols: 80,
        rows: 24,
      })) as { snapshot?: string };
      for (const needle of ["line1", "line2", "line3", "after-detach"]) {
        expect(restored.snapshot).toContain(needle);
      }
    } catch (error) {
      // No usable pty on this box (sandboxed container without a controlling
      // terminal) — skip rather than fail the manual run.
      if (error instanceof Error && /posix_spawn|forkpty|openpty|EACCES|ENOENT/.test(error.message)) {
        ctx.skip();
        return;
      }
      throw error;
    } finally {
      if (shellId) await core.handle(1, { type: "end", shellId });
    }
    expect(core.list()).toEqual([]);
  });
});
