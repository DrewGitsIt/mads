import { fork, type ChildProcess } from "node:child_process";
import path from "node:path";

/** One request as the proxy saw it (proxyServer.mts). Times are Date.now() ms. */
export interface ProxyRequest {
  id: number;
  method: string;
  path: string;
  query: string;
  sse: boolean;
  /** Answered by the proxy itself: the held-open /output stream. */
  held: boolean;
  delayMs: number;
  start: number;
  headers?: number;
  end?: number;
  status?: number;
  bytes: number;
  frames: number;
  aborted: boolean;
}

export interface ProxySnapshot {
  now: number;
  requests: ProxyRequest[];
  /** (time, requests in flight — so busy sockets) after every change. */
  timeline: [number, number][];
}

export interface DelayProxy {
  url: string;
  port: number;
  pid: number;
  setTarget(url: string): Promise<void>;
  snapshot(): Promise<ProxySnapshot>;
  /** Destroys every client socket at once (a sleep/wake; scenario 7). */
  cutAll(): Promise<void>;
  close(): Promise<void>;
}

/**
 * Starts the delay proxy in its own Node process (proxyServer.mts) so the
 * Playwright worker is never in the request path. `delays` is the
 * delayRules.ts spec ("" defaults, "off", or rules).
 */
export async function startDelayProxy(opts: { delays: string; holdOutput: boolean }): Promise<DelayProxy> {
  const child: ChildProcess = fork(path.join(__dirname, "proxyServer.mts"), [], {
    env: { ...process.env, PERF_DELAYS: opts.delays, PERF_HOLD_OUTPUT: opts.holdOutput ? "1" : "0" },
    // Plain node: the worker's own loader flags must not leak in.
    execArgv: ["--no-warnings"],
    stdio: ["ignore", "inherit", "inherit", "ipc"],
    detached: true,
  });
  let nextId = 1;
  const waiting = new Map<number, (v: unknown) => void>();
  const port = await new Promise<number>((resolve, reject) => {
    const t = setTimeout(() => reject(new Error("delay proxy did not start")), 10_000);
    child.once("exit", (code) => reject(new Error(`delay proxy exited (${code}) before it was ready`)));
    child.on("message", (m: { ready?: boolean; port?: number; id?: number; result?: unknown }) => {
      if (m.ready) {
        clearTimeout(t);
        resolve(m.port!);
      } else if (m.id !== undefined) {
        waiting.get(m.id)?.(m.result);
        waiting.delete(m.id);
      }
    });
  });
  const call = <T>(cmd: string, extra: Record<string, unknown> = {}) =>
    new Promise<T>((resolve, reject) => {
      if (!child.connected) return reject(new Error("delay proxy is gone"));
      const id = nextId++;
      waiting.set(id, resolve as (v: unknown) => void);
      child.send({ id, cmd, ...extra });
    });
  return {
    url: `http://127.0.0.1:${port}`,
    port,
    pid: child.pid!,
    setTarget: async (url) => {
      await call("setTarget", { url });
    },
    snapshot: () => call<ProxySnapshot>("snapshot"),
    cutAll: async () => {
      await call("cutAll");
    },
    close: async () => {
      if (child.exitCode !== null) return;
      const exited = new Promise((r) => child.once("exit", r));
      await call("close").catch(() => {});
      await Promise.race([exited, new Promise((r) => setTimeout(r, 3000))]);
      if (child.exitCode === null) child.kill("SIGKILL");
    },
  };
}
