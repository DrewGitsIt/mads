import { act, renderHook } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { FakeRouterClient, fixtureIsolatedRepoState, fixtureIsolatedSession, fixtureRepoWorktree } from "./fixtures";
import { abortError } from "./client";
import { GIT_UPDATING_FAILSAFE_MS, getGitUpdating } from "./gitUpdating";
import {
  REPO_STATE_COLD_RETRY_MS,
  REPO_STATE_FETCH_TIMEOUT_MS,
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

  it("marks its repos updating on a burst of repo.changed, and refetches only once the Router says they changed", async () => {
    const c = client();
    const { result } = renderHook(() => useSessionRepos(c, SID));
    await settle(0);
    c.sessionRepos[SID][0].base_worktree!.ahead = 2;
    act(() => {
      for (let i = 0; i < 50; i++) FakeRouterClient.emitRepoChanged(MADS_GIT);
    });
    await settle();
    // The Router recomputes on repo.changed (docs/GIT-STATE-DESIGN.md): nothing to read yet.
    expect(c.sessionReposCalls).toHaveLength(1);
    // repo.changed recomputes the change set too (an outside commit, stash or reset).
    expect(getGitUpdating(SID)).toBe("repos changeset counts");
    act(() => FakeRouterClient.emitGitStateUpdated(SID, ["repos"]));
    await settle(0);
    expect(c.sessionReposCalls).toHaveLength(2);
    expect(result.current.repos?.[0].base_worktree?.ahead).toBe(2);
    expect(getGitUpdating(SID)).toBe("changeset counts");
    act(() => FakeRouterClient.emitGitStateUpdated(SID, ["changeset", "counts"]));
    expect(getGitUpdating(SID)).toBe("");
  });

  it("doesn't refetch for an announcement whose result didn't change, but clears its mark", async () => {
    const c = client();
    renderHook(() => useSessionRepos(c, SID));
    await settle(0);
    act(() => FakeRouterClient.emitRepoChanged(MADS_GIT));
    expect(getGitUpdating(SID)).toBe("repos changeset counts");
    act(() => FakeRouterClient.emitGitStateUpdated(SID, ["repos"], { changed: [] }));
    await settle();
    expect(c.sessionReposCalls).toHaveLength(1);
    expect(getGitUpdating(SID)).toBe("changeset counts");
  });

  it("ignores an announcement older than its mark", async () => {
    const c = client();
    renderHook(() => useSessionRepos(c, SID));
    await settle(0);
    act(() => FakeRouterClient.emitGitStateUpdated(SID, ["counts"], { generation: 5_000_000 }));
    act(() => FakeRouterClient.emitRepoChanged(MADS_GIT));
    act(() =>
      FakeRouterClient.emitGitStateUpdated(SID, ["repos", "changeset", "counts"], { generation: 5_000_000, changed: [] }),
    );
    expect(getGitUpdating(SID)).toBe("repos changeset counts");
    act(() =>
      FakeRouterClient.emitGitStateUpdated(SID, ["repos", "changeset", "counts"], { generation: 5_000_001, changed: [] }),
    );
    expect(getGitUpdating(SID)).toBe("");
  });

  it("clears a mark that no announcement covers after the fail-safe, and re-reads the repos", async () => {
    const c = client();
    const { result } = renderHook(() => useSessionRepos(c, SID));
    await settle(0);
    expect(c.sessionReposCalls).toHaveLength(1);
    c.sessionRepos[SID][0].base_worktree!.ahead = 7;
    act(() => FakeRouterClient.emitRepoChanged(MADS_GIT));
    expect(getGitUpdating(SID)).not.toBe("");
    await settle(GIT_UPDATING_FAILSAFE_MS - 100);
    expect(getGitUpdating(SID)).not.toBe("");
    expect(c.sessionReposCalls).toHaveLength(1);
    await settle(200);
    expect(getGitUpdating(SID)).toBe("");
    // The announcement was lost: the fail-safe reads what the Router has now.
    expect(c.sessionReposCalls).toHaveLength(2);
    expect(result.current.repos?.[0].base_worktree?.ahead).toBe(7);
  });

  it("marks the repos again when the fail-safe's re-read says the Router is still updating", async () => {
    const c = client();
    renderHook(() => useSessionRepos(c, SID));
    await settle(0);
    act(() => FakeRouterClient.emitRepoChanged(MADS_GIT));
    c.sessionReposUpdating.add(SID);
    await settle(GIT_UPDATING_FAILSAFE_MS + 100);
    expect(c.sessionReposCalls).toHaveLength(2);
    expect(getGitUpdating(SID)).toContain("repos");
  });

  it("treats a cold answer ([] and X-Mads-Updating: 1) as loading, not as no repos", async () => {
    const c = client();
    c.sessionReposUpdating.add(SID);
    const stored = c.sessionRepos[SID];
    c.sessionRepos[SID] = [];
    vi.spyOn(c, "getSessionReposState").mockImplementationOnce(async (id, opts) => ({
      repos: await c.getSessionRepos(id, opts),
      updating: true,
      gitState: true,
    }));
    const { result } = renderHook(() => useSessionRepos(c, SID));
    await settle(0);
    expect(c.sessionReposCalls).toHaveLength(1);
    expect(result.current.repos).toBeNull();
    expect(getGitUpdating(SID)).toBe("repos");
    c.sessionRepos[SID] = stored;
    c.sessionReposUpdating.delete(SID);
    act(() => FakeRouterClient.emitGitStateUpdated(SID, ["repos"]));
    await settle(0);
    expect(result.current.repos).toHaveLength(1);
    expect(getGitUpdating(SID)).toBe("");
  });

  it("reads a cold answer again later if no announcement comes", async () => {
    const c = client();
    vi.spyOn(c, "getSessionReposState").mockImplementationOnce(async () => ({ repos: [], updating: true, gitState: true }));
    const { result } = renderHook(() => useSessionRepos(c, SID));
    await settle(0);
    expect(result.current.repos).toBeNull();
    await settle(REPO_STATE_COLD_RETRY_MS);
    expect(result.current.repos).toHaveLength(1);
  });

  it("against a Router that omits the headers (an older peer), refetches on repo.changed itself and marks nothing", async () => {
    const c = client();
    c.sessionReposLegacy.add(SID);
    const { result } = renderHook(() => useSessionRepos(c, SID));
    await settle(0);
    c.sessionRepos[SID][0].base_worktree!.ahead = 2;
    act(() => {
      for (let i = 0; i < 50; i++) FakeRouterClient.emitRepoChanged(MADS_GIT);
    });
    await settle();
    expect(c.sessionReposCalls).toHaveLength(2);
    expect(result.current.repos?.[0].base_worktree?.ahead).toBe(2);
    expect(getGitUpdating(SID)).toBe("");
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
    expect(getGitUpdating(SID)).toBe("repos changeset counts");
  });

  it.each(["commit.created", "commit.undone", "merge.completed"])(
    "marks every part updating on the session's %s, not another session's, without a read",
    async (type) => {
      const c = client();
      renderHook(() => useSessionRepos(c, SID));
      await settle(0);
      act(() => emitSession(type, "someone-else"));
      expect(getGitUpdating(SID)).toBe("");
      act(() => emitSession(type));
      await settle();
      expect(getGitUpdating(SID)).toBe("repos changeset counts");
      expect(c.sessionReposCalls).toHaveLength(1);
    },
  );

  it("does nothing on the session's session.updated (the Router's own counts write sends it)", async () => {
    const c = client();
    renderHook(() => useSessionRepos(c, SID));
    await settle(0);
    act(() => emitSession("session.updated"));
    await settle();
    expect(c.sessionReposCalls).toHaveLength(1);
    expect(getGitUpdating(SID)).toBe("");
  });

  it.each(["session.updated", "commit.created", "commit.undone", "merge.completed"])(
    "against an older Router, refetches on the session's %s, not another session's",
    async (type) => {
      const c = client();
      c.sessionReposLegacy.add(SID);
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

  it("subscribes to the session's session.git_state_updated", async () => {
    const c = client();
    renderHook(() => useSessionRepos(c, SID));
    await settle(0);
    const scoped = FakeEventSource.instances.filter((s) => !s.closed && s.url.includes("session.git_state_updated"));
    expect(scoped).toHaveLength(1);
    expect(c.sessionReposCalls).toEqual([SID]);
  });

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
  /** A getSessionRepos that never answers, rejecting with an AbortError when its signal aborts (as fetch does). */
  function hang(c: FakeRouterClient) {
    const signals: AbortSignal[] = [];
    const spy = vi.spyOn(c, "getSessionRepos").mockImplementation((_id, opts) => {
      const signal = opts?.signal;
      if (signal) signals.push(signal);
      return new Promise((_, reject) => signal?.addEventListener("abort", () => reject(abortError())));
    });
    return { spy, signals };
  }

  it("aborts the read in flight when its last reader leaves, silently", async () => {
    const c = client();
    const { spy, signals } = hang(c);
    const a = renderHook(() => useSessionRepos(c, SID));
    const b = renderHook(() => useSessionRepos(c, SID));
    await settle(0);
    expect(spy).toHaveBeenCalledTimes(1);
    a.unmount();
    expect(signals[0].aborted).toBe(false);
    b.unmount();
    expect(signals[0].aborted).toBe(true);
    await settle(0);
    // No error surfaced and no retry scheduled for an abort.
    expect(getSessionReposSnapshot(SID).error).toBeNull();
    await settle(31_000);
    expect(spy).toHaveBeenCalledTimes(1);
  });

  it("aborts a timed-out read before its retry, so only one request is ever out", async () => {
    const c = client();
    const { spy, signals } = hang(c);
    const { result } = renderHook(() => useSessionRepos(c, SID));
    await settle(0);
    expect(spy).toHaveBeenCalledTimes(1);
    await settle(REPO_STATE_FETCH_TIMEOUT_MS);
    expect(signals[0].aborted).toBe(true);
    expect(result.current.error).toMatch(/timed out/);
    spy.mockRestore();
    await settle(31_000);
    expect(result.current.repos).not.toBeNull();
    expect(result.current.error).toBeNull();
  });
});
