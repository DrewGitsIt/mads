import type { Page } from "@playwright/test";

/**
 * The in-page probe (round 3, F1–F4): everything settle needs from the page
 * is recorded inside it, in page time (performance.timeOrigin + now, the
 * same wall clock the harness and proxy use), so a reply that reaches the
 * harness late — behind a backlog of CDP events — still reports what was
 * true when. Installed with addInitScript before the app's first script.
 * Test-only; no product code is touched.
 *
 *  - requests: a fetch and an EventSource wrapper record each request the
 *    app makes (start, headers, failure, close). Matched against the
 *    proxy's log, a request the page started that the proxy hasn't seen is
 *    still queued in the browser for a socket.
 *  - resources: PerformanceObserver resource entries (startTime →
 *    requestStart), for the queueing of what isn't a fetch (scripts, css).
 *  - lag: every 50 ms, how late the timer fired or how long a
 *    MessageChannel round trip took, whichever is worse. lastBusy is the
 *    newest sample over 100 ms.
 *  - markers: the harness arms a named predicate before a click; a
 *    MutationObserver (and a 50 ms backstop) records the first page time
 *    it holds.
 *  - longtasks: PerformanceObserver longtask entries.
 */
export const PROBE_INIT = probeInit(process.env.PERF_PROBE !== "0");

/**
 * The probe for one build: PERF_PROBE=0 makes every side lite;
 * PERF_PROBE_CAND=0 only the candidate (BASE=X CAND=X measures the probe's
 * own overhead, interleaved ABBA like any comparison).
 */
export function probeFor(label: string): string {
  const full = process.env.PERF_PROBE !== "0" && !(label === "cand" && process.env.PERF_PROBE_CAND === "0");
  return full ? PROBE_INIT : probeInit(false);
}

/**
 * The probe's source. full=false (PERF_PROBE=0) keeps only the markers —
 * no request wrappers, observers or lag probe — to measure their overhead;
 * settle then skips the page-side inputs it can't have.
 */
export function probeInit(full: boolean): string {
  return `(() => {
  const FULL = ${full};
  const w = window;
  const now = () => performance.timeOrigin + performance.now();
  const P = (w.__perf = { lite: !FULL, reqs: [], res: [], lag: [], lastBusy: 0, lastSample: 0, markers: {}, armed: {}, long: [] });
  let id = 0;
  const abs = (u) => { try { return new URL(u, location.href).href; } catch { return String(u); } };

  if (FULL) {
  const of = w.fetch.bind(w);
  w.fetch = function (input, init) {
    const url = typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
    const method = String((init && init.method) || (input && input.method) || "GET").toUpperCase();
    const r = { id: ++id, kind: "fetch", method, url: abs(url), start: now() };
    P.reqs.push(r);
    return of(input, init).then(
      (res) => { r.headers = now(); r.status = res.status; return res; },
      (err) => { r.failed = now(); throw err; },
    );
  };

  const OES = w.EventSource;
  if (OES) {
    class PerfEventSource extends OES {
      constructor(url, cfg) {
        super(url, cfg);
        const r = { id: ++id, kind: "es", method: "GET", url: abs(url), start: now() };
        P.reqs.push(r);
        // A native auto-reconnect (readyState back to CONNECTING after an
        // error, then open again) is counted: /output and pipeline streams
        // do it on a drop; the bus closes and reopens its own instead.
        this.addEventListener("open", () => {
          if (r.open) r.reconnects = (r.reconnects || 0) + 1;
          else r.open = now();
        });
        this.addEventListener("error", () => { if (this.readyState === 2) r.closed = r.closed || now(); });
        this.__perfReq = r;
      }
      close() { if (this.__perfReq) this.__perfReq.closed = this.__perfReq.closed || now(); super.close(); }
    }
    w.EventSource = PerfEventSource;
  }

  try {
    new PerformanceObserver((list) => {
      for (const e of list.getEntries()) {
        P.res.push([e.name, e.initiatorType, performance.timeOrigin + e.startTime, e.requestStart ? performance.timeOrigin + e.requestStart : 0]);
      }
    }).observe({ type: "resource", buffered: true });
  } catch {}
  try {
    new PerformanceObserver((list) => {
      for (const e of list.getEntries()) P.long.push([performance.timeOrigin + e.startTime, e.duration]);
    }).observe({ type: "longtask", buffered: true });
  } catch {}

  let prev = performance.now();
  setInterval(() => {
    const t = performance.now();
    const drift = Math.max(0, t - prev - 50);
    prev = t;
    const ch = new MessageChannel();
    const sent = performance.now();
    ch.port1.onmessage = () => {
      const lag = Math.round(Math.max(drift, performance.now() - sent));
      const at = performance.timeOrigin + t;
      P.lag.push([at, lag]);
      if (P.lag.length > 40000) P.lag.splice(0, 20000);
      P.lastSample = at;
      if (lag > 100) P.lastBusy = at;
      ch.port1.close();
    };
    ch.port2.postMessage(0);
  }, 50);
  }

  const visible = (el) => !!el && (el.checkVisibility ? el.checkVisibility() : el.offsetParent !== null);
  const textIs = (sel, text) => [...document.querySelectorAll(sel)].some((e) => e.textContent.trim() === text && visible(e));
  const rail = (spec) => {
    const root = document.querySelector('[aria-label="Session list"]');
    if (!root) return false;
    if (spec.ids) return spec.ids.every((i) => root.querySelector('[data-rail-card][data-rail-id="' + i + '"] [role=status]'));
    return root.querySelectorAll("[data-rail-card]").length >= spec.n && root.querySelectorAll("[data-rail-card] [role=status]").length >= spec.n;
  };
  const preds = {
    rail,
    session: (s) =>
      [...document.querySelectorAll('[title="Rename session"]')].some((e) => e.textContent.trim() === s.name && visible(e)) &&
      visible(document.querySelector('#workspace-panel-conversation [data-testid="transcript-missing"], #workspace-panel-conversation [data-testid="conversation-stream"]')),
    delegate: () => visible(document.querySelector('[data-testid="delegate-stream"] [data-testid="activity-row"]')),
    retro: (s) => textIs("button", "Generate retro") && textIs("label, label span", s.name),
    launchpad: (s) => visible(document.querySelector('[aria-label="Describe your goal"]')) && rail(s.rail),
    esAfter: (s) => P.reqs.some((r) => r.kind === "es" && r.start >= s.after && new URL(r.url).pathname === "/events"),
    cardText: (s) => { const c = document.querySelector('[data-rail-card][data-rail-id="' + s.id + '"]'); return !!c && c.textContent.includes(s.text); },
    text: (s) => [...document.querySelectorAll(s.sel)].some((e) => visible(e) && s.any.some((t) => e.textContent.trim() === t)),
    exists: (s) => (s.n ? [...document.querySelectorAll(s.sel)].filter(visible).length >= s.n : visible(document.querySelector(s.sel))),
    esOpenAfter: (s) => P.reqs.some((r) => r.kind === "es" && r.open >= s.after && new URL(r.url).pathname === "/events"),
  };
  // Checked at most once per frame while anything is armed, and a marker
  // is disarmed once it holds.
  const check = () => {
    for (const [name, spec] of Object.entries(P.armed)) {
      try {
        if (preds[spec.kind](spec)) {
          P.markers[name] = now();
          delete P.armed[name];
        }
      } catch {}
    }
  };
  let queued = false;
  const schedule = () => {
    if (queued) return;
    for (const _ in P.armed) {
      queued = true;
      requestAnimationFrame(() => { queued = false; check(); });
      return;
    }
  };
  P.arm = (name, spec) => { delete P.markers[name]; P.armed[name] = spec; check(); };
  // A sync spec is checked in the observer callback itself, the microtask
  // after the DOM change: an element React mounts and removes within a few
  // milliseconds (a later task) is still caught, which a frame isn't.
  const onMutate = () => {
    for (const k in P.armed) { if (P.armed[k].sync) { check(); break; } }
    schedule();
  };
  const mo = () => new MutationObserver(onMutate).observe(document, { subtree: true, childList: true, attributes: true, characterData: true });
  if (document.documentElement) mo(); else document.addEventListener("DOMContentLoaded", mo);
  // Backstop for a marker that holds with no mutation (a request-only one).
  setInterval(() => { for (const _ in P.armed) { check(); return; } }, 100);
})();`;
}

export type MarkerSpec =
  | { kind: "rail"; n?: number; ids?: string[] }
  | { kind: "session"; name: string }
  | { kind: "delegate" }
  | { kind: "retro"; name: string }
  | { kind: "launchpad"; rail: { n?: number; ids?: string[] } }
  | { kind: "esAfter"; after: number }
  | { kind: "cardText"; id: string; text: string }
  | { kind: "text"; sel: string; any: string[] }
  | { kind: "esOpenAfter"; after: number }
  /** n: at least n visible matches. sync: checked on every DOM mutation, not once a frame (a short-lived element). */
  | { kind: "exists"; sel: string; n?: number; sync?: boolean };

export interface PageReq {
  id: number;
  kind: "fetch" | "es";
  method: string;
  url: string;
  start: number;
  headers?: number;
  status?: number;
  failed?: number;
  open?: number;
  closed?: number;
  /** Native EventSource auto-reconnects (open again after an error). */
  reconnects?: number;
}

export interface ProbeRead {
  /** PERF_PROBE=0: markers only. */
  lite: boolean;
  /** Page time when this read was taken (vs the harness's receive time: the CDP lag, F4). */
  pageNow: number;
  reqs: PageReq[];
  lastBusy: number;
  lastSample: number;
  markers: Record<string, number>;
}

/** One read of the probe. `reqsSince` limits the request list. */
export async function readProbe(page: Page, reqsSince = 0): Promise<ProbeRead> {
  return page.evaluate((since) => {
    const P = (window as unknown as { __perf: { lite: boolean; reqs: PageReq[]; lastBusy: number; lastSample: number; markers: Record<string, number> } }).__perf;
    return {
      lite: P.lite,
      pageNow: performance.timeOrigin + performance.now(),
      reqs: P.reqs.filter((r) => r.start >= since || (r.closed === undefined && r.failed === undefined)),
      lastBusy: P.lastBusy,
      lastSample: P.lastSample,
      markers: { ...P.markers },
    };
  }, reqsSince);
}

export async function arm(page: Page, name: string, spec: MarkerSpec): Promise<void> {
  await page.evaluate(([n, s]) => (window as unknown as { __perf: { arm(n: string, s: unknown): void } }).__perf.arm(n as string, s), [name, spec] as const);
}

/** Lag samples, long tasks and resource entries after `since` (page time). */
export async function probeSeries(page: Page, since: number) {
  return page.evaluate((s) => {
    const P = (window as unknown as { __perf: { lag: [number, number][]; long: [number, number][]; res: [string, string, number, number][] } }).__perf;
    return { lag: P.lag.filter((x) => x[0] >= s), long: P.long.filter((x) => x[0] >= s), res: P.res.filter((x) => x[2] >= s) };
  }, since);
}
