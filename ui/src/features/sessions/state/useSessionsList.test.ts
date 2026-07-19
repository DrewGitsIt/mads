import { act, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { useSessionsList } from "./useSessionsList";
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

  it("an earlier refetch that resolves after a later one doesn't clobber it (out-of-order responses)", async () => {
    // Deferred so the test controls exactly when each GET /sessions
    // resolves, independent of call order — reproducing a close's
    // session.activity_changed refetch racing an unrelated session.updated
    // refetch that was already in flight.
    function deferred<T>() {
      let resolve!: (v: T) => void;
      const promise = new Promise<T>((r) => (resolve = r));
      return { promise, resolve };
    }

    const mountResponse = deferred<Response>();
    const firstRefetch = deferred<Response>(); // triggered first, resolves last
    const secondRefetch = deferred<Response>(); // triggered second, resolves first

    const fetchMock = vi
      .fn()
      .mockReturnValueOnce(mountResponse.promise)
      .mockReturnValueOnce(firstRefetch.promise)
      .mockReturnValueOnce(secondRefetch.promise);
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
    act(() => {
      source.emit(
        "session.activity_changed",
        JSON.stringify({ seq: 2, type: "session.activity_changed", actor: "user", session_id: "s1" }),
      );
    });
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(3));

    // Later-triggered request (the real close) resolves first with the
    // correct, fresh state.
    secondRefetch.resolve(
      new Response(JSON.stringify([{ ...makeSession("s1"), activity: "inactive" }]), { status: 200 }),
    );
    await waitFor(() => expect(result.current.sessions[0].activity).toBe("inactive"));

    // Earlier-triggered request resolves last with stale data — it must not
    // overwrite the already-correct state.
    firstRefetch.resolve(new Response(JSON.stringify([makeSession("s1")]), { status: 200 }));
    await new Promise((r) => setTimeout(r, 0));
    expect(result.current.sessions[0].activity).toBe("inactive");
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
});
