import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, renderHook } from "@testing-library/react";
import { FOLD_MS } from "../../../components/Motion";
import type { Session } from "../../../router/types";
import { isSetAside } from "./railGroups";
import { LIFT_MS, STRIKE_MS, useDoneMotion } from "./useDoneMotion";

function session(overrides: Partial<Session> = {}): Session {
  return {
    id: "s1",
    name: "Settings UX audit",
    activity: "active",
    created_at: "2026-10-01T00:00:00Z",
    updated_at: "2026-10-20T10:00:00Z",
    ...overrides,
  };
}

const placed = (motion: ReturnType<typeof useDoneMotion>, s: Session) => isSetAside(motion.apply([s])[0]);

describe("useDoneMotion", () => {
  beforeEach(() => vi.useFakeTimers());
  afterEach(() => vi.useRealTimers());

  it("marks a check-off at once and holds the card in Active until the server answers", () => {
    const s = session();
    const { result } = renderHook(() => useDoneMotion());

    act(() => result.current.sent(s, true));
    expect(result.current.markOf("s1")).toBe("strike");
    expect(placed(result.current, s)).toBe(false);

    // A refetch can see the tag before the request's own response arrives;
    // the card still waits for its answer and its beat.
    expect(placed(result.current, { ...s, tags: ["aside"] })).toBe(false);

    act(() => vi.advanceTimersByTime(STRIKE_MS * 3));
    expect(result.current.markOf("s1")).toBe("strike");
    expect(placed(result.current, s)).toBe(false);
  });

  it("moves an answered check-off once its strike has shown, folding across, then hands back to the list", () => {
    const s = session();
    const answered = { ...s, tags: ["aside"], updated_at: "2026-10-20T10:00:01Z" };
    const { result } = renderHook(() => useDoneMotion());

    act(() => result.current.sent(s, true));
    act(() => result.current.confirmed(answered, true));
    // Answered fast: the strike still gets its beat.
    expect(result.current.leavingDone("s1")).toBeUndefined();
    expect(placed(result.current, s)).toBe(false);

    act(() => vi.advanceTimersByTime(STRIKE_MS));
    expect(result.current.leavingDone("s1")).toBe(false); // folding out of Active
    expect(result.current.markOf("s1")).toBe("strike");
    expect(placed(result.current, s)).toBe(true);

    act(() => vi.advanceTimersByTime(FOLD_MS));
    expect(result.current.leavingDone("s1")).toBeUndefined();
    expect(result.current.markOf("s1")).toBeUndefined();
    // The list hasn't refetched yet: the server's answer still places it.
    expect(placed(result.current, s)).toBe(true);

    act(() => result.current.settle([answered]));
    expect(placed(result.current, s)).toBe(false); // the stale record is the list's own again
  });

  it("moves at once when the answer comes after the strike's beat", () => {
    const s = session();
    const { result } = renderHook(() => useDoneMotion());

    act(() => result.current.sent(s, true));
    act(() => vi.advanceTimersByTime(STRIKE_MS + 900));
    act(() => result.current.confirmed({ ...s, tags: ["aside"] }, true));
    act(() => vi.advanceTimersByTime(0));
    expect(result.current.leavingDone("s1")).toBe(false);
  });

  it("takes the mark away and leaves the card in place when the server refuses", () => {
    const s = session();
    const { result } = renderHook(() => useDoneMotion());

    act(() => result.current.sent(s, true));
    act(() => result.current.failed("s1"));
    expect(result.current.markOf("s1")).toBeUndefined();
    expect(placed(result.current, s)).toBe(false);
    act(() => vi.advanceTimersByTime(STRIKE_MS + FOLD_MS));
    expect(result.current.leavingDone("s1")).toBeUndefined();
  });

  it("lifts a brought-back card, then places it at the rank the server gave it", () => {
    const s = session({ tags: ["aside"], rail_rank: 100 });
    const answered = { ...s, tags: [], rail_rank: 9_999, updated_at: "2026-10-20T10:00:01Z" };
    const { result } = renderHook(() => useDoneMotion());

    act(() => result.current.sent(s, false));
    expect(result.current.markOf("s1")).toBe("lift");
    act(() => result.current.confirmed(answered, false));
    act(() => vi.advanceTimersByTime(LIFT_MS));
    expect(result.current.leavingDone("s1")).toBe(true);
    const shown = result.current.apply([s])[0];
    expect(isSetAside(shown)).toBe(false);
    expect(shown.rail_rank).toBe(9_999);
  });

  it("ignores an answer a later click superseded", () => {
    const s = session();
    const { result } = renderHook(() => useDoneMotion());

    act(() => result.current.sent(s, true));
    act(() => result.current.sent(s, false)); // un-checked mid-strike
    expect(result.current.markOf("s1")).toBeUndefined();
    act(() => result.current.confirmed({ ...s, tags: ["aside"] }, true));
    act(() => vi.advanceTimersByTime(STRIKE_MS + FOLD_MS));
    expect(result.current.leavingDone("s1")).toBeUndefined();
    expect(placed(result.current, { ...s, tags: ["aside"] })).toBe(false);
  });

  it("lets the list win once it has moved past the server's answer", () => {
    const s = session();
    const answered = { ...s, tags: ["aside"], updated_at: "2026-10-20T10:00:01Z" };
    const { result } = renderHook(() => useDoneMotion());

    act(() => result.current.sent(s, true));
    act(() => result.current.confirmed(answered, true));
    act(() => vi.advanceTimersByTime(STRIKE_MS + FOLD_MS));
    // Brought back from another window since: newer, and not aside.
    const newer = { ...s, tags: [], updated_at: "2026-10-20T10:05:00Z" };
    act(() => result.current.settle([newer]));
    expect(placed(result.current, newer)).toBe(false);
  });
});
