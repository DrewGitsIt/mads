import { act, renderHook, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { useConfig } from "./useConfig";
import { fixtureConfig } from "../../../router/fixtures";

describe("useConfig", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("fetches config on mount and seeds saved/draft/initial", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(new Response(JSON.stringify(fixtureConfig), { status: 200 })),
    );

    const { result } = renderHook(() => useConfig());
    await waitFor(() => expect(result.current.loading).toBe(false));
    expect(result.current.saved).toEqual(fixtureConfig);
    expect(result.current.draft).toEqual(fixtureConfig);
    expect(result.current.initial).toEqual(fixtureConfig);
  });

  it("dirtyRestartFields reflects live edits to the draft, before any save", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(new Response(JSON.stringify(fixtureConfig), { status: 200 })),
    );

    const { result } = renderHook(() => useConfig());
    await waitFor(() => expect(result.current.loading).toBe(false));
    expect(result.current.dirtyRestartFields).toEqual([]);

    act(() => {
      result.current.setDraft({ ...result.current.draft!, concurrency_limit: 9 });
    });
    expect(result.current.dirtyRestartFields).toContain("concurrency_limit");

    act(() => {
      result.current.setDraft({ ...result.current.draft!, default_provider: "fake" });
    });
    expect(result.current.dirtyRestartFields).not.toContain("default_provider");
  });

  it("save() PUTs the draft and updates saved on success", async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(new Response(JSON.stringify(fixtureConfig), { status: 200 }))
      .mockResolvedValueOnce(
        new Response(JSON.stringify({ ...fixtureConfig, palette: "space" }), { status: 200 }),
      );
    vi.stubGlobal("fetch", fetchMock);

    const { result } = renderHook(() => useConfig());
    await waitFor(() => expect(result.current.loading).toBe(false));

    act(() => {
      result.current.setDraft({ ...result.current.draft!, palette: "space" });
    });

    let ok = false;
    await act(async () => {
      ok = await result.current.save();
    });
    expect(ok).toBe(true);
    expect(result.current.saved?.palette).toBe("space");
    expect(result.current.fieldErrors).toEqual({});
  });

  it("save() reports every field error at once and leaves the draft untouched", async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(new Response(JSON.stringify(fixtureConfig), { status: 200 }))
      .mockResolvedValueOnce(
        new Response(
          JSON.stringify({
            errors: [
              { field: "concurrency_limit", message: "must be at least 1" },
              { field: "log_retention_days", message: "must be at least 1" },
            ],
          }),
          { status: 400 },
        ),
      );
    vi.stubGlobal("fetch", fetchMock);

    const { result } = renderHook(() => useConfig());
    await waitFor(() => expect(result.current.loading).toBe(false));

    act(() => {
      result.current.setDraft({ ...result.current.draft!, concurrency_limit: -1, log_retention_days: -1 });
    });

    let ok = true;
    await act(async () => {
      ok = await result.current.save();
    });
    expect(ok).toBe(false);
    expect(Object.keys(result.current.fieldErrors)).toEqual(
      expect.arrayContaining(["concurrency_limit", "log_retention_days"]),
    );
    expect(result.current.draft?.concurrency_limit).toBe(-1);
    expect(result.current.saved?.concurrency_limit).toBe(fixtureConfig.concurrency_limit);
  });
});
