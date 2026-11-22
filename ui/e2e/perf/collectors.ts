import { execFile } from "node:child_process";
import { readFileSync } from "node:fs";
import type { CDPSession, Page } from "@playwright/test";

/**
 * Collectors for one measured window (tools/perf/docs/PLAN.md §3). The
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

  /** A fresh sample now: a process group's CPU since start (ms), for per-leg deltas. */
  async mark(name: string): Promise<number> {
    while (this.busy) await new Promise((r) => setTimeout(r, 20));
    await this.sample();
    const pid = this.pids[name];
    return pid === undefined ? 0 : Math.round(((this.last.get(pid) ?? 0) - (this.first.get(pid) ?? 0)) * 1000);
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

// ---------------------------------------------------------------------------
// Git processes: the shim's exact log, and ps sampling by pid ancestry.

/** One git call as the shim logged it (gitshim/main.go). */
export interface GitCall {
  t0: number;
  t1: number;
  ppid: number;
  sub: string;
  cpu_us: number;
  exit: number;
  /** GIT_INDEX_FILE was set (shim): with sub "add", one change-set root diffed (pkg/changes/gitops.go). */
  idx?: boolean;
}

export function readGitCalls(file: string | undefined): GitCall[] {
  if (!file) return [];
  let text = "";
  try {
    text = readFileSync(file, "utf8");
  } catch {
    return [];
  }
  const out: GitCall[] = [];
  for (const line of text.split("\n")) {
    if (!line) continue;
    try {
      out.push(JSON.parse(line) as GitCall);
    } catch {
      // a line cut short by a kill
    }
  }
  return out;
}

export interface GitStats {
  spawns: number;
  cpuMs: number;
  /** Sum of the calls' own durations (a call running beside another counts twice). */
  wallMs: number;
  /** Calls whose parent is the Router itself (the rest: session children, pipelines). */
  router: number;
  /** Change-set roots diffed: `add -A` into a temp shadow index, one per root per change-set compute. */
  changesetRoots: number;
  bySub: Record<string, { n: number; cpuMs: number }>;
}

/** The git calls that started in [from, to] (Unix ms). */
export function gitStats(calls: GitCall[], from: number, to: number, routerPid: number): GitStats {
  const s: GitStats = { spawns: 0, cpuMs: 0, wallMs: 0, router: 0, changesetRoots: 0, bySub: {} };
  for (const c of calls) {
    if (c.t0 < from || c.t0 > to) continue;
    s.spawns++;
    s.cpuMs += c.cpu_us / 1000;
    s.wallMs += c.t1 - c.t0;
    if (c.ppid === routerPid) s.router++;
    if (c.idx && c.sub === "add") s.changesetRoots++;
    const b = (s.bySub[c.sub || "?"] ??= { n: 0, cpuMs: 0 });
    b.n++;
    b.cpuMs += c.cpu_us / 1000;
  }
  s.cpuMs = Math.round(s.cpuMs);
  for (const b of Object.values(s.bySub)) b.cpuMs = Math.round(b.cpuMs);
  return s;
}

/**
 * Samples `ps` every intervalMs for git processes in the Router's process
 * tree (its own children and its session/pipeline children's), by pid
 * ancestry: the most running at once, and how many distinct ones a sample
 * caught. A git process whose parent is git (the shim's real git, git's
 * own helpers) is not counted again. Sampling misses most calls (they last
 * a few ms); the shim's log is the exact count, this the concurrency.
 */
export class GitPsSampler {
  private timer?: ReturnType<typeof setInterval>;
  private busy = false;
  peak = 0;
  readonly seen = new Set<number>();
  private series: [number, number][] = [];
  constructor(private readonly rootPid: number) {}

  private async sample(): Promise<void> {
    if (this.busy) return;
    this.busy = true;
    const out = await run("ps", ["-A", "-o", "pid=,ppid=,comm="]);
    const parent = new Map<number, number>();
    const comm = new Map<number, string>();
    for (const line of out.split("\n")) {
      const m = /^\s*(\d+)\s+(\d+)\s+(.*)$/.exec(line);
      if (!m) continue;
      parent.set(Number(m[1]), Number(m[2]));
      comm.set(Number(m[1]), m[3].trim());
    }
    const isGit = (pid: number) => /(^|\/)git$/.test(comm.get(pid) ?? "");
    const under = (pid: number) => {
      for (let p = parent.get(pid), hops = 0; p && hops < 12; p = parent.get(p), hops++) if (p === this.rootPid) return true;
      return false;
    };
    let n = 0;
    for (const pid of parent.keys()) {
      if (!isGit(pid) || isGit(parent.get(pid) ?? 0) || !under(pid)) continue;
      n++;
      this.seen.add(pid);
    }
    this.peak = Math.max(this.peak, n);
    this.series.push([Date.now(), n]);
    this.busy = false;
  }

  start(intervalMs = 100): void {
    void this.sample();
    this.timer = setInterval(() => void this.sample(), intervalMs);
  }

  /** Peak concurrent git processes sampled in [from, to]. */
  peakIn(from: number, to: number): number {
    return Math.max(0, ...this.series.filter(([t]) => t >= from && t <= to).map(([, n]) => n));
  }

  async stop(): Promise<void> {
    if (this.timer) clearInterval(this.timer);
    while (this.busy) await new Promise((r) => setTimeout(r, 20));
  }
}

/** The git routes the harness reports latency for, by request (method + path). */
export function gitRoute(method: string, p: string): string | undefined {
  if (!p.startsWith("/sessions/")) return undefined;
  if (method === "GET" && /^\/sessions\/[^/]+\/repos$/.test(p)) return "repos";
  if (method === "GET" && /\/changeset$/.test(p)) return "changeset";
  if (method === "POST" && /\/worktree\/refresh$/.test(p)) return "worktree/refresh";
  if (method === "GET" && /^\/sessions\/[^/]+\/worktree$/.test(p)) return "worktree";
  if (method === "GET" && /\/commit-plan$/.test(p)) return "commit-plan";
  if (method === "GET" && /^\/sessions\/[^/]+\/push$/.test(p)) return "push";
  if (method === "GET" && /^\/sessions\/[^/]+\/land$/.test(p)) return "land";
  return undefined;
}

export const GIT_ROUTES = ["repos", "changeset", "worktree/refresh", "worktree", "commit-plan", "push", "land"];
