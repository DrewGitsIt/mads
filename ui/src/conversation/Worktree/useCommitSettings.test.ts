import { act, renderHook } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { useCommitSettings } from "./useCommitSettings";
import { FakeRouterClient, fixtureConfig } from "../../router/fixtures";
import { FakeEventSource } from "../../router/testFakeEventSource";

describe("useCommitSettings", () => {
  let seq = 3_000_000;
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

  it("collapses a burst of 200 config.updated into at most 2 reloads; the mount read is immediate", async () => {
    const client = new FakeRouterClient();
    const getConfig = vi.spyOn(client, "getConfig");
    renderHook(() => useCommitSettings(client));
    expect(getConfig).toHaveBeenCalledTimes(1);
    await act(() => vi.advanceTimersByTimeAsync(0));
    act(() => {
      for (let i = 0; i < 200; i++) emitConfigUpdated();
    });
    await act(() => vi.advanceTimersByTimeAsync(1000));
    const reloads = getConfig.mock.calls.length - 1;
    expect(reloads).toBeGreaterThanOrEqual(1);
    expect(reloads).toBeLessThanOrEqual(2);
  });

  it("a config.updated after load still lands the new setting, within 500 ms", async () => {
    const client = new FakeRouterClient();
    const getConfig = vi.spyOn(client, "getConfig").mockResolvedValue({ ...fixtureConfig, auto_commit_messages: false });
    const { result } = renderHook(() => useCommitSettings(client));
    await act(() => vi.advanceTimersByTimeAsync(0));
    expect(result.current.autoCommitMessages).toBe(false);

    getConfig.mockResolvedValue({ ...fixtureConfig, auto_commit_messages: true });
    act(() => emitConfigUpdated());
    await act(() => vi.advanceTimersByTimeAsync(500));
    expect(result.current.autoCommitMessages).toBe(true);
  });
});
