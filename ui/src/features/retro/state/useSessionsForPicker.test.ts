import { act, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { useSessionsForPicker } from "./useSessionsForPicker";
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
});
