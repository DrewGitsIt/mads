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

/** Path with ids folded, so requests group by route. */
export function routeOf(path: string): string {
  return path
    .replace(/\/ses_[0-9a-f]+/g, "/:session")
    .replace(/\/a[0-9a-f]{16,}/g, "/:harness")
    .replace(/\/(dlg|lnk|wsp|tsk|job|ret|tpl)_[0-9a-f]+/g, "/:$1")
    .replace(/\/\d+(?=\/|$)/g, "/:n")
    .replace(/\/assets\/.*$/, "/assets/*");
}

/**
 * The one CDP use left (round 3, F2): Performance.getMetrics for
 * main-thread task and script time. No Network domain on this session, so
 * Chromium sends the harness none of a replay's per-message events.
 */
export class MainThreadMetrics {
  private cdp?: CDPSession;
  async attach(page: Page): Promise<void> {
    this.cdp = await page.context().newCDPSession(page);
    await this.cdp.send("Performance.enable", { timeDomain: "timeTicks" }).catch(() => {});
  }
  async read(): Promise<{ taskMs: number; scriptMs: number }> {
    const r = await this.cdp!.send("Performance.getMetrics").catch(() => ({ metrics: [] as { name: string; value: number }[] }));
    const get = (n: string) => (r.metrics.find((m) => m.name === n)?.value ?? 0) * 1000;
    return { taskMs: get("TaskDuration"), scriptMs: get("ScriptDuration") };
  }
  async detach(): Promise<void> {
    await this.cdp?.detach().catch(() => {});
  }
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
