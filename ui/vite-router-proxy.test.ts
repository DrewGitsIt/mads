// @vitest-environment node
import { createServer, type IncomingMessage, type Server } from "node:http";
import { afterEach, describe, expect, it } from "vitest";
import { createServer as createViteServer, type ViteDevServer } from "vite";
import { ROUTER_API_PATTERN, routerProxy } from "./vite-router-proxy";

describe("vite router proxy", () => {
  const closers: (() => Promise<void>)[] = [];
  afterEach(async () => {
    for (const c of closers.splice(0)) await c();
  });

  it("matches Router API paths, not dev pages", () => {
    const re = new RegExp(ROUTER_API_PATTERN);
    for (const p of ["/sessions", "/sessions/ses_1/output?v=2", "/tasks?session_id=x", "/events?since=1", "/health", "/children"]) {
      expect(re.test(p), p).toBe(true);
    }
    for (const p of ["/", "/tasks.html", "/files.html", "/src/main.tsx", "/sessionsX", "/@vite/client"]) {
      expect(re.test(p), p).toBe(false);
    }
  });

  it("forwards the request's own bearer and browser headers, adding no credential", async () => {
    const seen: IncomingMessage[] = [];
    const upstream: Server = createServer((req, res) => {
      seen.push(req);
      res.end("{}");
    });
    await new Promise<void>((r) => upstream.listen(0, "127.0.0.1", r));
    closers.push(() => new Promise((r) => upstream.close(() => r())));
    const addr = upstream.address() as { port: number };

    const vite: ViteDevServer = await createViteServer({
      configFile: false,
      logLevel: "silent",
      server: { port: 0, host: "127.0.0.1", proxy: routerProxy(`http://127.0.0.1:${addr.port}`) },
    });
    await vite.listen();
    closers.push(() => vite.close());
    const base = vite.resolvedUrls!.local[0].replace(/\/$/, "");

    await fetch(`${base}/sessions?x=1`, {
      headers: { Origin: "http://localhost:5173", Authorization: "Bearer the-requests-own" },
    });
    await fetch(`${base}/config`);
    expect(seen.map((r) => `${r.url} ${r.headers.authorization} ${r.headers.cookie}`)).toEqual([
      "/sessions?x=1 Bearer the-requests-own undefined",
      "/config undefined undefined",
    ]);
    expect(seen[0].headers.origin).toBe("http://localhost:5173");
  });

  // Adversarial (front door D7 vs. the doc's threat model, fixed by H3): the
  // dev proxy used to add the install token to *every* request reaching it,
  // including ones from no browser at all. While `npm run dev` runs, any
  // local process — another OS user's included, who can reach loopback but
  // can't read ~/.mads — gets the whole Router API by talking to
  // localhost:5173 instead of 17373: no Origin and no Sec-Fetch-Site, so the
  // Router's localguard waves it through, and the bearer does the rest.
  // Origin can't tell them apart either (curl can send any Origin), so the
  // proxy adds nothing: a request carrying no credential gets none.
  it("gives a request from no browser at all no credential", async () => {
    const seen: IncomingMessage[] = [];
    const upstream: Server = createServer((req, res) => {
      seen.push(req);
      res.end("{}");
    });
    await new Promise<void>((r) => upstream.listen(0, "127.0.0.1", r));
    closers.push(() => new Promise((r) => upstream.close(() => r())));
    const addr = upstream.address() as { port: number };
    const vite: ViteDevServer = await createViteServer({
      configFile: false,
      logLevel: "silent",
      server: { port: 0, host: "127.0.0.1", proxy: routerProxy(`http://127.0.0.1:${addr.port}`) },
    });
    await vite.listen();
    closers.push(() => vite.close());
    const base = vite.resolvedUrls!.local[0].replace(/\/$/, "");

    // What `curl http://localhost:5173/credentials` from any local user sends.
    await fetch(`${base}/credentials`);
    expect(seen).toHaveLength(1);
    expect(seen[0].headers.origin).toBeUndefined();
    expect(seen[0].headers["sec-fetch-site"]).toBeUndefined();
    expect(seen[0].headers.authorization, "a request from no browser was given the install token").toBeUndefined();
    expect(seen[0].headers.cookie, "a request from no browser was given a credential").toBeUndefined();
  });
});
