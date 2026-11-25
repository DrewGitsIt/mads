import { act, render } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { pageSize } from "./runway";
import { useRunway, type Runway } from "./useRunway";

// Stubbed geometry, in content coordinates: the scroller's view is 500px
// tall, the anchor (a person's message) starts at `anchorTop`, the reply ends
// (the spacer starts) at `tail`.
let anchorTop = 1012;
let tail = 1100;
let scrollTop = 0;
const page = pageSize(500);

let runway!: Runway;
function Host({ active, live = true, anchorKey = "u1" }: { active: boolean; live?: boolean; anchorKey?: string }) {
  runway = useRunway(active);
  return (
    <div data-testid="scroller">
      <div data-runway-anchor={anchorKey} data-runway-live={live ? "" : undefined} />
      <div ref={runway.spacerRef} data-testid="spacer" />
    </div>
  );
}

function mount(props: { active: boolean; live?: boolean; anchorKey?: string }) {
  const utils = render(<Host {...props} />);
  const el = utils.getByTestId("scroller");
  const spacer = utils.getByTestId("spacer");
  const anchor = el.querySelector<HTMLElement>("[data-runway-anchor]")!;
  Object.defineProperty(el, "clientHeight", { configurable: true, get: () => 500 });
  Object.defineProperty(el, "scrollTop", { configurable: true, get: () => scrollTop });
  el.getBoundingClientRect = () => ({ top: 0 }) as DOMRect;
  anchor.getBoundingClientRect = () => ({ top: anchorTop - scrollTop }) as DOMRect;
  spacer.getBoundingClientRect = () => ({ top: tail - scrollTop }) as DOMRect;
  Object.defineProperty(spacer, "offsetHeight", { configurable: true, get: () => parseFloat(spacer.style.height) || 0 });
  return { ...utils, el, spacer };
}
const spacerPx = (spacer: HTMLElement) => parseFloat(spacer.style.height);

describe("useRunway", () => {
  let frames: FrameRequestCallback[];
  let now: number;
  beforeEach(() => {
    anchorTop = 1012;
    tail = 1100;
    scrollTop = 0;
    frames = [];
    now = 0;
    vi.spyOn(performance, "now").mockImplementation(() => now);
    vi.stubGlobal("requestAnimationFrame", (cb: FrameRequestCallback) => frames.push(cb));
    vi.stubGlobal("cancelAnimationFrame", () => {});
  });
  afterEach(() => {
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
    delete document.documentElement.dataset.motion;
  });
  const runFrames = () => {
    while (frames.length) {
      now += 50;
      act(() => frames.shift()!(now));
    }
  };

  it("opens a live runway: the rest of the screen under the message, gliding there", () => {
    const { el, spacer } = mount({ active: true });
    expect(runway.beforePin(el)).toBe(true);
    expect(spacerPx(spacer)).toBe(400); // floor 1500 − tail 1100
    expect(runway.beforePin(el)).toBe(false); // nothing new: no glide
  });

  it("shrinks the spacer as the reply grows, so the bottom holds still", () => {
    const { el, spacer } = mount({ active: true });
    runway.beforePin(el);
    tail = 1300;
    expect(runway.beforePin(el)).toBe(false);
    expect(spacerPx(spacer)).toBe(200);
  });

  it("turns a page (gliding) when the reply reaches the bottom edge", () => {
    const { el, spacer } = mount({ active: true });
    runway.beforePin(el);
    tail = 1495;
    expect(runway.beforePin(el)).toBe(true);
    expect(spacer.dataset.pages).toBe("1");
    expect(spacerPx(spacer)).toBe(Math.round(1000 + page + 500 - 1495));
  });

  it("opens history that ended settled, with no space reserved and no glide", () => {
    const { el, spacer } = mount({ active: false, live: false });
    expect(runway.beforePin(el)).toBe(false);
    expect(spacerPx(spacer)).toBe(0);
  });

  it("opens history whose turn is still running on its runway, without a glide", () => {
    const { el, spacer } = mount({ active: true, live: false });
    expect(runway.beforePin(el)).toBe(false);
    expect(spacerPx(spacer)).toBe(400);
  });

  // A glide on open runs from the top through the older-page trigger, and the
  // page landing mid-glide could restore the reader far above the end.
  it("opens history whose reply is pages long at its end, without a glide", () => {
    tail = 3000;
    const { el, spacer } = mount({ active: false, live: false });
    expect(runway.beforePin(el)).toBe(false);
    expect(spacer.dataset.pages).toBe("4");
    expect(spacerPx(spacer)).toBe(0);
  });

  it("opens a running turn pages in without a glide, then turns the next page as usual", () => {
    tail = 1495;
    const { el, spacer } = mount({ active: true, live: false });
    expect(runway.beforePin(el)).toBe(false);
    expect(spacer.dataset.pages).toBe("1");
    tail = 1495 + page;
    expect(runway.beforePin(el)).toBe(true);
    expect(spacer.dataset.pages).toBe("2");
  });

  it("trims only what's below the view once the turn is over", () => {
    const { el, spacer, rerender } = mount({ active: true });
    runway.beforePin(el);
    scrollTop = 1000; // at the bottom: the whole runway is on screen
    rerender(<Host active={false} />);
    runway.onScroll();
    expect(spacerPx(spacer)).toBe(400);
    scrollTop = 800; // scrolled up 200: the bottom 200 of it is off screen
    runway.onScroll();
    expect(spacerPx(spacer)).toBe(200);
    scrollTop = 1000; // and it doesn't come back
    runway.onScroll();
    expect(spacerPx(spacer)).toBe(200);
  });

  it("trims everything below the view when the turn ends with the reader scrolled away", () => {
    const { el, spacer, rerender } = mount({ active: true });
    runway.beforePin(el);
    scrollTop = 0;
    rerender(<Host active={false} />);
    expect(spacerPx(spacer)).toBe(0);
  });

  it("doesn't trim while the turn is running", () => {
    const { el, spacer } = mount({ active: true });
    runway.beforePin(el);
    scrollTop = 500;
    runway.onScroll();
    expect(spacerPx(spacer)).toBe(400);
  });

  it("settles: the leftover space collapses over a few frames", () => {
    const { el, spacer, rerender } = mount({ active: true });
    runway.beforePin(el);
    rerender(<Host active={false} />);
    scrollTop = 1000;
    runway.settle();
    runFrames();
    expect(spacerPx(spacer)).toBe(0);
  });

  it("never settles under reduce motion: the text stays put", () => {
    document.documentElement.dataset.motion = "reduce";
    const { el, spacer, rerender } = mount({ active: true });
    runway.beforePin(el);
    scrollTop = 1000;
    rerender(<Host active={false} />);
    runway.settle();
    runFrames();
    expect(spacerPx(spacer)).toBe(400);
  });

  it("doesn't settle while the turn is running", () => {
    const { el, spacer } = mount({ active: true });
    runway.beforePin(el);
    runway.settle();
    runFrames();
    expect(spacerPx(spacer)).toBe(400);
  });

  it("starts over on a new message, after a settle", () => {
    const { el, spacer, rerender } = mount({ active: true });
    runway.beforePin(el);
    rerender(<Host active={false} />);
    runway.settle();
    runFrames();
    rerender(<Host active={true} anchorKey="u2" />);
    const anchor = el.querySelector<HTMLElement>("[data-runway-anchor]")!;
    anchor.getBoundingClientRect = () => ({ top: anchorTop - scrollTop }) as DOMRect;
    expect(runway.beforePin(el)).toBe(true);
    expect(spacerPx(spacer)).toBe(400);
  });

  it("reserves nothing with no anchor", () => {
    const { el, spacer } = mount({ active: true });
    el.querySelector("[data-runway-anchor]")!.remove();
    runway.beforePin(el);
    expect(spacerPx(spacer)).toBe(0);
  });
});
