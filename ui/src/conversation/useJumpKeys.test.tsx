import { fireEvent, render } from "@testing-library/react";
import { useRef } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { DOUBLE_TAP_MS, useJumpKeys, type JumpKeys } from "./useJumpKeys";

let now = 0;
function Host(keys: JumpKeys) {
  const ref = useRef<HTMLDivElement>(null);
  useJumpKeys(ref, keys);
  return <div ref={ref} data-testid="scroller" />;
}

function mount(hopResult = true) {
  const keys = { hop: vi.fn(() => hopResult), toTop: vi.fn(), toBottom: vi.fn() };
  render(<Host {...keys} />);
  const press = (key: "ArrowUp" | "ArrowDown", ms = 100, init: KeyboardEventInit = {}) => {
    now += ms;
    const e = new KeyboardEvent("keydown", { key, metaKey: true, cancelable: true, ...init });
    window.dispatchEvent(e);
    return e;
  };
  return { keys, press };
}

describe("useJumpKeys", () => {
  beforeEach(() => {
    now = 0;
    vi.spyOn(performance, "now").mockImplementation(() => now);
  });
  afterEach(() => vi.restoreAllMocks());

  it("one tap hops; a second of the same arrow goes to that end", () => {
    const { keys, press } = mount();
    expect(press("ArrowUp").defaultPrevented).toBe(true);
    expect(keys.hop).toHaveBeenCalledWith("up", expect.any(HTMLElement));
    press("ArrowUp");
    expect(keys.toTop).toHaveBeenCalledTimes(1);
    expect(keys.hop).toHaveBeenCalledTimes(1);
    press("ArrowDown");
    press("ArrowDown");
    expect(keys.toBottom).toHaveBeenCalledTimes(1);
  });

  it("a third tap starts over: hop, not another jump", () => {
    const { keys, press } = mount();
    press("ArrowUp");
    press("ArrowUp");
    press("ArrowUp");
    expect(keys.toTop).toHaveBeenCalledTimes(1);
    expect(keys.hop).toHaveBeenCalledTimes(2);
  });

  it("isn't a double tap when slow, mixed, or held down", () => {
    const { keys, press } = mount();
    press("ArrowUp");
    press("ArrowUp", DOUBLE_TAP_MS + 1);
    press("ArrowDown");
    press("ArrowUp");
    press("ArrowUp", 50, { repeat: true });
    expect(keys.toTop).not.toHaveBeenCalled();
    expect(keys.toBottom).not.toHaveBeenCalled();
  });

  it("works with Ctrl; ignores the arrows alone or with Shift", () => {
    const { keys, press } = mount();
    press("ArrowUp", 100, { metaKey: false });
    press("ArrowUp", 100, { metaKey: true, shiftKey: true });
    expect(keys.hop).not.toHaveBeenCalled();
    press("ArrowDown", 100, { metaKey: false, ctrlKey: true });
    press("ArrowDown", 100, { metaKey: false, ctrlKey: true });
    expect(keys.toBottom).toHaveBeenCalledTimes(1);
  });

  it("leaves the key alone when there's nothing to hop to, and still counts it", () => {
    const { keys, press } = mount(false);
    expect(press("ArrowUp").defaultPrevented).toBe(false);
    expect(press("ArrowUp").defaultPrevented).toBe(true);
    expect(keys.toTop).toHaveBeenCalledTimes(1);
  });

  it("works with no hop at all (a delegate's stream)", () => {
    const toTop = vi.fn();
    render(<Host toTop={toTop} toBottom={vi.fn()} />);
    fireEvent.keyDown(window, { key: "ArrowUp", metaKey: true });
    now += 100;
    fireEvent.keyDown(window, { key: "ArrowUp", metaKey: true });
    expect(toTop).toHaveBeenCalledTimes(1);
  });
});
