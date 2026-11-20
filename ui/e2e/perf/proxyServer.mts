// The delay proxy (docs/PERF-HARNESS-PLAN.md §2), as its own process: the
// Playwright worker only talks to it over IPC, so nothing in the measured
// path waits on the worker's event loop. Run by Node with type stripping:
//   node proxyServer.mts   (spawned by proxyClient.ts with an IPC channel)
//
// Between the browser and the Router: the page's origin is this proxy, so
// Chromium's 6-sockets-per-origin limit applies to it as it does to the
// Router live. Path rules add latency after the request is accepted — the
// browser's socket is held for the whole delay, the way a slow handler holds
// it. Everything else, SSE included, is piped through (backpressure
// honoured) with every header, the bearer token among them. The open
// conversation's /output stream is answered here, held open with heartbeats
// (delayRules.ts HELD_OUTPUT). It records every request: arrival, delay,
// response headers, end, bytes and SSE frames relayed, and a timeline of how
// many requests (so sockets: HTTP/1.1, no pipelining) were busy at once.

import { Agent, createServer, request as httpRequest } from "node:http";
import { delayFor, HELD_OUTPUT, parseDelays } from "./delayRules.ts";

const rules = parseDelays(process.env.PERF_DELAYS);
const holdOutput = process.env.PERF_HOLD_OUTPUT !== "0";
let target: URL | undefined;

const agent = new Agent({ keepAlive: true, maxSockets: Infinity });
const sockets = new Set();
let nextReq = 1;
const requests = [];
let busy = 0;
const timeline = [[Date.now(), 0]];
const mark = (d) => {
  busy += d;
  timeline.push([Date.now(), busy]);
};

const server = createServer((req, res) => {
  const url = new URL(req.url ?? "/", "http://proxy");
  const method = req.method ?? "GET";
  const held = holdOutput && method === "GET" && HELD_OUTPUT.test(url.pathname);
  const rec = {
    id: nextReq++,
    method,
    path: url.pathname,
    query: url.search,
    sse: held || (req.headers.accept ?? "").includes("text/event-stream"),
    held,
    delayMs: held ? 0 : delayFor(rules, method, url.pathname),
    start: Date.now(),
    headers: undefined,
    end: undefined,
    status: undefined,
    bytes: 0,
    frames: 0,
    aborted: false,
  };
  requests.push(rec);
  mark(1);
  let done = false;
  res.on("close", () => {
    if (done) return;
    done = true;
    rec.end = Date.now();
    rec.aborted = !res.writableFinished;
    mark(-1);
  });

  if (held) {
    rec.headers = Date.now();
    rec.status = 200;
    res.writeHead(200, { "Content-Type": "text/event-stream", "Cache-Control": "no-cache" });
    res.write(": held open by the perf proxy\n\n");
    const hb = setInterval(() => res.write(": heartbeat\n\n"), 15_000);
    res.on("close", () => clearInterval(hb));
    req.resume();
    return;
  }

  // The body is read while the delay holds the socket: a slow handler has
  // read its request before it stalls. Bodies here are small.
  const body = [];
  req.on("data", (c) => body.push(c));
  req.on("end", () => {
    setTimeout(forward, rec.delayMs);
  });

  function forward() {
    if (res.destroyed || done) return;
    if (!target) {
      res.writeHead(503, { "Content-Type": "text/plain" });
      res.end("delay proxy: no target yet");
      return;
    }
    const upstream = httpRequest(
      { agent, host: target.hostname, port: target.port, method, path: req.url, headers: req.headers },
      (up) => {
        rec.headers = Date.now();
        rec.status = up.statusCode;
        res.writeHead(up.statusCode ?? 502, up.statusMessage, up.headers);
        res.flushHeaders();
        let prevNL = false;
        up.on("data", (chunk) => {
          rec.bytes += chunk.length;
          if (rec.sse) {
            // Frames end in a blank line; JSON data lines never hold a raw newline.
            for (let i = 0; i < chunk.length; i++) {
              const nl = chunk[i] === 10;
              if (nl && prevNL) rec.frames++;
              prevNL = nl;
            }
          }
        });
        // pipe: a slow reader pauses the Router's side instead of the proxy
        // buffering an SSE replay in memory.
        up.pipe(res);
        up.on("error", () => res.destroy());
      },
    );
    upstream.on("error", (err) => {
      if (!res.headersSent) {
        res.writeHead(502, { "Content-Type": "text/plain" });
        res.end(`delay proxy: upstream: ${err.message}`);
      } else res.destroy();
    });
    // The page went away (a closed EventSource, an aborted fetch): stop the
    // Router's side too, as a direct connection would.
    res.on("close", () => {
      if (!res.writableFinished) upstream.destroy();
    });
    for (const c of body) upstream.write(c);
    upstream.end();
  }
});

server.on("connection", (s) => {
  s.setNoDelay(true);
  sockets.add(s);
  s.on("close", () => sockets.delete(s));
});

process.on("message", (msg) => {
  const reply = (result) => process.send({ id: msg.id, result });
  switch (msg.cmd) {
    case "setTarget":
      target = new URL(msg.url);
      return reply(true);
    case "snapshot":
      return reply({ now: Date.now(), requests, timeline });
    case "cutAll":
      for (const s of sockets) s.destroy();
      return reply(true);
    case "close":
      for (const s of sockets) s.destroy();
      agent.destroy();
      server.close(() => process.exit(0));
      setTimeout(() => process.exit(0), 2000).unref();
      return reply(true);
  }
});
// The worker died: never outlive it.
process.on("disconnect", () => process.exit(0));

server.listen(0, "127.0.0.1", () => {
  process.send({ ready: true, port: server.address().port });
});
