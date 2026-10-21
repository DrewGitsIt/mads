import { act, fireEvent, render, screen } from "@testing-library/react";
import { useRef } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { OLDER_PAGE_TRIGGER_PX, useOlderPages } from "./useOlderPages";

// A hand-laid-out scroller: each item's height is its data-h, stacked from
// the top; getBoundingClientRect reads the layout against the scroller's
// scrollTop, as a browser would.
let top = 0;
function layout(scroller: HTMLElement) {
  Object.defineProperty(scroller, "scrollTop", { configurable: true, get: () => top, set: (v: number) => (top = v) });
  scroller.getBoundingClientRect = () => ({ top: 0, bottom: 500, height: 500 }) as DOMRect;
}
beforeEach(() => {
  top = 0;
  vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockImplementation(function (this: HTMLElement) {
    let y = 0;
    for (let el = this.previousElementSibling; el; el = el.previousElementSibling) y += Number(el.getAttribute("data-h") ?? 0);
    const h = Number(this.getAttribute("data-h") ?? 0);
    return { top: y - top, bottom: y - top + h, height: h } as DOMRect;
  });
});
afterEach(() => vi.restoreAllMocks());

interface Item {
  key: string;
  h: number;
}

function Host({ items, hasMore, loadOlder }: { items: Item[]; hasMore: boolean; loadOlder: () => Promise<void> }) {
  const ref = useRef<HTMLDivElement | null>(null);
  const pages = useOlderPages(ref, { hasMore, loadOlder, version: items[0]?.key });
  return (
    <div
      data-testid="scroller"
      ref={(el) => {
        ref.current = el;
        if (el) layout(el);
      }}
      onScroll={pages.onScroll}
    >
      <div data-chat-rows>
        {items.map((it) => (
          <div key={it.key} data-h={it.h} data-find={it.key}>
            {it.key}
          </div>
        ))}
      </div>
    </div>
  );
}

const items = (keys: string[], h: (k: string) => number = () => 100): Item[] => keys.map((key) => ({ key, h: h(key) }));

describe("useOlderPages", () => {
  it("asks for the older page only near the top, and only while there is one", () => {
    const loadOlder = vi.fn(() => new Promise<void>(() => {}));
    const { rerender } = render(<Host items={items(["c", "d", "e"])} hasMore loadOlder={loadOlder} />);
    const el = screen.getByTestId("scroller");
    top = OLDER_PAGE_TRIGGER_PX + 1;
    fireEvent.scroll(el);
    expect(loadOlder).not.toHaveBeenCalled();
    top = OLDER_PAGE_TRIGGER_PX;
    fireEvent.scroll(el);
    fireEvent.scroll(el); // one in flight at a time
    expect(loadOlder).toHaveBeenCalledTimes(1);

    const none = vi.fn(() => Promise.resolve());
    rerender(<Host items={items(["c", "d", "e"])} hasMore={false} loadOlder={none} />);
    fireEvent.scroll(el);
    expect(none).not.toHaveBeenCalled();
  });

  it("keeps the item being read at the same offset when the older page is prepended", async () => {
    let land!: () => void;
    const loadOlder = () => new Promise<void>((r) => (land = r));
    const { rerender } = render(<Host items={items(["c", "d", "e"])} hasMore loadOlder={loadOlder} />);
    const el = screen.getByTestId("scroller");
    // Reading "d" (100px down), scrolled 130px: d's top sits 30px above the viewport.
    top = 130;
    fireEvent.scroll(el);

    // The page lands: three items of different heights above "c".
    const heights: Record<string, number> = { a1: 40, a2: 260, b: 75 };
    await act(async () => {
      land();
      await Promise.resolve();
    });
    rerender(<Host items={items(["a1", "a2", "b", "c", "d", "e"], (k) => heights[k] ?? 100)} hasMore loadOlder={loadOlder} />);

    // 375px landed above: "d" is back at -30, not wherever 130px now points.
    expect(top).toBe(130 + 375);
    expect(screen.getByText("d").getBoundingClientRect().top).toBe(-30);
  });

  it("anchors by the item, so a height change elsewhere in the same commit doesn't drift it", async () => {
    let land!: () => void;
    const loadOlder = () => new Promise<void>((r) => (land = r));
    const { rerender } = render(<Host items={items(["c", "d", "e"])} hasMore loadOlder={loadOlder} />);
    const el = screen.getByTestId("scroller");
    top = 150; // reading "d" from 50px into it
    fireEvent.scroll(el);
    await act(async () => {
      land();
      await Promise.resolve();
    });
    // The page lands while the live reply at the tail grew by 500px: a
    // scroll-height delta would read 620px; "d" moved by 120.
    rerender(
      <Host items={items(["x", "c", "d", "e"], (k) => (k === "x" ? 120 : k === "e" ? 600 : 100))} hasMore loadOlder={loadOlder} />,
    );
    expect(top).toBe(150 + 120);
    expect(screen.getByText("d").getBoundingClientRect().top).toBe(-50);
  });

  it("leaves the scroll alone when nothing was asked for", () => {
    const { rerender } = render(<Host items={items(["c"])} hasMore loadOlder={() => Promise.resolve()} />);
    top = 900;
    rerender(<Host items={items(["b", "c"])} hasMore loadOlder={() => Promise.resolve()} />);
    expect(top).toBe(900);
  });
});
