import { act, renderHook, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { useUpdateCheck } from "./useUpdateCheck";
import { fixtureUpdateCheckResult, fixtureVersionInfo } from "../../../router/fixtures";

describe("useUpdateCheck", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("fetches the current version on mount without checking the feed", async () => {
    const fetchMock = vi.fn().mockImplementation((input: RequestInfo | URL) => {
      const url = String(input);
      if (url.endsWith("/version")) {
        return Promise.resolve(new Response(JSON.stringify(fixtureVersionInfo), { status: 200 }));
      }
      throw new Error(`unexpected fetch: ${url}`);
    });
    vi.stubGlobal("fetch", fetchMock);

    const { result } = renderHook(() => useUpdateCheck());
    await waitFor(() => expect(result.current.currentVersion).toBe(fixtureVersionInfo.version));

    expect(fetchMock).toHaveBeenCalledTimes(1);
    expect(String(fetchMock.mock.calls[0][0])).toContain("/version");
    expect(result.current.result).toBeNull();
  });

  it("only checks the feed once check() is called", async () => {
    const fetchMock = vi.fn().mockImplementation((input: RequestInfo | URL) => {
      const url = String(input);
      if (url.endsWith("/version")) {
        return Promise.resolve(new Response(JSON.stringify(fixtureVersionInfo), { status: 200 }));
      }
      if (url.endsWith("/update/check")) {
        return Promise.resolve(new Response(JSON.stringify(fixtureUpdateCheckResult), { status: 200 }));
      }
      throw new Error(`unexpected fetch: ${url}`);
    });
    vi.stubGlobal("fetch", fetchMock);

    const { result } = renderHook(() => useUpdateCheck());
    await waitFor(() => expect(result.current.currentVersion).toBe(fixtureVersionInfo.version));

    expect(fetchMock.mock.calls.some((c) => String(c[0]).endsWith("/update/check"))).toBe(false);

    act(() => result.current.check());
    await waitFor(() => expect(result.current.checking).toBe(false));

    expect(result.current.result).toEqual(fixtureUpdateCheckResult);
    expect(fetchMock.mock.calls.filter((c) => String(c[0]).endsWith("/update/check"))).toHaveLength(1);
  });
});
