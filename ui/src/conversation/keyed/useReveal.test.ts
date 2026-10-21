import { act, renderHook } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { KeyedRow } from "./conversation";
import { useReveal } from "./useReveal";

// A hand-driven requestAnimationFrame: each tick advances the clock by `ms`.
let frames: ((ts: number) => void)[] = [];
let now = 0;
const tick = (ms = 1000) =>
  act(() => {
    now += ms;
    const due = frames;
    frames = [];
    due.forEach((f) => f(now));
  });

beforeEach(() => {
  frames = [];
  now = 0;
  vi.stubGlobal("requestAnimationFrame", (f: (ts: number) => void) => frames.push(f));
  vi.stubGlobal("cancelAnimationFrame", () => {
    frames = [];
  });
});
afterEach(() => vi.unstubAllGlobals());

const paced = (key: string, text: string): KeyedRow => ({ key, role: "agent", text, paced: true, live: true });
const text = (rows: KeyedRow[]) => rows.map((r) => r.text ?? r.role).join("|");

describe("useReveal", () => {
  it("Native (no pace) shows everything as it arrives", () => {
    const rows = [paced("a", "one two three")];
    const { result } = renderHook(() => useReveal(rows, null, false));
    expect(text(result.current.rows)).toBe("one two three");
    expect(result.current.pending).toBe(false);
    expect(frames).toHaveLength(0);
  });

  it("reveals paced text at the pace, frame by frame, and stops when drained", () => {
    // 600 wpm = ten words a second; a frame is at most 0.1s: a word a frame.
    const rows = [paced("a", "one two")];
    const { result } = renderHook(() => useReveal(rows, 600, true));
    expect(text(result.current.rows)).toBe("");
    tick(0); // the first frame only starts the clock
    tick(100);
    expect(text(result.current.rows)).toBe("one");
    tick(5000); // a stalled frame counts as 0.1s, not a dump
    expect(text(result.current.rows)).toBe("one two");
    expect(result.current.pending).toBe(false);
    expect(frames).toHaveLength(0);
  });

  it("is behind with two seconds of reading queued, and revealAll catches up", () => {
    const rows = [paced("a", "w1 w2 w3 w4 w5 w6 w7 w8 w9 w10")];
    const { result } = renderHook(() => useReveal(rows, 60, true));
    expect(result.current.behind).toBe(true);
    act(() => result.current.revealAll());
    expect(text(result.current.rows)).toBe("w1 w2 w3 w4 w5 w6 w7 w8 w9 w10");
    expect(result.current.behind).toBe(false);
    expect(result.current.pending).toBe(false);
  });

  it("switching to Native shows the rest, and back again never re-hides what was shown", () => {
    const rows = [paced("a", "one two three")];
    const { result, rerender } = renderHook(({ wpm }) => useReveal(rows, wpm, true), {
      initialProps: { wpm: 60 as number | null },
    });
    rerender({ wpm: null });
    expect(text(result.current.rows)).toBe("one two three");
    rerender({ wpm: 60 });
    expect(text(result.current.rows)).toBe("one two three");
  });

  it("keeps going as rows grow, holding a tool behind the text before it", () => {
    let rows: KeyedRow[] = [paced("a", "one")];
    const { result, rerender } = renderHook(() => useReveal(rows, 600, false));
    rows = [{ ...rows[0], text: "one two", open: false }, { key: "t", role: "tool", tool: { name: "ls" }, live: true }];
    rerender();
    expect(text(result.current.rows)).toBe("");
    tick(0);
    tick(100); // 600 wpm: one word per 0.1s frame
    expect(text(result.current.rows)).toBe("one");
    tick(100);
    expect(text(result.current.rows)).toBe("one two|tool");
  });

  it("revealAll(only) finishes the rows it picks and paces the rest", () => {
    const rows: KeyedRow[] = [{ ...paced("old", "the last reply"), current: false }, { ...paced("new", "a new one"), current: true }];
    const { result } = renderHook(() => useReveal(rows, 600, false));
    act(() => result.current.revealAll((r) => !r.current));
    expect(text(result.current.rows)).toBe("the last reply|");
    tick(0);
    tick(100);
    expect(text(result.current.rows)).toBe("the last reply|a");
  });
});
