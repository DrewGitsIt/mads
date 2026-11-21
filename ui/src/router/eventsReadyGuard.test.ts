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
});
