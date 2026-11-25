import { readFileSync } from "node:fs";
import path from "node:path";

/**
 * The local front door for the e2e harness (docs/LOCAL-FRONT-DOOR-DESIGN.md
 * D7): every topology has its own $MADS_HOME and so its own install token.
 * Node-side calls (seed helpers, spec assertions) send it as a bearer, and
 * every browser context sends it as a header (the page fixture's
 * extraHTTPHeaders) — what Electron's main process adds below the page for
 * the real app (H7).
 */

const tokens = new Map<string, string>();

function originOf(url: string): string {
  return new URL(url).origin;
}

/** Records a topology's token for its Router and Data Service URLs. */
export function registerToken(urls: string[], token: string): void {
  for (const u of urls) tokens.set(originOf(u), token);
}

/** Reads $home/local.token. */
export function readTokenFile(home: string): string {
  return readFileSync(path.join(home, "local.token"), "utf8").trim();
}

/**
 * The install token for the topology serving url: one this process booted
 * (registerToken), else the shared topology globalSetup booted, whose
 * $MADS_HOME and URLs reach every worker through the environment.
 */
export function tokenFor(url: string): string {
  const origin = originOf(url);
  const known = tokens.get(origin);
  if (known) return known;
  const shared = [process.env.MADS_ROUTER_URL, process.env.MADS_DATA_URL].filter(Boolean).map((u) => originOf(u!));
  if (process.env.MADS_HOME && shared.includes(origin)) {
    const tok = readTokenFile(process.env.MADS_HOME);
    tokens.set(origin, tok);
    return tok;
  }
  return "";
}

/** fetch with the install token of whichever topology url belongs to. */
export function apiFetch(url: string, init: RequestInit = {}): Promise<Response> {
  const headers = new Headers(init.headers);
  const tok = tokenFor(url);
  if (tok && !headers.has("Authorization")) headers.set("Authorization", `Bearer ${tok}`);
  return fetch(url, { ...init, headers });
}
