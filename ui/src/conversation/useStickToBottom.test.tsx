import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { useStickToBottom, type StickToBottom } from "./useStickToBottom";

// A hand-driven ResizeObserver: tests fire it to stand in for content
// growing without the host re-rendering (a tool status, an image loading).
let observers: { cb: () => void; targets: Set<Element> }[] = [];
class FakeResizeObserver {
  entry: { cb: () => void; targets: Set<Element> };
  constructor(cb: () => void) {
    this.entry = { cb, targets: new Set() };
    observers.push(this.entry);
  }
  observe(el: Element) {
    this.entry.targets.add(el);
  }
  unobserve(el: Element) {
    this.entry.targets.delete(el);
  }
  disconnect() {
    this.entry.targets.clear();
  }
}
/** Something under the scroller changed size. */
const resize = () => act(() => observers.forEach((o) => o.targets.size && o.cb()));
const watched = () => new Set(observers.flatMap((o) => [...o.targets]));

let height = 2000;
let client = 500;
let top = 0;
let beforePin: ((el: HTMLDivElement) => boolean | void) | undefined;
function Host({ rows, onStick }: { rows: number; onStick?: (s: StickToBottom<HTMLDivElement>) => void }) {
  const stick = useStickToBottom<HTMLDivElement>({ beforePin: (el) => beforePin?.(el) });
  onStick?.(stick);
  return (
    <div data-testid="scroller" ref={stick.ref} onScroll={stick.onScroll}>
      {Array.from({ length: rows }, (_, i) => (
        <p key={i} data-testid={`row-${i}`}>
          row {i}
        </p>
      ))}
    </div>
  );
}
function mount(rows = 1, onStick?: (s: StickToBottom<HTMLDivElement>) => void) {
  const utils = render(<Host rows={rows} onStick={onStick} />);
  const el = screen.getByTestId("scroller");
  Object.defineProperty(el, "scrollHeight", { configurable: true, get: () => height });
  Object.defineProperty(el, "clientHeight", { configurable: true, get: () => client });
  Object.defineProperty(el, "scrollTop", { configurable: true, get: () => top, set: (v: number) => (top = v) });
  scrollTo(el, 1500); // opens at the tail
  return { ...utils, el };
}
const scrollTo = (el: HTMLElement, at: number) => {
  top = at;
  fireEvent.scroll(el);
};

describe("useStickToBottom", () => {
  beforeEach(() => {
    observers = [];
    height = 2000;
    client = 500;
    top = 0;
    beforePin = undefined;
    vi.stubGlobal("ResizeObserver", FakeResizeObserver);
  });
  afterEach(() => {
    vi.unstubAllGlobals();
    delete document.documentElement.dataset.motion;
  });

  it("follows content that grows without the host re-rendering", () => {
    mount();
    height = 2300;
    resize();
    expect(top).toBe(2300);
  });

  it("follows the host's own renders", () => {
    const { rerender } = mount();
    height = 2300;
    rerender(<Host rows={2} />);
    expect(top).toBe(2300);
  });

  it("lets go once a scroll leaves the bottom by more than 10px, and stays put", () => {
    const { el, rerender } = mount();
    scrollTo(el, 1489); // 11px up
    height = 2300;
    resize();
    rerender(<Host rows={2} />);
    expect(top).toBe(1489);
  });

  it("holds through a scroll the browser makes, not the user", () => {
    // A growing composer shrinks the viewport and the browser shifts
    // scrollTop down as it lays out — that scroll lands before the re-pin.
    const { el } = mount();
    client = 382;
    scrollTo(el, 1548);
    resize();
    expect(top).toBe(2000);
  });

  it("still counts within 10px of the bottom as the tail", () => {
    const { el } = mount();
    scrollTo(el, 1490);
    height = 2300;
    resize();
    expect(top).toBe(2300);
  });

  it("re-engages when the user scrolls back to the bottom", () => {
    const { el } = mount();
    scrollTo(el, 200);
    scrollTo(el, 1500);
    height = 2300;
    resize();
    expect(top).toBe(2300);
  });

  it("watches children added after mount", async () => {
    const { rerender } = mount(1);
    rerender(<Host rows={2} />);
    // MutationObserver records arrive asynchronously.
    await waitFor(() => expect(watched().has(screen.getByTestId("row-1"))).toBe(true));
  });

  it("follow() re-engages and release() lets go without a scroll", () => {
    let stick!: StickToBottom<HTMLDivElement>;
    const { el } = mount(1, (s) => (stick = s));
    scrollTo(el, 200);
    stick.follow();
    height = 2300;
    resize();
    expect(top).toBe(2300);

    stick.release();
    height = 2600;
    resize();
    expect(top).toBe(2300);
  });

  it("hold() stays put at the tail, and letting go follows again only from the tail", () => {
    let stick!: StickToBottom<HTMLDivElement>;
    const { el } = mount(1, (s) => (stick = s));
    stick.hold(true);
    height = 2300;
    resize();
    expect(top).toBe(1500);

    // Still at the tail when let go: follows again.
    scrollTo(el, 1800);
    stick.hold(false);
    height = 2600;
    resize();
    expect(top).toBe(2600);

    // Let go somewhere up the history: stays there.
    stick.hold(true);
    scrollTo(el, 400);
    stick.hold(false);
    height = 2900;
    resize();
    expect(top).toBe(400);
  });

  it("shows its mode on the scroller", () => {
    let stick!: StickToBottom<HTMLDivElement>;
    const { el } = mount(1, (s) => (stick = s));
    expect(el.dataset.stick).toBe("follow");
    scrollTo(el, 200);
    expect(el.dataset.stick).toBe("free");
    stick.hold(true);
    expect(el.dataset.stick).toBe("held");
    scrollTo(el, 1500);
    expect(el.dataset.stick).toBe("held");
    stick.hold(false);
    expect(el.dataset.stick).toBe("follow");
  });

  it("calls beforePin before every pin, in every mode, so the bottom is sized first", () => {
    const seen: string[] = [];
    const { el } = mount();
    beforePin = (e) => {
      seen.push(e.dataset.stick!);
      height = 2400; // the spacer grows
    };
    resize();
    expect(top).toBe(2400);
    scrollTo(el, 200);
    resize();
    expect(seen).toEqual(["follow", "free"]);
    expect(top).toBe(200);
  });

  describe("a move beforePin asks to be smooth", () => {
    let calls: ScrollToOptions[];
    const withScrollTo = (el: HTMLElement) => {
      calls = [];
      el.scrollTo = ((o: ScrollToOptions) => calls.push(o)) as typeof el.scrollTo;
    };

    it("glides down to the new bottom, once per target", () => {
      const { el } = mount();
      withScrollTo(el);
      beforePin = () => true;
      height = 2400;
      resize();
      resize();
      expect(calls).toEqual([{ top: 1900, behavior: "smooth" }]);
      expect(top).toBe(1500);
    });

    it("keeps gliding to a bottom that moves on while in flight, without letting go", () => {
      const { el } = mount();
      withScrollTo(el);
      beforePin = () => true;
      height = 2400;
      resize();
      beforePin = undefined;
      scrollTo(el, 1700); // the glide's own scroll event
      height = 2500;
      resize();
      expect(calls.at(-1)).toEqual({ top: 2000, behavior: "smooth" });
      expect(el.dataset.stick).toBe("follow");
    });

    it("is instant under reduce motion", () => {
      document.documentElement.dataset.motion = "reduce";
      const { el } = mount();
      withScrollTo(el);
      beforePin = () => true;
      height = 2400;
      resize();
      expect(calls).toEqual([]);
      expect(top).toBe(2400);
    });

    it("is instant when the bottom rose (content shrank)", () => {
      const { el } = mount();
      withScrollTo(el);
      beforePin = () => true;
      height = 1800;
      resize();
      expect(calls).toEqual([]);
      expect(top).toBe(1800);
    });
  });

  it("disconnects when the scroller unmounts", () => {
    const { unmount } = mount();
    unmount();
    expect(watched().size).toBe(0);
  });
});
