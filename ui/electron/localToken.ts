import { readFileSync, statSync } from "node:fs";
import { homedir } from "node:os";
import { join } from "node:path";

/**
 * The local front door (docs/LOCAL-FRONT-DOOR-DESIGN.md H7): main — never
 * the renderer — reads the install token and adds it as
 * `Authorization: Bearer <token>` to the window's requests to the Router,
 * below the page (session.webRequest). Page code can't read a header added
 * there, and there is no cookie.
 */

export function tokenPath(env: NodeJS.ProcessEnv = process.env): string {
  return join(env.MADS_HOME || join(homedir(), ".mads"), "local.token");
}

/**
 * The token, or "" when the file is missing, unreadable, or fails the same
 * checks the services apply: a regular file this user owns that nobody
 * else can read or write. `why` says which.
 */
export function readTokenChecked(path: string = tokenPath()): { token: string; why?: string } {
  let st;
  try {
    st = statSync(path);
  } catch {
    return { token: "", why: `no install token at ${path}` };
  }
  if (!st.isFile()) return { token: "", why: `${path} is not a regular file` };
  if ((st.mode & 0o077) !== 0) {
    return { token: "", why: `${path} is readable or writable by other users (mode ${(st.mode & 0o777).toString(8)}); chmod 600 it, then mads token rotate` };
  }
  const uid = process.getuid?.();
  if (uid !== undefined && st.uid !== uid) return { token: "", why: `${path} is owned by uid ${st.uid}, not you` };
  try {
    return { token: readFileSync(path, "utf8").trim() };
  } catch {
    return { token: "", why: `can't read ${path}` };
  }
}

/** The token, or "" (see readTokenChecked). */
export function readToken(path: string = tokenPath()): string {
  return readTokenChecked(path).token;
}

/** Headers for main's own calls to the Router. */
export function authHeaders(token: string): Record<string, string> {
  return token ? { Authorization: `Bearer ${token}` } : {};
}

/**
 * The webRequest URL filter for the Router's origin only: the token goes to
 * the Router and to nothing else the window loads.
 */
export function routerFilter(routerUrl: string): { urls: string[] } {
  return { urls: [`${new URL(routerUrl).origin}/*`] };
}

type HeaderDetails = { url: string; requestHeaders: Record<string, string | string[]> };
type HeaderCallback = (response: { requestHeaders: Record<string, string | string[]> }) => void;

/**
 * The onBeforeSendHeaders listener: adds the current token (token() is a
 * cached read, refreshed when the file changes) to requests to the Router's
 * origin — checked again here, not only by the filter — replacing any
 * Authorization the page set itself. Every other request passes untouched.
 */
export function bearerInjector(routerUrl: string, token: () => string): (details: HeaderDetails, callback: HeaderCallback) => void {
  const origin = new URL(routerUrl).origin;
  return (details, callback) => {
    let sameOrigin = false;
    try {
      sameOrigin = new URL(details.url).origin === origin;
    } catch {
      sameOrigin = false;
    }
    const tok = token();
    if (!sameOrigin || !tok) {
      callback({ requestHeaders: details.requestHeaders });
      return;
    }
    const headers: Record<string, string | string[]> = {};
    for (const [k, v] of Object.entries(details.requestHeaders)) {
      if (k.toLowerCase() !== "authorization") headers[k] = v;
    }
    headers.Authorization = `Bearer ${tok}`;
    callback({ requestHeaders: headers });
  };
}
