import type { ActivityEntry } from "../activity/conversationItems";
import { commandInput } from "../activity/toolDisplay";

/** What the find bar asks for. */
export interface FindOptions {
  query: string;
  matchCase: boolean;
  /** Leave tool calls and their output out (thinking stays in). */
  skipTools: boolean;
}

/** A pattern for `query`, or null when there's nothing to find. */
export function findPattern({ query, matchCase }: Pick<FindOptions, "query" | "matchCase">): RegExp | null {
  if (!query) return null;
  // A RegExp rather than lowercasing both sides: lowercasing can change a
  // string's length, and the offsets have to hold in the original text.
  return new RegExp(query.replace(/[.*+?^${}()|[\]\\]/g, "\\$&"), matchCase ? "g" : "gi");
}

/** The identity of a search: matches found under one key are good for no other. */
export function findKey({ query, matchCase, skipTools }: FindOptions): string {
  return query ? `${query}\0${matchCase}\0${skipTools}` : "";
}

/** Every non-overlapping match of `pattern` (a findPattern) in `text`. */
export function findAll(text: string, pattern: RegExp): { start: number; length: number }[] {
  const out: { start: number; length: number }[] = [];
  pattern.lastIndex = 0;
  for (let m = pattern.exec(text); m; m = pattern.exec(text)) out.push({ start: m.index, length: m[0].length });
  return out;
}

export type EntryPart = "input" | "output" | "thinking";

/** One match in an activity entry's text — which the row itself never shows in full. */
export interface EntryMatch {
  entryKey: string;
  part: EntryPart;
  start: number;
  length: number;
  text: string;
}

/** Same keys as ActivityGroup's rows: the row's own, in the main conversation. */
export function entryKey(entry: ActivityEntry, i: number): string {
  if (entry.key) return entry.key;
  return entry.type === "tool" ? (entry.tool.id ?? `t${i}`) : `th${i}`;
}

/** The searchable text of an entry, by part. */
export function entryParts(entry: ActivityEntry, skipTools: boolean): { part: EntryPart; text: string }[] {
  if (entry.type === "thinking") return [{ part: "thinking", text: entry.text }];
  if (skipTools) return [];
  const { tool } = entry;
  const parts: { part: EntryPart; text: string }[] = [];
  let input = inputTexts.get(tool);
  if (input === undefined) inputTexts.set(tool, (input = inputText(tool.input, commandInput(tool)?.command)));
  if (input) parts.push({ part: "input", text: input });
  if (tool.result?.text) parts.push({ part: "output", text: tool.result.text });
  return parts;
}

/** A tool's input as searched, per call: stringifying a large input (a Write's content) on every search added up. */
const inputTexts = new WeakMap<object, string>();

function inputText(input: unknown, command: string | undefined): string {
  if (command) return command;
  if (input == null) return "";
  if (typeof input !== "object") return String(input);
  // Values only: the keys ("file_path", "command") are the tool's schema,
  // not anything the agent or the user said.
  return Object.values(input as Record<string, unknown>)
    .map((v) => (typeof v === "string" ? v : JSON.stringify(v)))
    .join("\n");
}

/**
 * Matches by searched text, for one pattern: tool output can be large, and
 * a streaming turn searches again often. Flat [start, length, start,
 * length…] — a short query finds hundreds of thousands, and an object each
 * was most of the cost.
 */
export type SearchCache = Map<string, number[]>;

/** findAll as flat offsets (SearchCache's). */
export function matchOffsets(text: string, pattern: RegExp): number[] {
  const out: number[] = [];
  pattern.lastIndex = 0;
  for (let m = pattern.exec(text); m; m = pattern.exec(text)) out.push(m.index, m[0].length);
  return out;
}

/**
 * A group's matches, as offsets per entry part in row order. They become
 * EntryMatch objects only one at a time (groupMatchAt), for the matches
 * find actually steps near.
 */
export interface GroupMatches {
  count: number;
  parts: { entryKey: string; part: EntryPart; text: string; offsets: number[] }[];
}

/** Every match in a group's entries, or undefined when there are none. */
export function searchGroup(
  entries: ActivityEntry[],
  pattern: RegExp,
  skipTools: boolean,
  cache: SearchCache = new Map(),
): GroupMatches | undefined {
  const parts: GroupMatches["parts"] = [];
  let count = 0;
  entries.forEach((entry, i) => {
    const key = entryKey(entry, i);
    for (const { part, text } of entryParts(entry, skipTools)) {
      let offsets = cache.get(text);
      if (!offsets) cache.set(text, (offsets = matchOffsets(text, pattern)));
      if (offsets.length === 0) continue;
      parts.push({ entryKey: key, part, text, offsets });
      count += offsets.length / 2;
    }
  });
  return count > 0 ? { count, parts } : undefined;
}

/** The group's `n`th match, in row order. */
export function groupMatchAt(g: GroupMatches, n: number): EntryMatch {
  for (const { entryKey, part, text, offsets } of g.parts) {
    const here = offsets.length / 2;
    if (n < here) return { entryKey, part, text, start: offsets[2 * n], length: offsets[2 * n + 1] };
    n -= here;
  }
  throw new RangeError(`no match ${n} in group`);
}

/** How many of the group's matches are in one row. */
export function entryMatchCount(g: GroupMatches | undefined, entryKey: string): number {
  let n = 0;
  for (const p of g?.parts ?? []) if (p.entryKey === entryKey) n += p.offsets.length / 2;
  return n;
}

/** One line of context around a match, whitespace folded, ellipsized at the cuts. */
export function snippet(text: string, start: number, length: number, radius = 48) {
  const from = Math.max(0, start - radius);
  const to = Math.min(text.length, start + length + radius);
  const fold = (s: string) => s.replace(/\s+/g, " ");
  return {
    before: (from > 0 ? "…" : "") + fold(text.slice(from, start)).trimStart(),
    match: fold(text.slice(start, start + length)),
    after: fold(text.slice(start + length, to)).trimEnd() + (to < text.length ? "…" : ""),
  };
}
