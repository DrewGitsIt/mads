import { mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";
import { _electron as electron, chromium, type Browser, type BrowserContext, type BrowserServer, type Page } from "@playwright/test";
import { MainThreadMetrics, ProcSampler, SocketSampler, routeOf } from "./collectors";
import { delayFor, parseDelays } from "./delayRules";
import { resolveCheck } from "./checkNames";
import { arm, probeFor, probeSeries, readProbe, type MarkerSpec, type PageReq } from "./probe";
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
  /** Correctness checks to run once per build (checks.ts); empty: none. */
  checks: string[];
  /**
   * Realism mode: the workload is a sanitised copy of a real database
   * (perfseed realism). Results must not name sessions: errors are
   * redacted and no screenshots are kept.
   */
  realism?: boolean;
  meta: Record<string, unknown>;
}

export function loadPlan(): Plan | undefined {
  const p = process.env.PERF_PLAN;
  if (!p) return undefined;
  const plan = JSON.parse(readFileSync(p, "utf8")) as Plan;
  plan.scenarios =
    plan.scenarios.length === 1 && plan.scenarios[0] === "none"
      ? []
      : plan.scenarios.length
        ? plan.scenarios.map((s) => scenarioById(s).id)
        : SCENARIOS.map((s) => s.id);
  plan.modes ??= ["on", "off"];
  plan.checks = (plan.checks ?? []).map(resolveCheck);
  return plan;
}

export interface LegResult {
  name: string;
  /** Click → its data marker held, in page time (the probe's MutationObserver). */
  markerMs: number;
  /** Click → the harness saw the marker (diagnostic: includes any CDP backlog). */
  markerSeenMs: number;
  /** Click → settled: the marker, then waitRule. */
  settledMs: number;
  /** Click → the last /events byte the proxy forwarded in the leg. */
  lastEventsByteMs: number;
  /** Main-thread time in the leg (CDP Performance.getMetrics deltas, click → settled). */
  taskMs: number;
  scriptMs: number;
  /** Lag-probe samples, click → settled: worst and p95 (ms). */
  lagMaxMs: number;
  lagP95Ms: number;
  /** Worst gap between a probe read's page time and when the harness got it (F4). */
  cdpLagMaxMs: number;
  domNodes: number;
  eventsBytes: number;
  eventsFrames: number;
  eventsConnects: number;
}

export interface CheckResult {
  name: string;
  ok: boolean;
  detail?: string;
  /**
   * Fails on main today, for a reason outside the bus (or a bug the bus
   * redesign is meant to fix): reported, not counted against the run. A
   * candidate passing it shows as an improvement; failing one base passes
   * is flagged.
   */
  knownOnMain?: string;
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
  /** The live-event smoke after the scenario, and any checks run in the same repetition. */
  checks: CheckResult[];
  /** Every proxy request in the window: route, start (ms after t0), queued in the browser, duration, status. */
  requests: [string, number, number | null, number | null, number | string | null][];
}

const WINDOW_CAP_MS = Number(process.env.PERF_WINDOW_CAP_MS ?? 120_000);
const PREPARE_CAP_MS = Number(process.env.PERF_PREPARE_CAP_MS ?? 180_000);
const SETTLE_CAP_MS = Number(process.env.PERF_SETTLE_CAP_MS ?? 180_000);
const MARKER_CAP_MS = Number(process.env.PERF_MARKER_CAP_MS ?? 30_000);
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
  // The backstop: no host but loopback resolves. No --disable-features
  // here: Chromium keeps only the last one, and Playwright passes its own.
  "--host-resolver-rules=MAP * ~NOTFOUND, EXCLUDE localhost, EXCLUDE 127.0.0.1",
];

const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));

function pct(xs: number[], q: number): number {
  if (xs.length === 0) return 0;
  const s = [...xs].sort((a, b) => a - b);
  return s[Math.min(s.length - 1, Math.max(0, Math.ceil(q * s.length) - 1))];
}

const isEvents = (r: ProxyRequest) => r.sse && !r.held && r.path === "/events";

/**
 * Pairs each request the page made with the proxy's record of it (same
 * method, path and query, arrived at or after the page started it). A page
 * request with no partner that hasn't failed or closed is still queued in
 * the browser, waiting for a socket.
 */
function matchRequests(page: PageReq[], proxy: ProxyRequest[], origin: string): Map<number, ProxyRequest | undefined> {
  const used = new Set<number>();
  const out = new Map<number, ProxyRequest | undefined>();
  const byKey = new Map<string, ProxyRequest[]>();
  for (const p of proxy) {
    const k = `${p.method} ${p.path}${p.query}`;
    (byKey.get(k) ?? byKey.set(k, []).get(k)!).push(p);
  }
  // The page's own "it got there" time bounds the match: an EventSource's
  // open, a fetch's headers. Requests with one are paired first, inside
  // [start, reached]; one that failed or closed without ever reaching the
  // proxy (an outage, an abort in the queue) must not take another's record.
  const reached = (r: PageReq) => r.open ?? r.headers;
  const ordered = [...page].sort((a, b) => Number(reached(b) !== undefined) - Number(reached(a) !== undefined) || a.start - b.start);
  for (const r of ordered) {
    if (!r.url.startsWith(origin)) continue;
    const u = new URL(r.url);
    const cands = byKey.get(`${r.method} ${u.pathname}${u.search}`) ?? [];
    const hi = reached(r);
    const lo = r.start - 20;
    const m = cands.find((p) => !used.has(p.id) && p.start >= lo && (hi === undefined ? true : p.start <= hi + 20));
    if (m && hi === undefined && (r.failed !== undefined || r.closed !== undefined) && m.start > (r.failed ?? r.closed)!) {
      out.set(r.id, undefined);
      continue;
    }
    if (m) used.add(m.id);
    out.set(r.id, m);
  }
  return out;
}

/** /events SSE bytes, frames and connects between two proxy snapshots. */
function eventsBetween(a: ProxySnapshot, b: ProxySnapshot) {
  const before = new Map(a.requests.map((r) => [r.id, r]));
  let bytes = 0;
  let frames = 0;
  let connects = 0;
  const per: { r: ProxyRequest; bytes: number; frames: number; isNew: boolean }[] = [];
  for (const r of b.requests) {
    if (!isEvents(r)) continue;
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

/** Everything the settle/quiet rule watches in one repetition. */
export interface Watch {
  stack: PerfStack;
  page: Page;
  delayed: (method: string, path: string) => boolean;
  /** Worst CDP lag seen by probe reads since the last reset (F4). */
  cdpLag: { max: number };
  /** Streams settled by the byte-idle fallback (no `id:` lines; F5). */
  fallbacks: { n: number };
}

const SHORT_QUIET_MS = 500;
const MAIN_IDLE_MS = 500;
const BYTE_IDLE_MS = 1500;
const NO_PROGRESS_MS = Number(process.env.PERF_NO_PROGRESS_MS ?? 30_000);

/**
 * The one rule both settle and quiet use, from `start` on, with every
 * input taken from the page probe and the proxy — never CDP's network
 * events, which reach the harness late during a big replay (round 3):
 *  - no non-delayed short request in flight at the proxy, none still queued
 *    in the browser (a page request the proxy hasn't seen), and none
 *    started for 500 ms;
 *  - every open /events stream has forwarded a frame at or past the newest
 *    seq it owes up to `head` (read from the database), or owes none; an
 *    /events EventSource still queued in the browser owes; a stream with
 *    bytes but no `id:` lines falls back to 1.5 s of byte idle (counted);
 *  - the page's lag probe has had no sample over 100 ms for 500 ms, judged
 *    in page time from the probe's own record, so a late read is still
 *    right.
 * Resolves with the instant all three first held, not counting the quiet
 * windows themselves.
 */
export async function waitRule(w: Watch, start: number, head: number, capMs: number): Promise<{ at: number; capped: boolean; reason?: string }> {
  const deadline = Date.now() + capMs;
  const targets = new Map<string, number>();
  const caughtAt = new Map<number, number>();
  const fellBack = new Set<number>();
  for (;;) {
    const snap = await w.stack.proxy.snapshot();
    const probe = await readProbe(w.page, start - 60_000);
    const received = Date.now();
    w.cdpLag.max = Math.max(w.cdpLag.max, received - probe.pageNow);
    const now = Math.max(received, probe.pageNow);
    const match = matchRequests(probe.reqs, snap.requests, w.stack.appURL);

    // Short requests: at the proxy, and queued in the browser.
    let lastShort = start;
    let busy = false;
    for (const r of snap.requests) {
      if (r.sse || r.held || w.delayed(r.method, r.path)) continue;
      if (r.end === undefined) busy = true;
      if (r.end === undefined || r.end >= start) lastShort = Math.max(lastShort, r.start, r.end ?? 0);
    }
    let eventsQueued = false;
    let queuedES: PageReq[] = [];
    for (const r of probe.reqs) {
      if (match.get(r.id) || r.failed !== undefined || r.closed !== undefined || r.headers !== undefined) continue;
      const u = new URL(r.url);
      if (w.delayed(r.method, u.pathname)) continue;
      if (r.kind === "es") {
        if (u.pathname === "/events") {
          eventsQueued = true;
          queuedES.push(r);
        }
        continue;
      }
      busy = true;
      lastShort = Math.max(lastShort, r.start);
    }
    const shortOk = !busy && now - lastShort >= SHORT_QUIET_MS;

    // /events caught up: each open stream has forwarded the newest event it
    // will actually send — matching its filter, after where it starts
    // (its own `: start <seq>` declaration if the server made one, else
    // its `since`, else 0), up to the head at `start`.
    let eventsOk = !eventsQueued;
    let eventsAt = start;
    let stuck = "";
    for (const r of snap.requests) {
      if (!isEvents(r) || r.end !== undefined) continue;
      const q = new URLSearchParams(r.query);
      const from = r.declaredStart ?? Number(q.get("since") ?? 0);
      const key = `${r.id}:${from}`;
      if (!targets.has(key)) targets.set(key, await streamTarget(w.stack, from, head, (q.get("types") ?? "").split(",").filter(Boolean)));
      const target = targets.get(key)!;
      if (target > 0 && r.maxSeq < target) {
        // F5: bytes flowing but no id: lines at all — wait for byte idle instead.
        if (r.maxSeq === 0 && r.bytes > 0 && r.lastByte !== undefined && now - r.lastByte >= BYTE_IDLE_MS) {
          if (!fellBack.has(r.id)) {
            fellBack.add(r.id);
            w.fallbacks.n++;
          }
        } else {
          eventsOk = false;
          // No progress for 30 s while still owing frames: name it and stop.
          if (now - Math.max(r.lastByte ?? r.start, start) > NO_PROGRESS_MS) {
            stuck = `/events stream stuck behind head: forwarded through seq ${r.maxSeq}, owes up to ${target} (start ${from}, head ${head}), no byte for ${Math.round((now - (r.lastByte ?? r.start)) / 1000)} s`;
          }
          continue;
        }
      }
      if (!caughtAt.has(r.id)) caughtAt.set(r.id, target > 0 ? (r.lastByte ?? now) : Math.max(r.start, start));
      eventsAt = Math.max(eventsAt, caughtAt.get(r.id)!);
    }
    if (eventsQueued && now - start > NO_PROGRESS_MS && !stuck) {
      const d = queuedES.map((r) => `started ${Math.round(r.start - start)} ms in, open ${r.open ? Math.round(r.open - start) : "-"}, reconnects ${r.reconnects ?? 0}`);
      stuck = `an /events EventSource the proxy never saw has been open in the browser for over 30 s (${d.join("; ")})`;
    }

    // Main thread, in page time: the probe is alive and calm (PERF_PROBE=0: not measured).
    const lastBusy = probe.lite ? start : Math.max(probe.lastBusy, start);
    const mainOk = probe.lite || (probe.lastSample >= probe.pageNow - 250 && probe.lastSample - lastBusy >= MAIN_IDLE_MS);

    if (shortOk && eventsOk && mainOk) return { at: Math.max(lastShort, eventsAt, lastBusy), capped: false };
    if (stuck) throw new Error(`settle: ${stuck}`);
    if (Date.now() > deadline) {
      const why = [!shortOk && "short requests still in flight or starting", !eventsOk && "/events behind head", !mainOk && "main thread busy"].filter(Boolean).join(", ");
      return { at: Date.now(), capped: true, reason: why };
    }
    await sleep(50);
  }
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

/** Everything a repetition owns, booted: the stack, a browser page with the probe, and the watch. */
export interface Rig {
  stack: PerfStack;
  page: Page;
  context: BrowserContext;
  browserPid: number;
  watch: Watch;
  metrics: MainThreadMetrics;
  /** Every URL the page requested (Playwright's own request events), for the live-port check. */
  urls: string[];
  ctx: ScenarioCtx;
  teardown(): Promise<void>;
}

/** Boots a repetition's stack and browser (or Electron). Registers teardown with abortActive. */
export async function bootRig(opts: {
  plan: Plan;
  build: Build;
  mode: DelayMode;
  tag: string;
  manifest: Manifest;
  fastModelURL?: string;
  env?: Record<string, string>;
}): Promise<Rig> {
  const { plan, build, mode, tag, manifest } = opts;
  const delays = mode === "on" ? plan.delays : "off";
  const rules = parseDelays(delays);
  let stack: PerfStack | undefined;
  let server: BrowserServer | undefined;
  let browser: Browser | undefined;
  let context: BrowserContext | undefined;
  let electronApp: Awaited<ReturnType<typeof electron.launch>> | undefined;
  let electronUserData: string | undefined;
  const metrics = new MainThreadMetrics();
  let tornDown = false;
  const teardown = async () => {
    if (tornDown) return;
    tornDown = true;
    await metrics.detach();
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
  try {
    stack = await bootPerfStack({
      build,
      seededDB: plan.seededDB,
      transcriptsDir: plan.transcriptsDir,
      delays,
      holdOutput: plan.holdOutput,
      tag,
      fastModelURL: opts.fastModelURL,
      env: opts.env,
    });
    if (manifest.parent?.running?.length) await waitDelegatesConcluded(stack, manifest.parent.id);
    if (manifest.typical?.running?.length) await waitDelegatesConcluded(stack, manifest.typical.id);
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
      await context.addInitScript(probeFor(build.label));
      page = await electronApp.firstWindow();
      // Main's ROUTER_BASE is VITE_ROUTER_URL: the window must load from the proxy, never the 17373 default.
      await page.waitForLoadState("domcontentloaded");
      if (!page.url().startsWith(stack.appURL)) throw new Error(`FATAL: Electron window loaded ${page.url()}, not the proxy ${stack.appURL}`);
    } else {
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
      await context.addInitScript(probeFor(build.label));
      page = await context.newPage();
    }
    for (const c of childrenOf(browserPid)) stack.own(c, "browser-child");
    // Every Playwright action fails in 30 s, naming itself, rather than
    // running into the test's 10-minute timeout.
    context.setDefaultTimeout(Number(process.env.PERF_ACTION_TIMEOUT_MS ?? 30_000));
    const urls: string[] = [];
    page.on("request", (r) => urls.push(r.url()));
    await metrics.attach(page);
    const watch: Watch = {
      stack,
      page,
      delayed: (m, p) => delayFor(rules, m, p) > 0,
      cdpLag: { max: 0 },
      fallbacks: { n: 0 },
    };
    const st = stack;
    const ctx: ScenarioCtx = {
      page,
      stack,
      manifest,
      browser: plan.browser,
      quiesce: async () => {
        const r = await waitRule(watch, Date.now(), await logHead(st), PREPARE_CAP_MS);
        if (r.capped) throw new Error(`page never went quiet: ${r.reason}`);
      },
    };
    return { stack, page, context, browserPid, watch, metrics, urls, ctx, teardown };
  } catch (err) {
    await teardown();
    throw err;
  }
}

/** The page asked for a live port: never allowed. */
export function liveHits(urls: string[]): string[] {
  return urls.filter((u) => LIVE_PORT.test(u));
}

/** Waits for a leg's armed marker, returning [page time, harness time]. Fails after 30 s, naming it. */
async function waitMarker(w: Watch, name: string, spec: MarkerSpec, capMs = MARKER_CAP_MS): Promise<[number, number]> {
  const deadline = Date.now() + capMs;
  for (;;) {
    const p = await readProbe(w.page, Number.MAX_SAFE_INTEGER);
    w.cdpLag.max = Math.max(w.cdpLag.max, Date.now() - p.pageNow);
    if (p.markers[name]) return [p.markers[name], Date.now()];
    if (Date.now() > deadline) throw new Error(`marker never appeared: ${name} ${JSON.stringify(spec)} (${capMs / 1000} s)`);
    await sleep(25);
  }
}

function redact(s: string, man: Manifest): string {
  let out = s;
  const names = [...man.sessions.map((x) => x.name), ...(man.parent?.running ?? []).map((d) => d.description)].filter(Boolean);
  for (const n of names.sort((a, b) => b.length - a.length)) out = out.split(n).join("<redacted>");
  return out.replace(/ses_[0-9a-f]+/g, "ses_<redacted>");
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
    checks: [],
    requests: [],
  };
  let rig: Rig | undefined;
  let fatal: Error | undefined;
  try {
    rig = await bootRig({ plan, build, mode, tag, manifest });
    const { stack, page, watch, metrics, ctx } = rig;
    await scenario.prepare(ctx);
    if (scenario.id !== "1-cold-load") await ctx.quiesce();

    const procs = new ProcSampler(stack.pids, ["router"], (pid) => stack.own(pid, "router-child"));
    const socks = new SocketSampler(stack.proxy.port, rig.browserPid);
    await procs.start();
    socks.start();
    const t0 = Date.now();
    const snap0 = await stack.proxy.snapshot();
    let prevSnap = snap0;
    let cdpLagAll = 0;

    for (const leg of scenario.legs) {
      const head = await logHead(stack);
      const m0 = await metrics.read();
      watch.cdpLag.max = 0;
      const clickAt = Date.now();
      const spec = leg.marker(ctx, clickAt);
      // A navigation replaces the document: arm in the next one too.
      if (leg.navigates) await rig.context.addInitScript(`window.__perf && window.__perf.arm(${JSON.stringify(leg.name)}, ${JSON.stringify(spec)});`);
      await arm(page, leg.name, spec);
      await leg.act(ctx);
      const [markerAt, markerSeen] = await waitMarker(watch, leg.name, spec, leg.markerCapMs);
      const rule = await waitRule(watch, clickAt, head, SETTLE_CAP_MS);
      if (rule.capped) throw new Error(`settle capped at ${SETTLE_CAP_MS / 1000} s: ${rule.reason}`);
      const settledAt = Math.max(markerAt, rule.at);
      const m1 = await metrics.read();
      const series = await probeSeries(page, clickAt);
      const lag = series.lag.filter((x) => x[0] <= settledAt).map((x) => x[1]);
      const domNodes = await page.evaluate(() => document.getElementsByTagName("*").length).catch(() => 0);
      const legLag = watch.cdpLag.max;
      cdpLagAll = Math.max(cdpLagAll, legLag);
      if (leg.quiesceAfter) await ctx.quiesce();
      const snap = await stack.proxy.snapshot();
      const ev = eventsBetween(prevSnap, snap);
      prevSnap = snap;
      const lastByte = Math.max(0, ...snap.requests.filter((r) => isEvents(r) && r.lastByte !== undefined && r.lastByte >= clickAt).map((r) => r.lastByte!));
      result.legs.push({
        name: leg.name,
        markerMs: Math.round(markerAt - clickAt),
        markerSeenMs: markerSeen - clickAt,
        settledMs: Math.round(settledAt - clickAt),
        lastEventsByteMs: lastByte ? lastByte - clickAt : 0,
        taskMs: Math.round(m1.taskMs - m0.taskMs),
        scriptMs: Math.round(m1.scriptMs - m0.scriptMs),
        lagMaxMs: Math.max(0, ...lag),
        lagP95Ms: pct(lag, 0.95),
        cdpLagMaxMs: Math.round(legLag),
        domNodes,
        eventsBytes: ev.bytes,
        eventsFrames: ev.frames,
        eventsConnects: ev.connects,
      });
      if (process.env.PERF_DEBUG && !plan.realism) {
        const dir = path.join(plan.runDir, "debug");
        mkdirSync(dir, { recursive: true });
        await page.screenshot({ path: path.join(dir, `${tag}-${result.legs.length}.png`) }).catch(() => {});
        writeFileSync(path.join(dir, `${tag}-${result.legs.length}.aria.txt`), await page.locator("body").ariaSnapshot().catch(() => ""));
      }
    }
    if ((await waitRule(watch, Date.now(), await logHead(stack), WINDOW_CAP_MS)).capped) result.windowCapped = true;
    const t1 = Date.now();
    const snap1 = await stack.proxy.snapshot();
    const probe = await readProbe(page, t0 - 1);
    const series = await probeSeries(page, t0);
    await socks.stop();
    const proc = await procs.stop();

    // The live app must never have been in reach: fail the run loudly.
    const live = [...liveHits(rig.urls), ...[...socks.foreign].filter((x) => /:(17373|17374|17380)$/.test(x))];
    if (live.length) {
      fatal = new Error(`LIVE PORT CONTACTED: ${live.join(", ")}`);
      throw fatal;
    }
    result.browserForeign = [...socks.foreign];

    // --- Deterministic counts, from the proxy.
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
    const shortsProxy = proxyNew.filter((r) => !r.sse && !r.held);
    for (const r of shortsProxy) {
      const k = `${r.method} ${routeOf(r.path)}`;
      result.requestsByRoute[k] = (result.requestsByRoute[k] ?? 0) + 1;
    }
    const batch = shortsProxy.filter((r) => r.path === "/events");
    result.det = {
      events_stream_bytes_proxy: ev.bytes,
      events_stream_frames: ev.frames,
      events_stream_connects: ev.connects,
      events_from_zero: ev.per.filter((p) => p.isNew && !new URLSearchParams(p.r.query).has("since")).length,
      events_batch_requests: batch.length,
      events_batch_bytes: batch.reduce((a, r) => a + r.bytes, 0),
      requests_short: shortsProxy.length,
      requests_delayed: proxyNew.filter((r) => r.delayMs > 0).length,
      output_streams_held: proxyNew.filter((r) => r.held).length,
      es_auto_reconnects: probe.reqs.filter((r) => r.kind === "es").reduce((a, r) => a + (r.reconnects ?? 0), 0),
      events_declared_start: ev.per.filter((p) => p.r.declaredStart !== undefined).length,
      peak_busy_sockets_proxy: peakIn(snap1.timeline, t0, t1),
      peak_sockets_lsof: socks.peak,
    };
    result.legs.forEach((l, i) => (result.det[`leg${i + 1}_events_bytes`] = l.eventsBytes));

    // --- Queueing: page request → the proxy saw it (or its cancel, or the window's end).
    const match = matchRequests(probe.reqs, snap1.requests, stack.appURL);
    const waits: number[] = [];
    const intervals: [number, number][] = [];
    const addWait = (route: string, start: number, until: number) => {
      const w = Math.max(0, until - start);
      waits.push(w);
      intervals.push([start, until]);
      const b = (result.queuedByRoute[route] ??= { n: 0, p50: 0, max: 0 });
      b.n++;
      b.max = Math.max(b.max, Math.round(w));
    };
    const perRoute = new Map<string, number[]>();
    for (const r of probe.reqs) {
      if (r.start < t0 || r.start > t1 || !r.url.startsWith(stack.appURL)) continue;
      const m = match.get(r.id);
      const until = m ? m.start : Math.min(r.failed ?? r.closed ?? t1, t1);
      const route = `${r.method} ${routeOf(new URL(r.url).pathname)}`;
      addWait(route, r.start, until);
      (perRoute.get(route) ?? perRoute.set(route, []).get(route)!).push(Math.max(0, until - r.start));
    }
    // Scripts, styles, the document: resource timing (they aren't fetches).
    for (const [name, initiator, start, reqStart] of series.res) {
      if (initiator === "fetch" || initiator === "xmlhttprequest" || initiator === "other" || !name.startsWith(stack.appURL)) continue;
      if (reqStart > 0) addWait(`GET ${routeOf(new URL(name).pathname)}`, start, reqStart);
    }
    for (const [k, xs] of perRoute) result.queuedByRoute[k].p50 = Math.round(pct(xs, 0.5));
    const longs = series.long.filter((x) => x[0] <= t1);
    result.timing = {
      settled_ms: result.legs.reduce((a, l) => a + l.settledMs, 0),
      marker_ms: result.legs.reduce((a, l) => a + l.markerMs, 0),
      main_task_ms: result.legs.reduce((a, l) => a + l.taskMs, 0),
      main_script_ms: result.legs.reduce((a, l) => a + l.scriptMs, 0),
      lag_max_ms: Math.max(0, ...result.legs.map((l) => l.lagMaxMs)),
      cdp_lag_max_ms: Math.round(cdpLagAll),
      byte_idle_fallbacks: watch.fallbacks.n,
      last_events_byte_ms: Math.max(0, ...ev.per.map((p) => (p.r.lastByte ?? 0) - t0)),
      window_ms: t1 - t0,
      all_sockets_busy_ms: Math.round(timeAtLeast(snap1.timeline, SOCKETS_PER_ORIGIN, t0, t1)),
      queued_total_ms: Math.round(waits.reduce((a, b) => a + b, 0)),
      queue_depth_max: maxDepth(intervals),
      queued_p50_ms: Math.round(pct(waits, 0.5)),
      queued_p95_ms: Math.round(pct(waits, 0.95)),
      queued_max_ms: Math.round(Math.max(0, ...waits)),
      queued_over_1s: waits.filter((w) => w > 1000).length,
      cancelled: probe.reqs.filter((r) => r.start >= t0 && r.failed !== undefined).length,
      unanswered_at_end: shortsProxy.filter((r) => r.end === undefined && r.delayMs === 0).length,
      longtask_total_ms: Math.round(longs.reduce((a, x) => a + x[1], 0)),
      router_cpu_ms: proc.router?.cpuMs ?? 0,
      data_cpu_ms: proc.data?.cpuMs ?? 0,
      children_cpu_ms: proc["router-children"]?.cpuMs ?? 0,
      router_peak_rss_mb: proc.router?.peakRssMB ?? 0,
      data_peak_rss_mb: proc.data?.peakRssMB ?? 0,
    };
    result.legs.forEach((l, i) => (result.timing[`leg${i + 1}_settled_ms`] = l.settledMs));
    result.socketOwners = [...socks.owners];
    result.requests = shortsProxy.map((r) => [
      `${r.method} ${routeOf(r.path)}`,
      Math.round(r.start - t0),
      null,
      r.end === undefined ? null : Math.round(r.end - r.start),
      r.status ?? null,
    ]);

    // The live-event smoke: something changed through the API shows within 2 s.
    if (scenario.smoke) result.checks.push(await scenario.smoke(ctx));
    result.ok = result.checks.every((c) => c.ok);
    if (!result.ok) result.error = `smoke failed: ${result.checks.filter((c) => !c.ok).map((c) => `${c.name}: ${c.detail}`).join("; ")}`;
  } catch (err) {
    result.error = err instanceof Error ? err.message : String(err);
    const pages = rig?.context.pages() ?? [];
    if (pages[0] && !plan.realism) {
      const shot = path.join(plan.runDir, "failures", `${tag}.png`);
      mkdirSync(path.dirname(shot), { recursive: true });
      await pages[0].screenshot({ path: shot, fullPage: true }).catch(() => {});
    }
  } finally {
    await rig?.teardown();
    active = undefined;
  }
  if (plan.realism && result.error) result.error = redact(result.error, manifest);
  if (fatal) throw fatal;
  return result;
}
