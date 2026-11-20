/**
 * The delay proxy's rules, shared by the proxy process (proxyServer.mts,
 * run by Node with type stripping — so this file must stay plain,
 * strippable TypeScript with no imports) and the collectors, which need to
 * know which requests the proxy holds on purpose.
 */

export interface DelayRule {
  /** Matched against the request path (no query string). */
  pattern: RegExp;
  delayMs: number;
  /** Methods it applies to; all when absent. */
  methods?: string[];
}

/** Defaults mimic the live measurements (docs/PERF-HARNESS-PLAN.md §2). */
export const DEFAULT_DELAY_RULES: DelayRule[] = [
  { pattern: /^\/sessions\/[^/]+\/repos$/, delayMs: 10_000, methods: ["GET"] },
  { pattern: /\/changeset$/, delayMs: 6_000, methods: ["GET"] },
  { pattern: /\/worktree\/refresh$/, delayMs: 16_000 },
];

/** "off", "" (defaults), or "<regex>=<ms>[,<regex>=<ms>...]". */
export function parseDelays(spec: string | undefined): DelayRule[] {
  if (spec === undefined || spec === "" || spec === "on") return DEFAULT_DELAY_RULES;
  if (spec === "off") return [];
  return spec.split(",").map((part) => {
    const i = part.lastIndexOf("=");
    if (i <= 0) throw new Error(`delays: bad rule ${JSON.stringify(part)} (want <regex>=<ms>)`);
    return { pattern: new RegExp(part.slice(0, i)), delayMs: Number(part.slice(i + 1)) };
  });
}

export function delayFor(rules: DelayRule[], method: string, path: string): number {
  const r = rules.find((x) => x.pattern.test(path) && (!x.methods || x.methods.includes(method)));
  return r?.delayMs ?? 0;
}

/**
 * The open conversation's data-plane stream (GET /sessions/{id}/output and
 * its ?v=2 keyed variant). A seeded session has no live child, so the
 * Router would answer 404 at once; live, the stream is held open and holds
 * one of the six sockets for as long as the conversation is on screen. The
 * proxy answers it itself with a held-open SSE stream that only sends
 * heartbeats (PERF_HOLD_OUTPUT=0 turns this off).
 */
export const HELD_OUTPUT = /^\/sessions\/[^/]+\/output$/;
