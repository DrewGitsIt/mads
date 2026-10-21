import { spawn, execFileSync, type ChildProcess } from "node:child_process";
import { mkdtempSync, mkdirSync, readdirSync, readlinkSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";

const repoRoot = path.resolve(__dirname, "../../..");
const uiDist = path.resolve(__dirname, "../../dist");

const listeningRe = /listening on (\S+)/;

interface ListeningProc {
  proc: ChildProcess;
  addr: string;
  exited: Promise<void>;
  terminate(graceMs?: number): Promise<void>;
}

function cleanEnv(home: string): NodeJS.ProcessEnv {
  const env: NodeJS.ProcessEnv = {};
  for (const [k, v] of Object.entries(process.env)) {
    if (k === "HOME" || k === "MADS_DATA_DIR" || k === "MADS_HOME") continue;
    env[k] = v;
  }
  env.HOME = home;
  env.MADS_HOME = home;
  return env;
}

function spawnListening(name: string, argv: string[], env: NodeJS.ProcessEnv): Promise<ListeningProc> {
  return new Promise((resolve, reject) => {
    const proc = spawn(argv[0], argv.slice(1), { env, stdio: ["ignore", "ignore", "pipe"] });
    const lines: string[] = [];
    let settled = false;

    let exitedResolve!: () => void;
    const exited = new Promise<void>((r) => {
      exitedResolve = r;
    });

    const timer = setTimeout(() => {
      if (settled) return;
      settled = true;
      proc.kill("SIGKILL");
      reject(new Error(`${name}: did not report a listen address in time:\n${lines.join("\n")}`));
    }, 15_000);

    proc.stderr?.setEncoding("utf8");
    let buf = "";
    proc.stderr?.on("data", (chunk: string) => {
      buf += chunk;
      let idx: number;
      while ((idx = buf.indexOf("\n")) !== -1) {
        const line = buf.slice(0, idx);
        buf = buf.slice(idx + 1);
        lines.push(line);
        if (!settled) {
          const m = listeningRe.exec(line);
          if (m) {
            settled = true;
            clearTimeout(timer);
            resolve({
              proc,
              addr: m[1],
              exited,
              terminate: (graceMs = 5000) => terminate(proc, exited, graceMs),
            });
          }
        }
      }
    });

    proc.on("exit", () => {
      exitedResolve();
      if (!settled) {
        settled = true;
        clearTimeout(timer);
        reject(new Error(`${name}: exited before reporting a listen address:\n${lines.join("\n")}`));
      }
    });

    proc.on("error", (err) => {
      if (!settled) {
        settled = true;
        clearTimeout(timer);
        reject(err);
      }
    });
  });
}

async function terminate(proc: ChildProcess, exited: Promise<void>, graceMs: number): Promise<void> {
  if (proc.exitCode !== null || proc.signalCode !== null) return;
  proc.kill("SIGTERM");
  let timedOut = false;
  const timer = new Promise<void>((r) => {
    setTimeout(() => {
      timedOut = true;
      r();
    }, graceMs);
  });
  await Promise.race([exited, timer]);
  if (timedOut && proc.exitCode === null && proc.signalCode === null) {
    proc.kill("SIGKILL");
    await exited;
  }
}

async function waitHealth(base: string): Promise<void> {
  const deadline = Date.now() + 10_000;
  while (Date.now() < deadline) {
    try {
      const res = await fetch(`${base}/health`);
      if (res.ok) return;
    } catch {
      // not up yet
    }
    await new Promise((r) => setTimeout(r, 50));
  }
  throw new Error(`${base}/health did not become healthy in time`);
}

/** Reimplements pkg/testleakcheck.Verify: scans /proc for live processes whose exe is binPath. */
function verifyNoLeaks(binPath: string): void {
  let entries: string[];
  try {
    entries = readdirSync("/proc");
  } catch {
    return; // non-Linux: nothing to check
  }
  const leaked: string[] = [];
  for (const e of entries) {
    if (!/^\d+$/.test(e)) continue;
    let exe: string;
    try {
      exe = readlinkSync(`/proc/${e}/exe`);
    } catch {
      continue;
    }
    if (exe === binPath) leaked.push(`pid ${e}`);
  }
  if (leaked.length > 0) {
    throw new Error(`testleakcheck: ${leaked.length} process(es) still running ${binPath} after test run: ${leaked.join(", ")}`);
  }
}

export interface Topology {
  dataURL: string;
  routerURL: string;
  madsBin: string;
  sessionBin: string;
  tmpDir: string;
  homeDir: string;
  /**
   * The services' $MADS_MERGIRAF: where they look for mergiraf instead of
   * PATH. Nothing is there unless a spec puts a stand-in there, so merges
   * are plain git by default whatever the machine has installed.
   */
  mergirafPath: string;
  /**
   * The services' $MADS_AGENT_BIN_DIR (pkg/binlocate): the only place they
   * look for an agent harness (claude, goose, kiro-cli, ...), never PATH or
   * a known install location. Nothing is there unless a spec drops a
   * stand-in script in, so the router's boot model discovery, /preflight's
   * version probes and any session spawn can't start a real harness: the
   * provider rows read not-installed. Every hermetic harness must set it.
   */
  agentBinDir: string;
  teardown(): Promise<void>;
}

export interface BootTopologyOptions {
  fastModelURL: string;
  /**
   * Scenario YAML for the fake provider in Router-launched pipeline runs
   * (POST /pipelines, the Tasks tab's Run button). Rules key on task titles,
   * so one suite-wide scenario scripts every spec's runs; unmatched prompts
   * get the fake's instant built-in reply.
   */
  pipelineScenario?: string;
  /**
   * Scenario YAML for the fake provider in every session child the Router
   * spawns (services/session -fake-scenario). Rules key on prompts, so one
   * suite-wide scenario scripts every spec's chat turns; unmatched prompts
   * get the fake's built-in dialect (echo, slow, ...).
   */
  sessionScenario?: string;
}

/** Builds fresh mads/data/router/session binaries into a scratch dir and boots the full topology, mirroring scripts/e2esmoke/main_test.go. */
export async function bootTopology(opts: BootTopologyOptions): Promise<Topology> {
  const buildDir = mkdtempSync(path.join(tmpdir(), "mads-ui-smoke-bin-"));
  const madsBin = path.join(buildDir, "mads");
  const dataBin = path.join(buildDir, "data");
  const routerBin = path.join(buildDir, "router");
  const sessionBin = path.join(buildDir, "session");
  const pipelineBin = path.join(buildDir, "pipeline");

  for (const [out, pkg] of [
    [madsBin, "./cmd/mads"],
    [dataBin, "./services/data"],
    [routerBin, "./services/router"],
    [sessionBin, "./services/session"],
    [pipelineBin, "./services/pipeline"],
  ] as const) {
    execFileSync("go", ["build", "-o", out, pkg], { cwd: repoRoot, stdio: "inherit" });
  }

  const tmpDir = mkdtempSync(path.join(tmpdir(), "mads-ui-smoke-run-"));
  const homeDir = path.join(tmpDir, "home");
  const dataDir = path.join(tmpDir, "data");
  mkdirSync(homeDir, { recursive: true });
  mkdirSync(dataDir, { recursive: true });
  // The scratch HOME stands in for a user who has git set up: Commit and
  // Land commit as the user's own identity and deliberately never fall back
  // to a made-up one (docs/SESSION-WORKTREES-DESIGN.md D5), so without this
  // every commit the journeys make is refused with "Author identity unknown".
  writeFileSync(path.join(homeDir, ".gitconfig"), "[user]\n\tname = e2e user\n\temail = e2e@smoke.test\n");
  const env = cleanEnv(homeDir);
  const mergirafPath = path.join(tmpDir, "mergiraf-standin", "mergiraf");
  env.MADS_MERGIRAF = mergirafPath;
  const agentBinDir = path.join(tmpDir, "agent-bin-standins");
  mkdirSync(agentBinDir, { recursive: true });
  env.MADS_AGENT_BIN_DIR = agentBinDir;

  // The real Pipeline Engine child, so the Tasks tab's Run button (POST
  // /pipelines) launches a real run like `mads run` does.
  let pipelineCmd = pipelineBin;
  if (opts.pipelineScenario) {
    const scenarioPath = path.join(tmpDir, "pipeline-scenario.yaml");
    writeFileSync(scenarioPath, opts.pipelineScenario);
    pipelineCmd += ` -fake-scenario ${scenarioPath}`;
  }

  // Every session child's fake provider follows the suite's chat script.
  let sessionScenarioArgs = "";
  if (opts.sessionScenario) {
    const scenarioPath = path.join(tmpDir, "session-scenario.yaml");
    writeFileSync(scenarioPath, opts.sessionScenario);
    sessionScenarioArgs = ` -fake-scenario ${scenarioPath}`;
  }

  const data = await spawnListening("data", [dataBin, "-addr", "127.0.0.1:0", "-data-dir", dataDir], env);
  const dataURL = `http://${data.addr}`;
  await waitHealth(dataURL);

  const router = await spawnListening(
    "router",
    [
      routerBin,
      "-addr",
      "127.0.0.1:0",
      "-data-url",
      dataURL,
      "-session-cmd",
      `${sessionBin} -data-url ${dataURL}${sessionScenarioArgs}`,
      "-pipeline-cmd",
      pipelineCmd,
      "-fastmodel-base-url",
      opts.fastModelURL,
      "-mads-home",
      homeDir,
      "-ui-dist",
      uiDist,
    ],
    env,
  );
  const routerURL = `http://${router.addr}`;
  await waitHealth(routerURL);

  const teardown = async () => {
    await router.terminate();
    await data.terminate();
    verifyNoLeaks(sessionBin);
    verifyNoLeaks(pipelineBin);
    rmSync(buildDir, { recursive: true, force: true });
    rmSync(tmpDir, { recursive: true, force: true });
  };

  return { dataURL, routerURL, madsBin, sessionBin, tmpDir, homeDir, mergirafPath, agentBinDir, teardown };
}
