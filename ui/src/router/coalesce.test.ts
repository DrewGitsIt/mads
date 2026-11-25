import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { coalesce } from "./coalesce";

function deferred() {
  let resolve!: () => void;
  let reject!: (err: unknown) => void;
  const promise = new Promise<void>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

describe("coalesce", () => {
  beforeEach(() => {
    vi.useFakeTimers();
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it("collapses a burst of 1,000 requests into one run", async () => {
    const run = vi.fn().mockResolvedValue(undefined);
    const c = coalesce(run);
    for (let i = 0; i < 1000; i++) c.request();
    expect(run).not.toHaveBeenCalled();
    await vi.advanceTimersByTimeAsync(49);
    expect(run).not.toHaveBeenCalled();
    await vi.advanceTimersByTimeAsync(1);
    expect(run).toHaveBeenCalledTimes(1);
    await vi.advanceTimersByTimeAsync(1000);
    expect(run).toHaveBeenCalledTimes(1);
  });

  it("a continuous stream still runs, bounded by maxWait", async () => {
    const run = vi.fn().mockResolvedValue(undefined);
    const c = coalesce(run);
    for (let t = 0; t < 2000; t += 10) {
      c.request();
      await vi.advanceTimersByTimeAsync(10);
    }
    // One run per 500 ms window across 2 s.
    expect(run.mock.calls.length).toBeGreaterThanOrEqual(3);
    expect(run.mock.calls.length).toBeLessThanOrEqual(5);
    await vi.advanceTimersByTimeAsync(1000);
    expect(run.mock.calls.length).toBeLessThanOrEqual(5);
  });

  it("requests during an in-flight run buy exactly one follow-up", async () => {
    const first = deferred();
    const run = vi.fn().mockReturnValueOnce(first.promise).mockResolvedValue(undefined);
    const c = coalesce(run);
    c.now();
    expect(run).toHaveBeenCalledTimes(1);
    for (let i = 0; i < 100; i++) c.request();
    c.now();
    await vi.advanceTimersByTimeAsync(1000);
    expect(run).toHaveBeenCalledTimes(1); // single-flight
    first.resolve();
    await vi.advanceTimersByTimeAsync(0);
    expect(run).toHaveBeenCalledTimes(2);
    await vi.advanceTimersByTimeAsync(1000);
    expect(run).toHaveBeenCalledTimes(2);
  });

  it("keeps working after a run rejects, including the follow-up", async () => {
    const first = deferred();
    const run = vi
      .fn()
      .mockReturnValueOnce(first.promise)
      .mockRejectedValueOnce(new Error("again"))
      .mockResolvedValue(undefined);
    const c = coalesce(run);
    c.now();
    c.request();
    first.reject(new Error("boom"));
    await vi.advanceTimersByTimeAsync(0);
    expect(run).toHaveBeenCalledTimes(2); // follow-up after a rejection
    await vi.advanceTimersByTimeAsync(0);
    c.request();
    await vi.advanceTimersByTimeAsync(50);
    expect(run).toHaveBeenCalledTimes(3);
  });

  it("survives a run that throws synchronously", async () => {
    const run = vi
      .fn()
      .mockImplementationOnce(() => {
        throw new Error("sync");
      })
      .mockResolvedValue(undefined);
    const c = coalesce(run);
    c.now();
    await vi.advanceTimersByTimeAsync(0);
    c.now();
    expect(run).toHaveBeenCalledTimes(2);
  });

  it("cancel drops the pending run and follow-up until the next request", async () => {
    const first = deferred();
    const run = vi.fn().mockReturnValueOnce(first.promise).mockResolvedValue(undefined);
    const c = coalesce(run);

    c.request();
    c.cancel();
    await vi.advanceTimersByTimeAsync(1000);
    expect(run).not.toHaveBeenCalled();

    c.now();
    c.request(); // would be a follow-up
    c.cancel();
    first.resolve();
    await vi.advanceTimersByTimeAsync(1000);
    expect(run).toHaveBeenCalledTimes(1);

    c.request();
    await vi.advanceTimersByTimeAsync(50);
    expect(run).toHaveBeenCalledTimes(2);
  });
});
