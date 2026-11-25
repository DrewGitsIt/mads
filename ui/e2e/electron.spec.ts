import { execFileSync, spawn, type ChildProcess } from "node:child_process";
import { existsSync, mkdtempSync, rmSync } from "node:fs";
import { createServer, type Server } from "node:http";
import { tmpdir } from "node:os";
import path from "node:path";
import { _electron as electron, type ElectronApplication, type Page } from "@playwright/test";
import { test, expect } from "./fixtures";
import { apiFetch } from "./lib/auth";

// The real Electron app (docs/LOCAL-FRONT-DOOR-DESIGN.md H7): the main process
// adds `Authorization: Bearer <install token>` to the window's requests to
// the Router's origin with session.webRequest, below the page. These run the
// built app (dist-electron + dist) against the shared topology.

const uiDir = path.resolve(__dirname, "..");
// eslint-disable-next-line @typescript-eslint/no-require-imports
const electronBin = require("electron") as unknown as string;
const haveElectron = existsSync(electronBin) && existsSync(path.join(uiDir, "dist-electron", "main.js"));

async function launch(env: Record<string, string>): Promise<{ app: ElectronApplication; win: Page; userData: string; launchedAt: number }> {
  const userData = mkdtempSync(path.join(tmpdir(), "mads-e2e-electron-"));
  const launchedAt = Date.now();
  const app = await electron.launch({
    executablePath: electronBin,
    args: [uiDir],
    env: { ...(process.env as Record<string, string>), MADS_USER_DATA_DIR: userData, ELECTRON_DISABLE_SANDBOX: "1", ...env },
  });
  const win = await app.firstWindow();
  return { app, win, userData, launchedAt };
}

/** Every live descendant of pid (Electron's GPU, network and renderer helpers). */
function descendants(pid: number): number[] {
  let rows: string;
  try {
    rows = execFileSync("ps", ["-A", "-o", "pid=,ppid="], { encoding: "utf8" });
  } catch {
    return [];
  }
  const children = new Map<number, number[]>();
  for (const line of rows.split("\n")) {
    const [c, p] = line.trim().split(/\s+/).map(Number);
    if (!c || !p) continue;
    children.set(p, [...(children.get(p) ?? []), c]);
  }
  const out: number[] = [];
  const stack = [pid];
  while (stack.length) {
    for (const c of children.get(stack.pop()!) ?? []) {
      out.push(c);
      stack.push(c);
    }
  }
  return out;
}

function alive(pid: number): boolean {
  try {
    process.kill(pid, 0);
    return true;
  } catch {
    return false;
  }
}

function kill(pid: number, sig: NodeJS.Signals): void {
  try {
    process.kill(pid, sig);
  } catch {
    // already gone
  }
}

// How long app.close() gets before the process tree is killed. Electron's
// own shutdown took 1.5-56 s under load, all of it on the test's budget.
const CLOSE_GRACE_MS = 10_000;

/**
 * Close the app, bounded: app.close() waits for Electron to exit, which under
 * load can outlast the test. After CLOSE_GRACE_MS the tree this test launched
 * (Playwright spawns Electron as its own process-group leader) is SIGKILLed,
 * and any helper left over even after a clean close is killed too, so none
 * outlives the test.
 */
async function close(l: { app: ElectronApplication; userData: string }): Promise<void> {
  const pid = l.app.process().pid;
  // Collected while main is alive: once it exits its helpers are reparented
  // and can no longer be found by walking from pid.
  const tree = pid ? [pid, ...descendants(pid)] : [];
  let timer: NodeJS.Timeout | undefined;
  const closed = await Promise.race([
    l.app.close().then(
      () => true,
      () => false,
    ),
    new Promise<boolean>((r) => (timer = setTimeout(() => r(false), CLOSE_GRACE_MS))),
  ]);
  clearTimeout(timer);
  if (pid) {
    const all = [...new Set([...tree, ...descendants(pid)])];
    if (!closed) {
      test.info().annotations.push({ type: "electron close", description: `app.close() exceeded ${CLOSE_GRACE_MS} ms; killed the tree` });
      kill(-pid, "SIGKILL");
    }
    for (const p of all) if (alive(p)) kill(p, "SIGKILL");
    const deadline = Date.now() + 5_000;
    while (all.some(alive) && Date.now() < deadline) await new Promise((r) => setTimeout(r, 50));
    const left = all.filter(alive);
    expect(left, "Electron processes left after close").toEqual([]);
  }
  rmSync(l.userData, { recursive: true, force: true });
}

/** A server on another origin that echoes the request headers it got, with CORS so page JS can read them. */
async function echoServer(): Promise<{ server: Server; url: string }> {
  const server = createServer((req, res) => {
    res.setHeader("Access-Control-Allow-Origin", "*");
    res.setHeader("Content-Type", "application/json");
    res.end(JSON.stringify(req.headers));
  });
  await new Promise<void>((r) => server.listen(0, "127.0.0.1", r));
  return { server, url: `http://127.0.0.1:${(server.address() as { port: number }).port}` };
}

/** The rail once GET /sessions has answered: the list, or its empty state. */
function sessionList(p: Page) {
  return p.locator('[aria-label="Session list"]').or(p.getByText("No sessions yet"));
}

const BENCH = process.env.MADS_E2E_BENCH === "1";
const BENCH_ROUNDS = BENCH ? 300 : 100;

function freePort(): Promise<number> {
  return new Promise((resolve) => {
    const s = createServer();
    s.listen(0, "127.0.0.1", () => {
      const p = (s.address() as { port: number }).port;
      s.close(() => resolve(p));
    });
  });
}

// Not serial: each test launches (and closes) its own Electron, so they share
// nothing but the topology, and one slow or failed test must not skip the rest.
test.describe("Electron injects the bearer below the page", () => {
  test.skip(!haveElectron, "no Electron binary or dist-electron build (npm run build; node node_modules/electron/install.js)");

  test("the window loads, streams events, and page code can't observe the token", async ({ routerURL }) => {
    test.setTimeout(60_000);
    const l = await launch({ VITE_ROUTER_URL: routerURL, MADS_HOME: process.env.MADS_HOME! });
    const echo = await echoServer();
    try {
      const { win } = l;
      await expect(sessionList(win)).toBeVisible({ timeout: 20_000 });
      const loadMs = Date.now() - l.launchedAt;
      test.info().annotations.push({ type: "electron launch → session list", description: `${loadMs} ms` });
      console.log("electron launch → session list", loadMs);
      // Page load alone (the process is up): reload to the session list.
      const r0 = Date.now();
      await win.reload();
      await expect(sessionList(win)).toBeVisible();
      console.log("electron reload → session list", Date.now() - r0);
      test.info().annotations.push({ type: "electron reload → session list", description: `${Date.now() - r0} ms` });

      // EventSource carries the header (only main can add one to it).
      const name = `electron-sse-${Date.now()}`;
      const got = win.evaluate(
        (n) =>
          new Promise<string>((done, fail) => {
            const es = new EventSource(`/events?types=session.created`);
            const timer = setTimeout(() => {
              es.close();
              fail(new Error("no event"));
            }, 10_000);
            es.addEventListener("session.created", (ev) => {
              if ((ev as MessageEvent).data.includes(n)) {
                clearTimeout(timer);
                es.close();
                done((ev as MessageEvent).data);
              }
            });
          }),
        name,
      );
      await win.waitForTimeout(300);
      const created = await apiFetch(`${routerURL}/sessions`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ name, provider: "fake", workspace: process.env.MADS_WORKSPACE_ID }),
      });
      expect(created.status).toBeLessThan(300);
      const {
        session: { id },
      } = (await created.json()) as { session: { id: string } };
      expect(await got).toContain(name);

      // Page code sets no header and still reaches the API...
      expect(await win.evaluate(async () => (await fetch("/config")).status)).toBe(200);
      // ...but can't see what was added: no cookie, nothing on window.mads
      // but the token *path*, and the header goes to no other origin (an
      // echo server reflects exactly what it received).
      expect(await win.evaluate(() => document.cookie)).toBe("");
      const auth = await win.evaluate(async () => {
        const a = (window as unknown as { mads?: { auth?: Record<string, () => Promise<unknown>> } }).mads?.auth ?? {};
        return { keys: Object.keys(a), info: a.tokenPath ? await a.tokenPath() : undefined };
      });
      expect(auth.keys).toEqual(["tokenPath"]);
      expect(JSON.stringify(auth.info)).not.toMatch(/[A-Za-z0-9_-]{43}/);
      const reflected = await win.evaluate(async (u) => (await fetch(`${u}/x`)).json(), echo.url);
      expect((reflected as Record<string, string>).authorization).toBeUndefined();
      // The Router itself, spelled as another origin (localhost vs
      // 127.0.0.1), gets no token either: the hook is origin-scoped.
      const other = routerURL.replace("127.0.0.1", "localhost");
      // Without the token the door answers 401 before CORS headers are
      // added, so the page sees a failed fetch; with it, it would be a 200.
      const viaOther = await win.evaluate(
        async (u) => fetch(`${u}/sessions`).then((r) => String(r.status), () => "refused"),
        other,
      );
      expect(viaOther).not.toBe("200");

      await apiFetch(`${routerURL}/sessions/${id}`, { method: "DELETE" });
    } finally {
      await new Promise<void>((r) => echo.server.close(() => r()));
      await close(l);
    }
  });

  // Latency (H7 asked for numbers): the hook routes each matching request
  // through main. Same Router, same endpoint, from the same window: through
  // the hook (the Router's own origin) and not (the localhost spelling of it,
  // outside the filter). GET /health needs no token, so both succeed.
  // Nothing asserts on these numbers, so by default each round is short
  // (1,240 sequential fetches overran 90 s under load); MADS_E2E_BENCH=1
  // runs the full 300-request rounds for real measurements.
  test("measures the webRequest hook's cost per request, and the first output frame", async ({ routerURL }) => {
    test.setTimeout(BENCH ? 180_000 : 60_000);
    const l = await launch({ VITE_ROUTER_URL: routerURL, MADS_HOME: process.env.MADS_HOME! });
    try {
      const { win } = l;
      await expect(sessionList(win)).toBeVisible({ timeout: 20_000 });
      const other = routerURL.replace("127.0.0.1", "localhost");
      const timing = await win.evaluate(
        async ({ hooked, unhooked, rounds }) => {
          const run = async (base: string, n: number) => {
            const t: number[] = [];
            for (let i = 0; i < n; i++) {
              const s = performance.now();
              await (await fetch(`${base}/health`, { cache: "no-store" })).text();
              t.push(performance.now() - s);
            }
            t.sort((a, b) => a - b);
            return { median: t[Math.floor(n / 2)], p95: t[Math.floor(n * 0.95)] };
          };
          await run(hooked, 20);
          await run(unhooked, 20);
          const a = await run(hooked, rounds);
          const b = await run(unhooked, rounds);
          const c = await run(hooked, rounds);
          const d = await run(unhooked, rounds);
          return { hooked: [a, c], unhooked: [b, d] };
        },
        { hooked: "", unhooked: other, rounds: BENCH_ROUNDS },
      );
      test.info().annotations.push({ type: "per-request (median/p95 ms), hooked", description: JSON.stringify(timing.hooked) });
      test.info().annotations.push({ type: "per-request (median/p95 ms), unhooked", description: JSON.stringify(timing.unhooked) });
      console.log("hook timing", JSON.stringify(timing));

      // First output frame: a new session's output stream, opened by the page.
      const created = await apiFetch(`${routerURL}/sessions`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ name: `electron-latency-${Date.now()}`, provider: "fake", workspace: process.env.MADS_WORKSPACE_ID }),
      });
      const {
        session: { id },
      } = (await created.json()) as { session: { id: string } };
      const frameMs = await win.evaluate(async (sid) => {
        const s = performance.now();
        const res = await fetch(`/sessions/${sid}/output?v=2`);
        const reader = res.body!.getReader();
        await reader.read();
        const ms = performance.now() - s;
        await reader.cancel();
        return ms;
      }, id);
      test.info().annotations.push({ type: "electron first output frame", description: `${frameMs.toFixed(1)} ms` });
      console.log("electron first output frame", frameMs.toFixed(1));
      await apiFetch(`${routerURL}/sessions/${id}`, { method: "DELETE" });
    } finally {
      await close(l);
    }
  });

  // The same two timings in Playwright's Chromium with extraHTTPHeaders (no
  // hook), for comparison.
  test("Chromium with extraHTTPHeaders: page load to session list, first output frame", async ({ page, routerURL }) => {
    const t0 = Date.now();
    await page.goto(routerURL);
    await expect(sessionList(page)).toBeVisible();
    const loadMs = Date.now() - t0;
    const created = await apiFetch(`${routerURL}/sessions`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ name: `chromium-latency-${Date.now()}`, provider: "fake", workspace: process.env.MADS_WORKSPACE_ID }),
    });
    const {
      session: { id },
    } = (await created.json()) as { session: { id: string } };
    const frameMs = await page.evaluate(async (sid) => {
      const s = performance.now();
      const res = await fetch(`/sessions/${sid}/output?v=2`);
      const reader = res.body!.getReader();
      await reader.read();
      const ms = performance.now() - s;
      await reader.cancel();
      return ms;
    }, id);
    console.log("chromium goto → session list", loadMs, "first output frame", frameMs.toFixed(1));
    test.info().annotations.push({ type: "chromium goto → session list / first frame", description: `${loadMs} ms / ${frameMs.toFixed(1)} ms` });
    await apiFetch(`${routerURL}/sessions/${id}`, { method: "DELETE" });
  });

  // Dev: the Vite page inside Electron (VITE_DEV_SERVER_URL), calling the
  // Router directly (VITE_ROUTER_URL) with the bearer main adds for the
  // Router's origin; the proxy adds nothing. Its own Router allows the dev
  // server's origin, as `-allowed-origins` does for `npm run dev`.
  test.describe("the Vite dev page inside Electron", () => {
    let second: import("./lib/topology").Topology | undefined;
    let fastModel: { url: string; close(): Promise<void> } | undefined;
    let vite: ChildProcess | undefined;
    let viteURL = "";

    test.beforeAll(async () => {
      test.setTimeout(180_000);
      const port = await freePort();
      viteURL = `http://127.0.0.1:${port}`;
      const { bootTopology } = await import("./lib/topology");
      const { startFastModelFixture } = await import("./lib/fastModelFixture");
      fastModel = await startFastModelFixture();
      second = await bootTopology({ fastModelURL: fastModel.url, routerArgs: ["-allowed-origins", viteURL] });
      await fetch(`${second.routerURL}/config`, {
        method: "PUT",
        headers: { Authorization: `Bearer ${second.token}`, "Content-Type": "application/json" },
        body: JSON.stringify({ ...(await (await fetch(`${second.routerURL}/config`, { headers: { Authorization: `Bearer ${second.token}` } })).json()), onboarding_complete: true }),
      });
      vite = spawn(path.join(uiDir, "node_modules", ".bin", "vite"), ["--port", String(port), "--strictPort", "--host", "127.0.0.1"], {
        cwd: uiDir,
        env: { ...process.env, VITE_ROUTER_URL: second.routerURL },
        stdio: "ignore",
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
      test.setTimeout(60_000);
      vite?.kill("SIGTERM");
      await second?.teardown();
      await fastModel?.close();
    });

    test("loads and reaches the Router with the bearer main adds", async () => {
      test.setTimeout(60_000);
      const l = await launch({ VITE_DEV_SERVER_URL: viteURL, VITE_ROUTER_URL: second!.routerURL, MADS_HOME: second!.homeDir });
      try {
        await expect(sessionList(l.win)).toBeVisible({ timeout: 30_000 });
        expect(await l.win.evaluate(async (u) => (await fetch(`${u}/config`)).status, second!.routerURL)).toBe(200);
        // The dev server itself got no token (it's outside the filter).
        expect(await l.win.evaluate(async (u) => (await fetch(`${u}/config`)).status, viteURL)).toBe(401);
      } finally {
        await close(l);
      }
    });
  });
});
