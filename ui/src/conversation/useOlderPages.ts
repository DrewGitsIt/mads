import { useCallback, useLayoutEffect, useRef, type MutableRefObject } from "react";

// How close to the top (px) a scroll has to come before the page before the
// oldest loaded row is fetched: early enough that it's usually in before the
// reader gets there.
export const OLDER_PAGE_TRIGGER_PX = 400;

/** The rendered list's container (ChatRenderer's root): its children are the conversation's items. */
export const ROWS_SELECTOR = "[data-chat-rows]";

interface Anchor {
  /** The first item at least partly in view when the page was asked for. */
  el: Element;
  /** Its key (data-find / data-find-group), to find it again if it remounted. */
  key?: string;
  /** Its top, relative to the scroller's top. */
  top: number;
}

export interface OlderPages {
  /** The scroller's onScroll (alongside useStickToBottom's). */
  onScroll: () => void;
}

/**
 * Scroll-up paging for the keyed conversation (docs/CHAT-STREAM-DESIGN.md
 * §5): near the top, with older rows to fetch, asks for the page before the
 * oldest loaded row. The page is prepended above whatever the reader is
 * looking at, so it records the first visible item — by key, and its offset
 * from the scroller's top — before asking, and puts that item back at the
 * same offset in the layout pass that renders the page. A scroll-height
 * delta alone would be thrown off by anything else that changed height in
 * the same commit (a live reply growing below, a group folding).
 *
 * `version` changes when an older page lands (the conversation's paging
 * cursor and has-more), which is when the anchor is restored.
 */
export function useOlderPages(
  element: MutableRefObject<HTMLElement | null>,
  { hasMore, loadOlder, version }: { hasMore: boolean; loadOlder: () => Promise<void>; version: unknown },
): OlderPages {
  const anchor = useRef<Anchor | null>(null);
  const loading = useRef(false);
  const hasMoreRef = useRef(hasMore);
  hasMoreRef.current = hasMore;
  const loadOlderRef = useRef(loadOlder);
  loadOlderRef.current = loadOlder;

  const onScroll = useCallback(() => {
    const el = element.current;
    if (!el || loading.current || !hasMoreRef.current) return;
    if (el.scrollTop > OLDER_PAGE_TRIGGER_PX) return;
    anchor.current = captureAnchor(el);
    loading.current = true;
    loadOlderRef
      .current()
      .catch(() => {
        // Nothing landed: the next scroll near the top asks again.
        anchor.current = null;
      })
      .finally(() => {
        loading.current = false;
      });
  }, [element]);

  useLayoutEffect(() => {
    const a = anchor.current;
    const el = element.current;
    anchor.current = null;
    if (!a || !el) return;
    const target = a.el.isConnected ? a.el : a.key ? findByKey(el, a.key) : null;
    if (!target) return;
    const moved = topIn(el, target) - a.top;
    if (moved !== 0) el.scrollTop += moved;
  }, [element, version]);

  return { onScroll };
}

function topIn(scroller: Element, el: Element): number {
  return el.getBoundingClientRect().top - scroller.getBoundingClientRect().top;
}

function itemKey(el: Element): string | undefined {
  const own = el.getAttribute("data-find") ?? el.getAttribute("data-find-group");
  if (own) return own;
  const inner = el.querySelector("[data-find],[data-find-group]");
  return inner?.getAttribute("data-find") ?? inner?.getAttribute("data-find-group") ?? undefined;
}

function findByKey(scroller: Element, key: string): Element | null {
  const rows = scroller.querySelector(ROWS_SELECTOR);
  if (!rows) return null;
  for (const child of rows.children) if (itemKey(child) === key) return child;
  return null;
}

/** The first item whose bottom is below the scroller's top edge. */
function captureAnchor(scroller: Element): Anchor | null {
  const rows = scroller.querySelector(ROWS_SELECTOR);
  if (!rows) return null;
  const top = scroller.getBoundingClientRect().top;
  for (const child of rows.children) {
    const r = child.getBoundingClientRect();
    if (r.bottom > top) return { el: child, key: itemKey(child), top: r.top - top };
  }
  return null;
}
