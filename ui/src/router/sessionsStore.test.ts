import { act, renderHook } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { useSessionsList } from "../features/sessions/state/useSessionsList";
import {
  getSessionsSnapshot,
  resetSessionsStoreForTests,
  SESSIONS_FETCH_TIMEOUT_MS,
  subscribeSessions,
} from "./sessionsStore";
import { FakeEventSource } from "./testFakeEventSource";
import type { Session } from "./types";

function makeSession(id: string, activity: Session["activity"] = "active"): Session {
  return {
    id,
    name: `session ${id}`,
    activity,
    created_at: "2026-07-01T00:00:00Z",
    updated_at: "2026-07-01T00:00:00Z",
  };
}

function ok(body: unknown): Response {
  return new Response(JSON.stringify(body), { status: 200 });
}

function deferred<T>() {
  let resolve!: (v: T) => void;
  const promise = new Promise<T>((r) => (resolve = r));
  return { promise, resolve };
}

function sessionsCalls(fetchMock: ReturnType<typeof vi.fn>): number {
  return fetchMock.mock.calls.filter(([url]) => new URL(String(url)).pathname === "/sessions").length;
}

function emitUpdated(seq: number): void {
  FakeEventSource.instances[0].emit(
    "session.updated",
    JSON.stringify({ seq, type: "session.updated", actor: "user", session_id: "s1" }),
  );
}

describe("sessionsStore", () => {
  beforeEach(() => {
    FakeEventSource.instances = [];
    vi.stubGlobal("EventSource", FakeEventSource);
    vi.useFakeTimers();
  });

  afterEach(() => {
    resetSessionsStoreForTests();
    vi.useRealTimers();
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
  });

  it("two mounted readers share ONE GET /sessions", async () => {
    const fetchMock = vi.fn().mockImplementation(() => Promise.resolve(ok([makeSession("s1")])));
    vi.stubGlobal("fetch", fetchMock);

    const { result } = renderHook(() => [useSessionsList(), useSessionsList()] as const);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(1000);
    });
    expect(sessionsCalls(fetchMock)).toBe(1);
    expect(FakeEventSource.instances).toHaveLength(1);
    expect(result.current[0].sessions.map((s) => s.id)).toEqual(["s1"]);
    expect(result.current[1].sessions).toBe(result.current[0].sessions);
  });

  it("1,000 session.updated frames in one tick cost at most 2 GETs", async () => {
    const fetchMock = vi.fn().mockImplementation(() => Promise.resolve(ok([makeSession("s1")])));
    vi.stubGlobal("fetch", fetchMock);

    renderHook(() => useSessionsList());
    await act(async () => {
      await vi.advanceTimersByTimeAsync(0);
    });
    act(() => {
      for (let i = 1; i <= 1000; i++) emitUpdated(i);
    });
    await act(async () => {
      await vi.advanceTimersByTimeAsync(2000);
    });
    expect(sessionsCalls(fetchMock)).toBeLessThanOrEqual(2);
  });

  it("a frame during an in-flight fetch buys exactly one follow-up, and the newer response wins", async () => {
    const mount = deferred<Response>();
    const fetchMock = vi
      .fn()
      .mockReturnValueOnce(mount.promise)
      .mockImplementation(() => Promise.resolve(ok([makeSession("s1", "inactive")])));
    vi.stubGlobal("fetch", fetchMock);

    const { result } = renderHook(() => useSessionsList());
    expect(sessionsCalls(fetchMock)).toBe(1);
    act(() => {
      emitUpdated(1);
      emitUpdated(2);
    });
    await act(async () => {
      await vi.advanceTimersByTimeAsync(1000);
    });
    expect(sessionsCalls(fetchMock)).toBe(1); // still single-flight

    await act(async () => {
      mount.resolve(ok([makeSession("s1", "active")]));
      await vi.advanceTimersByTimeAsync(0);
    });
    await act(async () => {
      await vi.advanceTimersByTimeAsync(1000);
    });
    expect(sessionsCalls(fetchMock)).toBe(2);
    expect(result.current.sessions[0].activity).toBe("inactive");
  });

  it("a failure keeps the list, sets error, and retries after backoff", async () => {
    vi.spyOn(Math, "random").mockReturnValue(0.5);
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(ok([makeSession("s1")]))
      .mockResolvedValueOnce(new Response("bad gateway", { status: 502 }))
      .mockResolvedValueOnce(new Response("bad gateway", { status: 502 }))
      .mockImplementation(() => Promise.resolve(ok([makeSession("s1"), makeSession("s2")])));
    vi.stubGlobal("fetch", fetchMock);

    const { result } = renderHook(() => useSessionsList());
    await act(async () => {
      await vi.advanceTimersByTimeAsync(0);
    });
    act(() => emitUpdated(1));
    await act(async () => {
      await vi.advanceTimersByTimeAsync(50);
    });
    expect(result.current.error).toMatch(/502/);
    expect(result.current.sessions.map((s) => s.id)).toEqual(["s1"]);

    // attempt 0: 0.5 * 2000 = 1000 ms.
    await act(async () => {
      await vi.advanceTimersByTimeAsync(999);
    });
    expect(sessionsCalls(fetchMock)).toBe(2);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(1);
    });
    expect(sessionsCalls(fetchMock)).toBe(3);
    expect(result.current.error).toMatch(/502/);

    // attempt 1: 0.5 * 4000 = 2000 ms.
    await act(async () => {
      await vi.advanceTimersByTimeAsync(2000);
    });
    expect(sessionsCalls(fetchMock)).toBe(4);
    expect(result.current.error).toBeNull();
    expect(result.current.sessions.map((s) => s.id)).toEqual(["s1", "s2"]);

    // Success cleared the retry: nothing more fires on its own.
    await act(async () => {
      await vi.advanceTimersByTimeAsync(60_000);
    });
    expect(sessionsCalls(fetchMock)).toBe(4);
  });

  it("the retry delay never drops below the floor", async () => {
    vi.spyOn(Math, "random").mockReturnValue(0);
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(new Response("boom", { status: 500 }))
      .mockImplementation(() => Promise.resolve(ok([])));
    vi.stubGlobal("fetch", fetchMock);

    renderHook(() => useSessionsList());
    await act(async () => {
      await vi.advanceTimersByTimeAsync(499);
    });
    expect(sessionsCalls(fetchMock)).toBe(1);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(1);
    });
    expect(sessionsCalls(fetchMock)).toBe(2);
  });

  it("the last reader leaving releases the bus; a late response is ignored after reset", async () => {
    const pending = deferred<Response>();
    const fetchMock = vi.fn().mockReturnValueOnce(pending.promise);
    vi.stubGlobal("fetch", fetchMock);

    const first = renderHook(() => useSessionsList());
    const second = renderHook(() => useSessionsList());
    const source = FakeEventSource.instances[0];
    first.unmount();
    expect(source.closed).toBe(false);
    second.unmount();
    expect(source.closed).toBe(true);

    resetSessionsStoreForTests();
    const before = getSessionsSnapshot();
    await act(async () => {
      pending.resolve(ok([makeSession("late")]));
      await vi.advanceTimersByTimeAsync(0);
    });
    expect(getSessionsSnapshot()).toBe(before);
    expect(before.sessions).toEqual([]);
    expect(before.loading).toBe(true);
  });

  it("a remount shows the last list at once, then revalidates", async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(ok([makeSession("s1")]))
      .mockResolvedValueOnce(ok([makeSession("s1"), makeSession("s2")]));
    vi.stubGlobal("fetch", fetchMock);

    const first = renderHook(() => useSessionsList());
    await act(async () => {
      await vi.advanceTimersByTimeAsync(0);
    });
    first.unmount();

    const second = renderHook(() => useSessionsList());
    expect(second.result.current.loading).toBe(false);
    expect(second.result.current.sessions.map((s) => s.id)).toEqual(["s1"]);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(0);
    });
    expect(second.result.current.sessions.map((s) => s.id)).toEqual(["s1", "s2"]);
  });

  it("keeps the snapshot reference when nothing changed", async () => {
    const fetchMock = vi.fn().mockImplementation(() => Promise.resolve(ok([makeSession("s1")])));
    vi.stubGlobal("fetch", fetchMock);

    const listener = vi.fn();
    const unsubscribe = subscribeSessions(listener);
    await vi.advanceTimersByTimeAsync(0);
    const snap = getSessionsSnapshot();
    expect(getSessionsSnapshot()).toBe(snap);
    expect(listener).toHaveBeenCalledTimes(1);

    // An identical refetch is a no-op for readers.
    emitUpdated(1);
    await vi.advanceTimersByTimeAsync(100);
    expect(sessionsCalls(fetchMock)).toBe(2);
    expect(getSessionsSnapshot()).toBe(snap);
    expect(listener).toHaveBeenCalledTimes(1);
    unsubscribe();
  });

  it("a fetch that never settles times out, takes the error path, and frees the next fetch", async () => {
    vi.spyOn(Math, "random").mockReturnValue(0.5);
    const followUp = deferred<Response>();
    const fetchMock = vi
      .fn()
      .mockReturnValueOnce(new Promise<Response>(() => {}))
      .mockReturnValueOnce(followUp.promise)
      .mockImplementation(() => Promise.resolve(ok([makeSession("s1")])));
    vi.stubGlobal("fetch", fetchMock);

    const { result } = renderHook(() => useSessionsList());
    act(() => emitUpdated(1)); // lands mid-flight: one follow-up once it ends
    await act(async () => {
      await vi.advanceTimersByTimeAsync(SESSIONS_FETCH_TIMEOUT_MS - 1);
    });
    expect(result.current.loading).toBe(true);
    expect(sessionsCalls(fetchMock)).toBe(1);

    await act(async () => {
      await vi.advanceTimersByTimeAsync(1);
    });
    expect(result.current.error).toBe("GET /sessions timed out");
    expect(result.current.loading).toBe(false);
    expect(sessionsCalls(fetchMock)).toBe(2); // the hung GET no longer holds the slot

    await act(async () => {
      followUp.resolve(ok([makeSession("s1")]));
      await vi.advanceTimersByTimeAsync(0);
    });
    expect(result.current.error).toBeNull();
    expect(result.current.sessions.map((s) => s.id)).toEqual(["s1"]);

    // And a later event still gets its own fetch.
    act(() => emitUpdated(2));
    await act(async () => {
      await vi.advanceTimersByTimeAsync(50);
    });
    expect(sessionsCalls(fetchMock)).toBe(3);
  });

  it("a timed-out fetch with nothing else pending recovers via the retry", async () => {
    vi.spyOn(Math, "random").mockReturnValue(0.5);
    const fetchMock = vi
      .fn()
      .mockReturnValueOnce(new Promise<Response>(() => {}))
      .mockImplementation(() => Promise.resolve(ok([makeSession("s1")])));
    vi.stubGlobal("fetch", fetchMock);

    const { result } = renderHook(() => useSessionsList());
    await act(async () => {
      await vi.advanceTimersByTimeAsync(SESSIONS_FETCH_TIMEOUT_MS);
    });
    expect(result.current.error).toBe("GET /sessions timed out");
    expect(sessionsCalls(fetchMock)).toBe(1);

    // attempt 0: 0.5 * 2000 = 1000 ms.
    await act(async () => {
      await vi.advanceTimersByTimeAsync(1000);
    });
    expect(sessionsCalls(fetchMock)).toBe(2);
    expect(result.current.error).toBeNull();
  });
});
