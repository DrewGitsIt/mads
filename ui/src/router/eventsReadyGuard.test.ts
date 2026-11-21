import { describe, expect, it } from "vitest";
import { readdirSync, readFileSync, statSync } from "node:fs";
import { join, relative } from "node:path";

// The bus is live-only (THE CONTRACT in events.ts): it never replays history,
// so a subscriber that also reads state on mount must do it after
// eventsReady() — subscribe, await ready, read, merge — or an event that lands
// between the read and the subscribe is lost. This scans every non-test file
// that calls subscribeToEvents( and requires it to reference eventsReady in
// code (comments don't count), or to be on the allowlist below.

const SRC = "src";

// Live-only subscribers: no mount read in the file, so nothing to order
// against the ready barrier. Keyed by path under src/.
const LIVE_ONLY: Record<string, string> = {
  // Mount health arrives as a prop from useCapabilities (which reads behind
  // eventsReady); each event refetches the capability, so no read to order.
  "library/capabilities/HealthDot.tsx": "health from props; refetch per event",
  // Reacts to session.deleted by ending that session's shell; there is no
  // state to load, only an action per event.
  "conversation/ShellDock/useEndDeletedSessionShells.ts": "action per event, no state",
};

function sourceFiles(dir: string): string[] {
  const out: string[] = [];
  for (const name of readdirSync(dir)) {
    const path = join(dir, name);
    if (statSync(path).isDirectory()) out.push(...sourceFiles(path));
    else if (/\.tsx?$/.test(name) && !/\.test\.tsx?$/.test(name) && !name.endsWith(".d.ts")) out.push(path);
  }
  return out;
}

// Source with block and line comments removed (a `//` after `:` or a quote is
// left alone, so URLs in strings survive).
function code(text: string): string {
  return text.replace(/\/\*[\s\S]*?\*\//g, "").replace(/(^|[^:"'`])\/\/[^\n]*/g, "$1");
}

describe("eventsReady guard", () => {
  it("every subscribeToEvents caller references eventsReady or is an allowlisted live-only subscriber", () => {
    const callers: string[] = [];
    const failures: string[] = [];
    for (const file of sourceFiles(SRC)) {
      if (file === join(SRC, "router", "events.ts")) continue;
      const body = code(readFileSync(file, "utf8"));
      if (!/\bsubscribeToEvents\(/.test(body)) continue;
      const rel = relative(SRC, file);
      callers.push(rel);
      if (/\beventsReady/.test(body) || rel in LIVE_ONLY) continue;
      failures.push(
        `${rel} calls subscribeToEvents( but never references eventsReady. ` +
          `If it reads state on mount, do subscribe → await eventsReady() → read → merge ` +
          `(see THE CONTRACT in router/events.ts), in this file or by calling the module that does the read ` +
          `from here; if it has no mount read, add it to LIVE_ONLY in this test with a one-line reason.`,
      );
    }
    // Sanity: the scan found the callers (there are ~40); a regex that
    // silently matched nothing would pass vacuously.
    expect(callers.length).toBeGreaterThan(30);
    expect(failures, failures.join("\n")).toEqual([]);
    // A stale allowlist entry hides nothing today but would excuse a future
    // file at that path: keep it to files that still subscribe.
    const stale = Object.keys(LIVE_ONLY).filter((rel) => !callers.includes(rel));
    expect(stale, `LIVE_ONLY entries that no longer call subscribeToEvents(: ${stale.join(", ")}`).toEqual([]);
  });

  // Mesh mode: a subscription with a sessionId goes on that session's bus,
  // which for a remote session is its host's log, with its own barrier. Its
  // REST read must wait for that bus (eventsReady(<session>)), not this
  // device's: the local barrier says nothing about where the peer's log
  // stood when the snapshot was served.
  //
  // What this can check, textually: each subscribeToEvents( call's first
  // argument is an object literal (anything else — a variable, a spread at
  // the top — can't be read here and must be allowlisted in NON_LITERAL);
  // a call is session-scoped when `sessionId` appears anywhere inside that
  // literal; and a file with one must call eventsReady with an argument
  // that names a session (an identifier or property path containing
  // "session", e.g. sessionId, open.sessionId) and never bare. It can't
  // tell which eventsReady call orders which read, nor follow a session id
  // renamed to something without "session" in it.
  it("every file that subscribes with a sessionId waits on that session's barrier", () => {
    const failures: string[] = [];
    let scoped = 0;
    for (const file of sourceFiles(SRC)) {
      if (file === join(SRC, "router", "events.ts")) continue;
      const body = code(readFileSync(file, "utf8"));
      const rel = relative(SRC, file);
      let fileScoped = false;
      for (const call of body.matchAll(/\bsubscribeToEvents\(\s*/g)) {
        const at = call.index + call[0].length;
        if (body[at] !== "{") {
          if (!(rel in NON_LITERAL)) {
            failures.push(`${rel}: subscribeToEvents( with a first argument that isn't an object literal: write the params inline, or allowlist the file in NON_LITERAL with a reason.`);
          }
          continue;
        }
        if (/\bsessionId\b/.test(braced(body, at))) fileScoped = true;
      }
      if (!fileScoped) continue;
      scoped++;
      if (rel in LIVE_ONLY || rel in SESSION_BARRIER_EXEMPT) continue;
      const args = [...body.matchAll(/\beventsReady\(([^)]*)\)/g)].map((m) => m[1].trim());
      if (!args.some((a) => /^[\w.?]*session[\w.?]*$/i.test(a))) {
        failures.push(`${rel} subscribes with a sessionId but never calls eventsReady(<session id>): pass the session id, so a remote session's read waits for its host's bus.`);
      }
      if (args.some((a) => a === "")) {
        failures.push(`${rel} subscribes with a sessionId and also calls eventsReady() with no session: if that read is ordered against the session's events, pass the id (or exempt it in SESSION_BARRIER_EXEMPT with a reason).`);
      }
    }
    expect(scoped).toBeGreaterThan(10);
    expect(failures, failures.join("\n")).toEqual([]);
  });
});

// The balanced {...} starting at text[open] === "{".
function braced(text: string, open: number): string {
  let depth = 0;
  for (let i = open; i < text.length; i++) {
    if (text[i] === "{") depth++;
    else if (text[i] === "}" && --depth === 0) return text.slice(open, i + 1);
  }
  return text.slice(open);
}

// Files whose subscribeToEvents( first argument isn't an object literal,
// with why that's fine. Keyed by path under src/.
const NON_LITERAL: Record<string, string> = {};

// Files that subscribe with a sessionId and legitimately also wait on this
// device's barrier (a read ordered against the local log). Keyed by path.
const SESSION_BARRIER_EXEMPT: Record<string, string> = {};
