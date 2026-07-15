import { afterEach, describe, expect, it, vi } from "vitest";
import { renderHook } from "@testing-library/react";
import { useAgentEnrichment } from "./useAgentEnrichment";

describe("useAgentEnrichment", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("describe POSTs /agents/describe and unwraps {result}", async () => {
    const fetchMock = vi.fn().mockResolvedValue(
      new Response(JSON.stringify({ result: "A test stabilizer." }), { status: 200 }),
    );
    vi.stubGlobal("fetch", fetchMock);

    const { result } = renderHook(() => useAgentEnrichment());
    const description = await result.current.describe("You stabilize flaky tests.");
    expect(description).toBe("A test stabilizer.");
    const [url] = fetchMock.mock.calls[0] as [URL];
    expect(url.toString()).toContain("/agents/describe");
  });

  it("suggestCapabilities POSTs /agents/suggest-capabilities and returns the raw shape", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(
        new Response(JSON.stringify({ tools: ["shell"], skills: ["migrations"] }), {
          status: 200,
        }),
      ),
    );

    const { result } = renderHook(() => useAgentEnrichment());
    const capabilities = await result.current.suggestCapabilities("x");
    expect(capabilities).toEqual({ tools: ["shell"], skills: ["migrations"] });
  });

  it("refine POSTs /agents/refine with prompt and instruction", async () => {
    const fetchMock = vi.fn().mockResolvedValue(
      new Response(JSON.stringify({ result: "Refined." }), { status: 200 }),
    );
    vi.stubGlobal("fetch", fetchMock);

    const { result } = renderHook(() => useAgentEnrichment());
    const refined = await result.current.refine("prompt", "make it concise");
    expect(refined).toBe("Refined.");
    const [, init] = fetchMock.mock.calls[0] as [URL, RequestInit];
    expect(init.body).toBe(JSON.stringify({ prompt: "prompt", instruction: "make it concise" }));
  });
});
