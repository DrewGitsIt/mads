import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, renderHook, waitFor } from "@testing-library/react";
import { useAgentGeneration } from "./useAgentGeneration";
import { FakeEventSource } from "../../router/testFakeEventSource";

describe("useAgentGeneration", () => {
  beforeEach(() => {
    FakeEventSource.instances = [];
    vi.stubGlobal("EventSource", FakeEventSource);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("sets the draft and similar fields from agent.draft_generated", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(new Response(JSON.stringify({ job_id: "j1" }), { status: 202 })),
    );

    const { result } = renderHook(() => useAgentGeneration());
    await act(async () => {
      await result.current.generate({ description: "x" });
    });
    expect(result.current.pending).toBe(true);

    const source = FakeEventSource.instances[0];
    source.emit(
      "agent.draft_generated",
      JSON.stringify({
        seq: 1,
        type: "agent.draft_generated",
        actor: "system",
        payload: {
          draft: { id: "a1", name: "x", prompt: "p", created_at: "t", updated_at: "t" },
          similar: { agent: "y", score: 0.5 },
        },
      }),
    );

    await waitFor(() => expect(result.current.pending).toBe(false));
    expect(result.current.draft?.name).toBe("x");
    expect(result.current.similar?.agent).toBe("y");
  });

  it("sets error on a matching job.failed event", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(new Response(JSON.stringify({ job_id: "j1" }), { status: 202 })),
    );

    const { result } = renderHook(() => useAgentGeneration());
    await act(async () => {
      await result.current.generate({ description: "x" });
    });

    const source = FakeEventSource.instances[0];
    source.emit(
      "job.failed",
      JSON.stringify({
        seq: 1,
        type: "job.failed",
        actor: "system",
        payload: { job_id: "j1", kind: "agent_generation", error: "boom" },
      }),
    );

    await waitFor(() => expect(result.current.error).toBe("boom"));
  });
});
