import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { renderHook, waitFor } from "@testing-library/react";
import { useTemplates } from "./useTemplates";
import { FakeEventSource } from "../../router/testFakeEventSource";

describe("useTemplates", () => {
  beforeEach(() => {
    FakeEventSource.instances = [];
    vi.stubGlobal("EventSource", FakeEventSource);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("fetches the template list on mount", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(
        new Response(
          JSON.stringify([
            { id: "t1", name: "deploy", source: "name: deploy\nstages: []", created_at: "t", updated_at: "t" },
          ]),
          { status: 200 },
        ),
      ),
    );

    const { result } = renderHook(() => useTemplates());
    await waitFor(() => expect(result.current.loading).toBe(false));
    expect(result.current.templates).toHaveLength(1);
    expect(result.current.templates[0].name).toBe("deploy");
  });

  it("refetches on template.updated", async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response("[]", { status: 200 }));
    vi.stubGlobal("fetch", fetchMock);

    renderHook(() => useTemplates());
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1));

    const source = FakeEventSource.instances[0];
    source.emit(
      "template.updated",
      JSON.stringify({ seq: 1, type: "template.updated", actor: "system", payload: {} }),
    );

    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2));
  });
});
