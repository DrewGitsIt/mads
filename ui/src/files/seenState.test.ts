import { describe, expect, it } from "vitest";
import { act, renderHook } from "@testing-library/react";
import { isUnseen, loadSeen, saveSeen, seenKey, useSeenState, withSeen } from "./seenState";

const f = { path: "/w/a.go", last_changed: "2026-10-14T10:00:00Z" };

describe("seen state", () => {
  it("a file is new with no entry, or when it changed after the entry", () => {
    expect(isUnseen({}, f)).toBe(true);
    expect(isUnseen({ [f.path]: "2026-10-14T10:00:00Z" }, f)).toBe(false);
    expect(isUnseen({ [f.path]: "2026-10-14T09:59:59Z" }, f)).toBe(true);
  });

  it("withSeen records the file's own last_changed", () => {
    const next = withSeen({}, f);
    expect(next).toEqual({ [f.path]: f.last_changed });
    expect(withSeen(next, f)).toBe(next);
  });

  it("persists per session under mads.changes.seen.<id> and survives junk", () => {
    saveSeen("s1", { a: "x" });
    expect(localStorage.getItem(seenKey("s1"))).toBe('{"a":"x"}');
    expect(loadSeen("s1")).toEqual({ a: "x" });
    expect(loadSeen("s2")).toEqual({});
    localStorage.setItem(seenKey("s3"), "not json");
    expect(loadSeen("s3")).toEqual({});
  });

  it("two hooks on one session stay in sync", () => {
    const a = renderHook(() => useSeenState("s1"));
    const b = renderHook(() => useSeenState("s1"));
    expect(b.result.current.isUnseen(f)).toBe(true);
    act(() => a.result.current.markSeen(f));
    expect(a.result.current.isUnseen(f)).toBe(false);
    expect(b.result.current.isUnseen(f)).toBe(false);
  });
});
