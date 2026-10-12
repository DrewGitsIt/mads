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

/** Same keys as ActivityGroup's rows. */
export function entryKey(entry: ActivityEntry, i: number): string {
  return entry.type === "tool" ? (entry.tool.id ?? `t${i}`) : `th${i}`;
}

/** The searchable text of an entry, by part. */
export function entryParts(entry: ActivityEntry, skipTools: boolean): { part: EntryPart; text: string }[] {
  if (entry.type === "thinking") return [{ part: "thinking", text: entry.text }];
  if (skipTools) return [];
  const { tool } = entry;
  const parts: { part: EntryPart; text: string }[] = [];
  const input = inputText(tool.input, commandInput(tool)?.command);
  if (input) parts.push({ part: "input", text: input });
  if (tool.result?.text) parts.push({ part: "output", text: tool.result.text });
  return parts;
}

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

/** Matches by searched text, for one pattern: tool output can be large, and a streaming turn re-renders often. */
export type SearchCache = Map<string, { start: number; length: number }[]>;

/** Every match in a group's entries, in row order. */
export function searchEntries(
  entries: ActivityEntry[],
  pattern: RegExp,
  skipTools: boolean,
  cache: SearchCache = new Map(),
): EntryMatch[] {
  const out: EntryMatch[] = [];
  entries.forEach((entry, i) => {
    const key = entryKey(entry, i);
    for (const { part, text } of entryParts(entry, skipTools)) {
      let found = cache.get(text);
      if (!found) cache.set(text, (found = findAll(text, pattern)));
      for (const { start, length } of found) {
        out.push({ entryKey: key, part, start, length, text });
      }
    }
  });
  return out;
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
