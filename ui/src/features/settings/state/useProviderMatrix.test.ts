import { renderHook, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { useProviderMatrix } from "./useProviderMatrix";
import { fixtureProviderBehaviors, fixtureProviders } from "../../../router/fixtures";

describe("useProviderMatrix", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("fetches providers and behaviors in parallel", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockImplementation((input: URL | string) => {
        const url = input.toString();
        if (url.includes("/providers/behaviors")) {
          return Promise.resolve(
            new Response(JSON.stringify(fixtureProviderBehaviors), { status: 200 }),
          );
        }
        return Promise.resolve(new Response(JSON.stringify(fixtureProviders), { status: 200 }));
      }),
    );

    const { result } = renderHook(() => useProviderMatrix());
    await waitFor(() => expect(result.current.loading).toBe(false));
    expect(result.current.providers).toEqual(fixtureProviders);
    expect(result.current.behaviors).toEqual(fixtureProviderBehaviors);
  });

  it("leaves internal rows like stable_keys out of the table", async () => {
    const withInternal = [...fixtureProviderBehaviors, { behavior: "stable_keys", level: "optional" }];
    vi.stubGlobal(
      "fetch",
      vi.fn().mockImplementation((input: URL | string) => {
        const url = input.toString();
        if (url.includes("/providers/behaviors")) {
          return Promise.resolve(new Response(JSON.stringify(withInternal), { status: 200 }));
        }
        return Promise.resolve(new Response(JSON.stringify(fixtureProviders), { status: 200 }));
      }),
    );

    const { result } = renderHook(() => useProviderMatrix());
    await waitFor(() => expect(result.current.loading).toBe(false));
    expect(result.current.behaviors).toEqual(fixtureProviderBehaviors);
  });
});
