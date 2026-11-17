import { spawn, type ChildProcess } from "node:child_process";
import { createServer } from "node:http";
import path from "node:path";
import { test, expect } from "./fixtures";

// Adversarial round 2 on the local front door (docs/LOCAL-FRONT-DOOR-DESIGN.md
// H3, H7): the Vite dev proxy authenticates nothing itself.

function freePort(): Promise<number> {
  return new Promise((resolve) => {
    const s = createServer();
    s.listen(0, "127.0.0.1", () => {
      const p = (s.address() as { port: number }).port;
      s.close(() => resolve(p));
    });
  });
}

test.describe("the Vite dev proxy (H3)", () => {
  test.describe.configure({ mode: "serial" });
  test.use({ withToken: false });
  let vite: ChildProcess | undefined;
  let viteURL = "";
  const seenAuth: string[] = [];

  test.beforeAll(async () => {
    test.setTimeout(60_000);
    const port = await freePort();
    viteURL = `http://127.0.0.1:${port}`;
    const uiDir = path.resolve(__dirname, "..");
    vite = spawn(path.join(uiDir, "node_modules", ".bin", "vite"), ["--port", String(port), "--strictPort", "--host", "127.0.0.1"], {
      cwd: uiDir,
      env: { ...process.env, MADS_ROUTER_URL: process.env.MADS_ROUTER_URL, VITE_ROUTER_URL: "" },
      stdio: ["ignore", "pipe", "pipe"],
    });
    const deadline = Date.now() + 40_000;
    while (Date.now() < deadline) {
      try {
        if ((await fetch(`${viteURL}/`)).ok) return;
      } catch {
        // not up yet
      }
      await new Promise((r) => setTimeout(r, 200));
    }
    throw new Error("vite dev server did not start");
  });
  test.afterAll(async () => {
    vite?.kill("SIGTERM");
    void seenAuth;
  });

  test("a process with no token gets the Router's 401 through the proxy, on JSON, SSE and stream paths", async ({ routerURL }) => {
    void routerURL;
    for (const p of ["/sessions", "/config", "/credentials", "/events?types=session.created", "/sessions/ses_x/output"]) {
      const ctl = new AbortController();
      const t = setTimeout(() => ctl.abort(), 5000);
      const res = await fetch(`${viteURL}${p}`, { signal: ctl.signal });
      clearTimeout(t);
      expect(res.status, p).toBe(401);
      await res.body?.cancel();
    }
  });

  test("the proxy forwards the request's own bearer and adds none", async ({ routerURL }) => {
    const tok = (await import("./lib/auth")).tokenFor(routerURL);
    const ok = await fetch(`${viteURL}/config`, { headers: { Authorization: `Bearer ${tok}` } });
    expect(ok.status).toBe(200);
    const bare = await fetch(`${viteURL}/config`);
    expect(bare.status).toBe(401);
  });
});
