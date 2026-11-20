import { mkdirSync, mkdtempSync, readFileSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";
import { _electron as electron, type Browser, type BrowserContext, type Page } from "@playwright/test";
import { LONG_TASK_INIT, NetCollector, ProcSampler, SocketSampler, isLongLived, longTasks, routeOf, type NetRequest } from "./collectors";
import { rulesFromEnv } from "./delayProxy";
import { SCENARIOS, scenarioById, useManifest, type Manifest, type Scenario, type ScenarioCtx } from "./scenarios";
import { bootPerfStack, waitDelegatesConcluded, type Build, type PerfStack } from "./stack";

/** The run the runner (run.sh) planned: builds, workload, scenarios, reps. */
export interface Plan {
  runDir: string;
  seededDB: string;
  manifestPath: string;
  builds: Build[];
  scenarios: string[];
  n: number;
  browser: "chromium" | "electron";
  /** PERF_DELAYS as given ("" = defaults, "off"). */
  delays: string;
  /** Track SSE messages over CDP too (heavy: ships every message's data to the harness). */
  cdpSse: boolean;
  meta: Record<string, unknown>;
}

export function loadPlan(): Plan | undefined {
  const p = process.env.PERF_PLAN;
  if (!p) return undefined;
  const plan = JSON.parse(readFileSync(p, "utf8")) as Plan;
  plan.scenarios = plan.scenarios.length ? plan.scenarios.map((s) => scenarioById(s).id) : SCENARIOS.map((s) => s.id);
  return plan;
}

export interface LegResult {
  name: string;
  /** Click → its DOM marker visible. */
  markerMs: number;
  /** Click → marker visible and 500 ms with no new short request. */
  settledMs: number;
}

export interface Result {
  scenario: string;
  build: string;
  rep: number;
  browser: string;
  ok: boolean;
  error?: string;
  legs: LegResult[];
  /** Deterministic: same build and workload, same number. */
  det: Record<string, number>;
  /** Timings. */
  timing: Record<string, number>;
  /** /events stream (re)connects in the window and the cursor each resumed from. */
  eventsConnects: { since: number | null; types: number; bytes: number; frames: number }[];
  requestsByRoute: Record<string, number>;
  blockedByRoute: Record<string, { n: number; p50: number; max: number }>;
  windowCapped: boolean;
  socketOwners: string[];
  /** Every request in the window: route, CDP type, start (ms after t0), queued+stalled, duration, status. */
  requests: [string, string, number, number | null, number | null, number | string | null][];
}

const STREAM_QUIET_MS = 1500;
const SHORT_QUIET_MS = 500;
const WINDOW_CAP_MS = Number(process.env.PERF_WINDOW_CAP_MS ?? 120_000);
const PREPARE_CAP_MS = Number(process.env.PERF_PREPARE_CAP_MS ?? 300_000);

const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));

/**
 * Waits until no long-lived stream has received a byte for streamQuietMs,
 * no short request has started for shortQuietMs, and none is in flight —
 * or until capMs. Returns whether it hit the cap.
 */
async function quiet(net: NetCollector, since: number, capMs: number, streamQuietMs = STREAM_QUIET_MS): Promise<boolean> {
  const deadline = Date.now() + capMs;
  for (;;) {
    const now = Date.now();
    const streamIdle = now - Math.max(net.lastStreamByte, since) >= streamQuietMs;
    const shortIdle = now - Math.max(net.lastShortStart, since) >= 1000 && net.shortInFlight().length === 0;
    if (streamIdle && shortIdle) return false;
    if (now > deadline) return true;
    await sleep(50);
  }
}

/** Marker visible, then 500 ms with no new short request: the settle instant. */
async function settle(net: NetCollector, markerAt: number): Promise<number> {
  const deadline = Date.now() + 60_000;
  for (;;) {
    const candidate = Math.max(markerAt, net.lastShortStart);
    if (Date.now() - candidate >= SHORT_QUIET_MS || Date.now() > deadline) return candidate;
    await sleep(25);
  }
}

function pct(xs: number[], q: number): number {
  if (xs.length === 0) return 0;
  const s = [...xs].sort((a, b) => a - b);
  return s[Math.min(s.length - 1, Math.max(0, Math.ceil(q * s.length) - 1))];
}

const isEventsStream = (r: NetRequest) => r.type === "EventSource" && r.path === "/events";

/** Guards the live app: nothing in a perf page may reach the live ports. */
async function guardLivePorts(context: BrowserContext, hits: string[]): Promise<void> {
  await context.route(/^https?:\/\/(localhost|127\.0\.0\.1|\[::1\]):(17373|17374|17380)\//, (route) => {
    hits.push(route.request().url());
    return route.abort();
  });
}

/**
 * Teardown of the repetition in progress, for the spec's afterEach: a test
 * that times out never reaches runOne's finally, and its stack must not
 * outlive it.
 */
let active: (() => Promise<void>) | undefined;
export async function abortActive(): Promise<void> {
  const f = active;
  active = undefined;
  await f?.();
}

/** Closes an Electron app, killing it if it won't quit (a quit guard dialog, a hang). */
async function closeElectron(app: Awaited<ReturnType<typeof electron.launch>>): Promise<void> {
  const proc = app.process();
  const closed = app.close().catch(() => {});
  const timedOut = await Promise.race([closed.then(() => false), sleep(10_000).then(() => true)]);
  if (timedOut || proc.exitCode === null) proc.kill("SIGKILL");
}

export async function runOne(opts: {
  plan: Plan;
  build: Build;
  scenario: Scenario;
  rep: number;
  browser?: Browser;
}): Promise<Result> {
  const { plan, build, scenario, rep } = opts;
  const manifest = JSON.parse(readFileSync(plan.manifestPath, "utf8")) as Manifest;
  useManifest(manifest);
  const result: Result = {
    scenario: scenario.id,
    build: build.label,
    rep,
    browser: plan.browser,
    ok: false,
    legs: [],
    det: {},
    timing: {},
    eventsConnects: [],
    requestsByRoute: {},
    blockedByRoute: {},
    windowCapped: false,
    socketOwners: [],
    requests: [],
  };
  const liveHits: string[] = [];
  let stack: PerfStack | undefined;
  let context: BrowserContext | undefined;
  let electronApp: Awaited<ReturnType<typeof electron.launch>> | undefined;
  let electronUserData: string | undefined;
  let net: NetCollector | undefined;
  let tornDown = false;
  const teardown = async () => {
    if (tornDown) return;
    tornDown = true;
    await net?.detach();
    if (electronApp) await closeElectron(electronApp);
    else await context?.close().catch(() => {});
    if (electronUserData) rmSync(electronUserData, { recursive: true, force: true });
    await stack?.teardown();
  };
  active = teardown;
  try {
    stack = await bootPerfStack({ build, seededDB: plan.seededDB, rules: rulesFromEnv(plan.delays) });
    await waitDelegatesConcluded(stack, manifest.parent.id);

    let page: Page;
    if (plan.browser === "electron") {
      if (!build.uiDir) throw new Error("electron mode needs the build's ui dir (dist-electron)");
      // eslint-disable-next-line @typescript-eslint/no-require-imports
      const electronBin = require("electron") as unknown as string;
      electronUserData = mkdtempSync(path.join(tmpdir(), "mads-perf-electron-"));
      electronApp = await electron.launch({
        executablePath: electronBin,
        args: [build.uiDir],
        env: {
          ...(process.env as Record<string, string>),
          VITE_ROUTER_URL: stack.appURL,
          MADS_HOME: stack.homeDir,
          MADS_USER_DATA_DIR: electronUserData,
          ELECTRON_DISABLE_SANDBOX: "1",
        },
      });
      context = electronApp.context();
      await context.addInitScript(LONG_TASK_INIT);
      await guardLivePorts(context, liveHits);
      page = await electronApp.firstWindow();
    } else {
      context = await opts.browser!.newContext({
        extraHTTPHeaders: { Authorization: `Bearer ${stack.token}` },
        viewport: { width: 1440, height: 900 },
      });
      await context.addInitScript(LONG_TASK_INIT);
      await guardLivePorts(context, liveHits);
      page = await context.newPage();
    }
    net = new NetCollector(stack.appURL, plan.cdpSse);
    await net.attach(page);

    const ctx: ScenarioCtx = {
      page,
      stack,
      manifest,
      browser: plan.browser,
      quiesce: async () => {
        if (await quiet(net!, Date.now(), PREPARE_CAP_MS)) {
          const open = net!.shortInFlight().map((r) => `${r.method} ${routeOf(r.path)} (${r.type})`);
          throw new Error(`page never went quiet while preparing; still in flight: ${open.join(", ") || "none (a stream kept receiving)"}`);
        }
      },
    };
    await scenario.prepare(ctx);
    if (scenario.legs.length && scenario.id !== "1-cold-load") await ctx.quiesce();

    const procs = new ProcSampler(stack.pids);
    const socks = new SocketSampler(stack.proxy.port);
    await procs.start();
    socks.start();
    const t0 = Date.now();
    net.startWindow(t0);
    stack.proxy.startWindow();

    for (const leg of scenario.legs) {
      const clickAt = Date.now();
      await leg.act(ctx);
      await leg.marker(ctx).first().waitFor({ state: "visible", timeout: 120_000 });
      const markerAt = Date.now();
      const settledAt = await settle(net, markerAt);
      result.legs.push({ name: leg.name, markerMs: markerAt - clickAt, settledMs: Math.round(settledAt - clickAt) });
    }
    result.windowCapped = await quiet(net, t0, WINDOW_CAP_MS);
    const t1 = Date.now();
    await socks.stop();
    const proc = await procs.stop();

    // --- Deterministic counts.
    const reqs = net.window(t0, t1);
    const started = reqs.filter((r) => r.start >= t0);
    const shorts = started.filter((r) => !isLongLived(r));
    const streams = reqs.filter(isEventsStream);
    const proxyLog = stack.proxy.log();
    const proxyEvents = proxyLog.filter((r) => r.sse && r.path === "/events");
    // Decoded body bytes: nothing here is compressed, so they are the wire's
    // body bytes. CDP's encodedDataLength lags on a streaming response (it
    // read 194 kB of a 1.1 MB replay in testing) and isn't used.
    const wireBytes = (r: NetRequest) => r.windowDecoded;
    for (const r of proxyEvents) {
      if (r.start < t0 && r.bytes === r.baseBytes) continue;
      const q = new URLSearchParams(r.query);
      result.eventsConnects.push({
        since: r.start >= t0 ? (q.has("since") ? Number(q.get("since")) : 0) : null,
        types: (q.get("types") ?? "").split(",").filter(Boolean).length,
        bytes: r.bytes - r.baseBytes,
        frames: r.frames - r.baseFrames,
      });
    }
    const batch = shorts.filter((r) => r.path === "/events");
    const batchProxy = proxyLog.filter((r) => !r.sse && r.path === "/events" && r.start >= t0);
    for (const r of shorts) {
      const k = `${r.method} ${routeOf(r.path)}`;
      result.requestsByRoute[k] = (result.requestsByRoute[k] ?? 0) + 1;
    }
    const peaks = stack.proxy.peaksBetween(t0, t1);
    result.det = {
      events_stream_bytes: streams.reduce((a, r) => a + wireBytes(r), 0),
      events_stream_bytes_proxy: proxyEvents.reduce((a, r) => a + (r.bytes - r.baseBytes), 0),
      events_stream_frames: proxyEvents.reduce((a, r) => a + (r.frames - r.baseFrames), 0),
      events_stream_connects: proxyEvents.filter((r) => r.start >= t0).length,
      events_from_zero: proxyEvents.filter((r) => r.start >= t0 && !new URLSearchParams(r.query).has("since")).length,
      events_batch_requests: batch.length,
      events_batch_bytes: batchProxy.reduce((a, r) => a + r.bytes, 0),
      requests_short: shorts.length,
      requests_delayed: proxyLog.filter((r) => r.start >= t0 && r.delayMs > 0).length,
      peak_connections_cdp: net.peakConnections(t0, t1),
      peak_sockets_proxy: peaks.sockets,
      peak_inflight_proxy: peaks.inFlight,
      peak_sockets_lsof: socks.peak,
    };
    if (plan.cdpSse) result.det.events_stream_messages_cdp = streams.reduce((a, r) => a + r.windowSseMessages, 0);

    // --- Timings.
    const blocked = shorts.filter((r) => r.blockedMs !== undefined).map((r) => r.blockedMs!);
    const unanswered = shorts.filter((r) => r.status === undefined && !r.failed);
    for (const r of shorts) {
      if (r.blockedMs === undefined) continue;
      const k = `${r.method} ${routeOf(r.path)}`;
      const b = (result.blockedByRoute[k] ??= { n: 0, p50: 0, max: 0 });
      b.n++;
      b.max = Math.max(b.max, Math.round(r.blockedMs));
    }
    for (const [k, b] of Object.entries(result.blockedByRoute)) {
      b.p50 = Math.round(pct(shorts.filter((r) => `${r.method} ${routeOf(r.path)}` === k && r.blockedMs !== undefined).map((r) => r.blockedMs!), 0.5));
    }
    const lt = await longTasks(page, t0, t1);
    // Click → the last /events byte of the window: how long the replay ran.
    const streamed = streams.some((r) => wireBytes(r) > 0);
    result.timing = {
      settled_ms: result.legs.reduce((a, l) => a + l.settledMs, 0),
      window_ms: t1 - t0,
      replay_ms: streamed ? Math.round(Math.max(0, Math.min(net.lastStreamByte, t1) - t0)) : 0,
      blocked_p50_ms: Math.round(pct(blocked, 0.5)),
      blocked_p95_ms: Math.round(pct(blocked, 0.95)),
      blocked_max_ms: Math.round(Math.max(0, ...blocked)),
      blocked_over_1s: blocked.filter((b) => b > 1000).length,
      unanswered_at_end: unanswered.length,
      longtask_total_ms: lt.totalMs,
      longtask_count: lt.count,
      router_cpu_ms: proc.router.cpuMs,
      data_cpu_ms: proc.data.cpuMs,
      router_peak_rss_mb: proc.router.peakRssMB,
      data_peak_rss_mb: proc.data.peakRssMB,
    };
    result.legs.forEach((l, i) => (result.timing[`leg${i + 1}_settled_ms`] = l.settledMs));
    result.socketOwners = [...socks.owners];
    result.requests = reqs.map((r) => [
      `${r.method} ${routeOf(r.path)}`,
      r.type,
      Math.round(r.start - t0),
      r.blockedMs === undefined ? null : Math.round(r.blockedMs),
      r.end === undefined ? null : Math.round(r.end - r.start),
      r.failed ?? r.status ?? null,
    ]);
    if (liveHits.length) throw new Error(`the page tried to reach a live port: ${liveHits.join(", ")}`);
    result.ok = true;
  } catch (err) {
    result.error = err instanceof Error ? err.message : String(err);
    // What the page looked like when it went wrong.
    const pages = context?.pages() ?? [];
    if (pages[0]) {
      const shot = path.join(plan.runDir, "failures", `${scenario.id}-${build.label}-${rep}.png`);
      mkdirSync(path.dirname(shot), { recursive: true });
      await pages[0].screenshot({ path: shot, fullPage: true }).catch(() => {});
    }
  } finally {
    await teardown();
    active = undefined;
  }
  return result;
}
