import { act, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { useRetroGeneration } from "./useRetroGeneration";
import { FakeEventSource } from "../../../router/testFakeEventSource";

let nextSeq = 1;
function envelope(type: string, payload: unknown) {
  return JSON.stringify({ seq: nextSeq++, type, actor: "system", payload });
}

describe("useRetroGeneration", () => {
  beforeEach(() => {
    nextSeq = 1;
    FakeEventSource.instances = [];
    vi.stubGlobal("EventSource", FakeEventSource);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("walks started -> gathering -> generating -> completed for the matching retro_id", async () => {
    const fetchMock = vi.fn().mockResolvedValue(
      new Response(JSON.stringify({ job_id: "job-1", retro_id: "retro-1" }), { status: 202 }),
    );
    vi.stubGlobal("fetch", fetchMock);

    const { result } = renderHook(() => useRetroGeneration());
    await act(async () => {
      await result.current.start(["s1", "s2"], "notes");
    });

    expect(result.current.phase).toBe("starting");
    expect(result.current.retroId).toBe("retro-1");

    const source = FakeEventSource.instances[0];
    act(() => source.emit("retro.started", envelope("retro.started", { retro_id: "retro-1" })));
    expect(result.current.phase).toBe("starting");

    act(() =>
      source.emit(
        "retro.progress",
        envelope("retro.progress", { retro_id: "retro-1", step: "gathering" }),
      ),
    );
    expect(result.current.phase).toBe("gathering");

    act(() =>
      source.emit(
        "retro.progress",
        envelope("retro.progress", { retro_id: "retro-1", step: "generating" }),
      ),
    );
    expect(result.current.phase).toBe("generating");

    act(() => source.emit("retro.completed", envelope("retro.completed", { retro_id: "retro-1" })));
    expect(result.current.phase).toBe("completed");
  });

  it("ignores events for a different retro_id", async () => {
    const fetchMock = vi.fn().mockResolvedValue(
      new Response(JSON.stringify({ job_id: "job-1", retro_id: "retro-1" }), { status: 202 }),
    );
    vi.stubGlobal("fetch", fetchMock);

    const { result } = renderHook(() => useRetroGeneration());
    await act(async () => {
      await result.current.start(["s1"], "");
    });

    const source = FakeEventSource.instances[0];
    act(() =>
      source.emit(
        "retro.progress",
        envelope("retro.progress", { retro_id: "retro-other", step: "gathering" }),
      ),
    );
    expect(result.current.phase).toBe("starting");
  });

  it("cancel POSTs to /jobs/:id/cancel and waits for retro.cancelled", async () => {
    const fetchMock = vi.fn().mockImplementation((input: URL | string) => {
      const url = input.toString();
      if (url.includes("/jobs/")) return Promise.resolve(new Response(null, { status: 204 }));
      return Promise.resolve(
        new Response(JSON.stringify({ job_id: "job-1", retro_id: "retro-1" }), { status: 202 }),
      );
    });
    vi.stubGlobal("fetch", fetchMock);

    const { result } = renderHook(() => useRetroGeneration());
    await act(async () => {
      await result.current.start(["s1"], "");
    });

    act(() => result.current.cancel());
    expect(result.current.phase).toBe("starting");

    const source = FakeEventSource.instances[0];
    act(() => source.emit("retro.cancelled", envelope("retro.cancelled", { retro_id: "retro-1" })));
    expect(result.current.phase).toBe("cancelled");

    await waitFor(() =>
      expect(fetchMock.mock.calls.some((call) => call[0].toString().includes("/jobs/job-1/cancel"))).toBe(
        true,
      ),
    );
  });

  it("reset returns to idle and clears retroId/error", async () => {
    const fetchMock = vi.fn().mockResolvedValue(
      new Response(JSON.stringify({ job_id: "job-1", retro_id: "retro-1" }), { status: 202 }),
    );
    vi.stubGlobal("fetch", fetchMock);

    const { result } = renderHook(() => useRetroGeneration());
    await act(async () => {
      await result.current.start(["s1"], "");
    });

    act(() => result.current.reset());
    expect(result.current.phase).toBe("idle");
    expect(result.current.retroId).toBeNull();
    expect(result.current.error).toBeNull();
  });
});
