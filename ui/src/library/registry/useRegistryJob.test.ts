import { act, renderHook } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { useRegistryJob } from "./useRegistryJob";
import { FakeEventSource } from "../../router/testFakeEventSource";
import { holdEventsHead } from "../../router/testEventsBarrier";

describe("useRegistryJob", () => {
  beforeEach(() => {
    FakeEventSource.instances = [];
    vi.stubGlobal("EventSource", FakeEventSource);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
    vi.useRealTimers();
  });

  it("rejects with an error, never POSTing, when the bus isn't ready within 10 s", async () => {
    vi.useFakeTimers();
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({ job_id: "j1" }), { status: 202 }));
    vi.stubGlobal("fetch", fetchMock);
    holdEventsHead(); // the head never answers: the services are down

    const { result } = renderHook(() => useRegistryJob());
    let failure: unknown;
    act(() => {
      result.current.install("agent", "reviewer").catch((err: unknown) => (failure = err));
    });
    expect(result.current.pending).toBe(true);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(10_000);
    });
    expect(result.current.pending).toBe(false);
    expect(result.current.error).toBe("Live updates aren't connected yet. Try again in a moment.");
    expect(failure).toBeInstanceOf(Error);
    expect(fetchMock.mock.calls.some(([url]) => String(url).includes("/registry"))).toBe(false);
  });
});
