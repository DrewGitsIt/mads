import { mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";
import { _electron as electron, chromium, type Browser, type BrowserContext, type BrowserServer, type Page } from "@playwright/test";
import { LAG_PROBE_INIT, LONG_TASK_INIT, NetCollector, ProcSampler, SocketSampler, isLongLived, lagSince, longTasks, routeOf, type NetRequest } from "./collectors";
import { delayFor, parseDelays } from "./delayRules";
import type { ProxyRequest, ProxySnapshot } from "./proxyClient";
import { SCENARIOS, scenarioById, useManifest, type Manifest, type Scenario, type ScenarioCtx } from "./scenarios";
import { bootPerfStack, childrenOf, logHead, streamTarget, waitDelegatesConcluded, type Build, type PerfStack } from "./stack";

export type DelayMode = "on" | "off";

/** The run the runner (run.sh) planned: builds, workload, scenarios, reps. */
export interface Plan {
  runDir: string;
  seededDB: string;
  manifestPath: string;
  /** perfseed -transcripts output, copied into each stack's $HOME. */
  transcriptsDir?: string;
  builds: Build[];
  scenarios: string[];
  n: number;
  browser: "chromium" | "electron";
  /** Delay modes to run every scenario in. */
  modes: DelayMode[];
  /** PERF_DELAYS for "on" ("" = defaults). */
  delays: string;
  /** Hold the open conversation's /output stream open at the proxy (delayRules.ts HELD_OUTPUT). */
  holdOutput: boolean;
  /** Track SSE messages over CDP too (heavy: ships every message's data to the harness). */
  cdpSse: boolean;
  /**
   * The B1 comparison only: put back the old context.route() live-port
   * guard, which routes every request through the Playwright worker.
   */
  legacyRouteGuard?: boolean;
  meta: Record<string, unknown>;
}

export function loadPlan(): Plan | undefined {
  const p = process.env.PERF_PLAN;
  if (!p) return undefined;
  const plan = JSON.parse(readFileSync(p, "utf8")) as Plan;
  plan.scenarios = plan.scenarios.length ? plan.scenarios.map((s) => scenarioById(s).id) : SCENARIOS.map((s) => s.id);
  plan.modes ??= ["on", "off"];
  return plan;
}

export interface LegResult {
  name: string;
  /** Click → its data marker visible. */
  markerMs: number;
  /** Click → settled: marker visible, then waitRule (short requests, /events caught up to head, main thread idle). */
  settledMs: number;
  /** Click → the last /events byte the proxy forwarded in the leg. */
  lastEventsByteMs: number;
  /** Main-thread time in the leg (CDP Performance.getMetrics deltas, click → settled). */
  taskMs: number;
  scriptMs: number;
  /** Lag-probe samples, click → settled: worst and p95 (ms). */
  lagMaxMs: number;
  lagP95Ms: number;
  /** DOM size at settle. */
  domNodes: number;
  eventsBytes: number;
  eventsFrames: number;
  eventsConnects: number;
}

export interface Result {
  scenario: string;
  build: string;
  mode: DelayMode;
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
  queuedByRoute: Record<string, { n: number; p50: number; max: number }>;
  windowCapped: boolean;
  socketOwners: string[];
  /** Connections the browser opened anywhere but the proxy (should be none). */
  browserForeign: string[];
  /** Every request in the window: route, CDP type, start (ms after t0), queued, duration, status. */
  requests: [string, string, number, number | null, number | null, number | string | null][];
}

const WINDOW_CAP_MS = Number(process.env.PERF_WINDOW_CAP_MS ?? 120_000);
const PREPARE_CAP_MS = Number(process.env.PERF_PREPARE_CAP_MS ?? 300_000);
const SETTLE_CAP_MS = Number(process.env.PERF_SETTLE_CAP_MS ?? 180_000);
const SOCKETS_PER_ORIGIN = 6;
const LIVE_PORT = /^(https?|wss?):\/\/(localhost|127\.0\.0\.1|\[::1\]|0\.0\.0\.0)(:(17373|17374|17380))\b/;

/** Chromium talks to the proxy and nothing else (T4; checked with lsof). */
export const CHROMIUM_ARGS = [
  "--disable-background-networking",
  "--disable-component-update",
  "--disable-sync",
  "--no-pings",
  "--no-first-run",
  "--disable-default-apps",
  "--disable-domain-reliability",
  "--disable-client-side-phishing-detection",
  "--disable-breakpad",
  "--metrics-recording-only",
  "--dns-prefetch-disable",
  "--disable-extensions",
  "--disable-component-extensions-with-background-pages",
  "--no-service-autorun",
  "--disable-field-trial-config",
  // The backstop: no host but loopback resolves, so whatever service the
  // flags above miss (lsof caught Google endpoints without it) can't
  // connect anywhere. No --disable-features here: Chromium keeps only the
  // last one, and Playwright passes its own.
  "--host-resolver-rules=MAP * ~NOTFOUND, EXCLUDE localhost, EXCLUDE 127.0.0.1",
];

const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));

const isEventsStream = (r: NetRequest) => r.type === "EventSource" && r.path === "/events";

/**
 * Everything the settle/quiet rule needs to watch one repetition.
 */
interface Watch {
  net: NetCollector;
  stack: PerfStack;
  page: Page;
  delayed: (r: NetRequest) => boolean;
  /** Stale CDP entries dropped (T5), for the result. */
  stale: { n: number };
}

const SHORT_QUIET_MS = 500;
const MAIN_IDLE_MS = 500;
const MAIN_LAG_BUSY_MS = 50;

/**
 * The one rule both settle (R2-1..R2-4) and quiet use, from `start` on:
 *  - no non-delayed short request in flight, and none started for 500 ms
 *    (R2-2/R2-3: delayed ones are the mock's, not the design's);
 *  - every open /events stream has forwarded a frame with seq at or past
 *    the newest event it owes up to the log head read at `start` (read from
 *    the database, not the page), or owes none (R2-1; a live-only stream
 *    starting at head is caught up at once);
 *  - the main thread answered the lag probe within 50 ms for 500 ms (R2-4).
 * Resolves with the instant all three first held — the latest of the last
 * short request's start or end, the frame that caught a stream up, and the
 * last laggy probe sample — not counting the quiet windows themselves.
 */
async function waitRule(w: Watch, start: number, head: number, capMs: number): Promise<{ at: number; capped: boolean }> {
  const deadline = Date.now() + capMs;
  const targets = new Map<number, number>();
  const caughtAt = new Map<number, number>();
  for (;;) {
    const now = Date.now();
    const snap = await w.stack.proxy.snapshot();
    const proxyOpen = snap.requests.filter((r) => r.end === undefined && !r.sse);
    const socketsFree = peakIn(snap.timeline, now - 5000, now) < SOCKETS_PER_ORIGIN;
    w.stale.n += w.net.dropStale((r) => proxyOpen.some((p) => p.method === r.method && p.path === r.path && p.query === r.query), socketsFree);

    // Short requests.
    const shorts = [...w.net.requests.values()].filter((r) => !isLongLived(r) && !w.delayed(r) && (r.end === undefined || r.end >= start));
    const inFlight = shorts.some((r) => r.end === undefined);
    const lastShort = Math.max(start, ...shorts.map((r) => Math.max(r.start, r.end ?? 0)));
    const shortOk = !inFlight && now - lastShort >= SHORT_QUIET_MS;

    // /events caught up to head.
    let eventsOk = true;
    let eventsAt = start;
    for (const r of snap.requests) {
      if (!(r.sse && !r.held && r.path === "/events") || r.end !== undefined) continue;
      if (!targets.has(r.id)) {
        const q = new URLSearchParams(r.query);
        const since = Number(q.get("since") ?? 0);
        targets.set(r.id, await streamTarget(w.stack, since, head, (q.get("types") ?? "").split(",").filter(Boolean)));
      }
      const target = targets.get(r.id)!;
      if (target > 0 && r.maxSeq < target) {
        eventsOk = false;
        continue;
      }
      if (!caughtAt.has(r.id)) caughtAt.set(r.id, target > 0 ? (r.lastByte ?? now) : Math.max(r.start, start));
      eventsAt = Math.max(eventsAt, caughtAt.get(r.id)!);
    }

    // Main thread.
    const lag = await lagSince(w.page, start - 1000).catch(() => [] as [number, number][]);
    const latest = lag.length ? lag[lag.length - 1][0] : 0;
    const busy = lag.filter((x) => x[1] >= MAIN_LAG_BUSY_MS && x[0] >= start);
    const lastBusy = busy.length ? busy[busy.length - 1][0] : start;
    const mainOk = latest >= Date.now() - 200 && Date.now() - lastBusy >= MAIN_IDLE_MS;

    if (shortOk && eventsOk && mainOk) return { at: Math.max(lastShort, eventsAt, lastBusy), capped: false };
    if (now > deadline) return { at: now, capped: true };
    await sleep(50);
  }
}

function pct(xs: number[], q: number): number {
  if (xs.length === 0) return 0;
  const s = [...xs].sort((a, b) => a - b);
  return s[Math.min(s.length - 1, Math.max(0, Math.ceil(q * s.length) - 1))];
}

/** /events SSE bytes, frames and connects between two proxy snapshots. */
function eventsBetween(a: ProxySnapshot, b: ProxySnapshot) {
  const before = new Map(a.requests.map((r) => [r.id, r]));
  let bytes = 0;
  let frames = 0;
  let connects = 0;
  const per: { r: ProxyRequest; bytes: number; frames: number; isNew: boolean }[] = [];
  for (const r of b.requests) {
    if (!(r.sse && !r.held && r.path === "/events")) continue;
    const old = before.get(r.id);
    const db = r.bytes - (old?.bytes ?? 0);
    const df = r.frames - (old?.frames ?? 0);
    if (!old) connects++;
    if (old && db === 0 && old.end !== undefined) continue;
    bytes += db;
    frames += df;
    per.push({ r, bytes: db, frames: df, isNew: !old });
  }
  return { bytes, frames, connects, per };
}

/** Time in [from, to] with at least `n` requests (so sockets) busy at the proxy. */
function timeAtLeast(timeline: [number, number][], n: number, from: number, to: number): number {
  let total = 0;
  for (let i = 0; i < timeline.length; i++) {
    const [t, v] = timeline[i];
    const next = i + 1 < timeline.length ? timeline[i + 1][0] : to;
    const s = Math.max(t, from);
    const e = Math.min(next, to);
    if (v >= n && e > s) total += e - s;
  }
  return total;
}

function peakIn(timeline: [number, number][], from: number, to: number): number {
  let peak = 0;
  let prev = 0;
  for (const [t, v] of timeline) {
    if (t < from) prev = v;
    else if (t <= to) peak = Math.max(peak, v);
  }
  return Math.max(peak, prev);
}

/**
 * When each short request got a socket (M4): CDP's sendStart if it got a
 * response; else when the proxy saw it arrive (a delayed or unanswered
 * one), matched by method, path and query; else its cancel time; else the
 * window's end.
 */
function queuedUntil(reqs: NetRequest[], proxyReqs: ProxyRequest[], t1: number): Map<string, number> {
  const used = new Set<number>();
  const out = new Map<string, number>();
  for (const r of [...reqs].sort((a, b) => a.start - b.start)) {
    if (r.sent !== undefined) {
      out.set(r.id, r.sent);
      continue;
    }
    const p = proxyReqs.find((x) => !used.has(x.id) && x.method === r.method && x.path === r.path && x.query === r.query && x.start >= r.start - 50);
    if (p) {
      used.add(p.id);
      out.set(r.id, p.start);
    } else out.set(r.id, Math.min(r.end ?? t1, t1));
  }
  return out;
}

/** Most requests waiting for a socket at once. */
function maxDepth(intervals: [number, number][]): number {
  const edges: [number, number][] = [];
  for (const [s, e] of intervals) if (e > s) edges.push([s, 1], [e, -1]);
  edges.sort((a, b) => a[0] - b[0] || a[1] - b[1]);
  let d = 0;
  let max = 0;
  for (const [, x] of edges) {
    d += x;
    max = Math.max(max, d);
  }
  return max;
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

export async function runOne(opts: { plan: Plan; build: Build; scenario: Scenario; rep: number; mode: DelayMode }): Promise<Result> {
  const { plan, build, scenario, rep, mode } = opts;
  const manifest = JSON.parse(readFileSync(plan.manifestPath, "utf8")) as Manifest;
  useManifest(manifest);
  const tag = `${scenario.id}-${mode}-${build.label}-${rep}`;
  const result: Result = {
    scenario: scenario.id,
    build: build.label,
    mode,
    rep,
    browser: plan.browser,
    ok: false,
    legs: [],
    det: {},
    timing: {},
    eventsConnects: [],
    requestsByRoute: {},
    queuedByRoute: {},
    windowCapped: false,
    socketOwners: [],
    browserForeign: [],
    requests: [],
  };
  const delays = mode === "on" ? plan.delays : "off";
  const rules = parseDelays(delays);
  const delayed = (r: NetRequest) => delayFor(rules, r.method, r.path) > 0;
  let stack: PerfStack | undefined;
  let server: BrowserServer | undefined;
  let browser: Browser | undefined;
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
    else {
      await context?.close().catch(() => {});
      await browser?.close().catch(() => {});
      await server?.close().catch(() => {});
    }
    if (electronUserData) rmSync(electronUserData, { recursive: true, force: true });
    await stack?.teardown();
  };
  active = teardown;
  let fatal: Error | undefined;
  try {
    stack = await bootPerfStack({
      build,
      seededDB: plan.seededDB,
      transcriptsDir: plan.transcriptsDir,
      delays,
      holdOutput: plan.holdOutput,
      tag,
    });
    await waitDelegatesConcluded(stack, manifest.parent.id);

    let page: Page;
    let browserPid: number;
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
      browserPid = electronApp.process().pid!;
      stack.own(browserPid, "electron");
      context = electronApp.context();
      await context.addInitScript(LONG_TASK_INIT);
      await context.addInitScript(LAG_PROBE_INIT);
      page = await electronApp.firstWindow();
      // Main's ROUTER_BASE is VITE_ROUTER_URL: the window must have loaded from the proxy, never the 17373 default.
      await page.waitForLoadState("domcontentloaded");
      if (!page.url().startsWith(stack.appURL)) throw new Error(`FATAL: Electron window loaded ${page.url()}, not the proxy ${stack.appURL}`);
    } else {
      // A browser per repetition, launched as a server so its pid is known
      // (and registered) the moment it exists.
      server = await chromium.launchServer({
        channel: "chromium",
        // Every origin but the proxy's goes to a dead proxy: the live ports
        // are unreachable. <-loopback> drops Chromium's implicit loopback
        // bypass; the proxy origin bypasses and keeps its own 6-socket pool.
        args: [...CHROMIUM_ARGS, "--proxy-server=127.0.0.1:1", `--proxy-bypass-list=<-loopback>;127.0.0.1:${stack.proxy.port}`],
      });
      browserPid = server.process().pid!;
      stack.own(browserPid, "chromium");
      browser = await chromium.connect(server.wsEndpoint());
      context = await browser.newContext({
        extraHTTPHeaders: { Authorization: `Bearer ${stack.token}` },
        viewport: { width: 1440, height: 900 },
      });
      await context.addInitScript(LONG_TASK_INIT);
      await context.addInitScript(LAG_PROBE_INIT);
      page = await context.newPage();
    }
    if (plan.legacyRouteGuard) {
      // The old guard, for the B1 comparison only.
      await context.route(/^https?:\/\/(localhost|127\.0\.0\.1|\[::1\]):(17373|17374|17380)\//, (route) => route.abort());
    }
    for (const c of childrenOf(browserPid)) stack.own(c, "browser-child");
    net = new NetCollector(stack.appURL, plan.cdpSse);
    await net.attach(page);
    const watch: Watch = { net, stack, page, delayed, stale: { n: 0 } };

    const ctx: ScenarioCtx = {
      page,
      stack,
      manifest,
      browser: plan.browser,
      quiesce: async () => {
        const from = Date.now();
        const r = await waitRule(watch, from, await logHead(stack!), PREPARE_CAP_MS);
        if (r.capped) {
          const open = net!.shortInFlight(delayed).map((x) => `${x.method} ${routeOf(x.path)} (${x.type})`);
          throw new Error(`page never went quiet; still in flight: ${open.join(", ") || "none (a stream behind head, or a busy main thread)"}`);
        }
      },
    };
    await scenario.prepare(ctx);
    if (scenario.id !== "1-cold-load") await ctx.quiesce();

    const procs = new ProcSampler(stack.pids, ["router"], (pid) => stack!.own(pid, "router-child"));
    const socks = new SocketSampler(stack.proxy.port, browserPid);
    await procs.start();
    socks.start();
    const t0 = Date.now();
    net.startWindow(t0);
    const snap0 = await stack.proxy.snapshot();

    let prevSnap = snap0;
    for (const leg of scenario.legs) {
      const head = await logHead(stack);
      const m0 = await net.mainThread();
      const clickAt = Date.now();
      await leg.act(ctx);
      await leg.ready(ctx);
      const markerAt = Date.now();
      const rule = await waitRule(watch, clickAt, head, SETTLE_CAP_MS);
      if (rule.capped) result.windowCapped = true;
      const settledAt = Math.max(markerAt, rule.at);
      const m1 = await net.mainThread();
      const lag = (await lagSince(page, clickAt)).filter((x) => x[0] <= settledAt).map((x) => x[1]);
      const domNodes = await page.evaluate(() => document.getElementsByTagName("*").length).catch(() => 0);
      if (leg.quiesceAfter) await ctx.quiesce();
      const snap = await stack.proxy.snapshot();
      const ev = eventsBetween(prevSnap, snap);
      prevSnap = snap;
      const lastByte = Math.max(
        0,
        ...snap.requests.filter((r) => r.path === "/events" && r.sse && r.lastByte !== undefined && r.lastByte >= clickAt).map((r) => r.lastByte!),
      );
      result.legs.push({
        name: leg.name,
        markerMs: markerAt - clickAt,
        settledMs: Math.round(settledAt - clickAt),
        lastEventsByteMs: lastByte ? lastByte - clickAt : 0,
        taskMs: Math.round(m1.taskMs - m0.taskMs),
        scriptMs: Math.round(m1.scriptMs - m0.scriptMs),
        lagMaxMs: Math.max(0, ...lag),
        lagP95Ms: pct(lag, 0.95),
        domNodes,
        eventsBytes: ev.bytes,
        eventsFrames: ev.frames,
        eventsConnects: ev.connects,
      });
      if (process.env.PERF_DEBUG) {
        const dir = path.join(plan.runDir, "debug");
        mkdirSync(dir, { recursive: true });
        await page.screenshot({ path: path.join(dir, `${tag}-${result.legs.length}.png`) }).catch(() => {});
        writeFileSync(path.join(dir, `${tag}-${result.legs.length}.aria.txt`), await page.locator("body").ariaSnapshot().catch(() => ""));
      }
    }
    if ((await waitRule(watch, Date.now(), await logHead(stack), WINDOW_CAP_MS)).capped) result.windowCapped = true;
    const t1 = Date.now();
    const snap1 = await stack.proxy.snapshot();
    await socks.stop();
    const proc = await procs.stop();

    // The live app must never have been in reach (B1b): fail the run loudly.
    const live = net.foreign.filter((u) => LIVE_PORT.test(u));
    if (live.length) {
      fatal = new Error(`LIVE PORT CONTACTED by the page: ${live.join(", ")}`);
      throw fatal;
    }
    result.browserForeign = [...new Set([...socks.foreign, ...net.foreign])];
    const liveSock = [...socks.foreign].filter((x) => /:(17373|17374|17380)$/.test(x));
    if (liveSock.length) {
      fatal = new Error(`LIVE PORT CONNECTION by the browser (lsof): ${liveSock.join(", ")}`);
      throw fatal;
    }

    // --- Deterministic counts.
    const reqs = net.window(t0, t1);
    const started = reqs.filter((r) => r.start >= t0);
    const shorts = started.filter((r) => !isLongLived(r));
    const ev = eventsBetween(snap0, snap1);
    for (const p of ev.per) {
      const q = new URLSearchParams(p.r.query);
      result.eventsConnects.push({
        since: p.isNew ? (q.has("since") ? Number(q.get("since")) : 0) : null,
        types: (q.get("types") ?? "").split(",").filter(Boolean).length,
        bytes: p.bytes,
        frames: p.frames,
      });
    }
    const proxyNew = snap1.requests.filter((r) => r.start >= t0);
    const batchProxy = proxyNew.filter((r) => !r.sse && r.path === "/events");
    for (const r of shorts) {
      const k = `${r.method} ${routeOf(r.path)}`;
      result.requestsByRoute[k] = (result.requestsByRoute[k] ?? 0) + 1;
    }
    result.det = {
      cdp_stale_dropped: watch.stale.n,
      events_stream_bytes_proxy: ev.bytes,
      events_stream_bytes: reqs.filter(isEventsStream).reduce((a, r) => a + r.windowDecoded, 0),
      events_stream_frames: ev.frames,
      events_stream_connects: ev.connects,
      events_from_zero: ev.per.filter((p) => p.isNew && !new URLSearchParams(p.r.query).has("since")).length,
      events_batch_requests: batchProxy.length,
      events_batch_bytes: batchProxy.reduce((a, r) => a + r.bytes, 0),
      requests_short: shorts.length,
      requests_delayed: proxyNew.filter((r) => r.delayMs > 0).length,
      output_streams_held: proxyNew.filter((r) => r.held).length,
      peak_busy_sockets_proxy: peakIn(snap1.timeline, t0, t1),
      peak_connections_cdp: net.peakConnections(t0, t1),
      peak_sockets_lsof: socks.peak,
    };
    if (plan.cdpSse) result.det.events_stream_messages_cdp = reqs.filter(isEventsStream).reduce((a, r) => a + r.windowSseMessages, 0);

    // --- Queueing (M3/M4): until a socket, a cancel, or the window's end.
    const until = queuedUntil(shorts, proxyNew.filter((r) => !r.sse), t1);
    const waits: number[] = [];
    const intervals: [number, number][] = [];
    for (const r of shorts) {
      const u = until.get(r.id)!;
      const w = Math.max(0, u - r.start);
      waits.push(w);
      intervals.push([r.start, u]);
      const k = `${r.method} ${routeOf(r.path)}`;
      const b = (result.queuedByRoute[k] ??= { n: 0, p50: 0, max: 0 });
      b.n++;
      b.max = Math.max(b.max, Math.round(w));
    }
    for (const [k, b] of Object.entries(result.queuedByRoute)) {
      b.p50 = Math.round(pct(shorts.filter((r) => `${r.method} ${routeOf(r.path)}` === k).map((r) => Math.max(0, until.get(r.id)! - r.start)), 0.5));
    }
    const lt = await longTasks(page, t0, t1);
    result.timing = {
      settled_ms: result.legs.reduce((a, l) => a + l.settledMs, 0),
      main_task_ms: result.legs.reduce((a, l) => a + l.taskMs, 0),
      main_script_ms: result.legs.reduce((a, l) => a + l.scriptMs, 0),
      lag_max_ms: Math.max(0, ...result.legs.map((l) => l.lagMaxMs)),
      last_events_byte_ms: Math.max(0, ...ev.per.map((p) => (p.r.lastByte ?? 0) - t0)),
      window_ms: t1 - t0,
      all_sockets_busy_ms: Math.round(timeAtLeast(snap1.timeline, SOCKETS_PER_ORIGIN, t0, t1)),
      queued_total_ms: Math.round(waits.reduce((a, b) => a + b, 0)),
      queue_depth_max: maxDepth(intervals),
      queued_p50_ms: Math.round(pct(waits, 0.5)),
      queued_p95_ms: Math.round(pct(waits, 0.95)),
      queued_max_ms: Math.round(Math.max(0, ...waits)),
      queued_over_1s: waits.filter((w) => w > 1000).length,
      cancelled: shorts.filter((r) => r.failed === "canceled").length,
      unanswered_at_end: shorts.filter((r) => r.end === undefined).length,
      longtask_total_ms: lt.totalMs,
      longtask_count: lt.count,
      router_cpu_ms: proc.router?.cpuMs ?? 0,
      data_cpu_ms: proc.data?.cpuMs ?? 0,
      children_cpu_ms: proc["router-children"]?.cpuMs ?? 0,
      router_peak_rss_mb: proc.router?.peakRssMB ?? 0,
      data_peak_rss_mb: proc.data?.peakRssMB ?? 0,
    };
    result.legs.forEach((l, i) => {
      result.timing[`leg${i + 1}_settled_ms`] = l.settledMs;
      result.det[`leg${i + 1}_events_bytes`] = l.eventsBytes;
    });
    result.socketOwners = [...socks.owners];
    result.requests = reqs.map((r) => [
      `${r.method} ${routeOf(r.path)}`,
      r.type,
      Math.round(r.start - t0),
      until.has(r.id) ? Math.round(until.get(r.id)! - r.start) : null,
      r.end === undefined ? null : Math.round(r.end - r.start),
      r.failed ?? r.status ?? null,
    ]);
    result.ok = true;
  } catch (err) {
    result.error = err instanceof Error ? err.message : String(err);
    const pages = context?.pages() ?? [];
    if (pages[0]) {
      const shot = path.join(plan.runDir, "failures", `${tag}.png`);
      mkdirSync(path.dirname(shot), { recursive: true });
      await pages[0].screenshot({ path: shot, fullPage: true }).catch(() => {});
    }
  } finally {
    await teardown();
    active = undefined;
  }
  if (fatal) throw fatal;
  return result;
}
