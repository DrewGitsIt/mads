import { act, renderHook, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { useCredentials } from "./useCredentials";

describe("useCredentials", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("fetches names on mount", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(new Response(JSON.stringify(["OPENAI_API_KEY"]), { status: 200 })),
    );
    const { result } = renderHook(() => useCredentials());
    await waitFor(() => expect(result.current.loading).toBe(false));
    expect(result.current.names).toEqual(["OPENAI_API_KEY"]);
  });

  it("add() only ever stores the returned name, never the value, in hook state", async () => {
    const secret = "sk-live-should-never-be-retained";
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(new Response(JSON.stringify([]), { status: 200 }))
      .mockResolvedValueOnce(
        new Response(JSON.stringify({ name: "OPENAI_API_KEY" }), { status: 201 }),
      );
    vi.stubGlobal("fetch", fetchMock);

    const { result } = renderHook(() => useCredentials());
    await waitFor(() => expect(result.current.loading).toBe(false));

    await act(async () => {
      await result.current.add("OPENAI_API_KEY", secret);
    });

    expect(result.current.names).toEqual(["OPENAI_API_KEY"]);
    expect(JSON.stringify(result.current)).not.toContain(secret);
  });

  it("importFromEnv never receives or stores a value, only names", async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(new Response(JSON.stringify([]), { status: 200 }))
      .mockResolvedValueOnce(new Response(JSON.stringify(["MADS_TEST_SECRET"]), { status: 200 }))
      .mockResolvedValueOnce(
        new Response(JSON.stringify({ imported: ["MADS_TEST_SECRET"] }), { status: 200 }),
      );
    vi.stubGlobal("fetch", fetchMock);

    const { result } = renderHook(() => useCredentials());
    await waitFor(() => expect(result.current.loading).toBe(false));

    await act(async () => {
      await result.current.loadEnvCandidates();
    });
    expect(result.current.envCandidates).toEqual(["MADS_TEST_SECRET"]);

    let imported: string[] = [];
    await act(async () => {
      imported = await result.current.importFromEnv(["MADS_TEST_SECRET"]);
    });
    expect(imported).toEqual(["MADS_TEST_SECRET"]);
    expect(result.current.names).toEqual(["MADS_TEST_SECRET"]);
    expect(result.current.envCandidates).toEqual([]);
  });

  it("remove() deletes and drops the name from state", async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(new Response(JSON.stringify(["OPENAI_API_KEY"]), { status: 200 }))
      .mockResolvedValueOnce(new Response(null, { status: 204 }));
    vi.stubGlobal("fetch", fetchMock);

    const { result } = renderHook(() => useCredentials());
    await waitFor(() => expect(result.current.loading).toBe(false));

    await act(async () => {
      await result.current.remove("OPENAI_API_KEY");
    });
    expect(result.current.names).toEqual([]);
  });
});
