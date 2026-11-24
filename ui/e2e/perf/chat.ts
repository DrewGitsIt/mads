import type { CDPSession, Page } from "@playwright/test";
import type { CheckResult } from "./measure";
import type { Manifest, Scenario, ScenarioCtx } from "./scenarios";

/**
 * Scenario 10x: a live reply streaming into a long conversation
 * (docs/CHAT-STREAM-PER-BATCH-COST.md). The conversation (perfseed chat: a
 * real claude-code transcript or a synthetic one of the same shape, served
 * by the fake provider) is loaded into the chat view through its own paging
 * — the mount's last page, scroll-up pages, or Ctrl+↑↑'s load-all — then the
 * fake streams a long Markdown reply at a set delta rate ("perf-stream"),
 * paced (a reading pace on: useReveal commits every frame) or not.
 *
 * Timing comes from the page (CHAT_PROBE, page time): every /output
 * text_delta and turn_end the app's EventSource gets, every React commit
 * (the DevTools global hook, which React calls in production builds too),
 * every frame, and after each frame the scroller's distance from its
 * bottom and how much of the reply is on screen. The main-thread split
 * (JS, style, layout, paint, GC) comes from a CDP trace read after the
 * fact, sliced by the probe's own performance.mark()s, so nothing is timed
 * by when CDP delivers.
 */

/** perfseed chat's manifest section (shape only). */
export interface ChatRef {
  id: string;
  name: string;
  ref: string;
  source: "real" | "synthetic";
  turns: number;
  rows: number;
  by_role: Record<string, number>;
  text_bytes: number;
}

/** The live reply: PERF_CHAT_CHARS characters as PERF_CHAT_DELTA-character deltas, PERF_CHAT_RATE a second. */
export const CHAT_CHARS = Number(process.env.PERF_CHAT_CHARS ?? 15_000);
export const CHAT_RATE = Number(process.env.PERF_CHAT_RATE ?? 60);
export const CHAT_DELTA = Number(process.env.PERF_CHAT_DELTA ?? 25);
/** The paced variants' stream_speed (Settings → Appearance): fast = 1500 words a minute. */
const PACED_SPEED = process.env.PERF_CHAT_PACE ?? "fast";
/** PERF_CHAT_TRACE=0: no CDP trace (no busy share or JS/layout/paint/GC split; the leg's CDP task time stays). */
const TRACE = process.env.PERF_CHAT_TRACE !== "0";
/** The mid size: the mount's page and this many more. */
const MID_PAGES = Number(process.env.PERF_CHAT_MID_PAGES ?? 10);
/** Rows per transcript page (useKeyedConversation's PAGE_LIMIT). */
const PAGE_ROWS = 100;
/** useStickToBottom's STICK_THRESHOLD_PX: this close to the bottom is at the tail. */
const STICK_PX = 10;
const T = 60_000;

/**
 * The in-page recorder, an init script beside probe.ts's. Records nothing
 * until armed (__perfChat.arm), so loading the conversation isn't counted.
 */
export const CHAT_PROBE = `(() => {
  const w = window;
  if (w.__perfChat) return;
  const now = () => performance.timeOrigin + performance.now();
  const C = (w.__perfChat = { on: false, paced: false, commits: [], commitsTotal: 0, deltas: [], key: "", wire: "", wireAlnum: 0, turnEnd: 0, frames: [], samples: [], done: false });
  const commit = () => { C.commitsTotal++; if (C.on) C.commits.push(now()); };
  // React reports every commit to the DevTools hook, production builds included.
  const h = w.__REACT_DEVTOOLS_GLOBAL_HOOK__;
  if (h) {
    const o = h.onCommitFiberRoot;
    h.onCommitFiberRoot = function () { commit(); return o && o.apply(this, arguments); };
  } else {
    let n = 0;
    w.__REACT_DEVTOOLS_GLOBAL_HOOK__ = {
      supportsFiber: true,
      renderers: new Map(),
      inject(r) { this.renderers.set(++n, r); return n; },
      onCommitFiberRoot: commit,
      onCommitFiberUnmount() {},
      onPostCommitFiberRoot() {},
      onScheduleFiberRoot() {},
      checkDCE() {},
    };
  }
  const alnum = (s) => s.replace(/[^A-Za-z0-9]/g, "");
  // The conversation stream's frames, as its EventSource gets them: one
  // listener of our own per kind, added beside the app's first.
  const ES = w.EventSource;
  if (ES) {
    const add = EventTarget.prototype.addEventListener;
    const own = ES.prototype.addEventListener;
    ES.prototype.addEventListener = function (type, fn, opt) {
      if ((type === "text_delta" || type === "turn_end") && String(this.url).includes("/output")) {
        const kinds = this.__perfChatKinds || (this.__perfChatKinds = new Set());
        if (!kinds.has(type)) { kinds.add(type); add.call(this, type, (ev) => frame(type, ev)); }
      }
      return own.call(this, type, fn, opt);
    };
  }
  function frame(type, ev) {
    if (!C.on) return;
    const t = now();
    if (type === "turn_end") {
      if (C.key && !C.turnEnd) { C.turnEnd = t; performance.mark("perf-chat-turn-end"); }
      return;
    }
    let d;
    try { d = JSON.parse(ev.data); } catch { return; }
    if (typeof d.key !== "string" || !d.key.endsWith(":reply") || typeof d.text !== "string") return;
    if (!C.key) { C.key = d.key; performance.mark("perf-chat-first-delta"); }
    if (d.key !== C.key) return;
    C.wire += d.text;
    C.wireAlnum += alnum(d.text).length;
    C.deltas.push([t, C.wireAlnum]);
  }
  // The reply as rendered: its text without the hover timestamp and code blocks' Copy buttons.
  const bubble = () => {
    if (!C.key) return null;
    const all = document.querySelectorAll('[data-chat-rows] [data-find]');
    for (let i = all.length - 1; i >= 0; i--) { const k = all[i].getAttribute("data-find"); if (k === C.key || k.endsWith(C.key)) return all[i]; }
    return null;
  };
  const rendered = (el) => {
    if (!el) return "";
    const tw = document.createTreeWalker(el, NodeFilter.SHOW_TEXT, { acceptNode: (n) => (n.parentElement && n.parentElement.closest("button, time") ? NodeFilter.FILTER_REJECT : NodeFilter.FILTER_ACCEPT) });
    let s = "";
    for (let n = tw.nextNode(); n; n = tw.nextNode()) s += n.nodeValue;
    return alnum(s);
  };
  C.rendered = () => rendered(bubble());
  const scroller = () => document.querySelector('[data-testid="conversation-stream"]');
  // After each frame (the next task: layout is clean, so reading it forces nothing).
  const sample = () => {
    const el = scroller();
    const b = bubble();
    const t = now();
    const r = b ? rendered(b).length : 0;
    let visible = 0;
    if (el && b) { const a = el.getBoundingClientRect(), x = b.getBoundingClientRect(); visible = x.bottom <= a.bottom + 1 && x.bottom >= a.top ? 1 : 0; }
    C.samples.push([t, el ? Math.round(el.scrollHeight - el.scrollTop - el.clientHeight) : -1, el ? el.getAttribute("data-stick") || "" : "", r, visible]);
    if (C.turnEnd && !C.done && (C.paced || r >= C.wireAlnum)) {
      C.done = true;
      document.documentElement.setAttribute("data-perf-chat-done", "");
    }
  };
  const loop = (ts) => {
    if (!C.on) return;
    C.frames.push(performance.timeOrigin + ts);
    setTimeout(sample, 0);
    requestAnimationFrame(loop);
  };
  C.arm = (paced) => {
    Object.assign(C, { on: true, paced, commits: [], deltas: [], key: "", wire: "", wireAlnum: 0, turnEnd: 0, frames: [], samples: [], done: false });
    document.documentElement.removeAttribute("data-perf-chat-done");
    requestAnimationFrame(loop);
  };
  C.disarm = () => { C.on = false; };
})();`;

interface ChatRecord {
  commits: number[];
  deltas: [number, number][];
  key: string;
  wire: string;
  wireAlnum: number;
  turnEnd: number;
  frames: number[];
  samples: [number, number, string, number, number][];
  rendered: string;
}

async function readChat(page: Page): Promise<ChatRecord> {
  return page.evaluate(() => {
    const C = (window as unknown as { __perfChat: ChatRecord & { rendered(): string } }).__perfChat;
    return { commits: C.commits, deltas: C.deltas, key: C.key, wire: C.wire, wireAlnum: C.wireAlnum, turnEnd: C.turnEnd, frames: C.frames, samples: C.samples, rendered: C.rendered() };
  });
}

function quantile(xs: number[], q: number): number {
  if (xs.length === 0) return NaN;
  const s = [...xs].sort((a, b) => a - b);
  return s[Math.min(s.length - 1, Math.max(0, Math.ceil(q * s.length) - 1))];
}

/** The window's numbers from the probe's record: [first delta, turn end]. */
export function summarize(r: Pick<ChatRecord, "commits" | "deltas" | "turnEnd" | "frames" | "samples">, longtasks: [number, number][]) {
  const from = r.deltas[0]?.[0] ?? NaN;
  const to = r.turnEnd || (r.deltas.at(-1)?.[0] ?? NaN);
  const inW = (t: number) => t >= from && t <= to;
  const secs = Math.max(1e-3, (to - from) / 1000);
  const frames = r.frames.filter(inW);
  const gaps = frames.slice(1).map((t, i) => t - frames[i]);
  const commits = r.commits.filter(inW).length;
  const lt = longtasks.filter(([t]) => inW(t));
  const ws = r.samples.filter(([t]) => inW(t));
  // A delta is on screen at the first sample after it that renders at least its running count.
  const onScreen: number[] = [];
  let j = 0;
  for (const [t, n] of r.deltas) {
    while (j < r.samples.length && (r.samples[j][0] < t || r.samples[j][3] < n)) j++;
    if (j < r.samples.length) onScreen.push(r.samples[j][0] - t);
  }
  return {
    windowMs: to - from,
    deltas: r.deltas.length,
    deltasPerSec: r.deltas.length / secs,
    commits,
    commitsPerSec: commits / secs,
    commitsPerDelta: commits / Math.max(1, r.deltas.length),
    frames: frames.length,
    frameP50: quantile(gaps, 0.5),
    frameP95: quantile(gaps, 0.95),
    framesOver50: gaps.filter((g) => g > 50).length,
    longtasks: lt.length,
    longtaskMs: lt.reduce((a, [, d]) => a + d, 0),
    offBottomPct: ws.length ? (100 * ws.filter(([, d]) => d > STICK_PX).length) / ws.length : NaN,
    offBottomMaxPx: Math.max(0, ...ws.map(([, d]) => d)),
    leftFollow: ws.filter(([, , m]) => m !== "" && m !== "follow").length,
    onScreenP50: quantile(onScreen, 0.5),
    onScreenP95: quantile(onScreen, 0.95),
    onScreenMax: Math.max(0, ...onScreen),
  };
}

// --- The trace: main-thread time in the window, by kind.

interface TraceEvent {
  name: string;
  ph: string;
  ts: number;
  dur?: number;
  pid: number;
  tid: number;
  cat?: string;
}

/** What one trace event's self time counts as (the spike's buckets, tools/analyze-trace.mjs). */
export function traceBucket(name: string): "js" | "style" | "layout" | "paint" | "gc" | "other" {
  if (/GC|Scavenge|MarkCompact|BlinkGC|V8\.GC/.test(name)) return "gc";
  if (/^(UpdateLayoutTree|RecalculateStyles|ParseAuthorStyleSheet|StyleRecalc|InvalidateLayout|ScheduleStyleRecalculation)/.test(name)) return "style";
  if (/^Layout/.test(name) || name === "UpdateLayerTree" || name === "LayoutShift") return "layout";
  if (/^(PrePaint|Paint|PaintImage|Layerize|UpdateLayer|CompositeLayers|Commit|PaintSetup|Rasterize|RasterTask|ScrollLayer)/.test(name) || /Paint|Composit|Layeriz|LayerTreeHost/.test(name)) return "paint";
  if (/^(FunctionCall|EvaluateScript|RunMicrotasks|TimerFire|EventDispatch|FireAnimationFrame|HandlePostMessage|ParseHTML|ProfileChunk)/.test(name) || /^v8/i.test(name)) return "js";
  return "other";
}

/**
 * Main-thread time between the probe's two marks: busy (top-level tasks,
 * clipped to the window) and self time by bucket. The main thread is the
 * one the marks were made on.
 */
export function analyzeTrace(events: TraceEvent[], startMark: string, endMark: string) {
  const start = events.find((e) => e.name === startMark && e.ph !== "M");
  const end = events.find((e) => e.name === endMark && e.ph !== "M");
  if (!start || !end || end.ts <= start.ts) return undefined;
  const [pid, tid] = [start.pid, start.tid];
  const lo = start.ts;
  const hi = end.ts;
  const iv: { name: string; s: number; e: number; self: number }[] = [];
  const open: TraceEvent[] = [];
  for (const e of events.filter((x) => x.pid === pid && x.tid === tid).sort((a, b) => a.ts - b.ts)) {
    if (e.ph === "X") iv.push({ name: e.name, s: e.ts, e: e.ts + (e.dur ?? 0), self: 0 });
    else if (e.ph === "B") open.push(e);
    else if (e.ph === "E") {
      const b = open.pop();
      if (b) iv.push({ name: b.name, s: b.ts, e: e.ts, self: 0 });
    }
  }
  const clipped = iv
    .map((x) => ({ ...x, s: Math.max(x.s, lo), e: Math.min(x.e, hi) }))
    .filter((x) => x.e > x.s)
    .sort((a, b) => a.s - b.s || b.e - a.e);
  const stack: typeof clipped = [];
  let busy = 0;
  for (const x of clipped) {
    while (stack.length && stack[stack.length - 1].e <= x.s) stack.pop();
    x.self = x.e - x.s;
    if (stack.length) stack[stack.length - 1].self -= x.e - x.s;
    else busy += x.e - x.s;
    stack.push(x);
  }
  const byBucket: Record<string, number> = { js: 0, style: 0, layout: 0, paint: 0, gc: 0, other: 0 };
  for (const x of clipped) byBucket[traceBucket(x.name)] += Math.max(0, x.self) / 1000;
  return { windowMs: (hi - lo) / 1000, busyMs: busy / 1000, byBucket };
}

async function startTrace(cdp: CDPSession): Promise<void> {
  await cdp.send("Tracing.start", {
    transferMode: "ReturnAsStream",
    traceConfig: {
      recordMode: "recordAsMuchAsPossible",
      includedCategories: ["toplevel", "devtools.timeline", "disabled-by-default-devtools.timeline", "v8.execute", "blink.user_timing", "disabled-by-default-v8.gc"],
    },
  } as never);
}

async function endTrace(cdp: CDPSession): Promise<TraceEvent[]> {
  const done = new Promise<{ stream?: string }>((r) => cdp.once("Tracing.tracingComplete", r as never));
  await cdp.send("Tracing.end");
  const { stream } = await done;
  if (!stream) return [];
  const parts: string[] = [];
  for (;;) {
    const r = (await cdp.send("IO.read", { handle: stream, size: 1 << 20 })) as { data: string; eof: boolean; base64Encoded?: boolean };
    parts.push(r.base64Encoded ? Buffer.from(r.data, "base64").toString() : r.data);
    if (r.eof) break;
  }
  await cdp.send("IO.close", { handle: stream });
  const raw = JSON.parse(parts.join("")) as TraceEvent[] | { traceEvents: TraceEvent[] };
  return Array.isArray(raw) ? raw : raw.traceEvents;
}

// --- The scenarios.

export interface ChatHelpers {
  manifest(): Manifest;
  load(ctx: ScenarioCtx): Promise<void>;
  openSession(ctx: ScenarioCtx, id: string, name?: string): Promise<void>;
  until(what: string, f: () => Promise<boolean>, timeoutMs?: number): Promise<void>;
  say(ctx: ScenarioCtx, id: string, text: string): Promise<void>;
  agentIdle(ctx: ScenarioCtx, id: string, timeoutMs?: number): Promise<void>;
  smoke(ctx: ScenarioCtx): Promise<CheckResult>;
}

type Size = { id: string; label: string; pages: number | "all" };
const SIZES: Size[] = [
  { id: "a", label: "first page", pages: 1 },
  { id: "b", label: `${MID_PAGES} pages`, pages: MID_PAGES },
  { id: "c", label: "every page", pages: "all" },
];

const auth = (ctx: ScenarioCtx) => ({ Authorization: `Bearer ${ctx.stack.token}` });

async function routerJSON<T>(ctx: ScenarioCtx, method: string, p: string, body?: unknown): Promise<T> {
  const res = await fetch(`${ctx.stack.routerURL}${p}`, {
    method,
    headers: { ...auth(ctx), ...(body ? { "Content-Type": "application/json" } : {}) },
    body: body ? JSON.stringify(body) : undefined,
  });
  const text = await res.text();
  if (!res.ok) throw new Error(`${method} ${p}: ${res.status} ${text.slice(0, 200)}`);
  return (text ? JSON.parse(text) : undefined) as T;
}

const stream = (p: Page) => p.locator('[data-testid="conversation-stream"]');
const items = (p: Page) => p.locator('[data-testid="conversation-stream"] [data-chat-rows] > *');

/** Distance from the scroller's bottom and its stick mode. */
const atTail = (p: Page) =>
  p.evaluate(() => {
    const el = document.querySelector('[data-testid="conversation-stream"]');
    return el ? { d: el.scrollHeight - el.scrollTop - el.clientHeight, mode: el.getAttribute("data-stick") ?? "" } : { d: -1, mode: "" };
  });

async function pressTwice(page: Page, key: string): Promise<void> {
  await page.keyboard.press(key);
  await page.waitForTimeout(80);
  await page.keyboard.press(key);
}

export function chatScenarios(h: ChatHelpers): Scenario[] {
  const out: Scenario[] = [];
  for (const paced of [false, true]) {
    for (const size of SIZES) {
      out.push(chatScenario(h, size, paced));
    }
  }
  return out;
}

function chatScenario(h: ChatHelpers, size: Size, paced: boolean): Scenario {
  const chat = (): ChatRef => {
    const c = (h.manifest() as Manifest & { chat?: ChatRef }).chat;
    if (!c) throw new Error("10x: the workload has no chat conversation (perfseed chat; run.sh seeds it for 10x)");
    return c;
  };
  const id = `10${size.id}${paced ? "p" : ""}-chat-${size.pages === "all" ? "all" : `page${size.pages}`}${paced ? "-paced" : ""}`;
  // One repetition's state between prepare, the leg and collect.
  let cdp: CDPSession | undefined;
  let tracing = false;
  let load: Record<string, number> = {};
  const prompt = `perf-stream ${CHAT_CHARS} ${CHAT_RATE} ${CHAT_DELTA}`;
  return {
    id,
    title: `Chat: stream a ${CHAT_CHARS}-character reply at ${CHAT_RATE} deltas/s into a long conversation, ${size.label} loaded${paced ? `, paced (${PACED_SPEED})` : ""}`,
    delaysOffOnly: true,
    liveOutput: true,
    prepare: async (ctx) => {
      const c = chat();
      cdp = undefined;
      tracing = false;
      load = {};
      if (paced) {
        const cfg = await routerJSON<Record<string, unknown>>(ctx, "GET", "/config");
        await routerJSON(ctx, "PUT", "/config", { ...cfg, stream_speed: PACED_SPEED });
      }
      await ctx.page.context().addInitScript(CHAT_PROBE);
      // Wake the session's child (the fake, resuming the seeded history)
      // before the page opens it: its start isn't part of what's measured.
      await h.say(ctx, c.id, "echo perf warm-up");
      await h.agentIdle(ctx, c.id);
      await h.load(ctx);
      await ctx.quiesce();
      await h.openSession(ctx, c.id, c.name);
      await h.until("the conversation's first page", async () => (await items(ctx.page).count()) > 0, T);
      await ctx.quiesce();
      const snap0 = await ctx.stack.proxy.snapshot();
      if (size.pages === "all") {
        // Ctrl+↑↑: every older page, merged as one (useKeyedConversation.loadAllOlder).
        await stream(ctx.page).hover();
        await pressTwice(ctx.page, "Control+ArrowUp");
        await h.until("every page loaded and the view at the first message", async () => {
          if (await ctx.page.getByTestId("loading-all").count()) return false;
          return ctx.page.evaluate(() => (document.querySelector('[data-testid="conversation-stream"]')?.scrollTop ?? 1) === 0);
        }, 120_000);
      } else {
        // Scroll-up paging: the scroller near its top asks for the page before the oldest row.
        // Each page counted at the proxy (GET …/transcript?before=…), not by
        // rendered items (a page can fold into an existing tool group).
        const olderAsked = async () => {
          const sn = await ctx.stack.proxy.snapshot();
          return sn.requests.filter((r) => /\/transcript$/.test(r.path) && /before=/.test(r.query)).length;
        };
        for (let i = 1; i < size.pages; i++) {
          const before = await olderAsked();
          let asked = false;
          // A scroll near the top asks; if the scroller already sat at 0 no scroll event fires, so nudge it first.
          for (let tries = 0; tries < 3 && !asked; tries++) {
            await stream(ctx.page).evaluate((el) => {
              if (el.scrollTop === 0) el.scrollTo({ top: 800 });
            });
            await ctx.page.waitForTimeout(50);
            await stream(ctx.page).evaluate((el) => el.scrollTo({ top: 0 }));
            const deadline = Date.now() + 5_000;
            while (!(asked = (await olderAsked()) > before) && Date.now() < deadline) await ctx.page.waitForTimeout(50);
          }
          await ctx.quiesce();
          // Nothing older (a short transcript): the paging stops there.
          if (!asked) break;
        }
      }
      await ctx.quiesce();
      // Back to the tail, following (Ctrl+↓↓), as a reader returns to the
      // live reply — and again, as a reader would, if it didn't get there in
      // 10 s (counted: chat_return_retries).
      let retries = -1;
      let last = { d: -1, mode: "" };
      for (let tries = 0; tries < 4; tries++) {
        retries++;
        await stream(ctx.page).hover();
        await pressTwice(ctx.page, "Control+ArrowDown");
        const deadline = Date.now() + 10_000;
        while (Date.now() < deadline) {
          last = await atTail(ctx.page);
          if (last.d >= 0 && last.d <= STICK_PX && last.mode === "follow") break;
          await ctx.page.waitForTimeout(100);
        }
        if (last.d >= 0 && last.d <= STICK_PX && last.mode === "follow") break;
      }
      if (!(last.d >= 0 && last.d <= STICK_PX && last.mode === "follow")) {
        throw new Error(`Ctrl+↓↓ never brought the view back to the tail, following (${retries + 1} tries; ${Math.round(last.d)} px from the bottom, mode ${last.mode || "?"})`);
      }
      await ctx.quiesce();
      const snap1 = await ctx.stack.proxy.snapshot();
      const seen = new Set(snap0.requests.map((r) => r.id));
      const pages = snap1.requests.filter((r) => !seen.has(r.id) && /\/transcript$/.test(r.path) && /before=/.test(r.query)).length;
      cdp = await ctx.page.context().newCDPSession(ctx.page);
      // The memory question: what the loaded conversation holds, after a full GC.
      await cdp.send("HeapProfiler.collectGarbage");
      const heap = (await cdp.send("Runtime.getHeapUsage")) as { usedSize: number; totalSize: number };
      const dom = await ctx.page.evaluate(() => ({
        all: document.getElementsByTagName("*").length,
        stream: document.querySelector('[data-testid="conversation-stream"]')?.getElementsByTagName("*").length ?? 0,
        items: document.querySelectorAll('[data-testid="conversation-stream"] [data-chat-rows] > *').length,
      }));
      const c2 = chat();
      load = {
        chat_older_pages: pages,
        chat_return_retries: retries,
        chat_rows_loaded: Math.min(c2.rows + 2, (pages + 1) * PAGE_ROWS),
        chat_items: dom.items,
        chat_dom_nodes: dom.all,
        chat_stream_dom_nodes: dom.stream,
        chat_heap_used_mb: Math.round((heap.usedSize / 1e6) * 10) / 10,
      };
    },
    legs: [
      {
        name: "stream",
        before: async (ctx) => {
          await ctx.page.evaluate((p) => (window as unknown as { __perfChat: { arm(p: boolean): void } }).__perfChat.arm(p), paced);
          if (TRACE && cdp) {
            await startTrace(cdp);
            tracing = true;
          }
          await ctx.page.getByPlaceholder(/Message the agent/).fill(prompt);
        },
        act: (ctx) => ctx.page.getByPlaceholder(/Message the agent/).press("Enter"),
        // Unpaced: the turn has ended and the whole reply is on screen. Paced: the turn has ended (the reveal plays on).
        marker: () => ({ kind: "exists", sel: "html[data-perf-chat-done]" }),
        markerCapMs: 120_000,
      },
    ],
    collect: async (ctx) => {
      const page = ctx.page;
      let events: TraceEvent[] = [];
      if (tracing && cdp) events = await endTrace(cdp).catch(() => []);
      tracing = false;
      const checks: CheckResult[] = [];
      // Paced: Show all, then the whole reply must be there too.
      if (paced) {
        const chip = page.getByRole("button", { name: "Show all", exact: true });
        if (await chip.count()) await chip.click().catch(() => {});
      }
      let rec = await readChat(page);
      const deadline = Date.now() + 30_000;
      while (rec.rendered.length < rec.wireAlnum && Date.now() < deadline) {
        await page.waitForTimeout(200);
        rec = await readChat(page);
      }
      // Settled at the tail: the last pin may be a smooth glide (a runway page turn). Up to 5 s.
      const lastSample = () =>
        page.evaluate(() => {
          const C = (window as unknown as { __perfChat: { samples: [number, number, string, number, number][] } }).__perfChat;
          return C.samples.at(-1)?.[4] ?? 0;
        });
      const tailFrom = Date.now();
      let tail = await atTail(page);
      let visible = await lastSample();
      while (!(tail.d >= 0 && tail.d <= STICK_PX && visible === 1) && Date.now() - tailFrom < 5000) {
        await page.waitForTimeout(100);
        tail = await atTail(page);
        visible = await lastSample();
      }
      const tailMs = Date.now() - tailFrom;
      await page.evaluate(() => (window as unknown as { __perfChat: { disarm(): void } }).__perfChat.disarm());
      const longs = await page.evaluate(() => (window as unknown as { __perf: { long: [number, number][] } }).__perf.long ?? []);
      const s = summarize(rec, longs);
      // The oracle: the reply as the agent stored it (GET /transcript, the session child's).
      const c = chat();
      const tr = await routerJSON<{ rows?: { role: string; text?: string; key?: string }[] } | { role: string; text?: string; key?: string }[]>(
        ctx,
        "GET",
        `/sessions/${c.id}/transcript?v=2&limit=5`,
      );
      const rows = Array.isArray(tr) ? tr : (tr.rows ?? []);
      const stored = rows.filter((r) => r.role === "agent").at(-1)?.text ?? "";
      const alnum = (x: string) => x.replace(/[^A-Za-z0-9]/g, "");
      checks.push({
        name: "the stream carried the whole reply (wire = the agent's stored reply)",
        ok: rec.wire === stored && stored.length === CHAT_CHARS,
        detail: `${rec.wire.length} chars on the wire in ${s.deltas} deltas, stored ${stored.length}, asked ${CHAT_CHARS}${rec.wire === stored ? "" : " — differ"}`,
      });
      checks.push({
        name: "the whole reply is on screen, in order (rendered text = wire, Markdown aside)",
        ok: rec.rendered === alnum(rec.wire) && rec.rendered.length > 0,
        detail: `${rec.rendered.length} of ${alnum(rec.wire).length} letters and digits rendered${rec.rendered === alnum(rec.wire) ? "" : `, first difference at ${firstDiff(rec.rendered, alnum(rec.wire))}`}`,
      });
      checks.push({
        name: "stick-to-bottom held (following throughout, at the tail after, reply's end in view)",
        ok: s.leftFollow === 0 && tail.mode === "follow" && tail.d >= 0 && tail.d <= STICK_PX && visible === 1,
        detail: `left follow in ${s.leftFollow} samples; after (${tailMs} ms): ${Math.round(tail.d)} px from the bottom, mode ${tail.mode || "?"}, reply end ${visible ? "in view" : "NOT in view"}; off the bottom ${fmt(s.offBottomPct)}% of frames, max ${s.offBottomMaxPx} px`,
      });

      const tr2 = analyzeTrace(events, "perf-chat-first-delta", "perf-chat-turn-end");
      const timing: Record<string, number> = {
        chat_window_ms: Math.round(s.windowMs),
        chat_deltas_per_s: round1(s.deltasPerSec),
        chat_commits_per_s: round1(s.commitsPerSec),
        chat_commits_per_delta: Math.round(s.commitsPerDelta * 100) / 100,
        chat_frame_p50_ms: round1(s.frameP50),
        chat_frame_p95_ms: round1(s.frameP95),
        chat_frames_over_50ms: s.framesOver50,
        chat_longtasks: s.longtasks,
        chat_longtask_ms: Math.round(s.longtaskMs),
        chat_on_screen_p50_ms: Math.round(s.onScreenP50),
        chat_on_screen_p95_ms: Math.round(s.onScreenP95),
        chat_off_bottom_pct: round1(s.offBottomPct),
        chat_off_bottom_max_px: s.offBottomMaxPx,
        chat_heap_used_mb: load.chat_heap_used_mb,
      };
      if (tr2) {
        const commits = Math.max(1, s.commits);
        timing.chat_busy_pct = round1((100 * tr2.busyMs) / tr2.windowMs);
        timing.chat_ms_per_commit = round1(tr2.busyMs / commits);
        timing.chat_ms_per_delta = round1(tr2.busyMs / Math.max(1, s.deltas));
        for (const [k, v] of Object.entries(tr2.byBucket)) timing[`chat_${k}_ms_per_commit`] = Math.round((v / commits) * 100) / 100;
      }
      const { chat_heap_used_mb: _heap, ...det } = load;
      return { det: { ...det, chat_deltas: s.deltas, chat_commits: s.commits }, timing, checks };
    },
    smoke: h.smoke,
  };
}

function firstDiff(a: string, b: string): number {
  let i = 0;
  while (i < a.length && i < b.length && a[i] === b[i]) i++;
  return i;
}
const round1 = (x: number) => (Number.isFinite(x) ? Math.round(x * 10) / 10 : NaN);
const fmt = (x: number) => (Number.isFinite(x) ? x.toFixed(1) : "–");
