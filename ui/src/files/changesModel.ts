// Pure helpers for the Changes tab and the Context Rail's Changes list
// (docs/CHANGES-DESIGN.md, docs/mockups/changes-v2.html). Tiers, turns,
// line counts and commits come from the server's ChangeSet; nothing here
// re-derives them — it only sorts, filters, clips and steps.
import { CHANGE_TIERS, type ChangeSet, type ChangeTier, type ChangeTurn, type ChangedFile } from "../router/types";

export const TIER_LABEL: Record<ChangeTier, string> = {
  source: "Source",
  tests: "Tests & fixtures",
  config: "Config & dependencies",
  generated: "Generated",
  scratch: "Scratch",
};

/** Section descriptions, as the mockup words them; tiers without one show none. */
export const TIER_DESC: Partial<Record<ChangeTier, string>> = {
  source: "Hand-written code and docs: read these first",
  generated: "Lockfiles, snapshots, mocks, *.gen.*: matched by linguist-generated and path rules",
  scratch: "Created this session and matched a scratch rule",
};

export const KIND_LETTER: Record<ChangedFile["change"], "A" | "M" | "D"> = {
  added: "A",
  modified: "M",
  deleted: "D",
};

/** Generated and scratch rows sink and dim in every grouping. */
export function isLowTier(f: Pick<ChangedFile, "tier">): boolean {
  return f.tier === "generated" || f.tier === "scratch";
}

export function tierRank(tier: ChangeTier): number {
  const i = CHANGE_TIERS.indexOf(tier);
  return i < 0 ? CHANGE_TIERS.length : i;
}

/** Lines touched — the size every sort and size bar uses. */
export function fileSize(f: Pick<ChangedFile, "added" | "deleted">): number {
  return f.added + f.deleted;
}

/** Tier order first, then size descending, then path for a stable order. */
export function compareTierThenSize(a: ChangedFile, b: ChangedFile): number {
  return (
    tierRank(a.tier) - tierRank(b.tier) ||
    fileSize(b) - fileSize(a) ||
    a.display.localeCompare(b.display)
  );
}

export function sortTierThenSize(files: ChangedFile[]): ChangedFile[] {
  return files.slice().sort(compareTierThenSize);
}

/** Size descending (the rail's order), path as the tiebreak. */
export function sortBySize(files: ChangedFile[]): ChangedFile[] {
  return files.slice().sort((a, b) => fileSize(b) - fileSize(a) || a.display.localeCompare(b.display));
}

export function basename(path: string): string {
  const trimmed = path.endsWith("/") ? path.slice(0, -1) : path;
  const i = trimmed.lastIndexOf("/");
  return i === -1 ? trimmed : trimmed.slice(i + 1);
}

export function dirname(path: string): string {
  const i = path.lastIndexOf("/");
  if (i === -1) return "";
  return i === 0 ? "/" : path.slice(0, i);
}

export const PROMPT_CHARS = 120;

/** The first ~120 chars of a prompt, on one line, with an ellipsis when cut. */
export function clipPrompt(prompt: string, max = PROMPT_CHARS): string {
  const one = prompt.replace(/\s+/g, " ").trim();
  return one.length > max ? one.slice(0, max - 1).trimEnd() + "…" : one;
}


/** Sum of +/− over files; null-safe for binary / unknown counts (both zero). */
export function sumLines(files: ChangedFile[]): { added: number; deleted: number } {
  let added = 0;
  let deleted = 0;
  for (const f of files) {
    added += f.added;
    deleted += f.deleted;
  }
  return { added, deleted };
}

/** Size bar width in px: sqrt scale, clamped 4–44 (mockup sizeBar). */
export function sizeBarWidth(added: number, deleted: number): number {
  return Math.max(4, Math.min(44, Math.round(Math.sqrt(added + deleted) * 2.2)));
}

const isWordChar = (c: string | undefined) => c !== undefined && /[\p{L}\p{N}]/u.test(c);
const isUpper = (c: string | undefined) => c !== undefined && c !== c.toLowerCase();

/**
 * Whether `query` (already lowercased) appears in `text` as a whole token:
 * not continuing a word on either side. A camelCase hump counts as a word
 * edge, so "changes" matches ChangesTab.tsx, while ".ts" matches foo.ts but
 * not foo.tsx and "build" matches build.go but not builder.go.
 */
export function tokenMatch(text: string, query: string): boolean {
  const lower = text.toLowerCase();
  if (!query) return true;
  const startsWord = isWordChar(query[0]);
  const endsWord = isWordChar(query[query.length - 1]);
  for (let i = lower.indexOf(query); i >= 0; i = lower.indexOf(query, i + 1)) {
    const j = i + query.length;
    const before = text[i - 1];
    const after = text[j];
    const startOk = !startsWord || !isWordChar(before) || (isUpper(text[i]) && !isUpper(before));
    const endOk = !endsWord || !isWordChar(after) || (isUpper(after) && !isUpper(text[j - 1]));
    if (startOk && endOk) return true;
  }
  return false;
}

export interface FilterResult {
  files: ChangedFile[];
  /**
   * Files the query matched only partway through a word, left out because
   * some file matched it as a whole token. 0 when partial matches are shown.
   */
  partial: number;
}

/**
 * Files that pass the list's filter box and (outside Turn grouping) the
 * strip's turn filter. Whole-token matches win: when any file matches the
 * query as a token, partial (substring) matches are hidden unless
 * `includePartial`, and counted. When none does, substring matching applies.
 */
export function filterFiles(
  files: ChangedFile[],
  opts: { query?: string; turns?: number[] | null; includePartial?: boolean },
): FilterResult {
  const q = opts.query?.trim().toLowerCase();
  const turns = opts.turns && opts.turns.length ? opts.turns : null;
  const inTurns = turns ? files.filter((f) => f.turns.some((t) => turns.includes(t))) : files;
  if (!q) return { files: inTurns, partial: 0 };
  const matched = inTurns.filter((f) => f.display.toLowerCase().includes(q));
  if (opts.includePartial) return { files: matched, partial: 0 };
  const exact = matched.filter((f) => tokenMatch(f.display, q));
  if (exact.length === 0) return { files: matched, partial: 0 };
  return { files: exact, partial: matched.length - exact.length };
}

/**
 * The strip's next selection after a click on turn `n`. A plain click
 * selects just `n`, or clears when `n` was the sole selection; shift-click
 * extends the current selection into a contiguous range (in strip order).
 * `order` is every turn number in strip order.
 */
export function nextTurnSelection(
  current: number[] | null,
  n: number,
  shift: boolean,
  order: number[],
): number[] | null {
  if (shift && current && current.length) {
    const idx = [current[0], current[current.length - 1], n].map((t) => order.indexOf(t)).filter((i) => i >= 0);
    const lo = Math.min(...idx);
    const hi = Math.max(...idx);
    return order.slice(lo, hi + 1);
  }
  if (current && current.length === 1 && current[0] === n) return null;
  return [n];
}

/**
 * One arrow step in filter mode: from the selection's far edge in the
 * direction of travel, or from outside the strip when nothing is selected
 * (› lands on the first turn, ‹ on the last). Clamped to the strip.
 */
export function stepTurnSelection(current: number[] | null, d: -1 | 1, order: number[]): number[] | null {
  if (!order.length) return null;
  let i: number;
  if (current && current.length) {
    const edge = d > 0 ? current[current.length - 1] : current[0];
    i = order.indexOf(edge) + d;
  } else {
    i = d > 0 ? 0 : order.length - 1;
  }
  return [order[Math.max(0, Math.min(order.length - 1, i))]];
}

/** The turn after/before `n` in strip order, clamped; the first turn when `n` is null. */
export function stepTurn(n: number | null, d: -1 | 1, order: number[]): number | null {
  if (!order.length) return null;
  if (n === null) return order[0];
  const i = order.indexOf(n);
  return order[Math.max(0, Math.min(order.length - 1, (i < 0 ? 0 : i) + d))];
}

export function turnByN(turns: ChangeTurn[], n: number): ChangeTurn | undefined {
  return turns.find((t) => t.n === n);
}

/** Non-scratch files — the count the rail header and tab badges show. */
export function countNonScratch(cs: ChangeSet | null | undefined): number {
  return cs ? cs.files.filter((f) => f.tier !== "scratch").length : 0;
}

// --- Rail turn selector ---------------------------------------------------

/** The rail's tier choice: one tier, or every file. */
export type RailTier = ChangeTier | "all";
/** A rail turn stop: null = all turns, a turn number, or "latest" (follows the newest turn). */
export type RailStop = number | null | "latest";

function inTier(f: ChangedFile, tier: RailTier): boolean {
  return tier === "all" || f.tier === tier;
}

/**
 * The stops ‹ › walk through: All turns, every turn with at least one file
 * in the chosen tier, then Latest (kept whenever the session has turns, so
 * a working agent can be followed even before the newest turn has files).
 */
export function railStops(cs: Pick<ChangeSet, "files" | "turns">, tier: RailTier): RailStop[] {
  const withFiles = cs.turns
    .map((t) => t.n)
    .filter((n) => cs.files.some((f) => inTier(f, tier) && f.turns.includes(n)));
  return cs.turns.length ? [null, ...withFiles, "latest"] : [null];
}

/** The turn number a stop shows: Latest resolves to the newest turn. */
export function resolveRailTurn(cs: Pick<ChangeSet, "turns">, stop: RailStop): number | null {
  if (stop === "latest") return cs.turns.length ? cs.turns[cs.turns.length - 1].n : null;
  return stop;
}

export function railFiles(cs: Pick<ChangeSet, "files" | "turns">, tier: RailTier, stop: RailStop): ChangedFile[] {
  const n = resolveRailTurn(cs, stop);
  return sortBySize(cs.files.filter((f) => inTier(f, tier) && (n === null || f.turns.includes(n))));
}

export function stepRailStop(stops: RailStop[], current: RailStop, d: -1 | 1): RailStop {
  let i = stops.indexOf(current);
  if (i < 0) i = 0;
  return stops[Math.max(0, Math.min(stops.length - 1, i + d))];
}

// --- Opening the Changes tab from elsewhere ---------------------------------

/**
 * What the rail (or anything else) asks the Changes tab to show: a file to
 * select, the tier to bring into view, and the turn to filter to. `turn`
 * null clears the strip's filter; undefined leaves it alone. Callers pass a
 * fresh object each time so a repeat request re-applies.
 */
export interface ChangesFocus {
  path?: string;
  tier?: RailTier;
  turn?: number | null;
}
