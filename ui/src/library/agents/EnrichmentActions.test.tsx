import { afterEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { EnrichmentActions } from "./EnrichmentActions";

describe("EnrichmentActions", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("calls onDescribe with the describe result", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(
        new Response(JSON.stringify({ result: "Stabilizes flaky tests." }), { status: 200 }),
      ),
    );
    const onDescribe = vi.fn();
    render(
      <EnrichmentActions
        prompt="You stabilize flaky tests."
        onDescribe={onDescribe}
        onSuggestCapabilities={vi.fn()}
        onRefine={vi.fn()}
      />,
    );
    fireEvent.click(screen.getByText("Describe"));
    await waitFor(() => expect(onDescribe).toHaveBeenCalledWith("Stabilizes flaky tests."));
  });

  it("calls onSuggestCapabilities with the raw AgentCapabilities result", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(
        new Response(JSON.stringify({ tools: ["shell"] }), { status: 200 }),
      ),
    );
    const onSuggestCapabilities = vi.fn();
    render(
      <EnrichmentActions
        prompt="You stabilize flaky tests."
        onDescribe={vi.fn()}
        onSuggestCapabilities={onSuggestCapabilities}
        onRefine={vi.fn()}
      />,
    );
    fireEvent.click(screen.getByText("Suggest capabilities"));
    await waitFor(() => expect(onSuggestCapabilities).toHaveBeenCalledWith({ tools: ["shell"] }));
  });

  it("calls onRefine with the refine result once an instruction is given", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(
        new Response(JSON.stringify({ result: "Refined prompt." }), { status: 200 }),
      ),
    );
    const onRefine = vi.fn();
    render(
      <EnrichmentActions
        prompt="You stabilize flaky tests."
        onDescribe={vi.fn()}
        onSuggestCapabilities={vi.fn()}
        onRefine={onRefine}
      />,
    );
    fireEvent.change(screen.getByPlaceholderText(/Refine instruction/), {
      target: { value: "make it more concise" },
    });
    fireEvent.click(screen.getByText("Refine"));
    await waitFor(() => expect(onRefine).toHaveBeenCalledWith("Refined prompt."));
  });
});
