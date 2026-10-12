import { useCallback, useEffect, useLayoutEffect, useRef, useState, type RefObject } from "react";
import { domMatches } from "./domMatches";
import { findPattern, type EntryMatch, type FindOptions } from "./findText";

/** The find bar's ask: what to look for, and which way to step. */
export interface FindRequest extends FindOptions {
  /** Bumped on every next/previous. */
  step: number;
  dir: 1 | -1;
}

/** What the bar shows: "current+1 of count", plus where each match sits. */
export interface FindStatus {
  count: number;
  /** Index of the current match, -1 when there's none. */
  current: number;
  /** Each match's position down the whole stream, 0–1, for the edge ticks. */
  ticks: number[];
  currentTick: number;
}

export const NO_FIND: FindStatus = { count: 0, current: -1, ticks: [], currentTick: -1 };

/** The current match, for what has to render differently to show it. */
export type FindTarget =
  | { kind: "dom"; id: string }
  | { kind: "entry"; id: string; itemKey: string; match: EntryMatch }
  | { kind: "summary"; id: string; itemKey: string; n: number };

type Hit =
  | { kind: "dom"; id: string; el: HTMLElement; range: Range }
  | { kind: "entry"; id: string; el: HTMLElement; itemKey: string; match: EntryMatch }
  | { kind: "summary"; id: string; el: HTMLElement; itemKey: string; n: number };

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
    index: -1,
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
      Object.assign(s, { queryKey: "", currentId: undefined, index: -1, anchor: undefined, scrollPending: false });
      setTarget(null);
      report(NO_FIND);
      return;
    }

    const hits = collect(root, pattern, request.skipTools, groups, summaries);
    const scroller = scrollerOf(root);
    const box = scroller.getBoundingClientRect();
    const toStream = (top: number) => top - box.top + scroller.scrollTop;

    const qk = keyOf(request);
    let index: number;
    if (qk !== s.queryKey) {
      const from = s.anchor ?? toStream(box.top);
      index = hits.findIndex((h) => toStream(quickRect(h).bottom) >= from);
      if (index < 0) index = hits.length - 1;
      s.queryKey = qk;
      s.step = request.step;
      s.scrollPending = index >= 0;
    } else if (request.step !== s.step) {
      s.step = request.step;
      const at = hits.findIndex((h) => h.id === s.currentId);
      index = hits.length === 0 ? -1 : at < 0 ? 0 : (at + request.dir + hits.length) % hits.length;
      s.scrollPending = index >= 0;
    } else {
      index = hits.findIndex((h) => h.id === s.currentId);
      if (index < 0) index = Math.min(s.index, hits.length - 1);
    }
    const cur = hits[index];
    s.index = index;
    s.currentId = cur?.id;
    s.anchor = cur ? toStream(quickRect(cur).top) : undefined;
    setTarget((prev) => (prev?.id === cur?.id ? prev : cur ? toTarget(cur) : null));

    const summaryRanges = new Map<HTMLElement, Range[]>();
    const rangeOf = (h: Hit): Range | undefined => {
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
    const tick = (h: Hit) => Math.round((toStream(quickRect(h).top) / height) * 1000) / 1000;
    report({
      count: hits.length,
      current: index,
      ticks: hits.map(tick),
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

function collect(
  root: HTMLElement,
  pattern: RegExp,
  skipTools: boolean,
  groups: ReadonlyMap<string, EntryMatch[]>,
  summaries: ReadonlyMap<string, number>,
): Hit[] {
  const hits: Hit[] = [];
  const seen = new Map<string, number>();
  for (const el of root.querySelectorAll<HTMLElement>("[data-find], [data-find-group], [data-find-summary]")) {
    const { find, findGroup, findSummary } = el.dataset;
    if (find !== undefined) {
      if (skipTools && el.dataset.findTool !== undefined) continue;
      // One item can render several searchable elements (a plan's items).
      const ord = seen.get(find) ?? 0;
      seen.set(find, ord + 1);
      domMatches(el, pattern).forEach((range, n) => hits.push({ kind: "dom", id: `d:${find}:${ord}:${n}`, el, range }));
    } else if (findGroup !== undefined) {
      for (const match of groups.get(findGroup) ?? []) {
        const id = `e:${findGroup}:${match.entryKey}:${match.part}:${match.start}`;
        hits.push({ kind: "entry", id, el, itemKey: findGroup, match });
      }
    } else if (findSummary !== undefined) {
      const count = summaries.get(findSummary) ?? 0;
      for (let n = 0; n < count; n++) {
        hits.push({ kind: "summary", id: `s:${findSummary}:${n}`, el, itemKey: findSummary, n });
      }
    }
  }
  return hits;
}

function toTarget(h: Hit): FindTarget {
  if (h.kind === "dom") return { kind: "dom", id: h.id };
  if (h.kind === "entry") return { kind: "entry", id: h.id, itemKey: h.itemKey, match: h.match };
  return { kind: "summary", id: h.id, itemKey: h.itemKey, n: h.n };
}

/** Where a match is, near enough for ordering and ticks: the text itself, or its group or marker. */
function quickRect(h: Hit): DOMRect {
  if (h.kind === "dom" && typeof h.range.getBoundingClientRect === "function") return h.range.getBoundingClientRect();
  return h.el.getBoundingClientRect();
}

/** Where a match is exactly, once it's rendered: null while its snippet or summary hasn't been. */
function exactRect(h: Hit, root: HTMLElement, range: Range | undefined): DOMRect | null {
  if (h.kind === "entry") {
    const snippet = [...root.querySelectorAll<HTMLElement>("[data-find-current]")].find(
      (el) => el.dataset.findCurrent === h.id,
    );
    return snippet ? snippet.getBoundingClientRect() : null;
  }
  if (range && typeof range.getBoundingClientRect === "function") return range.getBoundingClientRect();
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

function highlights(): HighlightRegistry | undefined {
  return typeof CSS !== "undefined" && "highlights" in CSS && typeof Highlight !== "undefined"
    ? CSS.highlights
    : undefined;
}

function paint(others: (Range | undefined)[], current: Range | undefined) {
  const registry = highlights();
  if (!registry) return;
  registry.set("find-match", new Highlight(...others.filter((r): r is Range => r !== undefined)));
  if (current) registry.set("find-current", new Highlight(current));
  else registry.delete("find-current");
}

function clearHighlights() {
  const registry = highlights();
  registry?.delete("find-match");
  registry?.delete("find-current");
}
