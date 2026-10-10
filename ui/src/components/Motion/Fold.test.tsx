import { describe, expect, it, vi } from "vitest";
import { act, render, renderHook, screen } from "@testing-library/react";
import { FOLD_MS, Fold } from "./Fold";
import { useArrivals } from "./useArrivals";
import { usePresence } from "./usePresence";

// Content → fold inner → fold.
const fold = (el: HTMLElement) => el.parentElement!.parentElement!;

describe("Fold", () => {
  it("mounted open shows its content at once, and only folds in when asked to appear", () => {
    const { unmount } = render(
      <Fold>
        <span>here</span>
      </Fold>,
    );
    const settled = fold(screen.getByText("here"));
    expect(settled.hasAttribute("data-open")).toBe(true);
    expect(settled.hasAttribute("data-appear")).toBe(false);
    expect(settled.hasAttribute("data-settled")).toBe(true);
    unmount();

    vi.useFakeTimers();
    try {
      render(
        <Fold appear fade>
          <span>arriving</span>
        </Fold>,
      );
      const arriving = fold(screen.getByText("arriving"));
      expect(arriving.hasAttribute("data-appear")).toBe(true);
      // Clips while it moves; may overflow once it has settled.
      expect(arriving.hasAttribute("data-settled")).toBe(false);
      act(() => vi.advanceTimersByTime(FOLD_MS));
      expect(arriving.hasAttribute("data-settled")).toBe(true);
    } finally {
      vi.useRealTimers();
    }
  });

  it("closing keeps the content until the fold has shut — hidden and inert meanwhile — then unmounts it", () => {
    vi.useFakeTimers();
    try {
      const body = (open: boolean) => (
        <Fold open={open}>
          <span>body</span>
        </Fold>
      );
      const { rerender } = render(body(true));
      rerender(body(false));
      const inner = screen.getByText("body").parentElement!;
      expect(inner.getAttribute("aria-hidden")).toBe("true");
      expect((inner as HTMLElement & { inert: boolean }).inert).toBe(true);
      act(() => vi.advanceTimersByTime(FOLD_MS - 1));
      expect(screen.queryByText("body")).not.toBeNull();
      act(() => vi.advanceTimersByTime(1));
      expect(screen.queryByText("body")).toBeNull();

      // Reopened: back at once, and folding open.
      rerender(body(true));
      expect(screen.getByText("body").parentElement!.hasAttribute("aria-hidden")).toBe(false);
    } finally {
      vi.useRealTimers();
    }
  });
});

describe("useArrivals", () => {
  it("counts only keys added after mount, and each only until it has rendered once", () => {
    // Read during render, the way a component asks.
    const { result, rerender } = renderHook(({ keys }) => {
      const arrived = useArrivals(keys);
      return keys.map(arrived);
    }, { initialProps: { keys: ["a"] } });
    expect(result.current).toEqual([false]);
    rerender({ keys: ["a", "b"] });
    expect(result.current).toEqual([false, true]);
    rerender({ keys: ["a", "b"] });
    expect(result.current).toEqual([false, false]);
  });

  it("disabled, nothing arrives", () => {
    const { result, rerender } = renderHook(({ keys }) => {
      const arrived = useArrivals(keys, false);
      return keys.map(arrived);
    }, { initialProps: { keys: ["a"] } });
    rerender({ keys: ["a", "b"] });
    expect(result.current).toEqual([false, false]);
  });
});

describe("usePresence", () => {
  it("stays present and leaving for the exit, then goes", () => {
    vi.useFakeTimers();
    try {
      const { result, rerender } = renderHook(({ show }) => usePresence(show, 120), {
        initialProps: { show: true },
      });
      expect(result.current).toEqual({ present: true, leaving: false });
      rerender({ show: false });
      expect(result.current).toEqual({ present: true, leaving: true });
      act(() => vi.advanceTimersByTime(120));
      expect(result.current).toEqual({ present: false, leaving: false });
      // Shown again mid-exit: back to plainly present.
      rerender({ show: true });
      rerender({ show: false });
      rerender({ show: true });
      expect(result.current).toEqual({ present: true, leaving: false });
    } finally {
      vi.useRealTimers();
    }
  });
});
