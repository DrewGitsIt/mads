import { renderHook } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { FakeRouterClient } from "../../router/fixtures";
import {
  WORKTREE_REFRESH_FOCUS_SKIP_MS,
  heldWorktreeRefreshesForTests,
  requestWorktreeRefresh,
  useWorktreeRefresh,
} from "./worktreeRefresh";

const SID = "session-active";

function holdRefreshes(c: FakeRouterClient) {
  const pending: (() => void)[] = [];
  c.refreshWorktreeCountsOutcome = () => new Promise<void>((resolve) => pending.push(resolve));
  return pending;
}

const flush = () => new Promise((r) => setTimeout(r, 0));

describe("worktreeRefresh", () => {
  afterEach(() => vi.restoreAllMocks());

  it("is single-flight across remounts: a remount joins the recount already out", async () => {
    const c = new FakeRouterClient();
    const pending = holdRefreshes(c);
    const a = renderHook(() => useWorktreeRefresh(c, SID));
    const b = renderHook(() => useWorktreeRefresh(c, SID));
    expect(c.refreshWorktreeCountsCalls).toEqual([SID]);
    a.unmount();
    const again = renderHook(() => useWorktreeRefresh(c, SID));
    expect(c.refreshWorktreeCountsCalls).toEqual([SID]);
    pending[0]();
    await flush();
    b.unmount();
    again.unmount();
  });

  it("aborts the recount once every view showing the session has gone", async () => {
    const c = new FakeRouterClient();
    holdRefreshes(c);
    const signals: AbortSignal[] = [];
    const real = c.refreshWorktreeCounts.bind(c);
    vi.spyOn(c, "refreshWorktreeCounts").mockImplementation((id, opts) => {
      if (opts?.signal) signals.push(opts.signal);
      return real(id, opts);
    });
    const a = renderHook(() => useWorktreeRefresh(c, SID));
    const b = renderHook(() => useWorktreeRefresh(c, SID));
    a.unmount();
    expect(signals[0].aborted).toBe(false);
    b.unmount();
    expect(signals[0].aborted).toBe(true);
    await flush();
    // An aborted recount doesn't count as finished: the next focus recounts.
    const { release } = requestWorktreeRefresh(c, SID, "focus");
    expect(c.refreshWorktreeCountsCalls).toEqual([SID, SID]);
    release();
  });

  it("skips a focus within the skip window of a finished recount, then recounts after it", async () => {
    const c = new FakeRouterClient();
    let now = 1_000_000;
    vi.spyOn(Date, "now").mockImplementation(() => now);
    const { unmount } = renderHook(() => useWorktreeRefresh(c, SID));
    await flush();
    expect(c.refreshWorktreeCountsCalls).toHaveLength(1);
    now += WORKTREE_REFRESH_FOCUS_SKIP_MS - 1;
    window.dispatchEvent(new Event("focus"));
    expect(c.refreshWorktreeCountsCalls).toHaveLength(1);
    now += 2;
    window.dispatchEvent(new Event("focus"));
    expect(c.refreshWorktreeCountsCalls).toHaveLength(2);
    await flush();
    // Opening the session always recounts, however recent the last one.
    unmount();
    renderHook(() => useWorktreeRefresh(c, SID));
    expect(c.refreshWorktreeCountsCalls).toHaveLength(3);
  });

  it("keeps sessions apart", () => {
    const c = new FakeRouterClient();
    holdRefreshes(c);
    renderHook(() => useWorktreeRefresh(c, SID));
    renderHook(() => useWorktreeRefresh(c, "other"));
    expect(c.refreshWorktreeCountsCalls).toEqual([SID, "other"]);
  });

  it("holds a release only while its recount runs: many focuses don't pile up", async () => {
    const c = new FakeRouterClient();
    let now = 1_000_000;
    vi.spyOn(Date, "now").mockImplementation(() => now);
    const { unmount } = renderHook(() => useWorktreeRefresh(c, SID));
    await flush();
    for (let i = 0; i < 20; i++) {
      now += WORKTREE_REFRESH_FOCUS_SKIP_MS + 1;
      window.dispatchEvent(new Event("focus"));
      window.dispatchEvent(new Event("focus")); // skipped or joined
      await flush();
    }
    expect(c.refreshWorktreeCountsCalls).toHaveLength(21);
    expect(heldWorktreeRefreshesForTests()).toBe(0);
    unmount();
    expect(heldWorktreeRefreshesForTests()).toBe(0);
  });
});
