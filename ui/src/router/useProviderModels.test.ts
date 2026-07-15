import { describe, expect, it, vi, afterEach } from "vitest";
import { renderHook, waitFor } from "@testing-library/react";
import { useProviderModels } from "./useProviderModels";

describe("useProviderModels", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("fetches providers then each provider's models, grouped by name", async () => {
    const fetchMock = vi.fn().mockImplementation((input: URL) => {
      const url = input.toString();
      if (url.endsWith("/providers")) {
        return Promise.resolve(new Response(JSON.stringify(["claude-code"]), { status: 200 }));
      }
      return Promise.resolve(
        new Response(
          JSON.stringify([{ id: "claude-opus-4.6", name: "claude-opus-4.6", default: true }]),
          { status: 200 },
        ),
      );
    });
    vi.stubGlobal("fetch", fetchMock);

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
});
