import { act, renderHook, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { useProfiles } from "./useProfiles";
import { fixtureConfig, fixtureProfile, fixtureProfiles } from "../../../router/fixtures";

describe("useProfiles", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("fetches profiles on mount", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(new Response(JSON.stringify(fixtureProfiles), { status: 200 })),
    );
    const { result } = renderHook(() => useProfiles());
    await waitFor(() => expect(result.current.loading).toBe(false));
    expect(result.current.profiles).toEqual(fixtureProfiles);
  });

  it("create() posts and refetches the list", async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(new Response(JSON.stringify([]), { status: 200 }))
      .mockResolvedValueOnce(new Response(JSON.stringify(fixtureProfile), { status: 201 }))
      .mockResolvedValueOnce(new Response(JSON.stringify(fixtureProfiles), { status: 200 }));
    vi.stubGlobal("fetch", fetchMock);

    const { result } = renderHook(() => useProfiles());
    await waitFor(() => expect(result.current.loading).toBe(false));

    await act(async () => {
      await result.current.create({ name: "focus-mode", settings: {} });
    });
    expect(result.current.profiles).toEqual(fixtureProfiles);
  });

  it("activate() posts to /profiles/:id/activate and returns the merged config", async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(new Response(JSON.stringify(fixtureProfiles), { status: 200 }))
      .mockResolvedValueOnce(new Response(JSON.stringify(fixtureConfig), { status: 200 }));
    vi.stubGlobal("fetch", fetchMock);

    const { result } = renderHook(() => useProfiles());
    await waitFor(() => expect(result.current.loading).toBe(false));

    let merged;
    await act(async () => {
      merged = await result.current.activate(fixtureProfile.id);
    });
    expect(merged).toEqual(fixtureConfig);
    const [, lastCall] = fetchMock.mock.calls;
    expect(lastCall[0].toString()).toContain(`/profiles/${fixtureProfile.id}/activate`);
    expect(lastCall[1]).toEqual(expect.objectContaining({ method: "POST" }));
  });
});
