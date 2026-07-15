import { act, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { useRetroDetail } from "./useRetroDetail";
import { fixtureRetro } from "../../../router/fixtures";

describe("useRetroDetail", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  beforeEach(() => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockImplementation((input: URL | string) => {
        const url = input.toString();
        if (url.includes("/retros/retro-1") && !url.includes("/dpas/")) {
          return Promise.resolve(new Response(JSON.stringify(fixtureRetro), { status: 200 }));
        }
        return Promise.resolve(new Response(null, { status: 204 }));
      }),
    );
  });

  it("fetches the retro by id", async () => {
    const { result } = renderHook(() => useRetroDetail("retro-1"));
    await waitFor(() => expect(result.current.retro?.id).toBe("retro-1"));
    expect(result.current.retro?.dpas).toHaveLength(3);
  });

  it("optimistically toggles a rejected DPA to accepted and POSTs accept", async () => {
    const { result } = renderHook(() => useRetroDetail("retro-1"));
    await waitFor(() => expect(result.current.retro).not.toBeNull());

    act(() => result.current.toggleDpa("dpa_2"));

    expect(result.current.retro?.dpas.find((d) => d.id === "dpa_2")?.status).toBe("accepted");
    await waitFor(() =>
      expect(
        (vi.mocked(fetch).mock.calls as [URL | string, RequestInit?][]).some(([input]) =>
          input.toString().includes("/dpas/dpa_2/accept"),
        ),
      ).toBe(true),
    );
  });

  it("optimistically toggles an accepted DPA to rejected and POSTs reject", async () => {
    const { result } = renderHook(() => useRetroDetail("retro-1"));
    await waitFor(() => expect(result.current.retro).not.toBeNull());

    act(() => result.current.toggleDpa("dpa_1"));

    expect(result.current.retro?.dpas.find((d) => d.id === "dpa_1")?.status).toBe("rejected");
    await waitFor(() =>
      expect(
        (vi.mocked(fetch).mock.calls as [URL | string, RequestInit?][]).some(([input]) =>
          input.toString().includes("/dpas/dpa_1/reject"),
        ),
      ).toBe(true),
    );
  });

  it("reverts the optimistic toggle if the request fails", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockImplementation((input: URL | string) => {
        const url = input.toString();
        if (url.includes("/retros/retro-1") && !url.includes("/dpas/")) {
          return Promise.resolve(new Response(JSON.stringify(fixtureRetro), { status: 200 }));
        }
        return Promise.resolve(new Response("boom", { status: 500 }));
      }),
    );

    const { result } = renderHook(() => useRetroDetail("retro-1"));
    await waitFor(() => expect(result.current.retro).not.toBeNull());

    act(() => result.current.toggleDpa("dpa_1"));
    expect(result.current.retro?.dpas.find((d) => d.id === "dpa_1")?.status).toBe("rejected");

    await waitFor(() =>
      expect(result.current.retro?.dpas.find((d) => d.id === "dpa_1")?.status).toBe("accepted"),
    );
  });
});
