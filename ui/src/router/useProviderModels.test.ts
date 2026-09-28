import { describe, expect, it, vi, afterEach, beforeEach } from "vitest";
import { renderHook, waitFor } from "@testing-library/react";
import {
  resetProviderModelCache,
  useProviderCatalogs,
  useProviderModels,
} from "./useProviderModels";

/** Scripts GET /providers plus a per-provider models response. */
function stubFetch(models: Record<string, unknown>, providers = ["claude-code"]) {
  const fetchMock = vi.fn().mockImplementation((input: URL) => {
    const url = input.toString();
    if (url.endsWith("/providers")) {
      return Promise.resolve(
        new Response(JSON.stringify(providers.map((name) => ({ name, matrix: {} }))), {
          status: 200,
        }),
      );
    }
    return Promise.resolve(new Response(JSON.stringify(models), { status: 200 }));
  });
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

describe("useProviderModels", () => {
  beforeEach(() => {
    resetProviderModelCache();
  });

  afterEach(() => {
    vi.unstubAllGlobals();
    vi.useRealTimers();
  });

  it("fetches providers then each provider's models, grouped by name", async () => {
    stubFetch({ models: [{ id: "claude-opus-4.6", name: "claude-opus-4.6", default: true }] });

    const { result } = renderHook(() => useProviderModels());

    await waitFor(() => expect(result.current["claude-code"]).toBeDefined());
    expect(result.current["claude-code"]).toEqual([
      { id: "claude-opus-4.6", name: "claude-opus-4.6", default: true },
    ]);
  });

  it("resolves to an empty object on fetch failure", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response("boom", { status: 500 })));

    const { result } = renderHook(() => useProviderModels());

    await waitFor(() => expect(result.current).toEqual({}));
  });

  // Five components mount this hook; each used to trigger its own fan-out.
  it("shares one fan-out across concurrent mounts", async () => {
    const fetchMock = stubFetch({ models: [{ id: "m1", name: "M1" }] });

    const a = renderHook(() => useProviderModels());
    const b = renderHook(() => useProviderModels());
    const c = renderHook(() => useProviderModels());

    await waitFor(() => expect(a.result.current["claude-code"]).toBeDefined());
    await waitFor(() => expect(b.result.current["claude-code"]).toBeDefined());
    await waitFor(() => expect(c.result.current["claude-code"]).toBeDefined());

    // One GET /providers and one GET /providers/claude-code/models total.
    expect(fetchMock).toHaveBeenCalledTimes(2);
  });

  // The discovery state is what lets the picker say WHY a group is empty.
  it("surfaces discovery state and detail", async () => {
    stubFetch({ models: [], state: "unavailable", detail: "claude not installed" });

    const { result } = renderHook(() => useProviderCatalogs());

    await waitFor(() => expect(result.current["claude-code"]).toBeDefined());
    expect(result.current["claude-code"]).toEqual({
      models: [],
      state: "unavailable",
      detail: "claude not installed",
      fetchedAt: undefined,
    });
  });

  // A null models array must never reach a caller that does .map on it.
  it("normalises a null model list to an empty array", async () => {
    stubFetch({ models: null, state: "ready" });

    const { result } = renderHook(() => useProviderCatalogs());

    await waitFor(() => expect(result.current["claude-code"]).toBeDefined());
    expect(result.current["claude-code"].models).toEqual([]);
  });

  // One provider being unreachable must not blank the whole picker.
  it("keeps other providers when one provider's fetch throws", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockImplementation((input: URL) => {
        const url = input.toString();
        if (url.endsWith("/providers")) {
          return Promise.resolve(
            new Response(JSON.stringify([{ name: "good" }, { name: "bad" }]), { status: 200 }),
          );
        }
        if (url.includes("/bad/")) return Promise.reject(new Error("network down"));
        return Promise.resolve(
          new Response(JSON.stringify({ models: [{ id: "m1", name: "M1" }], state: "ready" }), {
            status: 200,
          }),
        );
      }),
    );

    const { result } = renderHook(() => useProviderCatalogs());

    await waitFor(() => expect(result.current["good"]).toBeDefined());
    expect(result.current["good"].models).toHaveLength(1);
    expect(result.current["bad"]).toEqual({ models: [], state: "unavailable" });
  });
});
