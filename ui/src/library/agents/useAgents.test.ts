import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { renderHook, waitFor } from "@testing-library/react";
import { useAgents } from "./useAgents";
import { FakeEventSource } from "../../router/testFakeEventSource";

describe("useAgents", () => {
  beforeEach(() => {
    FakeEventSource.instances = [];
    vi.stubGlobal("EventSource", FakeEventSource);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("fetches the agent list on mount", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(
        new Response(JSON.stringify([{ id: "a1", name: "reviewer" }]), { status: 200 }),
      ),
    );

    const { result } = renderHook(() => useAgents());
    await waitFor(() => expect(result.current.loading).toBe(false));
    expect(result.current.agents).toHaveLength(1);
    expect(result.current.agents[0].name).toBe("reviewer");
  });

  it("refetches when an agent.* event arrives", async () => {
    let agents: unknown[] = [];
    vi.stubGlobal(
      "fetch",
      vi.fn().mockImplementation(() => Promise.resolve(new Response(JSON.stringify(agents), { status: 200 }))),
    );

    const { result } = renderHook(() => useAgents());
    await waitFor(() => expect(result.current.loading).toBe(false));

    agents = [{ id: "a1", name: "reviewer" }];
    const source = FakeEventSource.instances[0];
    source.emit(
      "agent.created",
      JSON.stringify({ seq: 1, type: "agent.created", actor: "user" }),
    );

    await waitFor(() => expect(result.current.agents).toHaveLength(1));
  });
});
