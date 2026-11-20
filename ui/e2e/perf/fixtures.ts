import { execFileSync } from "node:child_process";
import { mkdirSync, writeFileSync } from "node:fs";
import { createServer, type Server } from "node:http";
import path from "node:path";
import type { PerfStack } from "./stack";

/**
 * Live fixtures for scenario 6 and the correctness checks, made through the
 * stack's own Router (and, for log-only state, its own Data Service) — all
 * hermetic: the session children run the fake provider, pipeline runs the
 * fake with the perf pipeline scenario, and every repo is a scratch git repo
 * inside the repetition's temp dir.
 */

async function call<T>(stack: PerfStack, base: string, method: string, p: string, body?: unknown): Promise<T> {
  const res = await fetch(`${base}${p}`, {
    method,
    headers: { Authorization: `Bearer ${stack.token}`, ...(body ? { "Content-Type": "application/json" } : {}) },
    body: body ? JSON.stringify(body) : undefined,
  });
  const text = await res.text();
  if (!res.ok) throw new Error(`${method} ${p}: ${res.status} ${text.slice(0, 200)}`);
  return (text ? JSON.parse(text) : undefined) as T;
}
export const router = <T>(s: PerfStack, m: string, p: string, b?: unknown) => call<T>(s, s.routerURL, m, p, b);
export const data = <T>(s: PerfStack, m: string, p: string, b?: unknown) => call<T>(s, s.dataURL, m, p, b);

export async function renameSession(stack: PerfStack, id: string, name: string): Promise<void> {
  await router(stack, "POST", `/sessions/${id}/rename`, { name });
}

/** The task whose executor streams for ~15 minutes: a live attempt that outlasts any scenario. */
export const PINNED_TASK = "perf pinned: live attempt";
export const PIPELINE_SCENARIO = [
  "name: perf-pipelines",
  "rules:",
  `  - match: ${JSON.stringify(`Pipeline stage: executor\nTask: ${PINNED_TASK}\n`)}`,
  "    chunks:",
  ...Array.from({ length: 900 }, (_, i) => `      - ${JSON.stringify(`perf attempt step ${i + 1}\n`)}`),
  `      - ${JSON.stringify("STAGE: pass")}`,
  "    delay_ms: 1000",
  "",
].join("\n");

/** A scratch repo with one commit, in the repetition's temp dir. */
function scratchRepo(stack: PerfStack, name: string): string {
  const dir = path.join(path.dirname(stack.dbPath), "..", "repos", name);
  mkdirSync(dir, { recursive: true });
  const git = (...a: string[]) => execFileSync("git", ["-C", dir, ...a], { stdio: "ignore" });
  git("init", "-q", "-b", "main");
  writeFileSync(path.join(dir, "README.md"), "perf fixture\n");
  git("add", ".");
  git("-c", "user.name=perf", "-c", "user.email=perf@perf.test", "commit", "-q", "-m", "perf fixture");
  return dir;
}

export interface LiveFixture {
  session: { id: string; name: string };
  workspaceId: string;
  backgroundPrompt: string;
  pipelineRunning: boolean;
}

let n = 0;

/**
 * A live fake-provider session in its own scratch workspace. With
 * `background`, its first turn starts a background process that writes a
 * line a second for 15 minutes; with `pipeline`, a pipeline run is started
 * on a task whose executor streams for ~15 minutes; with `slow`, its first
 * turn streams until interrupted (so later messages queue).
 */
export async function liveFixture(
  stack: PerfStack,
  opts: { background?: boolean; pipeline?: boolean; slow?: boolean; name?: string },
): Promise<LiveFixture> {
  const tag = `${Date.now() % 1_000_000}-${++n}`;
  const repo = scratchRepo(stack, `fx-${tag}`);
  const ws = await router<{ id: string }>(stack, "POST", "/workspaces", { name: `perf fixture ${tag}`, repos: [{ path: repo, primary: true }], defaults: {} });
  const name = opts.name ?? `perf live ${tag}`;
  const created = await router<{ session: { id: string; name: string } }>(stack, "POST", "/sessions", {
    name,
    provider: "fake",
    workspace: ws.id,
    isolation: "current_dir",
    trust: "full",
  });
  const fx: LiveFixture = { session: { id: created.session.id, name }, workspaceId: ws.id, backgroundPrompt: "background 900 1000", pipelineRunning: false };
  if (opts.background) await router(stack, "POST", `/sessions/${fx.session.id}/messages`, { text: fx.backgroundPrompt });
  else if (opts.slow) await router(stack, "POST", `/sessions/${fx.session.id}/messages`, { text: "slow" });
  if (opts.pipeline) {
    const tpl = await router<{ id: string }>(stack, "POST", "/templates", {
      name: `perf-pinned-${tag}`,
      description: "perf: one slow executor stage",
      source: [`name: perf-pinned-${tag}`, "stages:", "  - name: executor", "    agent: executor", ""].join("\n"),
    });
    await router(stack, "POST", "/tasks/import", { tasks: [{ session_id: fx.session.id, title: PINNED_TASK, source: "user" }] });
    await router(stack, "POST", "/pipelines", { session_id: fx.session.id, template_id: tpl.id });
    await waitFor(async () => {
      const ev = await router<{ type: string; payload?: { stage?: string } }[]>(stack, "GET", `/events?session_id=${fx.session.id}&types=stage.started`);
      return ev.some((e) => e.payload?.stage === "executor");
    }, 60_000, "the pinned pipeline's executor stage to start");
    fx.pipelineRunning = true;
  }
  return fx;
}

export async function waitFor(f: () => Promise<boolean>, ms: number, what: string): Promise<void> {
  const deadline = Date.now() + ms;
  while (!(await f().catch(() => false))) {
    if (Date.now() > deadline) throw new Error(`timed out waiting for ${what}`);
    await new Promise((r) => setTimeout(r, 200));
  }
}

/**
 * A Fast Model endpoint that accepts every request and never answers, so a
 * retro started against it stays in progress for as long as a check needs.
 * Closing it ends the held requests.
 */
export async function startStuckFastModel(): Promise<{ url: string; close(): Promise<void> }> {
  const held = new Set<import("node:http").ServerResponse>();
  const server: Server = createServer((req, res) => {
    req.resume();
    held.add(res);
    res.on("close", () => held.delete(res));
  });
  await new Promise<void>((r) => server.listen(0, "127.0.0.1", r));
  return {
    url: `http://127.0.0.1:${(server.address() as { port: number }).port}/v1`,
    close: async () => {
      for (const r of held) r.destroy();
      await new Promise<void>((r) => server.close(() => r()));
    },
  };
}
