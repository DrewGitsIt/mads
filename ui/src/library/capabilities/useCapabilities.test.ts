import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { renderHook, waitFor } from "@testing-library/react";
import { useCapabilities } from "./useCapabilities";
import { FakeEventSource } from "../../router/testFakeEventSource";

describe("useCapabilities", () => {
  beforeEach(() => {
    FakeEventSource.instances = [];
    vi.stubGlobal("EventSource", FakeEventSource);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("fetches the capability list on mount", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(
        new Response(
          JSON.stringify([
            {
              id: "c1",
              name: "github",
              type: "mcp",
              config: {},
              created_at: "t",
              updated_at: "t",
            },
          ]),
          { status: 200 },
        ),
      ),
    );

    const { result } = renderHook(() => useCapabilities());
    await waitFor(() => expect(result.current.loading).toBe(false));
    expect(result.current.capabilities).toHaveLength(1);
    expect(result.current.capabilities[0].name).toBe("github");
  });

  it("refetches on capability.health_changed", async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response("[]", { status: 200 }));
    vi.stubGlobal("fetch", fetchMock);

    renderHook(() => useCapabilities());
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1));

    const source = FakeEventSource.instances[0];
    source.emit(
      "capability.health_changed",
      JSON.stringify({ seq: 1, type: "capability.health_changed", actor: "system", payload: {} }),
    );

    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2));
  });
});
