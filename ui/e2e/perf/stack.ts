import { execFileSync } from "node:child_process";
import { createHash } from "node:crypto";
import { appendFileSync, constants, copyFileSync, cpSync, existsSync, mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";
import { readTokenFile } from "../lib/auth";
import { cleanEnv, spawnListening, waitHealth, type ListeningProc } from "../lib/topology";
import { startDelayProxy, type DelayProxy } from "./proxyClient";

/**
 * One repetition's stack: the build's own data/router binaries on ephemeral
 * ports, a scratch $HOME/$MADS_HOME, an empty $MADS_AGENT_BIN_DIR (no real
 * provider CLI can start), a fresh copy of the run's seeded database, and the
 * delay proxy (its own process) in front of the Router. Same rules as
 * lib/topology.ts's bootTopology, whose helpers this uses — but with prebuilt
 * binaries and UI (one build per ref) and a database that already exists.
 *
 * Every process it starts is written to the run's pid registry
 * (PERF_PIDS_DIR) the moment it exists, so run.sh can kill whatever a dead
 * worker left behind.
 */
export interface PerfStack {
  proxy: DelayProxy;
  /** The browser's origin: the proxy. */
  appURL: string;
  routerURL: string;
  dataURL: string;
  token: string;
  homeDir: string;
  pids: { router: number; data: number };
  /** Records another process this repetition owns (the browser, Electron). */
  own(pid: number, name: string): void;
  teardown(): Promise<void>;
}

export interface Build {
  label: string;
  /** Holds data, router, session, pipeline. */
  binDir: string;
  uiDist: string;
  /** The ui/ directory (Electron's app dir: dist-electron + package.json). */
  uiDir?: string;
  /** What was built: ref and commit, or worktree and commit. */
  desc?: string;
}

/** The run's pid registry: one file per repetition, read by run.sh. */
function registry(tag: string): (line: string) => void {
  const dir = process.env.PERF_PIDS_DIR;
  if (!dir) return () => {};
  mkdirSync(dir, { recursive: true });
  const file = path.join(dir, `${tag}.txt`);
  return (line) => appendFileSync(file, line + "\n");
}

/** Descendants of pid (the Router's session/pipeline children). */
export function childrenOf(pid: number): number[] {
  try {
    return execFileSync("pgrep", ["-P", String(pid)], { encoding: "utf8" })
      .split("\n")
      .filter(Boolean)
      .map(Number);
  } catch {
    return [];
  }
}

export async function bootPerfStack(opts: {
  build: Build;
  seededDB: string;
  /** A ~/.claude/projects tree to copy into the scratch $HOME (perfseed -transcripts). */
  transcriptsDir?: string;
  delays: string;
  holdOutput: boolean;
  tag: string;
}): Promise<PerfStack> {
  const record = registry(opts.tag);
  const tmpDir = mkdtempSync(path.join(tmpdir(), "mads-perf-run-"));
  record(`dir ${tmpDir}`);
  const homeDir = path.join(tmpDir, "home");
  const dataDir = path.join(tmpDir, "data");
  mkdirSync(homeDir, { recursive: true });
  mkdirSync(dataDir, { recursive: true });
  // pkg/childsock's short fallback for a $MADS_HOME this deep.
  const sockFallback = `/tmp/mads-${process.getuid?.() ?? 0}-${createHash("sha256").update(homeDir).digest("hex").slice(0, 10)}`;
  record(`dir ${sockFallback}`);
  writeFileSync(path.join(homeDir, ".gitconfig"), "[user]\n\tname = perf user\n\temail = perf@perf.test\n");
  if (opts.transcriptsDir && existsSync(opts.transcriptsDir)) {
    cpSync(opts.transcriptsDir, path.join(homeDir, ".claude", "projects"), { recursive: true });
  }
  // A fresh copy per repetition: the Data Service migrates and appends to
  // it, and no repetition may see another's writes. A clone on APFS.
  copyFileSync(opts.seededDB, path.join(dataDir, "mads.db"), constants.COPYFILE_FICLONE);

  const env = cleanEnv(homeDir);
  env.MADS_MERGIRAF = path.join(tmpDir, "mergiraf-standin", "mergiraf");
  const agentBinDir = path.join(tmpDir, "agent-bin-standins");
  mkdirSync(agentBinDir, { recursive: true });
  env.MADS_AGENT_BIN_DIR = agentBinDir;

  const bin = (n: string) => path.join(opts.build.binDir, n);
  const procs: ListeningProc[] = [];
  let proxy: DelayProxy | undefined;
  const cleanup = async () => {
    await proxy?.close().catch(() => {});
    for (const p of [...procs].reverse()) await p.terminate();
    rmSync(tmpDir, { recursive: true, force: true });
    rmSync(sockFallback, { recursive: true, force: true });
  };
  try {
    proxy = await startDelayProxy({ delays: opts.delays, holdOutput: opts.holdOutput });
    record(`pid ${proxy.pid} proxy`);
    const data = await spawnListening("data", [bin("data"), "-addr", "127.0.0.1:0", "-data-dir", dataDir], env);
    procs.push(data);
    record(`pid ${data.proc.pid} data`);
    const dataURL = `http://${data.addr}`;
    await waitHealth(dataURL);
    const router = await spawnListening(
      "router",
      [
        bin("router"),
        "-addr",
        "127.0.0.1:0",
        "-data-url",
        dataURL,
        "-session-cmd",
        `${bin("session")} -data-url ${dataURL}`,
        "-pipeline-cmd",
        bin("pipeline"),
        "-mads-home",
        homeDir,
        "-ui-dist",
        opts.build.uiDist,
        // The page's origin is the proxy's, not the Router's own.
        "-allowed-origins",
        proxy.url,
      ],
      env,
    );
    procs.push(router);
    record(`pid ${router.proc.pid} router`);
    const routerURL = `http://${router.addr}`;
    await waitHealth(routerURL);
    await proxy.setTarget(routerURL);
    const token = readTokenFile(homeDir);
    return {
      proxy,
      appURL: proxy.url,
      routerURL,
      dataURL,
      token,
      homeDir,
      pids: { router: router.proc.pid!, data: data.proc.pid! },
      own: (pid, name) => record(`pid ${pid} ${name}`),
      teardown: cleanup,
    };
  } catch (err) {
    await cleanup();
    throw err;
  }
}

/** Data Service / Router GET with the stack's token. */
export async function apiGet<T>(stack: PerfStack, base: string, p: string): Promise<T> {
  const res = await fetch(`${base}${p}`, { headers: { Authorization: `Bearer ${stack.token}` } });
  if (!res.ok) throw new Error(`GET ${p}: ${res.status}`);
  return (await res.json()) as T;
}

/**
 * Waits for the Router's boot sweep to conclude the seeded running
 * delegates (orphaned, ended now), which is what puts them in the rail's
 * fade window for scenario 3. Fails loudly rather than letting the scenario
 * look for a row that will never appear.
 */
export async function waitDelegatesConcluded(stack: PerfStack, sessionId: string, timeoutMs = 15_000): Promise<void> {
  const deadline = Date.now() + timeoutMs;
  for (;;) {
    const list = await apiGet<{ state: string }[]>(stack, stack.dataURL, `/sessions/${sessionId}/delegates`);
    if (!list.some((d) => d.state === "running")) return;
    if (Date.now() > deadline) throw new Error(`boot sweep did not conclude the running delegates of ${sessionId}`);
    await new Promise((r) => setTimeout(r, 200));
  }
}
