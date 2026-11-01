import { extname, isAbsolute, join } from "node:path";
import { fileURLToPath } from "node:url";

/**
 * Where a link in rendered markdown points, read from the href exactly as the
 * agent wrote it. The renderer can't decide this from the anchor itself: the
 * browser resolves a relative `src/foo.ts` against the app's own origin
 * (the Router on localhost), which would make every file link look like a
 * localhost one. Pure, so main's opener and its tests share it.
 */
export type LinkTarget =
  | { kind: "web"; url: string }
  | { kind: "localhost"; url: string }
  /**
   * Paths to try in order: the path as written, then (when it ends in `:42`,
   * `:42:7` or `#L42`) the same path without the location — a file literally
   * named `foo.ts:42` is rare but legal, so it wins when it exists.
   */
  | { kind: "file"; candidates: string[] }
  /** Nothing mads opens: mailto:, javascript:, an in-page #anchor, or a relative path with no folder to resolve it against. */
  | { kind: "none" };

const LOOPBACK_HOSTS = new Set(["localhost", "127.0.0.1", "[::1]"]);

export function isLoopbackHost(hostname: string): boolean {
  return LOOPBACK_HOSTS.has(hostname) || hostname.endsWith(".localhost");
}

// A scheme is letters then ":" — but not when a number follows, which is a
// path with a line (`foo.ts:12`). "C:" style drive letters don't occur on
// the platforms mads runs on (macOS, Linux).
const SCHEME = /^[a-z][a-z0-9+.-]*:(?!\d)/i;
const LOCATION_SUFFIX = /(?::\d+(?::\d+)?|#L\d+(?:-L?\d+)?)$/;

/**
 * Resolves an href. `baseDir` is the session's folder (relative paths
 * resolve there); `home` expands a leading `~/`.
 */
export function resolveLinkTarget(href: string, baseDir: string | undefined, home: string): LinkTarget {
  const raw = href.trim();
  if (!raw || raw.startsWith("#")) return { kind: "none" };

  if (/^https?:/i.test(raw)) {
    let url: URL;
    try {
      url = new URL(raw);
    } catch {
      return { kind: "none" };
    }
    return { kind: isLoopbackHost(url.hostname) ? "localhost" : "web", url: url.href };
  }

  if (/^file:/i.test(raw)) {
    let path: string;
    try {
      // Rejects a remote host (file://server/x) and an encoded "/" in a segment.
      path = fileURLToPath(raw);
    } catch {
      return { kind: "none" };
    }
    return { kind: "file", candidates: withoutLocation(path) };
  }

  if (SCHEME.test(raw)) return { kind: "none" };

  // marked percent-encodes hrefs (a space becomes %20); the file on disk has the real characters.
  let path: string;
  try {
    path = decodeURIComponent(raw);
  } catch {
    path = raw;
  }
  // A query string means a URL, not a path; drop it rather than look for a file named "a?b".
  path = path.replace(/\?.*$/, "");
  if (path === "~" || path.startsWith("~/")) {
    path = join(home, path.slice(1));
  } else if (!isAbsolute(path)) {
    if (!baseDir) return { kind: "none" };
    path = join(baseDir, path);
  }
  return { kind: "file", candidates: withoutLocation(path) };
}

function withoutLocation(path: string): string[] {
  const stripped = path.replace(LOCATION_SUFFIX, "");
  return stripped !== path && stripped !== "" ? [path, stripped] : [path];
}

/**
 * Extensions whose default app runs the file rather than showing it. An
 * agent can write any file and link to it, and files it writes carry no
 * quarantine flag, so Gatekeeper never asks — this list is what makes mads
 * ask first. Lower-case, with the dot.
 */
const RUNNABLE_EXTENSIONS: Record<"darwin" | "linux", ReadonlySet<string>> = {
  darwin: new Set([
    ".app", ".command", ".tool", ".terminal", ".workflow", ".action", ".scpt", ".scptd",
    ".applescript", ".pkg", ".mpkg", ".dmg", ".prefpane", ".mobileconfig", ".shortcut",
    ".webloc", ".inetloc", ".fileloc", ".jar", ".jnlp", ".sh", ".zsh", ".bash", ".csh",
    ".py", ".pl", ".rb",
  ]),
  linux: new Set([
    ".desktop", ".appimage", ".run", ".bin", ".deb", ".rpm", ".snap", ".flatpakref",
    ".jar", ".jnlp", ".sh", ".bash", ".zsh", ".csh", ".py", ".pl", ".rb",
  ]),
};

/**
 * Whether opening this file with its default app could run something.
 * `realPath` must already have symlinks resolved (a `notes.txt` link to an
 * `.app` is an `.app`); `isFile`/`mode` come from a stat of it. A bundle
 * like `.app` is a directory, so the extension is checked whatever it is.
 */
export function isRunnable(
  realPath: string,
  stat: { isFile: boolean; mode: number },
  platform: NodeJS.Platform,
): boolean {
  const extensions = platform === "darwin" ? RUNNABLE_EXTENSIONS.darwin : RUNNABLE_EXTENSIONS.linux;
  if (extensions.has(extname(realPath).toLowerCase())) return true;
  return stat.isFile && (stat.mode & 0o111) !== 0;
}

/**
 * Splits a command line into argv the way a POSIX shell would for plain
 * words and quotes — no expansion, no operators. Settings stores the editor
 * as one string ("code -g", or a quoted path with spaces), and it is run
 * without a shell, so this is the only interpretation it gets. Returns
 * undefined for an empty or unbalanced command.
 */
export function splitCommand(command: string): string[] | undefined {
  const argv: string[] = [];
  let current = "";
  let inWord = false;
  let quote: '"' | "'" | undefined;
  for (let i = 0; i < command.length; i++) {
    const ch = command[i];
    if (quote === "'") {
      if (ch === "'") quote = undefined;
      else current += ch;
    } else if (quote === '"') {
      if (ch === '"') quote = undefined;
      else if (ch === "\\" && i + 1 < command.length && '"\\$`'.includes(command[i + 1])) current += command[++i];
      else current += ch;
    } else if (ch === "'" || ch === '"') {
      quote = ch;
      inWord = true;
    } else if (ch === "\\" && i + 1 < command.length) {
      current += command[++i];
      inWord = true;
    } else if (/\s/.test(ch)) {
      if (inWord) argv.push(current);
      current = "";
      inWord = false;
    } else {
      current += ch;
      inWord = true;
    }
  }
  if (quote) return undefined;
  if (inWord) argv.push(current);
  return argv.length > 0 ? argv : undefined;
}
