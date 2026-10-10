import { useEffect, useRef, useState, type RefObject } from "react";

export interface VisibleCardsOptions {
  /** Which card elements to watch. */
  selector: string;
  /** The data attribute holding a card's id, e.g. "data-task-id". */
  idAttribute: string;
  /** Attributes whose change can add or drop a card from `selector`. */
  watchAttributes?: string[];
}

/**
 * Which cards are on screen right now, by id. Main-stream copies are watched
 * against the stream's viewport minus the pinned strip floating over its
 * bottom edge (a card under the strip isn't visible); copies elsewhere in
 * scope (the dock) against the window. Re-scans for card elements every render — cards come and go
 * with groups collapsing, the dock opening and statuses flipping, none of
 * which the caller would otherwise hear about. Without IntersectionObserver
 * nothing counts as visible.
 */
export function useVisibleCards(
  scopeRef: RefObject<HTMLElement | null>,
  streamRef: RefObject<HTMLElement | null>,
  pinnedRef: RefObject<HTMLElement | null>,
  { selector, idAttribute, watchAttributes = [] }: VisibleCardsOptions,
): ReadonlySet<string> {
  const [visible, setVisible] = useState<ReadonlySet<string>>(() => new Set());
  const observers = useRef<{
    stream: IntersectionObserver;
    page: IntersectionObserver;
    root: Element;
    margin: string;
  } | null>(null);
  // Every observed card element → whether it currently intersects.
  const watched = useRef(new Map<Element, boolean>());

  const publish = () => {
    const ids = new Set<string>();
    for (const [el, on] of watched.current) {
      const id = el.getAttribute(idAttribute);
      if (on && id) ids.add(id);
    }
    setVisible((cur) => (cur.size === ids.size && [...ids].every((id) => cur.has(id)) ? cur : ids));
  };

  const scan = () => {
    const scope = scopeRef.current;
    const stream = streamRef.current;
    if (!scope || !stream || typeof IntersectionObserver === "undefined") return;
    const margin = `0px 0px -${pinnedRef.current?.offsetHeight ?? 0}px 0px`;
    let obs = observers.current;
    if (!obs || obs.root !== stream || obs.margin !== margin) {
      obs?.stream.disconnect();
      obs?.page.disconnect();
      const onEntries = (entries: IntersectionObserverEntry[]) => {
        for (const e of entries) {
          if (watched.current.has(e.target)) watched.current.set(e.target, e.isIntersecting);
        }
        publish();
      };
      obs = {
        stream: new IntersectionObserver(onEntries, { root: stream, rootMargin: margin }),
        page: new IntersectionObserver(onEntries),
        root: stream,
        margin,
      };
      observers.current = obs;
      watched.current = new Map();
    }
    const current = new Set(scope.querySelectorAll(selector));
    let dropped = false;
    for (const el of [...watched.current.keys()]) {
      if (current.has(el)) continue;
      obs.stream.unobserve(el);
      obs.page.unobserve(el);
      watched.current.delete(el);
      dropped = true;
    }
    for (const el of current) {
      if (watched.current.has(el)) continue;
      watched.current.set(el, false);
      (stream.contains(el) ? obs.stream : obs.page).observe(el);
    }
    if (dropped) publish();
  };
  const scanRef = useRef(scan);
  scanRef.current = scan;

  useEffect(() => scan());

  // A group the user expands or collapses, or a card's status flipping,
  // re-renders only its own subtree — watch the DOM for those, coalesced to
  // one scan per frame (the stream mutates constantly mid-turn).
  const watchKey = watchAttributes.join(" ");
  useEffect(() => {
    const scope = scopeRef.current;
    if (!scope || typeof MutationObserver === "undefined") return;
    let frame = 0;
    const mo = new MutationObserver(() => {
      if (frame) return;
      frame = requestAnimationFrame(() => {
        frame = 0;
        scanRef.current();
      });
    });
    const attributeFilter = watchKey ? watchKey.split(" ") : [];
    mo.observe(scope, {
      childList: true,
      subtree: true,
      ...(attributeFilter.length > 0 && { attributes: true, attributeFilter }),
    });
    return () => {
      mo.disconnect();
      cancelAnimationFrame(frame);
      observers.current?.stream.disconnect();
      observers.current?.page.disconnect();
      observers.current = null;
    };
  }, [scopeRef, watchKey]);

  return visible;
}
