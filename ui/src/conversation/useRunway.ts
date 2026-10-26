import { useCallback, useEffect, useMemo, useRef, type RefObject } from "react";
import { prefersReducedMotion } from "../tasks/GraphView/usePrefersReducedMotion";
import { runwayFor, trimmedCap } from "./runway";

// The settle's slide, as long as a page turn's smooth scroll.
const SETTLE_MS = 420;
const ease = (x: number) => (x < 0.5 ? 4 * x * x * x : 1 - Math.pow(-2 * x + 2, 3) / 2);

export interface Runway {
  /** The spacer row: last in the stream, just before the pinned cluster. */
  spacerRef: RefObject<HTMLDivElement>;
  /** useStickToBottom's beforePin: sizes the spacer; true = glide to the new bottom. */
  beforePin: (el: HTMLElement) => boolean;
  /** The scroller's onScroll (alongside the lock's): trims once the turn is over. */
  onScroll: () => void;
  /** The reader is back at the composer: collapse what's left (never under reduce motion). */
  settle: () => void;
}

interface Measured {
  anchorTop: number;
  tail: number;
  usable: number;
}

/**
 * The reading runway (docs/CHAT-RUNWAY-DESIGN.md): keeps the latest
 * user-role message ([data-runway-anchor]) at the top of the stream while
 * the reply fills the space under it, turning a page each time the reply
 * reaches the bottom edge. It only ever sizes the spacer — runway.ts decides
 * how big; useStickToBottom stays the one thing that scrolls.
 *
 * `turnActive`: a turn is showing (running, or its reveal still playing).
 * Until it's over, nothing is trimmed or settled.
 */
export function useRunway(turnActive: boolean): Runway {
  const spacerRef = useRef<HTMLDivElement>(null);
  const scroller = useRef<HTMLElement | null>(null);
  const active = useRef(turnActive);
  active.current = turnActive;
  // The runway in force: its anchor's key, the pages turned, and the cap
  // trim/settle lowered the floor to. `seen` is false until the first anchor.
  const state = useRef({ seen: false, key: "", pages: 0, cap: null as number | null, floor: 0 });
  const settling = useRef<number | null>(null);

  const stopSettle = () => {
    if (settling.current !== null) cancelAnimationFrame(settling.current);
    settling.current = null;
  };

  const measure = useCallback((el: HTMLElement, anchor: HTMLElement, spacer: HTMLElement): Measured => {
    const origin = el.getBoundingClientRect().top - el.scrollTop;
    // Everything after the spacer (the pinned cluster) covers the bottom of
    // the view: text is read above it.
    let after = parseFloat(getComputedStyle(el).paddingBottom) || 0;
    for (let n = spacer.nextElementSibling; n; n = n.nextElementSibling) after += (n as HTMLElement).offsetHeight;
    return {
      anchorTop: anchor.getBoundingClientRect().top - origin,
      tail: spacer.getBoundingClientRect().top - origin,
      usable: el.clientHeight - after,
    };
  }, []);

  /** Size the spacer for the runway in force; true if this turned a page or opened a live runway. */
  const apply = useCallback(
    (el: HTMLElement): boolean => {
      scroller.current = el;
      const spacer = spacerRef.current;
      if (!spacer) return false;
      const s = state.current;
      const anchors = el.querySelectorAll<HTMLElement>("[data-runway-anchor]");
      const anchor = anchors[anchors.length - 1];
      if (!anchor) {
        spacer.style.height = "0px";
        return false;
      }
      const m = measure(el, anchor, spacer);
      let glide = false;
      const key = anchor.dataset.runwayAnchor ?? "";
      if (!s.seen || key !== s.key) {
        stopSettle();
        // A message that arrived while watching opens a runway and glides to
        // it. One that was already there when the stream opened (history, a
        // replay) shows its turn settled — unless that turn is still running.
        const live = anchor.dataset.runwayLive !== undefined;
        Object.assign(s, { seen: true, key, pages: 0, cap: live || active.current ? null : m.tail });
        glide = live;
      }
      const r = runwayFor({ ...m, cap: s.cap });
      if (r.pages > s.pages) glide = true;
      s.pages = r.pages;
      s.floor = r.floor;
      spacer.style.height = `${r.spacer}px`;
      spacer.dataset.pages = String(r.pages);
      if (s.cap === null) delete spacer.dataset.cap;
      else spacer.dataset.cap = String(Math.round(s.cap));
      return glide;
    },
    [measure],
  );

  // Trim: drop what's below the view. Nothing on screen moves.
  const trim = useCallback(() => {
    const el = scroller.current;
    const spacer = spacerRef.current;
    const s = state.current;
    if (!el || !spacer || active.current || !s.seen || settling.current !== null) return;
    const anchors = el.querySelectorAll<HTMLElement>("[data-runway-anchor]");
    const anchor = anchors[anchors.length - 1];
    if (!anchor) return;
    const m = measure(el, anchor, spacer);
    const cap = trimmedCap(s.floor, m.tail, el.scrollTop + m.usable);
    if (cap < s.floor - 0.5) {
      s.cap = cap;
      apply(el);
    }
  }, [apply, measure]);

  // The turn just ended: whatever's already off screen goes now.
  useEffect(() => {
    if (!turnActive) trim();
  }, [turnActive, trim]);

  const settle = useCallback(() => {
    const el = scroller.current;
    const spacer = spacerRef.current;
    const s = state.current;
    if (!el || !spacer || active.current || !s.seen || settling.current !== null) return;
    if (prefersReducedMotion()) return; // the text stays put; trim has the space
    // The floor comes down to the reply's end; the lock, pinned to the
    // bottom, carries the reply down with it.
    const from = s.floor;
    const to = from - spacer.offsetHeight;
    if (from - to < 1) return;
    const start = performance.now();
    const step = (now: number) => {
      const k = Math.min(1, (now - start) / SETTLE_MS);
      s.cap = from + (to - from) * ease(k);
      apply(el);
      settling.current = k < 1 ? requestAnimationFrame(step) : null;
    };
    settling.current = requestAnimationFrame(step);
  }, [apply]);

  useEffect(() => stopSettle, []);

  return useMemo(() => ({ spacerRef, beforePin: apply, onScroll: trim, settle }), [apply, trim, settle]);
}
