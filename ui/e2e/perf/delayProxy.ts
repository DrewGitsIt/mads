import { createServer, request as httpRequest, Agent, type IncomingMessage, type Server, type ServerResponse } from "node:http";
import type { Socket } from "node:net";

/**
 * A reverse proxy between the browser and the Router (docs/PERF-HARNESS-PLAN.md
 * §2). The page's origin is the proxy, so Chromium's 6-sockets-per-origin
 * limit applies to it exactly as it does to the Router in the real app.
 *
 * Path rules add latency AFTER the request is accepted — the browser's
 * socket is held for the whole delay, the way a slow handler holds it — to
 * mimic the live app's slow `repos`, `changeset` and `worktree/refresh`.
 * Everything else, SSE included, streams through unbuffered with every
 * header passed on (the bearer token among them). Not usable for an HTTP/2
 * spike.
 *
 * It also counts what it sees, independently of the browser: open client
 * sockets, in-flight requests, and bytes relayed per request.
 */

export interface DelayRule {
  /** Matched against the request path (no query string). */
  pattern: RegExp;
  delayMs: number;
  /** Methods it applies to; all when absent. */
  methods?: string[];
}

export const DEFAULT_DELAY_RULES: DelayRule[] = [
  { pattern: /^\/sessions\/[^/]+\/repos$/, delayMs: 10_000, methods: ["GET"] },
  { pattern: /\/changeset$/, delayMs: 6_000, methods: ["GET"] },
  { pattern: /\/worktree\/refresh$/, delayMs: 16_000 },
];

/**
 * Rules from PERF_DELAYS: "off", or "<regex>=<ms>[,<regex>=<ms>...]";
 * unset is the defaults.
 */
export function rulesFromEnv(spec = process.env.PERF_DELAYS): DelayRule[] {
  if (spec === undefined || spec === "") return DEFAULT_DELAY_RULES;
  if (spec === "off") return [];
  return spec.split(",").map((part) => {
    const i = part.lastIndexOf("=");
    if (i <= 0) throw new Error(`PERF_DELAYS: bad rule ${JSON.stringify(part)} (want <regex>=<ms>)`);
    return { pattern: new RegExp(part.slice(0, i)), delayMs: Number(part.slice(i + 1)) };
  });
}

/** One request as the proxy saw it. Times are Date.now() milliseconds. */
export interface ProxyRequest {
  id: number;
  socket: number;
  method: string;
  path: string;
  query: string;
  sse: boolean;
  delayMs: number;
  start: number;
  /** When the Router's response headers reached the proxy. */
  headers?: number;
  end?: number;
  status?: number;
  bytes: number;
  /** SSE frames relayed (blank-line terminated), for a stream. */
  frames: number;
  /** bytes/frames already relayed when the current window started. */
  baseBytes: number;
  baseFrames: number;
  aborted?: boolean;
}

export interface ProxyCounters {
  /** Client (browser) sockets open right now, and the most at once. */
  openSockets: number;
  peakSockets: number;
  inFlight: number;
  peakInFlight: number;
}

export interface DelayProxy {
  url: string;
  port: number;
  setTarget(url: string): void;
  setRules(rules: DelayRule[]): void;
  /** Every request seen, in arrival order. */
  log(): ProxyRequest[];
  /**
   * Starts a measured window: drops finished requests, baselines the bytes
   * and frames of the ones still open, and restarts the peaks.
   */
  startWindow(): void;
  counters(): ProxyCounters;
  /**
   * Peak open client sockets and in-flight requests between two Date.now()
   * instants, from the proxy's own timeline.
   */
  peaksBetween(from: number, to: number): { sockets: number; inFlight: number };
  /** Destroys every client socket at once (a sleep/wake; scenario 7). */
  cutAll(): void;
  close(): Promise<void>;
}

export async function startDelayProxy(opts: { rules?: DelayRule[]; target?: string } = {}): Promise<DelayProxy> {
  let target = opts.target ? new URL(opts.target) : undefined;
  let rules = opts.rules ?? DEFAULT_DELAY_RULES;
  // Upstream sockets are not the thing under test: never queue on them.
  const agent = new Agent({ keepAlive: true, maxSockets: Infinity });
  const sockets = new Map<Socket, number>();
  let nextSocket = 1;
  let nextReq = 1;
  let requests: ProxyRequest[] = [];
  const counters: ProxyCounters = { openSockets: 0, peakSockets: 0, inFlight: 0, peakInFlight: 0 };
  // (time, sockets, inFlight) after every change, for peaksBetween.
  let timeline: [number, number, number][] = [];
  const mark = () => {
    timeline.push([Date.now(), counters.openSockets, counters.inFlight]);
    counters.peakSockets = Math.max(counters.peakSockets, counters.openSockets);
    counters.peakInFlight = Math.max(counters.peakInFlight, counters.inFlight);
  };

  const server: Server = createServer((req: IncomingMessage, res: ServerResponse) => {
    void handle(req, res);
  });
  server.on("connection", (socket: Socket) => {
    socket.setNoDelay(true);
    sockets.set(socket, nextSocket++);
    counters.openSockets++;
    mark();
    socket.on("close", () => {
      sockets.delete(socket);
      counters.openSockets--;
      mark();
    });
  });

  async function handle(req: IncomingMessage, res: ServerResponse): Promise<void> {
    const url = new URL(req.url ?? "/", "http://proxy");
    const method = req.method ?? "GET";
    const rule = rules.find((r) => r.pattern.test(url.pathname) && (!r.methods || r.methods.includes(method)));
    const rec: ProxyRequest = {
      id: nextReq++,
      socket: sockets.get(req.socket) ?? 0,
      method,
      path: url.pathname,
      query: url.search,
      sse: (req.headers.accept ?? "").includes("text/event-stream"),
      delayMs: rule?.delayMs ?? 0,
      start: Date.now(),
      bytes: 0,
      frames: 0,
      baseBytes: 0,
      baseFrames: 0,
    };
    requests.push(rec);
    counters.inFlight++;
    mark();
    let finished = false;
    const finish = (aborted: boolean) => {
      if (finished) return;
      finished = true;
      rec.end = Date.now();
      if (aborted) rec.aborted = true;
      counters.inFlight--;
      mark();
    };
    res.on("close", () => finish(!res.writableFinished));

    // Buffer the body while the delay holds the socket: a slow handler has
    // read its request before it stalls.
    const body: Buffer[] = [];
    req.on("data", (c: Buffer) => body.push(c));
    await new Promise<void>((r) => (req.readableEnded ? r() : req.on("end", () => r())));
    if (rec.delayMs > 0) await new Promise((r) => setTimeout(r, rec.delayMs));
    if (res.destroyed || req.socket.destroyed) {
      finish(true);
      return;
    }
    if (!target) {
      res.writeHead(503, { "Content-Type": "text/plain" });
      res.end("delay proxy: no target yet");
      return;
    }
    const upstream = httpRequest(
      {
        agent,
        host: target.hostname,
        port: target.port,
        method,
        path: req.url,
        // Every header as sent, Host included: the Router's guard accepts
        // any loopback Host, and the proxy's origin is on its allow-list.
        headers: req.headers,
      },
      (up) => {
        rec.headers = Date.now();
        rec.status = up.statusCode;
        res.writeHead(up.statusCode ?? 502, up.statusMessage, up.headers);
        // An SSE response must reach the page frame by frame.
        res.flushHeaders();
        let prevNL = false;
        up.on("data", (chunk: Buffer) => {
          rec.bytes += chunk.length;
          if (rec.sse) {
            // Frames end in a blank line; JSON data lines never hold a raw newline.
            for (let i = 0; i < chunk.length; i++) {
              const nl = chunk[i] === 10;
              if (nl && prevNL) rec.frames++;
              prevNL = nl;
            }
          }
          res.write(chunk);
        });
        up.on("end", () => res.end());
        up.on("error", () => res.destroy());
      },
    );
    upstream.on("error", (err) => {
      if (!res.headersSent) {
        res.writeHead(502, { "Content-Type": "text/plain" });
        res.end(`delay proxy: upstream: ${err.message}`);
      } else {
        res.destroy();
      }
    });
    // The page went away (a closed EventSource, an aborted fetch): stop the
    // Router's side too, as a direct connection would.
    res.on("close", () => {
      if (!res.writableFinished) upstream.destroy();
    });
    for (const c of body) upstream.write(c);
    upstream.end();
  }

  await new Promise<void>((r) => server.listen(0, "127.0.0.1", r));
  const port = (server.address() as { port: number }).port;
  mark();

  return {
    url: `http://127.0.0.1:${port}`,
    port,
    setTarget(u) {
      target = new URL(u);
    },
    setRules(r) {
      rules = r;
    },
    log: () => requests,
    startWindow() {
      requests = requests.filter((r) => r.end === undefined);
      for (const r of requests) {
        r.baseBytes = r.bytes;
        r.baseFrames = r.frames;
      }
      counters.peakSockets = counters.openSockets;
      counters.peakInFlight = counters.inFlight;
      timeline = [[Date.now(), counters.openSockets, counters.inFlight]];
    },
    counters: () => ({ ...counters }),
    peaksBetween(from, to) {
      let sockets = 0;
      let inFlight = 0;
      let before: [number, number, number] | undefined;
      for (const t of timeline) {
        if (t[0] < from) {
          before = t;
          continue;
        }
        if (t[0] > to) break;
        sockets = Math.max(sockets, t[1]);
        inFlight = Math.max(inFlight, t[2]);
      }
      // The state already in force when the window opened counts too.
      if (before) {
        sockets = Math.max(sockets, before[1]);
        inFlight = Math.max(inFlight, before[2]);
      }
      return { sockets, inFlight };
    },
    cutAll() {
      for (const s of sockets.keys()) s.destroy();
    },
    async close() {
      for (const s of sockets.keys()) s.destroy();
      agent.destroy();
      await new Promise<void>((r) => server.close(() => r()));
    },
  };
}
