import { execFile, execFileSync } from "node:child_process";
import { createHash } from "node:crypto";
import { appendFileSync, constants, copyFileSync, cpSync, existsSync, mkdirSync, mkdtempSync, rmSync, unlinkSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";
import { readTokenFile } from "../lib/auth";
import { cleanEnv, spawnListening, waitHealth, type ListeningProc } from "../lib/topology";
import { startDelayProxy, type DelayProxy } from "./proxyClient";
import { PIPELINE_SCENARIO } from "./fixtures";

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
  /** This repetition's own database copy (the Data Service's, read-only here). */
  dbPath: string;
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

/**
 * The run's pid registry: one file per repetition, read by run.sh after
 * Playwright exits. A pid line carries the process's start time and full
 * command (`ps -ww -o lstart=,command=`) so run.sh kills it only if all three
 * still match — never a recycled pid. A repetition that tears down cleanly
 * deletes its file; one left behind means the worker died mid-repetition.
 */
interface Registry {
  pid(pid: number, name: string): void;
  dir(d: string): void;
  clear(): void;
}
function registry(tag: string): Registry {
  const dir = process.env.PERF_PIDS_DIR;
  if (!dir) return { pid: () => {}, dir: () => {}, clear: () => {} };
  mkdirSync(dir, { recursive: true });
  const file = path.join(dir, `${tag}.txt`);
  const ps = (field: string, pid: number) => {
    try {
      return execFileSync("ps", ["-ww", "-o", `${field}=`, "-p", String(pid)], { encoding: "utf8" }).trim();
    } catch {
      return "";
    }
  };
  return {
    pid: (pid, name) => {
      const lstart = ps("lstart", pid);
      const command = ps("command", pid);
      if (lstart && command) appendFileSync(file, `pid\t${pid}\t${lstart}\t${name}\t${command}\n`);
    },
    dir: (d) => appendFileSync(file, `dir\t${d}\n`),
    clear: () => {
      try {
        unlinkSync(file);
      } catch {
        // nothing recorded
      }
    },
  };
}

/** Registry files left by a repetition that never finished its teardown. */
export function strandedRepetitions(): string[] {
  const dir = process.env.PERF_PIDS_DIR;
  if (!dir || !existsSync(dir)) return [];
  return execFileSync("ls", [dir], { encoding: "utf8" }).split("\n").filter((f) => f.endsWith(".txt"));
}

function sqlite(db: string, q: string): Promise<string> {
  return new Promise((resolve, reject) => {
    execFile("sqlite3", ["-readonly", db, q], { timeout: 10_000 }, (err, out) => (err ? reject(err) : resolve(out.trim())));
  });
}

/** The log's head seq, read straight from the repetition's database (not through the page). */
export async function logHead(stack: PerfStack): Promise<number> {
  return Number((await sqlite(stack.dbPath, "SELECT COALESCE(max(seq),0) FROM events")) || 0);
}

/**
 * The newest seq in (since, head] that a /events stream with this types
 * filter has to deliver, or 0 when it has nothing to send (filter matches
 * nothing there, or since >= head). Patterns as the Data Service reads them:
 * exact, or "domain.*".
 */
export async function streamTarget(stack: PerfStack, since: number, head: number, types: string[]): Promise<number> {
  if (since >= head) return 0;
  const clauses = types
    .filter((t) => /^[a-z_.*]+$/.test(t))
    .map((t) => (t.endsWith(".*") ? `type LIKE '${t.slice(0, -2)}.%'` : `type = '${t}'`));
  const where = clauses.length ? ` AND (${clauses.join(" OR ")})` : "";
  return Number((await sqlite(stack.dbPath, `SELECT COALESCE(max(seq),0) FROM events WHERE seq > ${since} AND seq <= ${head}${where}`)) || 0);
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
  /** A Fast Model endpoint for the Router (checks only; scenarios run without one, so jobs skip). */
  fastModelURL?: string;
  /** Extra environment for the stack's processes (e.g. MADS_FAKE_START_DELAY_MS). */
  env?: Record<string, string>;
}): Promise<PerfStack> {
  const reg = registry(opts.tag);
  const tmpDir = mkdtempSync(path.join(tmpdir(), "mads-perf-run-"));
  reg.dir(tmpDir);
  const homeDir = path.join(tmpDir, "home");
  const dataDir = path.join(tmpDir, "data");
  mkdirSync(homeDir, { recursive: true });
  mkdirSync(dataDir, { recursive: true });
  // pkg/childsock's short fallback for a $MADS_HOME this deep.
  const sockFallback = `/tmp/mads-${process.getuid?.() ?? 0}-${createHash("sha256").update(homeDir).digest("hex").slice(0, 10)}`;
  reg.dir(sockFallback);
  writeFileSync(path.join(homeDir, ".gitconfig"), "[user]\n\tname = perf user\n\temail = perf@perf.test\n");
  if (opts.transcriptsDir && existsSync(opts.transcriptsDir)) {
    cpSync(opts.transcriptsDir, path.join(homeDir, ".claude", "projects"), { recursive: true });
  }
  // A fresh copy per repetition: the Data Service migrates and appends to
  // it, and no repetition may see another's writes. A clone on APFS.
  copyFileSync(opts.seededDB, path.join(dataDir, "mads.db"), constants.COPYFILE_FICLONE);

  const env = cleanEnv(homeDir);
  // No credential of the harness's own environment reaches the stack.
  for (const k of Object.keys(env)) {
    if (/API_?KEY|TOKEN|SECRET|PASSW|CREDENTIAL|_AUTH|SESSION_KEY|PRIVATE_KEY/i.test(k)) delete env[k];
  }
  // Anything a child tries to reach that isn't loopback goes to a dead
  // proxy; and git can't discover a repo above the run's temp tree.
  for (const k of ["HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "all_proxy"]) env[k] = "http://127.0.0.1:1";
  env.NO_PROXY = env.no_proxy = "127.0.0.1,localhost,::1";
  env.GIT_CEILING_DIRECTORIES = process.env.PERF_GIT_CEILING || path.dirname(tmpDir);
  Object.assign(env, opts.env ?? {});
  env.MADS_MERGIRAF = path.join(tmpDir, "mergiraf-standin", "mergiraf");
  const agentBinDir = path.join(tmpDir, "agent-bin-standins");
  mkdirSync(agentBinDir, { recursive: true });
  env.MADS_AGENT_BIN_DIR = agentBinDir;

  const bin = (n: string) => path.join(opts.build.binDir, n);
  // Pipeline runs the Router launches use the fake provider with the perf
  // script (fixtures.ts: the pinned task's slow executor).
  const pipelineScenario = path.join(tmpDir, "pipeline-scenario.yaml");
  writeFileSync(pipelineScenario, PIPELINE_SCENARIO);
  const procs: ListeningProc[] = [];
  let proxy: DelayProxy | undefined;
  const cleanup = async () => {
    await proxy?.close().catch(() => {});
    for (const p of [...procs].reverse()) await p.terminate();
    rmSync(tmpDir, { recursive: true, force: true });
    rmSync(sockFallback, { recursive: true, force: true });
    reg.clear();
  };
  try {
    proxy = await startDelayProxy({ delays: opts.delays, holdOutput: opts.holdOutput });
    reg.pid(proxy.pid, "proxy");
    // The services' working directory, which a spawned session child
    // resolves "." to: a scratch git repo here, never the checkout the
    // harness runs from (whose git status a session would otherwise read).
    const cwd = path.join(tmpDir, "cwd");
    mkdirSync(cwd, { recursive: true });
    execFileSync("git", ["-C", cwd, "init", "-q", "-b", "main"]);
    execFileSync("git", ["-C", cwd, "-c", "user.name=perf", "-c", "user.email=perf@perf.test", "commit", "-q", "--allow-empty", "-m", "perf cwd"]);
    const data = await spawnListening("data", [bin("data"), "-addr", "127.0.0.1:0", "-data-dir", dataDir], env, cwd);
    procs.push(data);
    reg.pid(data.proc.pid!, "data");
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
        `${bin("pipeline")} -fake-scenario ${pipelineScenario}`,
        ...(opts.fastModelURL ? ["-fastmodel-base-url", opts.fastModelURL] : []),
        "-mads-home",
        homeDir,
        "-ui-dist",
        opts.build.uiDist,
        // The page's origin is the proxy's, not the Router's own.
        "-allowed-origins",
        proxy.url,
      ],
      env,
      cwd,
    );
    procs.push(router);
    reg.pid(router.proc.pid!, "router");
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
      dbPath: path.join(dataDir, "mads.db"),
      own: (pid, name) => reg.pid(pid, name),
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
