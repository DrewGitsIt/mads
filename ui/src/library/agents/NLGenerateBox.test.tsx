import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { NLGenerateBox } from "./NLGenerateBox";
import { FakeEventSource } from "../../router/testFakeEventSource";
import type { Agent } from "../../router/types";

function makeDraft(overrides: Partial<Agent> = {}): Agent {
  return {
    id: "a1",
    name: "flaky-test-stabilizer",
    prompt: "You stabilize flaky tests.",
    created_at: "2026-07-01T00:00:00Z",
    updated_at: "2026-07-01T00:00:00Z",
    ...overrides,
  };
}

describe("NLGenerateBox", () => {
  beforeEach(() => {
    FakeEventSource.instances = [];
    vi.stubGlobal("EventSource", FakeEventSource);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("renders a draft-styled definition from the agent.draft_generated event", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(new Response(JSON.stringify({ job_id: "j1" }), { status: 202 })),
    );

    render(<NLGenerateBox onApply={vi.fn()} />);
    fireEvent.change(screen.getByLabelText("Describe the agent you want"), {
      target: { value: "Stabilizes flaky tests" },
    });
    fireEvent.click(screen.getByText("Generate"));

    await waitFor(() => expect(FakeEventSource.instances.length).toBe(1));
    const source = FakeEventSource.instances[0];
    source.emit(
      "agent.draft_generated",
      JSON.stringify({
        seq: 1,
        type: "agent.draft_generated",
        actor: "system",
        payload: { draft: makeDraft() },
      }),
    );

    await waitFor(() => expect(screen.getByText("flaky-test-stabilizer")).toBeTruthy());
    expect(screen.getByText("Draft")).toBeTruthy();
  });

  it("shows the similarity warning when a near-duplicate is present", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(new Response(JSON.stringify({ job_id: "j1" }), { status: 202 })),
    );

    render(<NLGenerateBox onApply={vi.fn()} />);
    fireEvent.change(screen.getByLabelText("Describe the agent you want"), {
      target: { value: "Stabilizes flaky tests" },
    });
    fireEvent.click(screen.getByText("Generate"));

    await waitFor(() => expect(FakeEventSource.instances.length).toBe(1));
    const source = FakeEventSource.instances[0];
    source.emit(
      "agent.draft_generated",
      JSON.stringify({
        seq: 1,
        type: "agent.draft_generated",
        actor: "system",
        payload: {
          draft: makeDraft(),
          similar: { agent: "test-doctor", score: 0.92 },
        },
      }),
    );

    await waitFor(() => expect(screen.getByText(/test-doctor/)).toBeTruthy());
    expect(screen.getByText(/92% match/)).toBeTruthy();
  });
});
