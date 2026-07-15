import { renderHook } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { useAttentionBadge } from "./useAttentionBadge";
import type { Session } from "../../../router/types";

function makeSession(overrides: Partial<Session>): Session {
  return {
    id: "s1",
    name: "session",
    activity: "inactive",
    created_at: "2026-07-01T00:00:00Z",
    updated_at: "2026-07-01T00:00:00Z",
    ...overrides,
  };
}

describe("useAttentionBadge", () => {
  afterEach(() => {
    delete (window as { mads?: unknown }).mads;
  });

  it("pushes the derived attention count to the badge bridge on mount", () => {
    const setCount = vi.fn();
    (window as { mads?: unknown }).mads = { badge: { setCount } };
    const sessions = [makeSession({ id: "s1" })];

    renderHook(() => useAttentionBadge(sessions, { s1: 1 }));

    expect(setCount).toHaveBeenCalledWith(1);
  });

  it("re-pushes the badge bridge when the derived count changes across rerenders", () => {
    const setCount = vi.fn();
    (window as { mads?: unknown }).mads = { badge: { setCount } };
    const sessions = [makeSession({ id: "s1" })];

    const { rerender } = renderHook(
      ({ openTaskCounts }) => useAttentionBadge(sessions, openTaskCounts),
      { initialProps: { openTaskCounts: { s1: 0 } } },
    );
    expect(setCount).toHaveBeenLastCalledWith(0);

    rerender({ openTaskCounts: { s1: 1 } });
    expect(setCount).toHaveBeenLastCalledWith(1);
  });

  it("does not throw when no Electron badge bridge is present", () => {
    const sessions = [makeSession({ id: "s1" })];
    expect(() => renderHook(() => useAttentionBadge(sessions, { s1: 1 }))).not.toThrow();
  });
});
