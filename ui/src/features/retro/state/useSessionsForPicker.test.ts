import { act, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { useSessionsForPicker } from "./useSessionsForPicker";
import { useSessionsList } from "../../sessions/state/useSessionsList";
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

describe("useSessionsForPicker", () => {
  beforeEach(() => {
    FakeEventSource.instances = [];
    vi.stubGlobal("EventSource", FakeEventSource);
  });

  afterEach(() => {
    resetSessionsStoreForTests();
    vi.unstubAllGlobals();
  });

  it("fetches sessions on mount", async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValue(new Response(JSON.stringify([makeSession("s1")]), { status: 200 }));
    vi.stubGlobal("fetch", fetchMock);

    const { result } = renderHook(() => useSessionsForPicker());

    await waitFor(() => expect(result.current.loading).toBe(false));
    expect(result.current.sessions.map((s) => s.id)).toEqual(["s1"]);
  });

  it("refetches on a session.* SSE event", async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(new Response(JSON.stringify([]), { status: 200 }))
      .mockResolvedValueOnce(new Response(JSON.stringify([makeSession("s1")]), { status: 200 }));
    vi.stubGlobal("fetch", fetchMock);

    const { result } = renderHook(() => useSessionsForPicker());
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

  it("shares the window-wide store: mounted alongside useSessionsList, one GET /sessions", async () => {
    const fetchMock = vi
      .fn()
      .mockImplementation(() =>
        Promise.resolve(new Response(JSON.stringify([makeSession("s1")]), { status: 200 })),
      );
    vi.stubGlobal("fetch", fetchMock);

    const { result } = renderHook(() => ({ picker: useSessionsForPicker(), list: useSessionsList() }));

    await waitFor(() => expect(result.current.picker.loading).toBe(false));
    expect(result.current.picker.sessions).toBe(result.current.list.sessions);
    const sessionGets = fetchMock.mock.calls.filter(([url]) => String(url).endsWith("/sessions"));
    expect(sessionGets).toHaveLength(1);
  });

  it("reports a failed fetch as error, not as loading forever", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response("boom", { status: 500 })));

    const { result } = renderHook(() => useSessionsForPicker());

    await waitFor(() => expect(result.current.loading).toBe(false));
    expect(result.current.error).toMatch(/500/);
    expect(result.current.sessions).toEqual([]);
  });
});
