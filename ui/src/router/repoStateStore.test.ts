import { act, renderHook } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { FakeRouterClient, fixtureIsolatedRepoState, fixtureIsolatedSession, fixtureRepoWorktree } from "./fixtures";
import {
  getSessionReposSnapshot,
  refreshSessionRepos,
  resetRepoStateStoreForTests,
  useRepoChanged,
  useSessionRepos,
} from "./repoStateStore";
import { FakeEventSource } from "./testFakeEventSource";
import type { SessionRepoState } from "./types";

const SID = fixtureIsolatedSession.id;
const MADS_GIT = "/home/user/repo/mads/.git";

function client(repos: SessionRepoState[] = [structuredClone(fixtureIsolatedRepoState)]) {
  const c = new FakeRouterClient({ [SID]: fixtureIsolatedSession });
  c.sessionRepos[SID] = repos;
  return c;
}

let nextSeq = 1;
function emitSession(type: string, sessionId = SID): void {
  const data = JSON.stringify({ seq: nextSeq++, type, actor: "system", session_id: sessionId, payload: {} });
  for (const s of FakeEventSource.instances) if (!s.closed && s.url.includes(type)) s.emit(type, data);
}

async function settle(ms = 600): Promise<void> {
  await act(async () => {
    await vi.advanceTimersByTimeAsync(ms);
  });
}

describe("repoStateStore (COMMIT-LAND-PUSH D10)", () => {
  beforeEach(() => {
    FakeEventSource.instances = [];
    vi.stubGlobal("EventSource", FakeEventSource);
    vi.useFakeTimers();
  });

  afterEach(() => {
    resetRepoStateStoreForTests();
    vi.useRealTimers();
    vi.unstubAllGlobals();
  });

  it("is unknown until the first answer, then loads once for every reader", async () => {
    const c = client();
    const { result } = renderHook(() => [useSessionRepos(c, SID), useSessionRepos(c, SID)] as const);
    expect(result.current[0].repos).toBeNull();
    await settle(0);
    expect(c.sessionReposCalls).toEqual([SID]);
    expect(result.current[0].repos?.[0].base_worktree?.git_dir).toBe(MADS_GIT);
    expect(result.current[1].repos).toBe(result.current[0].repos);
    // One window-wide subscription.
    expect(FakeEventSource.instances.filter((s) => !s.closed)).toHaveLength(1);
    expect(FakeEventSource.instances[0].url).toContain("repo.changed");
  });

  it("refetches once for a burst of repo.changed naming one of its repos", async () => {
    const c = client();
    const { result } = renderHook(() => useSessionRepos(c, SID));
    await settle(0);
    c.sessionRepos[SID][0].base_worktree!.ahead = 2;
    act(() => {
      for (let i = 0; i < 50; i++) FakeRouterClient.emitRepoChanged(MADS_GIT);
    });
    await settle();
    expect(c.sessionReposCalls).toHaveLength(2);
    expect(result.current.repos?.[0].base_worktree?.ahead).toBe(2);
  });

  it("ignores repo.changed for repos the session isn't in", async () => {
    const c = client();
    renderHook(() => useSessionRepos(c, SID));
    await settle(0);
    act(() => FakeRouterClient.emitRepoChanged("/home/user/repo/other/.git"));
    await settle();
    expect(c.sessionReposCalls).toHaveLength(1);
  });

  it("matches a second repo by its worktree's git dir", async () => {
    const learn: SessionRepoState = {
      root: "/home/user/repo/learn",
      worktree: fixtureRepoWorktree({ path: "/home/user/repo/learn", main: true }),
    };
    const c = client([structuredClone(fixtureIsolatedRepoState), learn]);
    renderHook(() => useSessionRepos(c, SID));
    await settle(0);
    act(() => FakeRouterClient.emitRepoChanged("/home/user/repo/learn/.git"));
    await settle();
    expect(c.sessionReposCalls).toHaveLength(2);
  });

  it.each(["session.updated", "commit.created", "commit.undone", "merge.completed"])(
    "refetches on the session's %s, not another session's",
    async (type) => {
      const c = client();
      renderHook(() => useSessionRepos(c, SID));
      await settle(0);
      act(() => emitSession(type, "someone-else"));
      await settle();
      expect(c.sessionReposCalls).toHaveLength(1);
      act(() => emitSession(type));
      await settle();
      expect(c.sessionReposCalls).toHaveLength(2);
    },
  );

  it("tells change listeners when one of the session's repos changed", async () => {
    const c = client();
    const onChange = vi.fn();
    renderHook(() => {
      useSessionRepos(c, SID);
      useRepoChanged(c, SID, onChange);
    });
    await settle(0);
    act(() => FakeRouterClient.emitRepoChanged("/home/user/repo/other/.git"));
    expect(onChange).not.toHaveBeenCalled();
    act(() => FakeRouterClient.emitRepoChanged(MADS_GIT));
    expect(onChange).toHaveBeenCalledTimes(1);
  });

  it("keeps the last answer for a remount and revalidates behind it", async () => {
    const c = client();
    const first = renderHook(() => useSessionRepos(c, SID));
    await settle(0);
    const kept = first.result.current.repos;
    first.unmount();
    expect(getSessionReposSnapshot(SID).repos).toBe(kept);
    const second = renderHook(() => useSessionRepos(c, SID));
    expect(second.result.current.repos).toBe(kept);
    await settle(0);
    expect(c.sessionReposCalls).toHaveLength(2);
    // Unchanged: the same array, so readers don't re-render for nothing.
    expect(second.result.current.repos).toBe(kept);
  });

  it("refreshes on demand, and retries a failed read", async () => {
    const c = client();
    const spy = vi.spyOn(c, "getSessionRepos").mockRejectedValueOnce(new Error("router restarting"));
    const { result } = renderHook(() => useSessionRepos(c, SID));
    await settle(0);
    expect(result.current).toEqual({ repos: null, error: "router restarting" });
    await settle(31_000);
    expect(result.current.repos).not.toBeNull();
    expect(result.current.error).toBeNull();
    const calls = spy.mock.calls.length;
    act(() => refreshSessionRepos(SID));
    await settle(0);
    expect(spy.mock.calls.length).toBe(calls + 1);
  });
});
