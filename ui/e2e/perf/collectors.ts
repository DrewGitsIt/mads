import { execFile } from "node:child_process";
import type { CDPSession, Page } from "@playwright/test";

/**
 * Collectors for one measured window (docs/PERF-HARNESS-PLAN.md §3). The
 * network ones read the Chrome DevTools Protocol; the process ones sample
 * `ps` and `lsof` for the stack's own pids and the proxy's port only.
 */

// ---------------------------------------------------------------------------
// Network, via CDP.

interface CdpTiming {
  requestTime: number;
  sendStart: number;
  receiveHeadersEnd: number;
}

export interface NetRequest {
  id: string;
  url: string;
  path: string;
  method: string;
  type: string;
  /** Wall-clock ms (Date.now() scale) of requestWillBeSent. */
  start: number;
  startMono: number;
  /** When the request left the queue for a socket (wall ms), from ResourceTiming. */
  sent?: number;
  headers?: number;
  end?: number;
  status?: number;
  connectionId?: number;
  failed?: string;
  query: string;
  /** Decoded body bytes inside the current window (= wire body bytes: nothing is compressed). */
  windowDecoded: number;
  windowSseMessages: number;
  /** Queueing + stalled time: requestWillBeSent → sendStart. */
  blockedMs?: number;
}

/** A request whose life is the stream itself, not a round trip. */
export function isLongLived(r: Pick<NetRequest, "type" | "path">): boolean {
  return r.type === "EventSource" || /^\/sessions\/[^/]+\/output$/.test(r.path);
}

/** Path with ids folded, so requests group by route. */
export function routeOf(path: string): string {
  return path
    .replace(/\/ses_[0-9a-f]+/g, "/:session")
    .replace(/\/a[0-9a-f]{16,}/g, "/:harness")
    .replace(/\/(dlg|lnk|wsp|tsk|job|ret)_[0-9a-f]+/g, "/:$1")
    .replace(/\/\d+(?=\/|$)/g, "/:n")
    .replace(/\/assets\/.*$/, "/assets/*");
}

export class NetCollector {
  readonly requests = new Map<string, NetRequest>();
  /** Every URL the page asked for on any other origin (the live-port check reads these). */
  readonly foreign: string[] = [];
  private monoToWall?: number;
  private windowStart = 0;
  private cdp?: CDPSession;
  /** Wall ms of the newest byte on any long-lived stream, and of the newest short request start. */
  lastStreamByte = 0;
  /** Wall ms of the newest byte on an /events stream. */
  lastEventsByte = 0;
  lastShortStart = 0;
  constructor(
    private readonly origin: string,
    private readonly trackSseMessages: boolean,
  ) {}

  async attach(page: Page): Promise<void> {
    const cdp = await page.context().newCDPSession(page);
    this.cdp = cdp;
    // Keep Chromium from buffering response bodies for getResponseBody: a
    // 100+ MB replay would otherwise sit in the inspector's buffer.
    await cdp.send("Network.enable", { maxTotalBufferSize: 4 * 1024 * 1024, maxResourceBufferSize: 256 * 1024 });
    await cdp.send("Performance.enable", { timeDomain: "timeTicks" }).catch(() => {});
    cdp.on("Network.requestWillBeSent", (e) => {
      // A new document (a reload, Electron's included): the old page's
      // requests are abandoned and may never report an end. Close them here
      // or they read as in flight forever.
      if (e.type === "Document") {
        const at = e.wallTime * 1000;
        for (const r of this.requests.values()) {
          if (r.end === undefined && r.type !== "Document") {
            r.end = at;
            r.failed = "navigated";
          }
        }
      }
      if (!e.request.url.startsWith(this.origin)) {
        if (/^(https?|wss?):/.test(e.request.url)) this.foreign.push(e.request.url);
        return;
      }
      const wall = e.wallTime * 1000;
      this.monoToWall ??= wall - e.timestamp * 1000;
      const u = new URL(e.request.url);
      const r: NetRequest = {
        id: e.requestId,
        url: e.request.url,
        path: u.pathname,
        query: u.search,
        method: e.request.method,
        type: e.type ?? "Other",
        start: wall,
        startMono: e.timestamp,
        windowDecoded: 0,
        windowSseMessages: 0,
      };
      this.requests.set(e.requestId, r);
      if (!isLongLived(r)) this.lastShortStart = Math.max(this.lastShortStart, wall);
    });
    cdp.on("Network.responseReceived", (e) => {
      const r = this.requests.get(e.requestId);
      if (!r) return;
      r.status = e.response.status;
      r.connectionId = e.response.connectionId;
      const t = e.response.timing as CdpTiming | undefined;
      if (t) {
        r.blockedMs = Math.max(0, (t.requestTime - r.startMono) * 1000 + Math.max(t.sendStart, 0));
        r.sent = this.wall(t.requestTime) + Math.max(t.sendStart, 0);
        r.headers = this.wall(t.requestTime) + Math.max(t.receiveHeadersEnd, 0);
      }
    });
    cdp.on("Network.dataReceived", (e) => {
      const r = this.requests.get(e.requestId);
      if (!r) return;
      const at = this.wall(e.timestamp);
      if (isLongLived(r)) this.lastStreamByte = Math.max(this.lastStreamByte, at);
      if (r.type === "EventSource" && r.path === "/events") this.lastEventsByte = Math.max(this.lastEventsByte, at);
      if (at < this.windowStart) return;
      r.windowDecoded += e.dataLength;
    });
    if (this.trackSseMessages) {
      cdp.on("Network.eventSourceMessageReceived", (e) => {
        const r = this.requests.get(e.requestId);
        if (r && this.wall(e.timestamp) >= this.windowStart) r.windowSseMessages++;
      });
    }
    cdp.on("Network.loadingFinished", (e) => {
      const r = this.requests.get(e.requestId);
      if (r) r.end = this.wall(e.timestamp);
    });
    cdp.on("Network.loadingFailed", (e) => {
      const r = this.requests.get(e.requestId);
      if (!r) return;
      r.end = this.wall(e.timestamp);
      r.failed = e.canceled ? "canceled" : e.errorText;
    });
  }

  private wall(mono: number): number {
    return (this.monoToWall ?? Date.now() - mono * 1000) + mono * 1000;
  }

  /** Starts a measured window now: byte counters only count from here. */
  startWindow(at = Date.now()): void {
    this.windowStart = at;
    for (const r of this.requests.values()) {
      r.windowDecoded = 0;
      r.windowSseMessages = 0;
    }
  }

  /** Short (non-stream) requests still waiting for their end; `skip` drops ones held on purpose. */
  shortInFlight(skip?: (r: NetRequest) => boolean): NetRequest[] {
    return [...this.requests.values()].filter((r) => !isLongLived(r) && r.end === undefined && !skip?.(r));
  }

  /** Short requests that started in [from, to] and have neither finished nor failed. */
  shortOpenBetween(from: number, to: number): NetRequest[] {
    return [...this.requests.values()].filter((r) => !isLongLived(r) && r.start >= from && r.start <= to && r.end === undefined);
  }

  /** CDP Performance metrics, ms: main-thread task and script time so far. */
  async mainThread(): Promise<{ taskMs: number; scriptMs: number }> {
    const r = await this.cdp!.send("Performance.getMetrics").catch(() => ({ metrics: [] as { name: string; value: number }[] }));
    const get = (n: string) => (r.metrics.find((m) => m.name === n)?.value ?? 0) * 1000;
    return { taskMs: get("TaskDuration"), scriptMs: get("ScriptDuration") };
  }

  /**
   * Marks requests CDP still holds as in flight when they can't be: started
   * over `ageMs` ago, the proxy never saw them or saw them finish, and the
   * proxy has had a free socket all along (so they aren't queued for one).
   * Returns how many it marked.
   */
  dropStale(proxyInFlight: (r: NetRequest) => boolean, socketsFree: boolean, ageMs = 5000): number {
    if (!socketsFree) return 0;
    let n = 0;
    const now = Date.now();
    for (const r of this.requests.values()) {
      if (r.end !== undefined || isLongLived(r) || now - r.start < ageMs || proxyInFlight(r)) continue;
      r.end = now;
      r.failed = "stale";
      n++;
    }
    return n;
  }

  async detach(): Promise<void> {
    await this.cdp?.detach().catch(() => {});
  }

  /** Every request that started in, or carried bytes into, [from, to]. */
  window(from: number, to: number): NetRequest[] {
    return [...this.requests.values()].filter(
      (r) => (r.start >= from && r.start <= to) || r.windowDecoded > 0,
    );
  }

  /**
   * Peak number of distinct connections carrying a request at once in
   * [from, to], by the CDP connectionId of each request from the moment it
   * left the queue (sendStart) until it ended. A request with no response
   * yet has no connectionId and is not counted (see the proxy's socket
   * count for those).
   */
  peakConnections(from: number, to: number): number {
    const edges: [number, number, number][] = []; // time, +1/-1, connection
    for (const r of this.requests.values()) {
      if (r.connectionId === undefined || r.sent === undefined) continue;
      const s = Math.max(r.sent, from);
      const e = Math.min(r.end ?? to, to);
      if (e < from || s > to || e < s) continue;
      edges.push([s, 1, r.connectionId], [e, -1, r.connectionId]);
    }
    edges.sort((a, b) => a[0] - b[0] || b[1] - a[1]);
    const live = new Map<number, number>();
    let peak = 0;
    for (const [, d, c] of edges) {
      const n = (live.get(c) ?? 0) + d;
      if (n <= 0) live.delete(c);
      else live.set(c, n);
      peak = Math.max(peak, live.size);
    }
    return peak;
  }
}

// ---------------------------------------------------------------------------
// Main-thread responsiveness, from inside the page.

/**
 * A lag probe: every 50 ms, how late the timer fired and how long a
 * MessageChannel round trip took — whichever is worse. A busy main thread
 * shows as lag even when no single task is "long". Samples are
 * [wall ms, lag ms].
 */
export const LAG_PROBE_INIT = `(() => {
  const w = window;
  w.__perfLag = [];
  let prev = performance.now();
  setInterval(() => {
    const now = performance.now();
    const drift = Math.max(0, now - prev - 50);
    prev = now;
    const ch = new MessageChannel();
    const t = performance.now();
    ch.port1.onmessage = () => {
      const rtt = performance.now() - t;
      w.__perfLag.push([performance.timeOrigin + now, Math.round(Math.max(drift, rtt))]);
      if (w.__perfLag.length > 40000) w.__perfLag.splice(0, 20000);
      ch.port1.close();
    };
    ch.port2.postMessage(0);
  }, 50);
})();`;

/** Lag samples taken after `since` (wall ms). */
export async function lagSince(page: Page, since: number): Promise<[number, number][]> {
  return page.evaluate((s) => ((window as unknown as { __perfLag?: [number, number][] }).__perfLag ?? []).filter((x) => x[0] > s), since);
}

// Long tasks, from inside the page.

/** Installed with page.addInitScript before the app's first script runs. */
export const LONG_TASK_INIT = `(() => {
  const w = window;
  w.__perfLongTasks = [];
  try {
    new PerformanceObserver((list) => {
      for (const e of list.getEntries()) w.__perfLongTasks.push([e.startTime + performance.timeOrigin, e.duration]);
    }).observe({ type: "longtask", buffered: true });
  } catch (e) { w.__perfLongTasksError = String(e); }
})();`;

/** Total long-task time and count whose start falls in [from, to] (wall ms). */
export async function longTasks(page: Page, from: number, to: number): Promise<{ totalMs: number; count: number; maxMs: number }> {
  const all = await page.evaluate(
    () => (window as unknown as { __perfLongTasks?: [number, number][] }).__perfLongTasks ?? [],
  );
  let totalMs = 0;
  let count = 0;
  let maxMs = 0;
  for (const [start, dur] of all) {
    if (start < from || start > to) continue;
    totalMs += dur;
    count++;
    maxMs = Math.max(maxMs, dur);
  }
  return { totalMs: Math.round(totalMs), count, maxMs: Math.round(maxMs) };
}

// ---------------------------------------------------------------------------
// Processes: ps for the stack's own pids, lsof for the proxy's port.

function run(cmd: string, args: string[]): Promise<string> {
  return new Promise((resolve) => {
    execFile(cmd, args, { timeout: 5000 }, (_err, stdout) => resolve(stdout ?? ""));
  });
}

/** "[[dd-]hh:]mm:ss[.ss]" → seconds. */
export function parsePsTime(s: string): number {
  let days = 0;
  let rest = s.trim();
  const dash = rest.indexOf("-");
  if (dash > 0) {
    days = Number(rest.slice(0, dash));
    rest = rest.slice(dash + 1);
  }
  const parts = rest.split(":").map(Number);
  let secs = 0;
  for (const p of parts) secs = secs * 60 + p;
  return days * 86400 + secs;
}

export interface ProcStats {
  cpuMs: number;
  peakRssMB: number;
}

/** Samples cumulative CPU time and RSS of pids every intervalMs. */
export class ProcSampler {
  private timer?: ReturnType<typeof setInterval>;
  private first = new Map<number, number>();
  private last = new Map<number, number>();
  private peak = new Map<number, number>();
  private busy = false;
  /** Children of these pids (the Router's session/pipeline processes) are sampled too, under `<name>-children`. */
  constructor(
    private readonly pids: Record<string, number>,
    private readonly parents: string[] = [],
    private readonly onChild: (pid: number) => void = () => {},
  ) {}

  private readonly childGroups = new Map<number, string>();

  private async sample(): Promise<void> {
    if (this.busy) return;
    this.busy = true;
    for (const name of this.parents) {
      const out = await run("pgrep", ["-P", String(this.pids[name])]);
      for (const c of out.split("\n").filter(Boolean).map(Number)) {
        if (!this.childGroups.has(c)) {
          this.childGroups.set(c, `${name}-children`);
          this.onChild(c);
        }
      }
    }
    const ids = [...Object.values(this.pids), ...this.childGroups.keys()].join(",");
    const out = await run("ps", ["-o", "pid=,rss=,time=", "-p", ids]);
    for (const line of out.trim().split("\n")) {
      const [pid, rss, time] = line.trim().split(/\s+/);
      if (!pid) continue;
      const p = Number(pid);
      const cpu = parsePsTime(time);
      if (!this.first.has(p)) this.first.set(p, cpu);
      this.last.set(p, cpu);
      this.peak.set(p, Math.max(this.peak.get(p) ?? 0, Number(rss) / 1024));
    }
    this.busy = false;
  }

  async start(intervalMs = 250): Promise<void> {
    await this.sample();
    this.timer = setInterval(() => void this.sample(), intervalMs);
  }

  async stop(): Promise<Record<string, ProcStats>> {
    if (this.timer) clearInterval(this.timer);
    while (this.busy) await new Promise((r) => setTimeout(r, 20));
    await this.sample();
    const out: Record<string, ProcStats> = {};
    const add = (name: string, pid: number) => {
      const o = (out[name] ??= { cpuMs: 0, peakRssMB: 0 });
      o.cpuMs += Math.round(((this.last.get(pid) ?? 0) - (this.first.get(pid) ?? 0)) * 1000);
      o.peakRssMB = Math.max(o.peakRssMB, Math.round((this.peak.get(pid) ?? 0) * 10) / 10);
    };
    for (const [name, pid] of Object.entries(this.pids)) add(name, pid);
    for (const [pid, name] of this.childGroups) add(name, pid);
    return out;
  }
}

/**
 * Samples the test browser's TCP connections with lsof: how many go to the
 * proxy's port at once (its sockets to the page's origin), and any that go
 * anywhere else (there should be none: background networking is off). The
 * browser is its pid and every descendant (Chromium's network service is a
 * child process).
 */
export class SocketSampler {
  private timer?: ReturnType<typeof setInterval>;
  private busy = false;
  peak = 0;
  samples = 0;
  readonly owners = new Set<string>();
  readonly foreign = new Set<string>();
  constructor(
    private readonly port: number,
    private readonly browserPid: number,
  ) {}

  private async tree(): Promise<Set<number>> {
    const all = new Set<number>([this.browserPid]);
    let frontier = [this.browserPid];
    while (frontier.length) {
      const out = await run("pgrep", ["-P", frontier.join(",")]);
      frontier = out.split("\n").filter(Boolean).map(Number).filter((p) => !all.has(p));
      for (const p of frontier) all.add(p);
    }
    return all;
  }

  private async sample(): Promise<void> {
    if (this.busy) return;
    this.busy = true;
    const pids = await this.tree();
    const out = await run("lsof", ["-nP", "-a", "-iTCP", "-p", [...pids].join(","), "-Fpcnt"]);
    let pid = 0;
    let cmd = "";
    let n = 0;
    for (const line of out.split("\n")) {
      if (line.startsWith("p")) pid = Number(line.slice(1));
      else if (line.startsWith("c")) cmd = line.slice(1);
      else if (line.startsWith("n") && line.includes("->")) {
        if (line.endsWith(`->127.0.0.1:${this.port}`)) {
          n++;
          this.owners.add(`${cmd}(${pid})`);
        } else {
          // Loopback pairs inside the browser (Electron's own sockets) are
          // internal — unless the far end is a live mads port.
          const remote = line.slice(line.indexOf("->") + 2);
          const loopback = /^(127\.0\.0\.1|\[::1\]):/.test(remote);
          if (!loopback || /:(17373|17374|17380)$/.test(remote)) this.foreign.add(`${cmd}: ${line.slice(1)}`);
        }
      }
    }
    this.peak = Math.max(this.peak, n);
    this.samples++;
    this.busy = false;
  }

  start(intervalMs = 300): void {
    void this.sample();
    this.timer = setInterval(() => void this.sample(), intervalMs);
  }

  async stop(): Promise<void> {
    if (this.timer) clearInterval(this.timer);
    while (this.busy) await new Promise((r) => setTimeout(r, 20));
  }
}
