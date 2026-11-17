// The Vite dev server's same-origin proxy to the Router
// (docs/LOCAL-FRONT-DOOR-DESIGN.md H3, H7). The dev page calls its own
// origin; this forwards every Router API path to the Router and **adds no
// credential of its own**: a request carries whatever it already had and
// nothing else, so a process that reaches localhost:5173 without the token
// gets the Router's 401, exactly as it would at the Router itself. The dev
// page runs inside Electron (VITE_DEV_SERVER_URL): set VITE_ROUTER_URL to
// the Router and the page calls it directly, with the bearer Electron's
// main process adds for the Router's origin.
//
// Browser headers (Origin, Sec-Fetch-*) are forwarded untouched, so the
// Router's localguard still sees a dev-server page — start the Router with
// -allowed-origins http://localhost:5173 as before — and still refuses a
// foreign page that tries to reach the Router through the dev server.
import type { ProxyOptions } from "vite";

/**
 * Every top-level path segment the Router serves an API under
 * (services/router/internal/api/api.go). services/router's front-door test
 * fails if a new one is missing here.
 */
export const ROUTER_API_PREFIXES = [
  "accounts",
  "agents",
  "attachment-drafts",
  "capabilities",
  "children",
  "config",
  "credentials",
  "delegates",
  "events",
  "fast-model",
  "health",
  "imports",
  "jobs",
  "link-digests",
  "links",
  "metrics",
  "pipelines",
  "preflight",
  "profiles",
  "proposals",
  "providers",
  "registry",
  "retros",
  "sessions",
  "tasks",
  "templates",
  "update",
  "version",
  "workspaces",
];

/** Matches a Router API path (and its query), never a dev page such as /tasks.html. */
export const ROUTER_API_PATTERN = `^/(?:${ROUTER_API_PREFIXES.join("|")})(?:[/?]|$)`;

export function routerProxy(
  target: string = process.env.MADS_ROUTER_URL || "http://localhost:17373",
): Record<string, ProxyOptions> {
  return {
    [ROUTER_API_PATTERN]: { target, changeOrigin: false },
  };
}
