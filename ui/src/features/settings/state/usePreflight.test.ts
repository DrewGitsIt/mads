import { act, renderHook, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { usePreflight } from "./usePreflight";
import { fixturePreflightChecks } from "../../../router/fixtures";

describe("usePreflight", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("fetches checks on mount", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(new Response(JSON.stringify(fixturePreflightChecks), { status: 200 })),
    );
    const { result } = renderHook(() => usePreflight());
    await waitFor(() => expect(result.current.loading).toBe(false));
    expect(result.current.checks).toEqual(fixturePreflightChecks);
  });

  it("rerun() refetches", async () => {
    const fetchMock = vi
      .fn()
      .mockImplementation(() =>
        Promise.resolve(new Response(JSON.stringify(fixturePreflightChecks), { status: 200 })),
      );
    vi.stubGlobal("fetch", fetchMock);
    const { result } = renderHook(() => usePreflight());
    await waitFor(() => expect(result.current.loading).toBe(false));

    act(() => result.current.rerun());
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2));
  });
});
