import { startTransition, useEffect, useMemo, useState } from "react";
import type { ActivityEntry } from "../activity/conversationItems";
import {
  entryParts,
  findKey,
  findPattern,
  matchOffsets,
  searchGroup,
  type FindOptions,
  type GroupMatches,
  type SearchCache,
} from "./findText";

/** What the stream holds that find matches from the transcript, not the DOM: activity groups and compaction summaries. */
export type Searchable = { key: string; entries: ActivityEntry[] } | { key: string; summary: string };

/** Matches in the transcript, for one search. */
export interface TranscriptMatches {
  /** The search these are for (findKey); "" for none. */
  key: string;
  groups: ReadonlyMap<string, GroupMatches>;
  summaries: ReadonlyMap<string, number>;
}

export const NO_MATCHES: TranscriptMatches = { key: "", groups: new Map(), summaries: new Map() };

/** Matching works this long at a stretch, then hands the main thread back (a frame is ~16 ms). */
const SLICE_MS = 4;

/**
 * Matches in activity rows and compaction summaries, found off the render
 * path: in slices of a few milliseconds between frames, so a short query
 * over a long session's tool output (hundreds of thousands of matches)
 * never holds up a keystroke. A new search, or new content, drops the work
 * in progress; text already matched for the same pattern isn't matched
 * again. Until a search's matches are in, the previous search's stay —
 * `key` says which search they belong to.
 */
export function useTranscriptMatches(sources: readonly Searchable[], options: FindOptions | undefined): TranscriptMatches {
  const key = options ? findKey(options) : "";
  const query = options?.query ?? "";
  const matchCase = Boolean(options?.matchCase);
  const skipTools = Boolean(options?.skipTools);
  // The cache is per pattern: matches found for one query are no good for the next.
  const { pattern, cache } = useMemo(
    () => ({ pattern: findPattern({ query, matchCase }), cache: new Map() as SearchCache }),
    [query, matchCase],
  );
  const [result, setResult] = useState(NO_MATCHES);

  // `sources` is new every render: content (a streaming turn) can change
  // what there is to match on any of them.
  useEffect(() => {
    if (!pattern) {
      setResult(NO_MATCHES);
      return;
    }
    let cancelled = false;
    let i = 0;
    const groups = new Map<string, GroupMatches>();
    const summaries = new Map<string, number>();
    const offsetsOf = (text: string) => {
      let found = cache.get(text);
      if (!found) cache.set(text, (found = matchOffsets(text, pattern)));
      return found;
    };
    const work = () => {
      if (cancelled) return;
      const until = performance.now() + SLICE_MS;
      for (; i < sources.length; i++) {
        const s = sources[i];
        if ("entries" in s) {
          // Each part's text matched (and cached) on its own, so a large
          // group splits across slices; then the group is assembled from
          // the cache.
          // The clock is read after each piece of work, not before: a
          // slice always gets somewhere, even one that starts late.
          for (const entry of s.entries) {
            for (const { text } of entryParts(entry, skipTools)) {
              if (cache.has(text)) continue;
              offsetsOf(text);
              if (performance.now() > until) return later(work);
            }
          }
          const g = searchGroup(s.entries, pattern, skipTools, cache);
          if (g) groups.set(s.key, g);
        } else {
          const fresh = !cache.has(s.summary);
          const n = offsetsOf(s.summary).length / 2;
          if (n > 0) summaries.set(s.key, n);
          if (fresh && performance.now() > until) {
            i++;
            return later(work);
          }
        }
      }
      const next = { key, groups, summaries };
      // The stream re-renders with them (chips, the current match): in
      // slices, as a transition, not in one go.
      startTransition(() => setResult((prev) => (same(prev, next) ? prev : next)));
    };
    work();
    return () => {
      cancelled = true;
    };
  }, [pattern, cache, sources, key, skipTools]);

  return result;
}

/** Runs `fn` as its own task, after anything waiting (a keystroke, a frame). */
function later(fn: () => void) {
  if (typeof MessageChannel === "undefined") {
    window.setTimeout(fn, 0);
    return;
  }
  const channel = new MessageChannel();
  channel.port1.onmessage = () => {
    channel.port1.close();
    fn();
  };
  channel.port2.postMessage(null);
}

/** The same matches: so content that adds none doesn't re-render the stream. */
function same(a: TranscriptMatches, b: TranscriptMatches): boolean {
  if (a.key !== b.key || a.groups.size !== b.groups.size || a.summaries.size !== b.summaries.size) return false;
  for (const [k, n] of b.summaries) if (a.summaries.get(k) !== n) return false;
  for (const [k, g] of b.groups) {
    const h = a.groups.get(k);
    if (!h || h.count !== g.count || h.parts.length !== g.parts.length) return false;
    // Offsets come from the cache, so the same text matched alike is the same array.
    if (g.parts.some((p, j) => p.offsets !== h.parts[j].offsets || p.entryKey !== h.parts[j].entryKey)) return false;
  }
  return true;
}
