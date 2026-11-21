import { act, renderHook } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import type { Session } from "../../../router/types";
import { isGoneError, nextSelection, useSelectionFollowsList } from "./selectionFollowsList";

const order = { active: ["a", "b", "c"], done: ["x", "y"] };
const s = (id: string) => ({ id }) as Session;

describe("nextSelection", () => {
  it("opens the next card in the same section", () => {
    expect(nextSelection(order, "b")).toBe("c");
    expect(nextSelection(order, "x")).toBe("y");
  });
  it("falls back to the previous card at the end of a section", () => {
    expect(nextSelection(order, "c")).toBe("b");
    expect(nextSelection(order, "y")).toBe("x");
  });
  it("never crosses into Done: the last active card goes back, not down", () => {
    expect(nextSelection({ active: ["a"], done: ["x"] }, "a")).toBeUndefined();
  });
  it("skips cards that went too", () => {
    expect(nextSelection(order, "a", new Set(["b"]))).toBe("c");
  });
  it("is the Launchpad for a session the rail isn't showing", () => {
    expect(nextSelection(order, "zz")).toBeUndefined();
  });
});

describe("useSelectionFollowsList", () => {
  function setup(initial: { sessions: Session[]; open?: string; loading?: boolean; error?: string | null }) {
    const select = vi.fn();
    const hook = renderHook(
      (p: { sessions: Session[]; open?: string; loading?: boolean; error?: string | null }) =>
        useSelectionFollowsList({
          sessions: p.sessions,
          loading: p.loading ?? false,
          error: p.error ?? null,
          openSessionId: p.open,
          select,
        }),
      { initialProps: initial },
    );
    return { ...hook, select };
  }

  it("opens the neighbour when the open session leaves the list", () => {
    const { result, rerender, select } = setup({ sessions: [s("a"), s("b"), s("c")], open: "b" });
    act(() => result.current.reportOrder({ active: ["a", "b", "c"], done: [] }));
    // The rail re-reports first (its effects run before ours): the order
    // without "b" must not lose its neighbours.
    act(() => result.current.reportOrder({ active: ["a", "c"], done: [] }));
    rerender({ sessions: [s("a"), s("c")], open: "b" });
    expect(select).toHaveBeenCalledWith("c");
  });

  it("goes to the Launchpad when nothing is left", () => {
    const { result, rerender, select } = setup({ sessions: [s("a")], open: "a" });
    act(() => result.current.reportOrder({ active: ["a"], done: [] }));
    rerender({ sessions: [], open: "a" });
    expect(select).toHaveBeenCalledWith(undefined);
  });

  it("leaves a just-created session alone until its row has ever arrived", () => {
    const { rerender, select } = setup({ sessions: [s("a")], open: "new" });
    rerender({ sessions: [s("a")], open: "new" });
    expect(select).not.toHaveBeenCalled();
  });

  it("ignores a list that is loading or failed", () => {
    const { rerender, select } = setup({ sessions: [s("a"), s("b")], open: "b" });
    rerender({ sessions: [], open: "b", error: "GET /sessions timed out" });
    rerender({ sessions: [], open: "b", loading: true });
    expect(select).not.toHaveBeenCalled();
  });

  it("deleting a session that isn't open moves nothing", () => {
    const { rerender, select } = setup({ sessions: [s("a"), s("b")], open: "a" });
    rerender({ sessions: [s("a")], open: "a" });
    expect(select).not.toHaveBeenCalled();
  });

  it("keeps an off-list selection open after a scope change, until the user picks another", () => {
    const { result, rerender, select } = setup({ sessions: [s("a"), s("r")], open: "r" });
    act(() => result.current.keepOffList("r"));
    rerender({ sessions: [s("a")], open: "r" });
    expect(select).not.toHaveBeenCalled();
    // Picked something else, then came back to a list without it.
    rerender({ sessions: [s("a"), s("r")], open: "a" });
    rerender({ sessions: [s("a")], open: "a" });
    expect(select).not.toHaveBeenCalled();
  });

  it("leave() moves to the neighbour at once", () => {
    const { result, select } = setup({ sessions: [s("a"), s("b"), s("c")], open: "a" });
    act(() => result.current.reportOrder({ active: ["a", "b", "c"], done: [] }));
    act(() => result.current.leave("a"));
    expect(select).toHaveBeenCalledWith("b");
  });

  it("is stable across renders", () => {
    const { result, rerender } = setup({ sessions: [s("a")], open: "a" });
    const first = result.current;
    rerender({ sessions: [s("a")], open: "a" });
    expect(result.current).toBe(first);
  });
});

describe("isGoneError", () => {
  it("is a 404 from the Router, nothing else", () => {
    expect(isGoneError(new Error('router request failed: 404 {"error":"not found"}'))).toBe(true);
    expect(isGoneError(new Error("router request failed: 502 bad gateway"))).toBe(false);
    expect(isGoneError("404")).toBe(false);
  });
});
