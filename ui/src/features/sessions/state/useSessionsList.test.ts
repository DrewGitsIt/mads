import { act, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { useSessionsList } from "./useSessionsList";
import { resetSessionsStoreForTests } from "../../../router/sessionsStore";
import { FakeEventSource } from "../../../router/testFakeEventSource";
import type { Session } from "../../../router/types";

function makeSession(id: string): Session {
  return {
    id,
    name: `session ${id}`,
    activity: "active",
    created_at: "2026-07-01T00:00:00Z",
    updated_at: "2026-07-01T00:00:00Z",
  };
}

describe("useSessionsList", () => {
  beforeEach(() => {
    FakeEventSource.instances = [];
    vi.stubGlobal("EventSource", FakeEventSource);
  });

  afterEach(() => {
    // The list is one window-wide store; the bus resets itself once the
    // store's subscription (the last one) goes.
    resetSessionsStoreForTests();
    vi.unstubAllGlobals();
  });

  it("fetches sessions on mount", async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValue(new Response(JSON.stringify([makeSession("s1")]), { status: 200 }));
    vi.stubGlobal("fetch", fetchMock);

    const { result } = renderHook(() => useSessionsList());

    await waitFor(() => expect(result.current.loading).toBe(false));
    expect(result.current.sessions.map((s) => s.id)).toEqual(["s1"]);
  });

  it("a new session appears after a session.created SSE event triggers a refetch", async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(new Response(JSON.stringify([]), { status: 200 }))
      .mockResolvedValueOnce(
        new Response(JSON.stringify([makeSession("s1")]), { status: 200 }),
      );
    vi.stubGlobal("fetch", fetchMock);

    const { result } = renderHook(() => useSessionsList());
    await waitFor(() => expect(result.current.loading).toBe(false));
    expect(result.current.sessions).toEqual([]);

    const source = FakeEventSource.instances[0];
    act(() => {
      source.emit(
        "session.created",
        JSON.stringify({ seq: 1, type: "session.created", actor: "user", session_id: "s1" }),
      );
    });

    await waitFor(() => expect(result.current.sessions.map((s) => s.id)).toEqual(["s1"]));
  });

  it("an event during an in-flight refetch gets one follow-up, so stale data can't be the last word", async () => {
    // Deferred so the test controls exactly when each GET /sessions
    // resolves — reproducing a close's session.activity_changed landing
    // while an unrelated session.updated refetch is already in flight. The
    // store never runs two fetches at once, so the in-flight (stale) answer
    // lands first and the follow-up (fresh) one lands after it.
    function deferred<T>() {
      let resolve!: (v: T) => void;
      const promise = new Promise<T>((r) => (resolve = r));
      return { promise, resolve };
    }

    const mountResponse = deferred<Response>();
    const firstRefetch = deferred<Response>(); // in flight when the close lands
    const followUp = deferred<Response>(); // issued only after firstRefetch settles

    const fetchMock = vi
      .fn()
      .mockReturnValueOnce(mountResponse.promise)
      .mockReturnValueOnce(firstRefetch.promise)
      .mockReturnValueOnce(followUp.promise);
    vi.stubGlobal("fetch", fetchMock);

    const { result } = renderHook(() => useSessionsList());
    mountResponse.resolve(new Response(JSON.stringify([makeSession("s1")]), { status: 200 }));
    await waitFor(() => expect(result.current.loading).toBe(false));

    const source = FakeEventSource.instances[0];
    act(() => {
      source.emit(
        "session.updated",
        JSON.stringify({ seq: 1, type: "session.updated", actor: "user", session_id: "s1" }),
      );
    });
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2));
    act(() => {
      source.emit(
        "session.activity_changed",
        JSON.stringify({ seq: 2, type: "session.activity_changed", actor: "user", session_id: "s1" }),
      );
    });
    await new Promise((r) => setTimeout(r, 100));
    expect(fetchMock).toHaveBeenCalledTimes(2); // single-flight: no overlapping GET

    // The in-flight request lands with stale data...
    firstRefetch.resolve(new Response(JSON.stringify([makeSession("s1")]), { status: 200 }));
    // ...and the event that arrived meanwhile buys exactly one more fetch.
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(3));
    followUp.resolve(
      new Response(JSON.stringify([{ ...makeSession("s1"), activity: "inactive" }]), { status: 200 }),
    );
    await waitFor(() => expect(result.current.sessions[0].activity).toBe("inactive"));
    await new Promise((r) => setTimeout(r, 100));
    expect(fetchMock).toHaveBeenCalledTimes(3);
  });

  it("createSession POSTs to /sessions and returns the created session", async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(new Response(JSON.stringify([]), { status: 200 }))
      .mockResolvedValueOnce(
        new Response(
          JSON.stringify({ session: makeSession("s2"), addr: "localhost:0" }),
          { status: 201 },
        ),
      );
    vi.stubGlobal("fetch", fetchMock);

    const { result } = renderHook(() => useSessionsList());
    await waitFor(() => expect(result.current.loading).toBe(false));

    const created = await result.current.createSession({ name: "s2" });
    expect(created.id).toBe("s2");
  });

  it("surfaces a fetch error", async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response("boom", { status: 500 }));
    vi.stubGlobal("fetch", fetchMock);

    const { result } = renderHook(() => useSessionsList());
    await waitFor(() => expect(result.current.loading).toBe(false));
    expect(result.current.error).toMatch(/500/);
  });

  // The refetches are event-driven, so a failure used to stand until some
  // unrelated session event came along — and even then a success never
  // cleared it, leaving "Couldn't load sessions" up over a healthy list.
  it("clears the error once a later fetch succeeds", async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(new Response(JSON.stringify([makeSession("s1")]), { status: 200 }))
      .mockResolvedValueOnce(new Response("bad gateway", { status: 502 }))
      .mockResolvedValue(new Response(JSON.stringify([makeSession("s1")]), { status: 200 }));
    vi.stubGlobal("fetch", fetchMock);

    const { result } = renderHook(() => useSessionsList());
    await waitFor(() => expect(result.current.loading).toBe(false));

    act(() => {
      FakeEventSource.instances[0].emit(
        "session.updated",
        JSON.stringify({ seq: 1, type: "session.updated", actor: "user", session_id: "s1" }),
      );
    });
    await waitFor(() => expect(result.current.error).toMatch(/502/));
    expect(result.current.sessions.map((s) => s.id)).toEqual(["s1"]);

    act(() => result.current.retry());
    await waitFor(() => expect(result.current.error).toBeNull());
  });

  it("retries a failed fetch on its own, with no event to trigger it", async () => {
    vi.useFakeTimers();
    try {
      const fetchMock = vi
        .fn()
        .mockResolvedValueOnce(new Response("boom", { status: 500 }))
        .mockResolvedValue(new Response(JSON.stringify([makeSession("s1")]), { status: 200 }));
      vi.stubGlobal("fetch", fetchMock);

      const { result } = renderHook(() => useSessionsList());
      await act(async () => {
        await vi.advanceTimersByTimeAsync(0);
      });
      expect(result.current.error).toMatch(/500/);

      await act(async () => {
        await vi.advanceTimersByTimeAsync(2_000);
      });
      expect(fetchMock).toHaveBeenCalledTimes(2);
      expect(result.current.error).toBeNull();
      expect(result.current.sessions.map((s) => s.id)).toEqual(["s1"]);
    } finally {
      vi.useRealTimers();
    }
  });
});
