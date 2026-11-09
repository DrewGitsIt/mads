import { act, renderHook } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { useStreamSpeed } from "./streamSpeed";
import { FakeRouterClient, fixtureConfig } from "../../router/fixtures";
import { FakeEventSource } from "../../router/testFakeEventSource";

// The bus replays its durable log on connect: 30 config.updated on a fresh
// load each used to cost a GET /config.
describe("useStreamSpeed", () => {
  let seq = 2_000_000;
  const emitConfigUpdated = () => {
    seq += 1;
    for (const s of FakeEventSource.instances) {
      s.emit("config.updated", JSON.stringify({ seq, timestamp: "2026-11-09T10:00:00Z", type: "config.updated", actor: "user" }));
    }
  };

  beforeEach(() => {
    vi.useFakeTimers();
    FakeEventSource.instances = [];
    vi.stubGlobal("EventSource", FakeEventSource);
  });

  afterEach(() => {
    vi.useRealTimers();
    vi.unstubAllGlobals();
  });

  it("reads once on mount, immediately", async () => {
    const client = new FakeRouterClient();
    const getConfig = vi.spyOn(client, "getConfig").mockResolvedValue({ ...fixtureConfig, stream_speed: "slow" });
    const { result } = renderHook(() => useStreamSpeed(client));
    expect(getConfig).toHaveBeenCalledTimes(1);
    await act(() => vi.advanceTimersByTimeAsync(0));
    expect(result.current.speed).toBe("slow");
  });

  it("collapses a burst of 200 config.updated into at most 2 reloads", async () => {
    const client = new FakeRouterClient();
    const getConfig = vi.spyOn(client, "getConfig");
    renderHook(() => useStreamSpeed(client));
    await act(() => vi.advanceTimersByTimeAsync(0));
    const before = getConfig.mock.calls.length;
    act(() => {
      for (let i = 0; i < 200; i++) emitConfigUpdated();
    });
    await act(() => vi.advanceTimersByTimeAsync(1000));
    const reloads = getConfig.mock.calls.length - before;
    expect(reloads).toBeGreaterThanOrEqual(1);
    expect(reloads).toBeLessThanOrEqual(2);
  });

  it("a config.updated after load still lands the new pace, within 500 ms", async () => {
    const client = new FakeRouterClient();
    const getConfig = vi.spyOn(client, "getConfig").mockResolvedValue({ ...fixtureConfig, stream_speed: "off" });
    const { result } = renderHook(() => useStreamSpeed(client));
    await act(() => vi.advanceTimersByTimeAsync(0));
    expect(result.current.speed).toBe("off");

    getConfig.mockResolvedValue({ ...fixtureConfig, stream_speed: "custom", stream_wpm: 420 });
    act(() => emitConfigUpdated());
    await act(() => vi.advanceTimersByTimeAsync(500));
    expect(result.current).toEqual({ speed: "custom", wpm: 420 });
  });
});
