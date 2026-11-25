import type { CDPSession, Page } from "@playwright/test";
import type { CheckResult } from "./measure";
import type { ProxySnapshot } from "./proxyClient";
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
 * bottom and how much of the reply is on screen. The proxy stamps when each
 * text_delta frame went out to the page. The main-thread split (JS, style,
 * layout, paint, GC) comes from a CDP trace read after the fact, sliced by
 * the probe's own performance.mark()s, so nothing is timed by when CDP
 * delivers.
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

const env = (k: string, d: number) => Number(process.env[k] ?? d);
/** The unpaced reply: PERF_CHAT_CHARS characters as PERF_CHAT_DELTA-character deltas, PERF_CHAT_RATE a second. */
export const CHAT_CHARS = env("PERF_CHAT_CHARS", 15_000);
export const CHAT_RATE = env("PERF_CHAT_RATE", 60);
export const CHAT_DELTA = env("PERF_CHAT_DELTA", 25);
/**
 * The paced variants' reply: shorter, so the window (first delta → the
 * reveal's end) holds the whole reveal: at fast (1500 words a minute, about
 * 150 characters a second of this text) 3,000 characters reveal in ~20 s.
 */
export const PACED_CHARS = env("PERF_CHAT_PACED_CHARS", 3_000);
/**
 * 10cs, the fixed-work measure: every page loaded (10c's size) at a delta
 * rate the main thread keeps up with, so busy and ms per delta are costs,
 * not the ceiling a saturated 10c/10cp reports.
 */
export const SLOW_RATE = env("PERF_CHAT_SLOW_RATE", 15);
export const SLOW_CHARS = env("PERF_CHAT_SLOW_CHARS", 6_000);
/** The paced variants' stream_speed (Settings → Appearance): fast = 1500 words a minute. */
const PACED_SPEED = process.env.PERF_CHAT_PACE ?? "fast";
/** PERF_CHAT_TRACE=0: no CDP trace (no busy share or JS/layout/paint/GC split; the leg's CDP task time stays). */
const TRACE = process.env.PERF_CHAT_TRACE !== "0";
/** 10b's size: the mount's last page plus scroll-up pages to PERF_CHAT_MID_PAGES pages in all. */
const MID_PAGES = env("PERF_CHAT_MID_PAGES", 10);
/** Rows per transcript page (useKeyedConversation's PAGE_LIMIT). */
const PAGE_ROWS = 100;
/** useStickToBottom's STICK_THRESHOLD_PX: this close to the bottom is at the tail. */
const STICK_PX = 10;
/**
 * The stick check's allowance. While following, the reply's end leaves the
 * view only during a runway page turn's smooth glide, so never for longer
 * than useStickToBottom's MOVE_MS (1 s: a smooth move that hasn't landed by
 * then has failed). The share of frames it is out of view is reported, not
 * checked: it grows with frame time (a saturated 10c glides in fewer,
 * longer frames: 11% at main, each stretch under 250 ms).
 */
const END_HIDDEN_MAX_MS = env("PERF_CHAT_END_HIDDEN_MAX_MS", 1000);
/** A saturated cell: main thread busy over this share of the window. */
export const SATURATED_PCT = 90;
/**
 * PERF_CHAT_SAMPLE=min: the per-frame sample every 30th frame and no
 * mid-stream text checks (the final one stays): the probe's own overhead,
 * measured against the default run (PLAN.md, "Probe overhead").
 */
const SAMPLE_MIN = process.env.PERF_CHAT_SAMPLE === "min";
const SAMPLE_EVERY = SAMPLE_MIN ? 30 : 1;
/** The streamed text is compared to the wire every this many samples (and at the end). */
const CHECK_EVERY = SAMPLE_MIN ? 0 : env("PERF_CHAT_CHECK_EVERY", 10);
/**
 * PERF_CHAT_FAULT=drop|dup|swap: the text check's self-test. The probe's
 * reference copy of the wire (never the wire check's) drops, doubles, or
 * swaps with the next one the delta at PERF_CHAT_FAULT_AT (default 50), so
 * a correct render must fail the check.
 */
const FAULT = process.env.PERF_CHAT_FAULT ?? "";
const FAULT_AT = env("PERF_CHAT_FAULT_AT", 50);
const T = 60_000;

/** Letters and digits only: what the text comparison sees (Markdown syntax is all punctuation). */
export function alnum(s: string): string {
  return s.replace(/[^A-Za-z0-9]/g, "");
}

/**
 * Whether the reply as rendered (letters and digits) is the reference —
 * whole, or (whole false) a prefix of it, as a reply mid-stream is. A
 * message stamp read along with it at either end (formatMessageTime's
 * "22:59", "Sep 28 16:54", "10:59 PM") is allowed, so a change to the
 * stamp's markup that the text walk no longer skips doesn't fail it; any
 * dropped, doubled or reordered delta does. Injected into the page as is
 * (toString): self-contained.
 */
export function replyMatches(rendered: string, ref: string, whole: boolean): boolean {
  const stamp = /^(?:[A-Z][a-z]{2}[0-9]{1,2})?[0-9]{3,4}(?:AM|PM)?$/;
  const ok = (s: string) => (whole ? s === ref : ref.startsWith(s));
  if (ok(rendered)) return true;
  for (let n = 3; n <= 13 && n <= rendered.length; n++) {
    if (stamp.test(rendered.slice(0, n)) && ok(rendered.slice(n))) return true;
    if (stamp.test(rendered.slice(rendered.length - n)) && ok(rendered.slice(0, rendered.length - n))) return true;
  }
  return false;
}

/** The probe's reference copy of the wire, with PERF_CHAT_FAULT's fault applied (index i, text t). Injected as is. */
export function faultedAppend(ref: string, held: string, i: number, t: string, fault: string, at: number): [string, string] {
  if (i !== at && i !== at + 1) return [ref + held + t, ""];
  if (fault === "drop" && i === at) return [ref, ""];
  if (fault === "dup" && i === at) return [ref + t + t, ""];
  if (fault === "swap" && i === at) return [ref, t];
  if (fault === "swap" && i === at + 1) return [ref + t + held, ""];
  return [ref + held + t, ""];
}

/**
 * The in-page recorder, an init script beside probe.ts's. Records nothing
 * until armed (__perfChat.arm), so loading the conversation isn't counted.
 *
 * The streamed text check: the reply's letters and digits as rendered are
 * a prefix of what the wire has brought every CHECK_EVERY samples, and at
 * the end — once the whole reply is on screen (unpaced) or revealed
 * (paced), or a grace period after turn end — the whole of it. That end
 * snapshot is of the streamed render: the post-turn_end GET /transcript
 * (refreshLast, which merges the stored text over the live row) is held in
 * the page until it is taken.
 */
export const CHAT_PROBE = `(() => {
  const w = window;
  if (w.__perfChat) return;
  const now = () => performance.timeOrigin + performance.now();
  const SAMPLE_EVERY = ${SAMPLE_EVERY}, CHECK_EVERY = ${CHECK_EVERY}, FAULT = ${JSON.stringify(FAULT)}, FAULT_AT = ${FAULT_AT};
  const replyMatches = ${replyMatches.toString()};
  const faultedAppend = ${faultedAppend.toString()};
  const fresh = () => ({
    on: false, paced: false, graceMs: 15000, commits: [], deltas: [], key: "", wire: "", ref: "", refHeld: "", wireAlnum: 0,
    turnEnd: 0, firstMark: 0, frames: [], samples: [], nFrames: 0, el: null, scans: 0,
    checks: 0, bad: null, final: null, held: [], heldN: 0, refreshInWindow: 0,
    probeMs: 0, probeN: 0, probeIv: [], listenMs: 0,
  });
  const C = (w.__perfChat = Object.assign(fresh(), { commitsTotal: 0, long: [] }));
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
  // Long tasks of its own (not probe.ts's, which PERF_PROBE=0 leaves out).
  try {
    new PerformanceObserver((list) => {
      for (const e of list.getEntries()) C.long.push([performance.timeOrigin + e.startTime, e.duration]);
    }).observe({ type: "longtask", buffered: false });
  } catch {}
  const RE = /[^A-Za-z0-9]/g;
  const alnum = (s) => s.replace(RE, "");
  // The conversation stream's frames, as its EventSource gets them: one
  // listener of our own per kind, added ahead of the app's first.
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
    const s0 = performance.now();
    const t = performance.timeOrigin + s0;
    if (type === "turn_end") {
      if (C.key && !C.turnEnd) { C.turnEnd = t; performance.mark("perf-chat-turn-end"); }
      return;
    }
    let d;
    try { d = JSON.parse(ev.data); } catch { return; }
    if (typeof d.key !== "string" || !d.key.endsWith(":reply") || typeof d.text !== "string") return;
    if (!C.key) { C.key = d.key; C.firstMark = performance.timeOrigin + performance.mark("perf-chat-first-delta").startTime; }
    if (d.key !== C.key) return;
    const a = alnum(d.text);
    [C.ref, C.refHeld] = faultedAppend(C.ref, C.refHeld, C.deltas.length, a, FAULT, FAULT_AT);
    C.wire += d.text;
    C.wireAlnum += a.length;
    C.deltas.push([t, C.wireAlnum]);
    C.listenMs += performance.now() - s0;
  }
  // The post-turn_end refresh (GET …/transcript, no before=) is held until
  // the end snapshot; one during the window is only counted.
  const of = w.fetch;
  w.fetch = function (input) {
    const p = of.apply(this, arguments);
    if (!C.on || !C.key || C.final) return p;
    const url = typeof input === "string" ? input : input instanceof URL ? input.href : (input && input.url) || "";
    if (!/[/]transcript([?]|$)/.test(url) || /[?&]before=/.test(url)) return p;
    if (!C.turnEnd) { C.refreshInWindow++; return p; }
    C.heldN++;
    return p.then((res) => new Promise((resolve) => C.held.push(() => resolve(res))));
  };
  const release = () => { const hs = C.held; C.held = []; for (const f of hs) f(); };
  // The reply's bubble: its own [data-find] (a scan of every row only if that misses).
  const bubble = () => {
    if (!C.key) return null;
    if (C.el && C.el.isConnected) return C.el;
    C.el = document.querySelector('[data-find="' + CSS.escape(C.key) + '"]');
    if (!C.el) {
      C.scans++;
      const all = document.querySelectorAll("[data-chat-rows] [data-find]");
      for (let i = all.length - 1; i >= 0 && !C.el; i--) { const k = all[i].getAttribute("data-find"); if (k && k.endsWith(C.key)) C.el = all[i]; }
    }
    return C.el;
  };
  // Its text, minus what isn't the reply: buttons (a code block's Copy),
  // the hover stamp, anything find skips. Counted (letters and digits), or built.
  const SKIP = "button, time, [data-find-skip]";
  const walk = (el, build) => {
    const tw = document.createTreeWalker(el, NodeFilter.SHOW_ELEMENT | NodeFilter.SHOW_TEXT, {
      acceptNode: (n) => (n.nodeType === 1 ? (n.matches(SKIP) ? NodeFilter.FILTER_REJECT : NodeFilter.FILTER_SKIP) : NodeFilter.FILTER_ACCEPT),
    });
    let s = "", c = 0;
    for (let n = tw.nextNode(); n; n = tw.nextNode()) { if (build) s += n.nodeValue; else c += n.nodeValue.replace(RE, "").length; }
    return build ? alnum(s) : c;
  };
  C.renderedNow = () => { const b = bubble(); return b ? walk(b, true) : ""; };
  const snapshot = (complete) => {
    const text = C.renderedNow();
    C.final = { at: now(), text, complete, ok: replyMatches(text, C.ref + C.refHeld, true) };
    if (C.paced) performance.mark("perf-chat-reveal-end");
    release();
    document.documentElement.setAttribute("data-perf-chat-done", "");
  };
  C.finish = () => { if (!C.final && C.key) snapshot(false); release(); };
  const scroller = () => document.querySelector('[data-testid="conversation-stream"]');
  // After each frame (the next task). Its own cost is measured.
  const sample = () => {
    if (!C.on) return;
    const s0 = performance.now();
    const el = scroller();
    const b = bubble();
    const t = performance.timeOrigin + s0;
    const r = b ? walk(b, false) : 0;
    let visible = 0;
    if (el && b) { const a = el.getBoundingClientRect(), x = b.getBoundingClientRect(); visible = x.bottom <= a.bottom + 1 && x.bottom >= a.top ? 1 : 0; }
    C.samples.push([t, el ? Math.round(el.scrollHeight - el.scrollTop - el.clientHeight) : -1, el ? el.getAttribute("data-stick") || "" : "", r, visible]);
    if (b && CHECK_EVERY && C.samples.length % CHECK_EVERY === 0 && !C.final) {
      C.checks++;
      const text = walk(b, true);
      if (!C.bad && !replyMatches(text, C.ref + C.refHeld, false)) {
        let i = 0;
        const ref = C.ref + C.refHeld;
        while (i < text.length && i < ref.length && text[i] === ref[i]) i++;
        C.bad = { at: t, rendered: text.length, wire: ref.length, diff: i };
      }
    }
    if (C.turnEnd && !C.final && C.key) {
      if (r >= (C.ref + C.refHeld).length) snapshot(true);
      else if (t - C.turnEnd > C.graceMs) snapshot(false);
    }
    const s1 = performance.now();
    C.probeMs += s1 - s0;
    C.probeN++;
    C.probeIv.push([performance.timeOrigin + s0, performance.timeOrigin + s1]);
    try { performance.measure("perf-chat-sample", { start: s0, end: s1 }); } catch {}
  };
  const loop = (ts) => {
    if (!C.on) return;
    C.frames.push(performance.timeOrigin + ts);
    if (++C.nFrames % SAMPLE_EVERY === 0) setTimeout(sample, 0);
    requestAnimationFrame(loop);
  };
  C.arm = (paced, graceMs) => {
    Object.assign(C, fresh(), { on: true, paced, graceMs });
    document.documentElement.removeAttribute("data-perf-chat-done");
    requestAnimationFrame(loop);
  };
  C.disarm = () => { C.finish(); C.on = false; try { performance.clearMeasures("perf-chat-sample"); } catch {} };
})();`;

type Sample = [number, number, string, number, number];

interface ChatRecord {
  commits: number[];
  deltas: [number, number][];
  key: string;
  wire: string;
  wireAlnum: number;
  /** The probe's reference (the wire's letters and digits, PERF_CHAT_FAULT applied). */
  ref: string;
  turnEnd: number;
  /** Page time of the perf-chat-first-delta mark (the trace's start). */
  firstMark: number;
  frames: number[];
  samples: Sample[];
  /** Mid-stream text checks made, and the first that failed. */
  checks: number;
  bad: { at: number; rendered: number; wire: number; diff: number } | null;
  /** The end snapshot of the streamed render. */
  final: { at: number; text: string; complete: boolean; ok: boolean } | null;
  heldN: number;
  refreshInWindow: number;
  scans: number;
  probeMs: number;
  probeN: number;
  probeIv: [number, number][];
  listenMs: number;
  long: [number, number][];
  /** Page Date.now() − page time, to put proxy (Date.now()) stamps on the page's clock. */
  clockOffset: number;
}

async function readChat(page: Page): Promise<ChatRecord> {
  return page.evaluate(() => {
    type C = Omit<ChatRecord, "clockOffset"> & { refHeld: string };
    const C = (window as unknown as { __perfChat: C }).__perfChat;
    return {
      commits: C.commits,
      deltas: C.deltas,
      key: C.key,
      wire: C.wire,
      wireAlnum: C.wireAlnum,
      ref: C.ref + C.refHeld,
      turnEnd: C.turnEnd,
      firstMark: C.firstMark,
      frames: C.frames,
      samples: C.samples,
      checks: C.checks,
      bad: C.bad,
      final: C.final,
      heldN: C.heldN,
      refreshInWindow: C.refreshInWindow,
      scans: C.scans,
      probeMs: C.probeMs,
      probeN: C.probeN,
      probeIv: C.probeIv,
      listenMs: C.listenMs,
      long: C.long,
      clockOffset: Date.now() - (performance.timeOrigin + performance.now()),
    };
  });
}

export function quantile(xs: number[], q: number): number {
  if (xs.length === 0) return NaN;
  const s = [...xs].sort((a, b) => a - b);
  return s[Math.min(s.length - 1, Math.max(0, Math.ceil(q * s.length) - 1))];
}

/** For each delta (time, running count), how long until the first sample at or after it that renders that count. */
export function onScreenLatency(deltas: [number, number][], samples: Sample[], sent?: number[]): number[] {
  const out: number[] = [];
  let j = 0;
  deltas.forEach(([t, n], i) => {
    while (j < samples.length && (samples[j][0] < t || samples[j][3] < n)) j++;
    if (j < samples.length) out.push(samples[j][0] - (sent ? sent[i] : t));
  });
  return out;
}

/**
 * The window's numbers from the probe's record: [first delta, to] (to: turn
 * end unpaced, the reveal's end paced). `sent`: when the proxy sent each
 * delta, on the page's clock (one per delta), for wire → on screen.
 */
export function summarize(
  r: Pick<ChatRecord, "commits" | "deltas" | "turnEnd" | "frames" | "samples">,
  longtasks: [number, number][],
  opts: { to?: number; sent?: number[] } = {},
) {
  const from = r.deltas[0]?.[0] ?? NaN;
  const to = opts.to || r.turnEnd || (r.deltas.at(-1)?.[0] ?? NaN);
  const inW = (t: number) => t >= from && t <= to;
  const secs = Math.max(1e-3, (to - from) / 1000);
  const frames = r.frames.filter(inW);
  const gaps = frames.slice(1).map((t, i) => t - frames[i]);
  const commits = r.commits.filter(inW).length;
  const lt = longtasks.filter(([t]) => inW(t));
  const ws = r.samples.filter(([t]) => inW(t));
  // The reply's end in view: counted from the first sample that renders any of it.
  const firstShown = ws.findIndex((x) => x[3] > 0);
  const shown = firstShown < 0 ? [] : ws.slice(firstShown);
  const onScreen = onScreenLatency(r.deltas, r.samples);
  const sent = opts.sent && opts.sent.length === r.deltas.length ? onScreenLatency(r.deltas, r.samples, opts.sent) : [];
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
    samples: ws.length,
    offBottomPct: ws.length ? (100 * ws.filter(([, d]) => d > STICK_PX).length) / ws.length : NaN,
    offBottomMaxPx: Math.max(0, ...ws.map(([, d]) => d)),
    endHiddenPct: shown.length ? (100 * shown.filter((x) => x[4] !== 1).length) / shown.length : NaN,
    endHiddenMaxMs: longestHidden(shown),
    leftFollow: ws.filter(([, , m]) => m !== "" && m !== "follow").length,
    onScreenP50: quantile(onScreen, 0.5),
    onScreenP95: quantile(onScreen, 0.95),
    onScreenMax: Math.max(0, ...onScreen),
    wireOnScreenP50: quantile(sent, 0.5),
    wireOnScreenP95: quantile(sent, 0.95),
  };
}

/** The longest stretch (first hidden sample → the next shown one) with the reply's end out of view. */
function longestHidden(samples: Sample[]): number {
  let worst = 0;
  let since = NaN;
  for (const [t, , , , v] of samples) {
    if (v !== 1 && Number.isNaN(since)) since = t;
    if (v === 1 && !Number.isNaN(since)) {
      worst = Math.max(worst, t - since);
      since = NaN;
    }
  }
  if (!Number.isNaN(since) && samples.length) worst = Math.max(worst, samples[samples.length - 1][0] - since);
  return worst;
}

// --- The trace: main-thread time in the window, by kind.

export interface TraceEvent {
  name: string;
  ph: string;
  ts: number;
  dur?: number;
  pid: number;
  tid: number;
  cat?: string;
}

export type Bucket = "js" | "style" | "layout" | "paint" | "gc" | "other";
export const BUCKETS: Bucket[] = ["js", "style", "layout", "paint", "gc", "other"];

/** What one trace event's self time counts as (the spike's buckets, tools/analyze-trace.mjs). */
export function traceBucket(name: string): Bucket {
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
 * one the marks were made on. `probe`: the recorder's own sample() calls
 * (page-time intervals) and the start mark's page time, to report how much
 * of the window, and of which buckets, the probe itself took.
 */
export function analyzeTrace(events: TraceEvent[], startMark: string, endMark: string, probe?: { intervals: [number, number][]; startPage: number }) {
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
  // The probe's sample() calls on the trace's clock (disjoint, sorted).
  const pv = (probe?.intervals ?? [])
    .map(([a, b]) => [lo + (a - probe!.startPage) * 1000, lo + (b - probe!.startPage) * 1000] as [number, number])
    .filter(([a, b]) => b > lo && a < hi)
    .sort((a, b) => a[0] - b[0]);
  let k = 0;
  const overlap = (s: number, e: number) => {
    while (k < pv.length && pv[k][1] <= s) k++;
    let ov = 0;
    for (let j = k; j < pv.length && pv[j][0] < e; j++) ov += Math.max(0, Math.min(e, pv[j][1]) - Math.max(s, pv[j][0]));
    return ov;
  };
  const stack: (typeof clipped)[number][] = [];
  const ovAll = new Map<(typeof clipped)[number], number>();
  const ovKids = new Map<(typeof clipped)[number], number>();
  let busy = 0;
  for (const x of clipped) {
    while (stack.length && stack[stack.length - 1].e <= x.s) stack.pop();
    x.self = x.e - x.s;
    const parent = stack[stack.length - 1];
    if (parent) parent.self -= x.e - x.s;
    else busy += x.e - x.s;
    if (pv.length) {
      const ov = overlap(x.s, x.e);
      ovAll.set(x, ov);
      if (parent) ovKids.set(parent, (ovKids.get(parent) ?? 0) + ov);
    }
    stack.push(x);
  }
  const byBucket: Record<Bucket, number> = { js: 0, style: 0, layout: 0, paint: 0, gc: 0, other: 0 };
  for (const x of clipped) byBucket[traceBucket(x.name)] += Math.max(0, x.self) / 1000;
  // The probe's share: the part of each event's self time inside a sample() call.
  let probeMs: Record<Bucket, number> | undefined;
  if (probe) {
    probeMs = { js: 0, style: 0, layout: 0, paint: 0, gc: 0, other: 0 };
    for (const x of clipped) probeMs[traceBucket(x.name)] += Math.max(0, (ovAll.get(x) ?? 0) - (ovKids.get(x) ?? 0)) / 1000;
  }
  return { windowMs: (hi - lo) / 1000, busyMs: busy / 1000, byBucket, probeMs };
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

/** When the proxy sent each of the window's deltas, on the page's clock; undefined unless it saw exactly as many. */
export function proxyDeltaTimes(snap: ProxySnapshot, rec: Pick<ChatRecord, "deltas" | "turnEnd" | "clockOffset">): number[] | undefined {
  if (rec.deltas.length === 0) return undefined;
  const lo = rec.deltas[0][0] + rec.clockOffset - 2000;
  const hi = (rec.turnEnd || rec.deltas.at(-1)![0]) + rec.clockOffset + 100;
  const at = snap.requests
    .filter((r) => r.sse && /\/output$/.test(r.path))
    .flatMap((r) => r.deltaAt ?? [])
    .filter((t) => t >= lo && t <= hi)
    .sort((a, b) => a - b);
  // The last ones: anything earlier in the slack is the warm-up's.
  const mine = at.slice(-rec.deltas.length);
  if (mine.length !== rec.deltas.length) return undefined;
  return mine.map((t) => t - rec.clockOffset);
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
type Variant = { suffix: string; idTail: string; paced: boolean; chars: number; rate: number; delta: number };
const UNPACED: Variant = { suffix: "", idTail: "", paced: false, chars: CHAT_CHARS, rate: CHAT_RATE, delta: CHAT_DELTA };
const PACED: Variant = { suffix: "p", idTail: "-paced", paced: true, chars: PACED_CHARS, rate: CHAT_RATE, delta: CHAT_DELTA };
const SLOW: Variant = { suffix: "s", idTail: "-slow", paced: false, chars: SLOW_CHARS, rate: SLOW_RATE, delta: CHAT_DELTA };

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

type Row = { role: string; text?: string; key?: string; notice?: boolean; attribution?: unknown };
type Page_ = { rows?: Row[]; has_more?: boolean };

/** A row the chat view draws as the user's own bubble ([data-user-msg]). */
const isUserBubble = (r: Row) => r.role === "user" && !r.notice && !r.attribution;

/**
 * What a size should have loaded: the transcript paged exactly as the app
 * pages it (useKeyedConversation: the last 100 rows, then before=<the
 * oldest row's key>, 100 at a time, while has_more), through the Router, to
 * the size's page count or the end. Older pages, rows, user bubbles.
 */
export async function expectedLoad(get: (before?: string) => Promise<Page_>, pages: number | "all") {
  const max = pages === "all" ? Infinity : pages;
  let page = await get();
  const rows: Row[] = [...(page.rows ?? [])];
  let older = 0;
  while (page.has_more && older + 1 < max && page.rows?.length) {
    page = await get(page.rows[0].key);
    rows.unshift(...(page.rows ?? []));
    older++;
  }
  return { older, rows: rows.length, users: rows.filter(isUserBubble).length };
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
  for (const v of [UNPACED, PACED]) for (const size of SIZES) out.push(chatScenario(h, size, v));
  out.push(chatScenario(h, SIZES[2], SLOW));
  return out;
}

function chatScenario(h: ChatHelpers, size: Size, v: Variant): Scenario {
  const chat = (): ChatRef => {
    const c = (h.manifest() as Manifest & { chat?: ChatRef }).chat;
    if (!c) throw new Error("10x: the workload has no chat conversation (perfseed chat; run.sh seeds it for 10x)");
    return c;
  };
  const { paced } = v;
  const id = `10${size.id}${v.suffix}-chat-${size.pages === "all" ? "all" : `page${size.pages}`}${v.idTail}`;
  // One repetition's state between prepare, the leg and collect.
  let cdp: CDPSession | undefined;
  let tracing = false;
  let load: Record<string, number> = {};
  let loadChecks: CheckResult[] = [];
  const prompt = `perf-stream ${v.chars} ${v.rate} ${v.delta}`;
  // Paced: the reveal at ~150 characters a second, and then some.
  const graceMs = paced ? 30_000 + (v.chars / 150) * 2000 : 15_000;
  return {
    id,
    title:
      `Chat: stream a ${v.chars}-character reply at ${v.rate} deltas/s into a long conversation, ${size.label} loaded` +
      (paced ? `, paced (${PACED_SPEED})` : "") +
      (v === SLOW ? " (the fixed-work rate: under saturation)" : ""),
    delaysOffOnly: true,
    liveOutput: true,
    prepare: async (ctx) => {
      const c = chat();
      cdp = undefined;
      tracing = false;
      load = {};
      loadChecks = [];
      // The pace, set either way (a fresh stack's default is off, but say so).
      const cfg = await routerJSON<Record<string, unknown>>(ctx, "GET", "/config");
      await routerJSON(ctx, "PUT", "/config", { ...cfg, stream_speed: paced ? PACED_SPEED : "off" });
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
          // Nothing older asked for: the load check below fails if there should have been.
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
      const dom = await ctx.page.evaluate(() => {
        const s = document.querySelector('[data-testid="conversation-stream"]');
        return {
          all: document.getElementsByTagName("*").length,
          stream: s?.getElementsByTagName("*").length ?? 0,
          items: document.querySelectorAll('[data-testid="conversation-stream"] [data-chat-rows] > *').length,
          users: s?.querySelectorAll("[data-user-msg]").length ?? 0,
          times: s?.getElementsByTagName("time").length ?? 0,
        };
      });
      // What this size should have loaded: the whole transcript as the session serves it (the warm-up turn included).
      const want = await expectedLoad(
        (before) => routerJSON<Page_>(ctx, "GET", `/sessions/${c.id}/transcript?v=2&limit=${PAGE_ROWS}${before ? `&before=${encodeURIComponent(before)}` : ""}`),
        size.pages,
      );
      loadChecks = [
        {
          name: "the loaded size is the scenario's (older pages fetched; every loaded user message rendered)",
          ok: pages === want.older && dom.users === want.users,
          detail: `${pages} older pages fetched, want ${want.older}; ${dom.users} user messages rendered, want ${want.users} (of ${want.rows} rows)`,
        },
      ];
      const rows = want.rows;
      load = {
        chat_older_pages: pages,
        chat_return_retries: retries,
        chat_rows_loaded: rows,
        chat_items: dom.items,
        chat_user_msgs: dom.users,
        chat_time_elements: dom.times,
        chat_dom_nodes: dom.all,
        chat_stream_dom_nodes: dom.stream,
        chat_dom_nodes_per_row: Math.round((dom.stream / Math.max(1, rows)) * 10) / 10,
        chat_heap_used_mb: Math.round((heap.usedSize / 1e6) * 10) / 10,
      };
    },
    legs: [
      {
        name: "stream",
        before: async (ctx) => {
          await ctx.page.evaluate(([p, g]) => (window as unknown as { __perfChat: { arm(p: boolean, g: number): void } }).__perfChat.arm(p, g), [paced, graceMs] as [boolean, number]);
          if (TRACE && cdp) {
            await startTrace(cdp);
            tracing = true;
          }
          await ctx.page.getByPlaceholder(/Message the agent/).fill(prompt);
        },
        act: (ctx) => ctx.page.getByPlaceholder(/Message the agent/).press("Enter"),
        // The end snapshot is taken: the whole reply on screen (unpaced) or
        // revealed (paced), or the grace period after turn end ran out.
        marker: () => ({ kind: "exists", sel: "html[data-perf-chat-done]" }),
        markerCapMs: 60_000 + graceMs,
        // The marker's time is the page's own; polling it slowly keeps the
        // harness's page.evaluate calls out of the window's main thread.
        markerPollMs: 250,
      },
    ],
    collect: async (ctx) => {
      const page = ctx.page;
      let events: TraceEvent[] = [];
      if (tracing && cdp) events = await endTrace(cdp).catch(() => []);
      tracing = false;
      const checks: CheckResult[] = [...loadChecks];
      const rec = await readChat(page);
      const proxySnap = await ctx.stack.proxy.snapshot();
      const sent = proxyDeltaTimes(proxySnap, rec);
      // After the end snapshot the held refresh went through: the stored
      // reply merged over the live row must read the same. Up to 10 s.
      const wireAlnum = alnum(rec.wire);
      const mergedNow = () => page.evaluate(() => (window as unknown as { __perfChat: { renderedNow(): string } }).__perfChat.renderedNow());
      let merged = await mergedNow();
      for (const until = Date.now() + 10_000; !replyMatches(merged, wireAlnum, true) && Date.now() < until; ) {
        await page.waitForTimeout(200);
        merged = await mergedNow();
      }
      // Settled at the tail: the last pin may be a smooth glide (a runway page turn). Up to 5 s.
      const lastSample = () =>
        page.evaluate(() => {
          const C = (window as unknown as { __perfChat: { samples: Sample[] } }).__perfChat;
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
      const end = paced ? rec.final?.at : rec.turnEnd;
      const s = summarize(rec, rec.long, { to: end, sent });
      // The oracle: the reply as the agent stored it (GET /transcript, the session child's).
      const c = chat();
      const tr = await routerJSON<{ rows?: Row[] } | Row[]>(ctx, "GET", `/sessions/${c.id}/transcript?v=2&limit=5`);
      const rows = Array.isArray(tr) ? tr : (tr.rows ?? []);
      const stored = rows.filter((r) => r.role === "agent").at(-1)?.text ?? "";
      checks.push({
        name: "the stream carried the whole reply (wire = the agent's stored reply)",
        ok: rec.wire === stored && stored.length === v.chars,
        detail: `${rec.wire.length} chars on the wire in ${s.deltas} deltas, stored ${stored.length}, asked ${v.chars}${rec.wire === stored ? "" : " — differ"}`,
      });
      const fin = rec.final;
      checks.push({
        name: "the streamed reply rendered whole and in order (every checked frame a prefix of the wire; the last before the post-turn merge all of it)",
        ok: !!fin && fin.complete && fin.ok && !rec.bad && fin.text.length > 0,
        detail:
          `${rec.checks} frames checked mid-stream${rec.bad ? `, the first bad one ${Math.round(rec.bad.at - (rec.deltas[0]?.[0] ?? 0))} ms in (${rec.bad.rendered} rendered vs ${rec.bad.wire} received, first difference at ${rec.bad.diff})` : ", all prefixes"}; ` +
          (fin
            ? `end snapshot ${fin.complete ? "complete" : "TIMED OUT"} ${Math.round(fin.at - rec.turnEnd)} ms after turn end, ${fin.text.length} of ${rec.ref.length} letters and digits${fin.ok ? "" : `, first difference at ${firstDiff(fin.text, rec.ref)}`}`
            : "no end snapshot") +
          `; post-turn refresh held ${rec.heldN}×, refreshes inside the window ${rec.refreshInWindow}${FAULT ? `; FAULT INJECTED (${FAULT} at delta ${FAULT_AT})` : ""}`,
      });
      checks.push({
        name: "after the merge the reply still reads the same (stored render = wire)",
        ok: replyMatches(merged, wireAlnum, true) && merged.length > 0,
        detail: `${merged.length} of ${wireAlnum.length} letters and digits${replyMatches(merged, wireAlnum, true) ? "" : `, first difference at ${firstDiff(merged, wireAlnum)}`}`,
      });
      checks.push({
        name: `stick-to-bottom held (following throughout; the reply's end never out of view over ${END_HIDDEN_MAX_MS} ms at a time; at the tail after)`,
        ok: s.leftFollow === 0 && s.endHiddenMaxMs <= END_HIDDEN_MAX_MS && tail.mode === "follow" && tail.d >= 0 && tail.d <= STICK_PX && visible === 1,
        detail:
          `left follow in ${s.leftFollow} samples; reply end out of view in ${fmt(s.endHiddenPct)}% of ${s.samples} frames, at most ${Math.round(s.endHiddenMaxMs)} ms at a time; ` +
          `off the bottom ${fmt(s.offBottomPct)}% of frames, max ${s.offBottomMaxPx} px; after (${tailMs} ms): ${Math.round(tail.d)} px from the bottom, mode ${tail.mode || "?"}, reply end ${visible ? "in view" : "NOT in view"}`,
      });

      const tr2 = analyzeTrace(events, "perf-chat-first-delta", paced ? "perf-chat-reveal-end" : "perf-chat-turn-end", {
        intervals: rec.probeIv,
        startPage: rec.firstMark,
      });
      const winS = Math.max(1e-3, s.windowMs / 1000);
      const timing: Record<string, number> = {
        chat_window_ms: Math.round(s.windowMs),
        chat_deltas_per_s: round1(s.deltasPerSec),
        chat_commits_per_s: round1(s.commitsPerSec),
        chat_commits_per_delta: round2(s.commitsPerDelta),
        chat_frame_p50_ms: round1(s.frameP50),
        chat_frame_p95_ms: round1(s.frameP95),
        chat_frames_over_50ms: s.framesOver50,
        chat_longtasks: s.longtasks,
        chat_longtask_ms: Math.round(s.longtaskMs),
        chat_off_bottom_pct: round1(s.offBottomPct),
        chat_off_bottom_max_px: s.offBottomMaxPx,
        chat_end_hidden_pct: round1(s.endHiddenPct),
        chat_end_hidden_max_ms: Math.round(s.endHiddenMaxMs),
        chat_heap_used_mb: load.chat_heap_used_mb,
        // The recorder's own cost (in-page clock): sample() per frame, the delta listener.
        chat_probe_ms_per_s: round2((rec.probeMs + rec.listenMs) / winS),
        chat_probe_ms_per_sample: rec.probeN ? round2(rec.probeMs / rec.probeN) : NaN,
      };
      if (paced) {
        // The window ends at the reveal's end: delta → on screen is the reveal's pace, not a cost.
        timing.chat_reveal_after_turn_end_ms = fin && rec.turnEnd ? Math.round(fin.at - rec.turnEnd) : NaN;
      } else {
        timing.chat_on_screen_p50_ms = Math.round(s.onScreenP50);
        timing.chat_on_screen_p95_ms = Math.round(s.onScreenP95);
        timing.chat_wire_on_screen_p50_ms = Math.round(s.wireOnScreenP50);
        timing.chat_wire_on_screen_p95_ms = Math.round(s.wireOnScreenP95);
      }
      if (tr2) {
        const deltas = Math.max(1, s.deltas);
        const commits = Math.max(1, s.commits);
        const secs = Math.max(1e-3, tr2.windowMs / 1000);
        timing.chat_busy_pct = round1((100 * tr2.busyMs) / tr2.windowMs);
        timing.chat_busy_ms = Math.round(tr2.busyMs);
        timing.chat_ms_per_s = round1(tr2.busyMs / secs);
        timing.chat_ms_per_delta = round2(tr2.busyMs / deltas);
        timing.chat_ms_per_commit = round2(tr2.busyMs / commits);
        for (const k of BUCKETS) {
          const ms = tr2.byBucket[k];
          timing[`chat_${k}_ms_per_delta`] = round2(ms / deltas);
          timing[`chat_${k}_ms_per_s`] = round2(ms / secs);
          timing[`chat_${k}_ms_per_commit`] = round2(ms / commits);
        }
        if (tr2.probeMs) {
          const p = BUCKETS.reduce((a, k) => a + tr2.probeMs![k], 0);
          timing.chat_probe_trace_pct_of_busy = round2((100 * p) / Math.max(1e-3, tr2.busyMs));
          timing.chat_probe_layout_ms_per_s = round2((tr2.probeMs.layout + tr2.probeMs.style) / secs);
        }
      }
      const { chat_heap_used_mb: _heap, ...det } = load;
      return {
        det: { ...det, chat_deltas: s.deltas, chat_commits: s.commits, chat_proxy_deltas_matched: sent ? 1 : 0, chat_bubble_scans: rec.scans },
        timing,
        checks,
      };
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
const round2 = (x: number) => (Number.isFinite(x) ? Math.round(x * 100) / 100 : NaN);
const fmt = (x: number) => (Number.isFinite(x) ? x.toFixed(1) : "–");
