import { act, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { useRetroHistory } from "./useRetroHistory";
import { FakeEventSource } from "../../../router/testFakeEventSource";
import { fixtureRetro, fixtureRetroList } from "../../../router/fixtures";

describe("useRetroHistory", () => {
  beforeEach(() => {
    FakeEventSource.instances = [];
    vi.stubGlobal("EventSource", FakeEventSource);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("fetches retros on mount", async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValue(new Response(JSON.stringify(fixtureRetroList), { status: 200 }));
    vi.stubGlobal("fetch", fetchMock);

    const { result } = renderHook(() => useRetroHistory());

    await waitFor(() => expect(result.current.loading).toBe(false));
    expect(result.current.retros.map((r) => r.id)).toEqual([fixtureRetro.id]);
  });

  it("refetches on a retro.completed SSE event", async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(new Response(JSON.stringify([]), { status: 200 }))
      .mockResolvedValueOnce(new Response(JSON.stringify(fixtureRetroList), { status: 200 }));
    vi.stubGlobal("fetch", fetchMock);

    const { result } = renderHook(() => useRetroHistory());
    await waitFor(() => expect(result.current.loading).toBe(false));
    expect(result.current.retros).toEqual([]);

    const source = FakeEventSource.instances[0];
    act(() => {
      source.emit(
        "retro.completed",
        JSON.stringify({
          seq: 1,
          type: "retro.completed",
          actor: "system",
          payload: { retro_id: fixtureRetro.id, dpa_count: 3 },
        }),
      );
    });

    await waitFor(() => expect(result.current.retros.map((r) => r.id)).toEqual([fixtureRetro.id]));
  });
});
