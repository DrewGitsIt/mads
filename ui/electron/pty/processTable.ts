import { execFile } from "node:child_process";
import { readFile } from "node:fs/promises";

/**
 * "What is running under this pid" for the shell host's D5 inspection
 * (docs/SHELL-DOCK-DESIGN.md). One walk (describeTree) over a ProcessSource,
 * and two sources behind it: Linux reads /proc, macOS takes one `ps` snapshot.
 * Both sources take their I/O as an argument so the parsing and the walk are
 * unit tested with a fake /proc tree and captured ps output
 * (processTable.test.ts). Nothing here polls; callers ask on demand.
 */

export interface ProcessInfo {
  pid: number;
  /** Full command line, whitespace-joined and truncated (see truncateCommand). */
  command: string;
  /** Resident set size in KiB; 0 when unknown. */
  rssKb: number;
}

export interface ProcessSource {
  /** Direct children of pid. Empty when pid is gone or unreadable. */
  children(pid: number): Promise<number[]>;
  /** The process's command and memory; undefined when it vanished or can't be read. */
  info(pid: number): Promise<ProcessInfo | undefined>;
  /**
   * The process's parent pid; undefined when it vanished or can't be read.
   * ShellHostCore checks a shell is its own child with this before it signals
   * anything under it.
   */
  parent(pid: number): Promise<number | undefined>;
}

export interface ProcessTree {
  /** The root itself (the shell); undefined when it can't be read. */
  root: ProcessInfo | undefined;
  /** Every descendant, parents before children. */
  descendants: ProcessInfo[];
}

/** Commands shown to people (a dialog line) are cut here. */
export const COMMAND_MAX = 80;

// A runaway fork bomb must not turn "what's running" into an unbounded walk.
const MAX_DESCENDANTS = 500;

export function truncateCommand(command: string, max = COMMAND_MAX): string {
  const flat = command.replace(/\s+/g, " ").trim();
  return flat.length > max ? `${flat.slice(0, max - 1)}…` : flat;
}

/** Breadth-first walk of rootPid's descendants. A pid seen twice (a stale snapshot) is listed once. */
export async function describeTree(source: ProcessSource, rootPid: number): Promise<ProcessTree> {
  const root = await source.info(rootPid);
  const seen = new Set<number>([rootPid]);
  const descendants: ProcessInfo[] = [];
  let level = [rootPid];
  while (level.length > 0 && descendants.length < MAX_DESCENDANTS) {
    const next: number[] = [];
    for (const parent of level) {
      for (const child of await source.children(parent)) {
        if (seen.has(child)) continue;
        seen.add(child);
        const info = await source.info(child);
        // A child that exited between the two reads is simply not running.
        if (info) descendants.push(info);
        next.push(child);
      }
    }
    level = next;
  }
  return { root, descendants };
}

/** The I/O the Linux source needs; defaults to the real filesystem. */
export interface ProcFs {
  readFile(path: string): Promise<string>;
}

const realProcFs: ProcFs = { readFile: (path) => readFile(path, "utf8") };

/**
 * /proc/<pid>/task/<pid>/children, /proc/<pid>/cmdline and the VmRSS line of
 * /proc/<pid>/status. These are world-readable for root-owned processes too
 * (only environ/fd/etc. are not), so a `sudo` child still shows up. A
 * process that vanished (ENOENT/ESRCH) or can't be read (EACCES) yields
 * nothing rather than an error.
 */
export function createLinuxSource(fs: ProcFs = realProcFs): ProcessSource {
  const tryRead = async (path: string): Promise<string | undefined> => {
    try {
      return await fs.readFile(path);
    } catch {
      return undefined;
    }
  };
  return {
    async children(pid) {
      const text = await tryRead(`/proc/${pid}/task/${pid}/children`);
      return (text ?? "")
        .split(/\s+/)
        .filter(Boolean)
        .map(Number)
        .filter((n) => Number.isInteger(n) && n > 0);
    },
    async info(pid) {
      const cmdline = await tryRead(`/proc/${pid}/cmdline`);
      if (cmdline === undefined) return undefined;
      let command = cmdline.split("\0").filter(Boolean).join(" ");
      // Kernel threads and zombies have an empty cmdline; comm still names them.
      if (!command) command = (await tryRead(`/proc/${pid}/comm`))?.trim() ?? "";
      const status = await tryRead(`/proc/${pid}/status`);
      const rss = status?.match(/^VmRSS:\s+(\d+)\s*kB/m);
      return { pid, command: truncateCommand(command), rssKb: rss ? Number(rss[1]) : 0 };
    },
    async parent(pid) {
      // /proc/<pid>/stat is "pid (comm) state ppid …", and comm may itself
      // contain spaces and parens, so read past the LAST ")".
      const stat = await tryRead(`/proc/${pid}/stat`);
      const rest = stat?.slice(stat.lastIndexOf(")") + 1).trim().split(/\s+/);
      const ppid = Number(rest?.[1]);
      return Number.isInteger(ppid) && ppid >= 0 ? ppid : undefined;
    },
  };
}

/** `ps -A -o pid=,ppid=,rss=,command=` — the one snapshot macOS gets. */
export const PS_ARGS = ["-A", "-o", "pid=,ppid=,rss=,command="];

export interface PsRow {
  pid: number;
  ppid: number;
  rssKb: number;
  command: string;
}

/** Parses PS_ARGS output. The command is the rest of the line, spaces and all. */
export function parsePs(output: string): PsRow[] {
  const rows: PsRow[] = [];
  for (const line of output.split("\n")) {
    const match = line.match(/^\s*(\d+)\s+(\d+)\s+(\d+)\s+(.*\S)\s*$/);
    if (!match) continue;
    rows.push({
      pid: Number(match[1]),
      ppid: Number(match[2]),
      rssKb: Number(match[3]),
      command: match[4],
    });
  }
  return rows;
}

/** A source over one ps snapshot, taken lazily on first use and kept for this source's life. */
export function createPsSource(runPs: () => Promise<string>): ProcessSource {
  let table: Promise<{ byPid: Map<number, PsRow>; kids: Map<number, number[]> }> | undefined;
  const load = () => {
    table ??= runPs().then(
      (output) => {
        const byPid = new Map<number, PsRow>();
        const kids = new Map<number, number[]>();
        for (const row of parsePs(output)) {
          byPid.set(row.pid, row);
          kids.set(row.ppid, [...(kids.get(row.ppid) ?? []), row.pid]);
        }
        return { byPid, kids };
      },
      () => ({ byPid: new Map<number, PsRow>(), kids: new Map<number, number[]>() }),
    );
    return table;
  };
  return {
    async children(pid) {
      return (await load()).kids.get(pid) ?? [];
    },
    async info(pid) {
      const row = (await load()).byPid.get(pid);
      return row && { pid, command: truncateCommand(row.command), rssKb: row.rssKb };
    },
    async parent(pid) {
      return (await load()).byPid.get(pid)?.ppid;
    },
  };
}

function runRealPs(): Promise<string> {
  return new Promise((resolve, reject) => {
    execFile("ps", PS_ARGS, { maxBuffer: 16 * 1024 * 1024 }, (error, stdout) =>
      error ? reject(error) : resolve(stdout),
    );
  });
}

/** A fresh source for this platform. One per inspect request: a ps snapshot must not outlive it. */
export function createProcessSource(platform: NodeJS.Platform = process.platform): ProcessSource {
  if (platform === "linux") return createLinuxSource();
  if (platform === "darwin") return createPsSource(runRealPs);
  return { children: async () => [], info: async () => undefined, parent: async () => undefined };
}
