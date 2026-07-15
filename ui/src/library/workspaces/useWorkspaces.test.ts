import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { renderHook, waitFor } from "@testing-library/react";
import { useWorkspaces } from "./useWorkspaces";
import { FakeEventSource } from "../../router/testFakeEventSource";

describe("useWorkspaces", () => {
  beforeEach(() => {
    FakeEventSource.instances = [];
    vi.stubGlobal("EventSource", FakeEventSource);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("fetches the workspace list on mount", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(
        new Response(
          JSON.stringify([
            { id: "w1", name: "mads", defaults: {}, created_at: "t", updated_at: "t" },
          ]),
          { status: 200 },
        ),
      ),
    );

    const { result } = renderHook(() => useWorkspaces());
    await waitFor(() => expect(result.current.loading).toBe(false));
    expect(result.current.workspaces).toHaveLength(1);
    expect(result.current.workspaces[0].name).toBe("mads");
  });

  it("refetches on workspace.updated", async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response("[]", { status: 200 }));
    vi.stubGlobal("fetch", fetchMock);

    renderHook(() => useWorkspaces());
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1));

    const source = FakeEventSource.instances[0];
    source.emit(
      "workspace.updated",
      JSON.stringify({ seq: 1, type: "workspace.updated", actor: "system", payload: {} }),
    );

    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2));
  });
});
