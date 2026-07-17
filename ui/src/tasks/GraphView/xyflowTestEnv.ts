import { NODE_HEIGHT, NODE_WIDTH } from "./layout";

/**
 * happy-dom (this repo's vitest environment) doesn't run real layout, so
 * every element's offsetWidth/offsetHeight/getBoundingClientRect stay 0 and
 * ResizeObserver never fires. xyflow treats an unmeasured node as invisible
 * and never renders edges touching it. This installs a synchronous
 * ResizeObserver stub plus fixed node-sized geometry so nodes measure as
 * NODE_WIDTH x NODE_HEIGHT, scoped to whichever test file calls it in a
 * beforeAll/afterAll pair — not a global src/setupTests.ts change, so it
 * can't affect non-graph tests.
 */
export function installXyflowMeasurements(): () => void {
  const previousResizeObserver = globalThis.ResizeObserver;
  const previousGetBoundingClientRect = HTMLElement.prototype.getBoundingClientRect;
  const previousOffsetWidth = Object.getOwnPropertyDescriptor(HTMLElement.prototype, "offsetWidth");
  const previousOffsetHeight = Object.getOwnPropertyDescriptor(HTMLElement.prototype, "offsetHeight");

  class StubResizeObserver {
    constructor(private callback: ResizeObserverCallback) {}
    observe(target: Element) {
      const rect = target.getBoundingClientRect();
      this.callback([{ target, contentRect: rect } as ResizeObserverEntry], this as unknown as ResizeObserver);
    }
    unobserve() {}
    disconnect() {}
  }

  globalThis.ResizeObserver = StubResizeObserver as unknown as typeof ResizeObserver;
  Object.defineProperty(HTMLElement.prototype, "offsetWidth", { configurable: true, get: () => NODE_WIDTH });
  Object.defineProperty(HTMLElement.prototype, "offsetHeight", { configurable: true, get: () => NODE_HEIGHT });
  HTMLElement.prototype.getBoundingClientRect = function () {
    return {
      x: 0,
      y: 0,
      width: NODE_WIDTH,
      height: NODE_HEIGHT,
      top: 0,
      left: 0,
      right: NODE_WIDTH,
      bottom: NODE_HEIGHT,
      toJSON() {},
    } as DOMRect;
  };

  return () => {
    globalThis.ResizeObserver = previousResizeObserver;
    HTMLElement.prototype.getBoundingClientRect = previousGetBoundingClientRect;
    if (previousOffsetWidth) Object.defineProperty(HTMLElement.prototype, "offsetWidth", previousOffsetWidth);
    if (previousOffsetHeight) Object.defineProperty(HTMLElement.prototype, "offsetHeight", previousOffsetHeight);
  };
}
