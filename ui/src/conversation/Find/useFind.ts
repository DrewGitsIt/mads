import { useCallback, useEffect, useLayoutEffect, useRef, useState, type RefObject } from "react";
import { domMatches } from "./domMatches";
import { findPattern, type EntryMatch, type FindOptions } from "./findText";

/** The find bar's ask: what to look for, and which way to step. */
export interface FindRequest extends FindOptions {
  /** Bumped on every next/previous. */
  step: number;
  dir: 1 | -1;
}

/**
 * Matches past this many aren't counted, painted or ticked: the bar says
 * "More than" this instead. Nobody reads a thousand matches one by one,
 * and every match costs a measurement and a highlight.
 */
export const FIND_CAP = 1000;

/** What the bar shows: "current+1 of count", plus where each match sits. */
export interface FindStatus {
  /** Matches found, up to FIND_CAP. */
  count: number;
  /** More than FIND_CAP: the matches in hand are the ones nearest the current one. */
  capped: boolean;
  /** Index of the current match, -1 when there's none (or when capped, as it has no place in a count). */
  current: number;
  /**
   * Where the matches sit down the whole stream, 0–1, for the edge ticks:
   * one per distinct position at the edge's resolution, so a thousand
   * matches in one tool's output make one tick, not a thousand.
   */
  ticks: number[];
  currentTick: number;
}

export const NO_FIND: FindStatus = { count: 0, capped: false, current: -1, ticks: [], currentTick: -1 };

/** The current match, for what has to render differently to show it. */
export type FindTarget =
  | { kind: "dom"; id: string }
  | { kind: "entry"; id: string; itemKey: string; match: EntryMatch }
  | { kind: "summary"; id: string; itemKey: string; n: number };

/** An element of the stream that holds matches (see useFind), in document order. */
interface Source {
  kind: "dom" | "entry" | "summary";
  el: HTMLElement;
  /** Its item key; for "dom", with the ordinal among elements sharing one. */
  key: string;
}

/** A match: its source's index, and its place among that source's matches. */
type At = { src: number; n: number };

type Hit = At &
  (
    | { kind: "dom"; id: string; el: HTMLElement; range: AbstractRange }
    | { kind: "entry"; id: string; el: HTMLElement; itemKey: string; match: EntryMatch }
    | { kind: "summary"; id: string; el: HTMLElement; itemKey: string; n: number }
  );

/** Re-scans driven by content (a streaming turn) run at most this often. */
const RESCAN_MS = 150;
/** A match already this far inside the viewport isn't scrolled to. */
const VIEW_MARGIN = 48;
/** The pinned strip (working indicator, queue) covers about this much of the bottom. */
const VIEW_BOTTOM = 120;

const keyOf = (r?: FindRequest) => (r?.query ? `${r.query}\0${r.matchCase}\0${r.skipTools}` : "");

/**
 * Find in conversation. The stream's elements opt in by what they hold:
 *
 *  - `data-find="<item key>"`: rendered text, searched in the DOM and
 *    painted with the CSS Custom Highlight API (no markup changes, so the
 *    markdown renderer and Copy never see them). `data-find-tool` marks one
 *    "no tools" leaves out.
 *  - `data-find-group="<item key>"`: an activity group, whose rows show one
 *    line each — its matches come from the transcript (`groups`), and the
 *    group renders the current one as a snippet (`data-find-current`).
 *  - `data-find-summary="<item key>"`: a compaction marker, its summary
 *    behind a disclosure — counted from the transcript (`summaries`) and,
 *    when open, painted in its `data-find-summary-body`.
 *
 * Document order is match order. The current match is held by identity, so
 * output arriving mid-search doesn't move it; a new query starts from the
 * current match (or the top of the view), and next/previous wrap.
 */
export function useFind(
  rootRef: RefObject<HTMLElement | null>,
  request: FindRequest | undefined,
  groups: ReadonlyMap<string, EntryMatch[]>,
  summaries: ReadonlyMap<string, number>,
  onStatus?: (status: FindStatus) => void,
): FindTarget | null {
  const [target, setTarget] = useState<FindTarget | null>(null);
  const latest = useRef({ request, groups, summaries, onStatus });
  latest.current = { request, groups, summaries, onStatus };
  const st = useRef({
    queryKey: "",
    step: 0,
    currentId: undefined as string | undefined,
    /** The current match's source key, to find it again without matching the whole stream. */
    currentSource: undefined as string | undefined,
    /** The current match's top, in stream coordinates — where a new query starts from. */
    anchor: undefined as number | undefined,
    scrollPending: false,
    waits: 0,
    status: "",
    lastRun: 0,
    timer: 0,
  });

  const run = useCallback(() => {
    const { request, groups, summaries, onStatus } = latest.current;
    const s = st.current;
    const root = rootRef.current;
    const pattern = request ? findPattern(request) : null;
    const report = (status: FindStatus) => {
      const k = JSON.stringify(status);
      if (k === s.status) return;
      s.status = k;
      onStatus?.(status);
    };
    if (!root || !request || !pattern) {
      clearHighlights();
      Object.assign(s, { queryKey: "", currentId: undefined, currentSource: undefined, anchor: undefined, scrollPending: false });
      setTarget(null);
      report(NO_FIND);
      return;
    }

    const srcs = sources(root, request.skipTools);
    const found = new Map<number, Hit[]>();
    const hitsOf = (i: number) => {
      let hs = found.get(i);
      if (!hs) found.set(i, (hs = matchesIn(srcs[i], i, pattern, groups, summaries)));
      return hs;
    };
    const hitAt = ({ src, n }: At) => hitsOf(src)[n];
    // Neighbours, wrapping at the ends. Only called with a match in hand, so
    // there is always one to land on.
    const next = ({ src, n }: At): At => {
      for (n++; n >= hitsOf(src).length; n = 0) src = (src + 1) % srcs.length;
      return { src, n };
    };
    const prev = ({ src, n }: At): At => {
      for (n--; n < 0; n = hitsOf(src).length - 1) src = (src - 1 + srcs.length) % srcs.length;
      return { src, n };
    };

    const scroller = scrollerOf(root);
    const box = scroller.getBoundingClientRect();
    const toStream = (top: number) => top - box.top + scroller.scrollTop;
    // Each box measured once per run, however many matches it stands for (a
    // group's every match is its header). Kept in stream coordinates, which
    // the reveal's scroll below doesn't change; viewport ones it would.
    const spans = new Map<object, { top: number; bottom: number }>();
    const spanOf = (key: object, rect: () => DOMRect) => {
      let at = spans.get(key);
      if (!at) {
        const r = rect();
        spans.set(key, (at = { top: toStream(r.top), bottom: toStream(r.bottom) }));
      }
      return at;
    };
    const span = (h: Hit) => (h.kind === "dom" ? spanOf(h.range, () => quickRect(h)) : spanOf(h.el, () => quickRect(h)));
    // The first match whose bottom is at or below `from`, else the last one.
    // Elements wholly above it are passed by on their own box, unmatched.
    const atOrAfter = (from: number): At | undefined => {
      for (let i = 0; i < srcs.length; i++) {
        const el = srcs[i].el;
        if (spanOf(el, () => el.getBoundingClientRect()).bottom < from) continue;
        const n = hitsOf(i).findIndex((h) => span(h).bottom >= from);
        if (n >= 0) return { src: i, n };
      }
      for (let i = srcs.length - 1; i >= 0; i--) {
        if (hitsOf(i).length > 0) return { src: i, n: hitsOf(i).length - 1 };
      }
      return undefined;
    };
    const held = (): At | undefined => {
      const i = srcs.findIndex((x) => x.key === s.currentSource);
      const n = i < 0 ? -1 : hitsOf(i).findIndex((h) => h.id === s.currentId);
      return n >= 0 ? { src: i, n } : undefined;
    };

    const qk = keyOf(request);
    let at: At | undefined;
    if (qk !== s.queryKey) {
      at = atOrAfter(s.anchor ?? toStream(box.top));
      s.queryKey = qk;
      s.step = request.step;
      s.scrollPending = at !== undefined;
    } else if (request.step !== s.step) {
      s.step = request.step;
      const from = held();
      if (from) at = request.dir === 1 ? next(from) : prev(from);
      else {
        // The current match is gone (its text changed): step from where it was.
        const near = atOrAfter(s.anchor ?? toStream(box.top));
        at = near && request.dir === -1 ? prev(near) : near;
      }
      s.scrollPending = at !== undefined;
    } else {
      at = held() ?? atOrAfter(s.anchor ?? toStream(box.top));
    }

    // The matches in hand: the current one and its neighbours either way,
    // up to the cap — so stepping always has the next one, and what's on
    // screen around the current match is painted.
    const hits: Hit[] = [];
    let capped = false;
    if (at) {
      const taken = new Set<string>();
      const take = (a: At) => {
        const h = hitAt(a);
        if (taken.has(h.id)) return false;
        taken.add(h.id);
        hits.push(h);
        return true;
      };
      take(at);
      let ahead = at;
      let behind = at;
      const open = [true, true];
      while (hits.length < FIND_CAP && (open[0] || open[1])) {
        if (open[0]) open[0] = take((ahead = next(ahead)));
        if (open[1] && hits.length < FIND_CAP) open[1] = take((behind = prev(behind)));
      }
      // Stopped at the cap with the circle unclosed: there are more.
      capped = !taken.has(hitAt(next(ahead)).id);
      hits.sort((a, b) => a.src - b.src || a.n - b.n);
    }
    const cur = at && hitAt(at);
    const index = cur ? hits.indexOf(cur) : -1;
    s.currentId = cur?.id;
    s.currentSource = cur && srcs[cur.src].key;
    s.anchor = cur ? span(cur).top : undefined;
    setTarget((prev) => (prev?.id === cur?.id ? prev : cur ? toTarget(cur) : null));

    const summaryRanges = new Map<HTMLElement, AbstractRange[]>();
    const rangeOf = (h: Hit): AbstractRange | undefined => {
      if (h.kind === "dom") return h.range;
      if (h.kind !== "summary") return undefined;
      if (!summaryRanges.has(h.el)) {
        const body = h.el.querySelector("[data-find-summary-body]");
        summaryRanges.set(h.el, body ? domMatches(body, pattern) : []);
      }
      return summaryRanges.get(h.el)![h.n];
    };
    paint(
      hits.filter((h) => h !== cur).map(rangeOf),
      cur && rangeOf(cur),
    );

    if (s.scrollPending && cur) {
      const r = exactRect(cur, root, rangeOf(cur));
      if (r || ++s.waits > 2) {
        reveal(scroller, r ?? cur.el.getBoundingClientRect());
        s.scrollPending = false;
        s.waits = 0;
      }
    }

    const height = Math.max(1, scroller.scrollHeight);
    const tick = (h: Hit) => Math.round((span(h).top / height) * 1000) / 1000;
    report({
      count: hits.length,
      capped,
      current: capped ? -1 : index,
      ticks: [...new Set(hits.map(tick))],
      currentTick: cur ? tick(cur) : -1,
    });
  }, [rootRef]);

  // Every commit: a new query or a step runs now (and so does a scroll still
  // waiting on the render it asked for); content changes are coalesced, so a
  // streaming turn doesn't re-scan the whole history on every token.
  useLayoutEffect(() => {
    const s = st.current;
    const req = latest.current.request;
    const urgent = keyOf(req) !== s.queryKey || (req?.query && req.step !== s.step) || s.scrollPending;
    if (urgent) {
      window.clearTimeout(s.timer);
      s.timer = 0;
      s.lastRun = performance.now();
      run();
      return;
    }
    if (!s.queryKey || s.timer) return;
    s.timer = window.setTimeout(
      () => {
        s.timer = 0;
        s.lastRun = performance.now();
        run();
      },
      Math.max(0, RESCAN_MS - (performance.now() - s.lastRun)),
    );
  });

  // Layout that moves without a commit here (a group opened by hand, a
  // resize) moves the ticks.
  useEffect(() => {
    const root = rootRef.current;
    const s = st.current;
    if (!root || typeof ResizeObserver === "undefined") return;
    const observer = new ResizeObserver(() => {
      if (!s.queryKey || s.timer) return;
      s.timer = window.setTimeout(() => {
        s.timer = 0;
        run();
      }, RESCAN_MS);
    });
    observer.observe(root);
    return () => observer.disconnect();
  }, [rootRef, run]);

  useEffect(
    () => () => {
      window.clearTimeout(st.current.timer);
      clearHighlights();
    },
    [],
  );

  return target;
}

function sources(root: HTMLElement, skipTools: boolean): Source[] {
  const out: Source[] = [];
  const seen = new Map<string, number>();
  for (const el of root.querySelectorAll<HTMLElement>("[data-find], [data-find-group], [data-find-summary]")) {
    const { find, findGroup, findSummary } = el.dataset;
    if (find !== undefined) {
      if (skipTools && el.dataset.findTool !== undefined) continue;
      // One item can render several searchable elements (a plan's items).
      const ord = seen.get(find) ?? 0;
      seen.set(find, ord + 1);
      out.push({ kind: "dom", el, key: `${find}:${ord}` });
    } else if (findGroup !== undefined) {
      out.push({ kind: "entry", el, key: findGroup });
    } else if (findSummary !== undefined) {
      out.push({ kind: "summary", el, key: findSummary });
    }
  }
  return out;
}

function matchesIn(
  { kind, el, key }: Source,
  src: number,
  pattern: RegExp,
  groups: ReadonlyMap<string, EntryMatch[]>,
  summaries: ReadonlyMap<string, number>,
): Hit[] {
  if (kind === "dom") {
    return domMatches(el, pattern).map((range, n) => ({ kind, id: `d:${key}:${n}`, el, range, src, n }));
  }
  if (kind === "entry") {
    return (groups.get(key) ?? []).map((match, n) => ({
      kind,
      id: `e:${key}:${match.entryKey}:${match.part}:${match.start}`,
      el,
      itemKey: key,
      match,
      src,
      n,
    }));
  }
  return Array.from({ length: summaries.get(key) ?? 0 }, (_, n) => ({
    kind,
    id: `s:${key}:${n}`,
    el,
    itemKey: key,
    src,
    n,
  }));
}

function toTarget(h: Hit): FindTarget {
  if (h.kind === "dom") return { kind: "dom", id: h.id };
  if (h.kind === "entry") return { kind: "entry", id: h.id, itemKey: h.itemKey, match: h.match };
  return { kind: "summary", id: h.id, itemKey: h.itemKey, n: h.n };
}

/** Where a match is, near enough for ordering and ticks: the text itself, or its group or marker. */
function quickRect(h: Hit): DOMRect {
  return (h.kind === "dom" && rangeRect(h.range)) || h.el.getBoundingClientRect();
}

/** Where a match is exactly, once it's rendered: null while its snippet or summary hasn't been. */
function exactRect(h: Hit, root: HTMLElement, range: AbstractRange | undefined): DOMRect | null {
  if (h.kind === "entry") {
    const snippet = [...root.querySelectorAll<HTMLElement>("[data-find-current]")].find(
      (el) => el.dataset.findCurrent === h.id,
    );
    return snippet ? snippet.getBoundingClientRect() : null;
  }
  const rect = range && rangeRect(range);
  if (rect) return rect;
  if (h.kind === "summary") return h.el.querySelector("[data-find-summary-body]") ? h.el.getBoundingClientRect() : null;
  return h.el.getBoundingClientRect();
}

/** Centers `r` in the scroller unless it's already comfortably in view. */
function reveal(scroller: HTMLElement, r: DOMRect) {
  const box = scroller.getBoundingClientRect();
  if (r.top >= box.top + VIEW_MARGIN && r.bottom <= box.bottom - VIEW_BOTTOM) return;
  scroller.scrollTop += r.top - box.top - (scroller.clientHeight - r.height) / 2;
}

function scrollerOf(el: HTMLElement): HTMLElement {
  for (let p = el.parentElement; p; p = p.parentElement) {
    const { overflowY } = getComputedStyle(p);
    if (overflowY === "auto" || overflowY === "scroll") return p;
  }
  return (document.scrollingElement as HTMLElement | null) ?? document.documentElement;
}

/**
 * One live Range, moved onto each match to measure it. Matches are static
 * ranges (domMatches): a live one is something every DOM removal anywhere
 * in the window has to update, and find can hold thousands.
 */
let probe: Range | undefined;

/** Where `range` is, or null when it can't be measured (its text has gone, or the DOM has no range layout). */
function rangeRect(range: AbstractRange): DOMRect | null {
  let live: Range;
  if (range instanceof Range) live = range;
  else {
    if (!range.startContainer.isConnected || !range.endContainer.isConnected) return null;
    live = probe ??= document.createRange();
    try {
      live.setStart(range.startContainer, range.startOffset);
      live.setEnd(range.endContainer, range.endOffset);
    } catch {
      // The text shrank under it since the scan.
      return null;
    }
  }
  return typeof live.getBoundingClientRect === "function" ? live.getBoundingClientRect() : null;
}

function highlights(): HighlightRegistry | undefined {
  return typeof CSS !== "undefined" && "highlights" in CSS && typeof Highlight !== "undefined"
    ? CSS.highlights
    : undefined;
}

function paint(others: (AbstractRange | undefined)[], current: AbstractRange | undefined) {
  const registry = highlights();
  if (!registry) return;
  registry.set("find-match", new Highlight(...others.filter((r): r is AbstractRange => r !== undefined)));
  if (current) registry.set("find-current", new Highlight(current));
  else registry.delete("find-current");
}

function clearHighlights() {
  const registry = highlights();
  registry?.delete("find-match");
  registry?.delete("find-current");
}
