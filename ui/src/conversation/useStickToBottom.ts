import { useCallback, useLayoutEffect, useMemo, useRef, type MutableRefObject } from "react";
import { prefersReducedMotion } from "../tasks/GraphView/usePrefersReducedMotion";

// How close to the bottom (px) still counts as "at the tail": scrolling back
// within it re-engages the follow; any further up lets go.
export const STICK_THRESHOLD_PX = 10;
// A smooth move that hasn't landed by now (cancelled, or its scroll events
// lost) no longer counts as in flight.
const MOVE_MS = 1000;

/**
 * follow: pinned to the bottom, following whatever grows there.
 * free:   the user scrolled up; nothing moves until they're back at the bottom.
 * held:   hold(true) — nothing moves, whatever the user does (find is open).
 */
export type StickMode = "follow" | "free" | "held";

export interface StickOptions<T extends HTMLElement> {
  /**
   * Called with the scroller before every pin, in every mode: sizes whatever
   * the bottom depends on (the runway's spacer) before anything moves, so the
   * two never race. Returning true asks for the move to the new bottom to be
   * smooth (instant anyway under reduce motion).
   */
  beforePin?: (el: T) => boolean | void;
}

export interface StickToBottom<T extends HTMLElement> {
  /** Callback ref for the scroller — attaches and detaches its observers. */
  ref: (el: T | null) => void;
  /** The scroller itself, for anything else that needs to measure it. */
  element: MutableRefObject<T | null>;
  /** The scroller's onScroll. */
  onScroll: () => void;
  /** Re-engage the follow (the user asked for an outcome at the tail). */
  follow: () => void;
  /** Let go of the tail (the user is jumping up the history). */
  release: () => void;
  /**
   * Hold still while `on` (find is open: new output mustn't scroll the match
   * being read away). Letting go follows the tail again only if the
   * scroller is at it.
   */
  hold: (on: boolean) => void;
}

/**
 * A scroll lock on a scroller's tail: while at the bottom it follows whatever
 * grows there; the moment the user scrolls up they have full control, until
 * they scroll back down to it. The mode is a ref (not state) so updating it
 * from the scroll handler never itself triggers a render; the scroller shows
 * it as data-stick for debugging and tests.
 *
 * It follows growth from every source, not just the host's renders: a
 * ResizeObserver watches the scroller (a composer growing a line shrinks its
 * viewport without moving scrollTop or firing a scroll) and each of its
 * children (tool statuses, groups opening, images loading, the pinned strip's
 * working indicator — none of which re-render the host), and a
 * MutationObserver brings children added later under watch. The host's own
 * commits re-pin too, before paint.
 *
 * This is the only code that moves the scroller while following. A host that
 * wants a different bottom (useRunway) changes the content's height in
 * `beforePin`, never scrollTop.
 */
export function useStickToBottom<T extends HTMLElement = HTMLDivElement>(
  options: StickOptions<T> = {},
): StickToBottom<T> {
  const element = useRef<T | null>(null);
  const mode = useRef<StickMode>("follow");
  // The scroller's last scrollTop as onScroll saw it (or pin set it).
  const lastTop = useRef(0);
  // A smooth move in flight: where it's going and when it started.
  const moving = useRef<{ to: number; at: number } | null>(null);
  const beforePin = useRef(options.beforePin);
  beforePin.current = options.beforePin;
  const detach = useRef<(() => void) | null>(null);

  const setMode = useCallback((m: StickMode) => {
    mode.current = m;
    if (m !== "follow") moving.current = null;
    if (element.current) element.current.dataset.stick = m;
  }, []);

  const pin = useCallback(() => {
    const el = element.current;
    if (!el) return;
    const smooth = beforePin.current?.(el) === true;
    if (mode.current !== "follow") return;
    const bottom = el.scrollHeight - el.clientHeight;
    const inFlight = moving.current && performance.now() - moving.current.at < MOVE_MS ? moving.current : null;
    // Smooth only goes down: its scroll events then never read as the user
    // scrolling up. A bottom that rose (content shrank) is clamped instantly.
    if ((smooth || inFlight) && bottom > el.scrollTop + 1 && !prefersReducedMotion() && el.scrollTo) {
      if (inFlight?.to !== bottom) {
        moving.current = { to: bottom, at: inFlight?.at ?? performance.now() };
        el.scrollTo({ top: bottom, behavior: "smooth" });
      }
      return;
    }
    moving.current = null;
    el.scrollTop = el.scrollHeight;
    lastTop.current = el.scrollTop;
  }, []);

  const ref = useCallback(
    (el: T | null) => {
      element.current = el;
      detach.current?.();
      detach.current = null;
      if (el) el.dataset.stick = mode.current;
      if (!el || typeof ResizeObserver === "undefined") return;
      const resize = new ResizeObserver(pin);
      resize.observe(el);
      for (const child of el.children) resize.observe(child);
      let mutation: MutationObserver | undefined;
      if (typeof MutationObserver !== "undefined") {
        mutation = new MutationObserver((records) => {
          for (const r of records) {
            r.addedNodes.forEach((n) => n instanceof Element && resize.observe(n));
            r.removedNodes.forEach((n) => n instanceof Element && resize.unobserve(n));
          }
        });
        mutation.observe(el, { childList: true });
      }
      detach.current = () => {
        resize.disconnect();
        mutation?.disconnect();
      };
    },
    [pin],
  );

  // Every host commit: whatever it rendered at the tail is followed into view.
  useLayoutEffect(pin);

  // Only a scroll up lets go. The browser moves scrollTop on its own too —
  // a composer growing forces a layout that shifts it, and that scroll event
  // can land before the ResizeObserver re-pins — and reading "not at the
  // bottom" off one of those would drop the lock with no user scroll at all.
  // A smooth pin only scrolls down, so it never reads as a scroll up either.
  const onScroll = useCallback(() => {
    const el = element.current;
    if (!el) return;
    const top = el.scrollTop;
    if (moving.current && Math.abs(top - moving.current.to) <= 1) moving.current = null;
    if (mode.current !== "held") {
      if (el.scrollHeight - top - el.clientHeight <= STICK_THRESHOLD_PX) setMode("follow");
      else if (top < lastTop.current) setMode("free");
    }
    lastTop.current = top;
  }, [setMode]);

  const follow = useCallback(() => setMode("follow"), [setMode]);
  const release = useCallback(() => setMode("free"), [setMode]);

  const hold = useCallback(
    (on: boolean) => {
      const el = element.current;
      if (on) setMode("held");
      else if (mode.current === "held")
        setMode(el && el.scrollHeight - el.scrollTop - el.clientHeight <= STICK_THRESHOLD_PX ? "follow" : "free");
    },
    [setMode],
  );

  return useMemo(
    () => ({ ref, element, onScroll, follow, release, hold }),
    [ref, onScroll, follow, release, hold],
  );
}
