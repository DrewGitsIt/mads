import { afterEach, describe, expect, it, vi } from "vitest";
import { renderHook, waitFor } from "@testing-library/react";
import type { ActivityEntry } from "../activity/conversationItems";
import { findKey, type FindOptions } from "./findText";
import { useTranscriptMatches, type Searchable } from "./useTranscriptMatches";

const group = (key: string, output: string): Searchable => ({
  key,
  entries: [{ type: "tool", tool: { id: `${key}-t`, name: "Bash", input: { command: "ls" }, result: { text: output } } } as ActivityEntry],
});

const sources = Array.from({ length: 40 }, (_, i) => group(`g${i}`, `worktree ${i} and the worktree again`));
const opts = (query: string): FindOptions => ({ query, matchCase: false, skipTools: false });

describe("useTranscriptMatches", () => {
  afterEach(() => vi.restoreAllMocks());

  // Every reading of the clock moves it a slice on: each slice matches one
  // text, then hands the thread back.
  const slowClock = () => {
    let t = 0;
    vi.spyOn(performance, "now").mockImplementation(() => (t += 5));
  };

  it("matches across slices, without holding the render up", async () => {
    slowClock();
    const { result } = renderHook(() => useTranscriptMatches(sources, opts("worktree")));
    // Not done in the render (nor the first slice): nothing claims to be for this search yet.
    expect(result.current.key).toBe("");
    await waitFor(() => expect(result.current.key).toBe(findKey(opts("worktree"))));
    expect(result.current.groups.size).toBe(40);
    expect(result.current.groups.get("g7")?.count).toBe(2);
  });

  it("drops a search in progress for the next one", async () => {
    slowClock();
    const { result, rerender } = renderHook(({ q }) => useTranscriptMatches(sources, opts(q)), {
      initialProps: { q: "worktree" },
    });
    rerender({ q: "again" });
    await waitFor(() => expect(result.current.key).toBe(findKey(opts("again"))));
    expect([...result.current.groups.values()].every((g) => g.count === 1)).toBe(true);
  });

  it("keeps the same result when content adds no matches, so the stream needn't re-render", () => {
    const { result, rerender } = renderHook(({ s }) => useTranscriptMatches(s, opts("worktree")), {
      initialProps: { s: sources },
    });
    const first = result.current;
    expect(first.key).toBe(findKey(opts("worktree")));
    rerender({ s: [...sources, group("extra", "nothing to see")] });
    expect(result.current).toBe(first);
  });
});
