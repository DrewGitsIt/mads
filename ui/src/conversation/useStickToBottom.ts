import { useCallback, useLayoutEffect, useMemo, useRef, type MutableRefObject } from "react";

// How close to the bottom (px) still counts as "at the tail": scrolling back
// within it re-engages the follow; any further up lets go.
export const STICK_THRESHOLD_PX = 10;

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
 * they scroll back down to it. The lock is a ref (not state)
 * so updating it from the scroll handler never itself triggers a render.
 *
 * It follows growth from every source, not just the host's renders: a
 * ResizeObserver watches the scroller (a composer growing a line shrinks its
 * viewport without moving scrollTop or firing a scroll) and each of its
 * children (tool statuses, groups opening, images loading, the pinned strip's
 * working indicator — none of which re-render the host), and a
 * MutationObserver brings children added later under watch. The host's own
 * commits re-pin too, before paint.
 */
export function useStickToBottom<T extends HTMLElement = HTMLDivElement>(): StickToBottom<T> {
  const element = useRef<T | null>(null);
  const stuck = useRef(true);
  const held = useRef(false);
  // The scroller's last scrollTop as onScroll saw it (or pin set it).
  const lastTop = useRef(0);
  const detach = useRef<(() => void) | null>(null);

  const pin = useCallback(() => {
    const el = element.current;
    if (!el || !stuck.current || held.current) return;
    el.scrollTop = el.scrollHeight;
    lastTop.current = el.scrollTop;
  }, []);

  const ref = useCallback(
    (el: T | null) => {
      element.current = el;
      detach.current?.();
      detach.current = null;
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
  const onScroll = useCallback(() => {
    const el = element.current;
    if (!el) return;
    const top = el.scrollTop;
    if (el.scrollHeight - top - el.clientHeight <= STICK_THRESHOLD_PX) stuck.current = true;
    else if (top < lastTop.current) stuck.current = false;
    lastTop.current = top;
  }, []);

  const follow = useCallback(() => {
    stuck.current = true;
  }, []);

  const release = useCallback(() => {
    stuck.current = false;
  }, []);

  const hold = useCallback((on: boolean) => {
    held.current = on;
    const el = element.current;
    if (!on && el) stuck.current = el.scrollHeight - el.scrollTop - el.clientHeight <= STICK_THRESHOLD_PX;
  }, []);

  return useMemo(
    () => ({ ref, element, onScroll, follow, release, hold }),
    [ref, onScroll, follow, release, hold],
  );
}
