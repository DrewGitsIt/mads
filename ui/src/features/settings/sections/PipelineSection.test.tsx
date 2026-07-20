import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { PipelineSection } from "./PipelineSection";
import { fixtureConfig } from "../../../router/fixtures";
import type { UseConfig } from "../state/useConfig";

function makeConfig(overrides: Partial<UseConfig> = {}): UseConfig {
  return {
    initial: fixtureConfig,
    saved: fixtureConfig,
    draft: fixtureConfig,
    loading: false,
    fieldErrors: {},
    dirtyRestartFields: [],
    setDraft: vi.fn(),
    save: vi.fn().mockResolvedValue(true),
    refetch: vi.fn().mockResolvedValue(undefined),
    ...overrides,
  };
}

describe("PipelineSection", () => {
  it("shows a static cost hint for the current synthesis mode", () => {
    render(<PipelineSection config={makeConfig()} />);
    expect(screen.getByText(/summary only/i)).toBeTruthy();
  });

  it("changing synthesis mode updates the draft and its cost hint", () => {
    const config = makeConfig();
    render(<PipelineSection config={config} />);
    fireEvent.change(screen.getByLabelText(/synthesis mode/i), { target: { value: "full" } });
    expect(config.setDraft).toHaveBeenCalledWith(expect.objectContaining({ synthesis_mode: "full" }));
  });

  it("renders every field error from one submit", () => {
    const config = makeConfig({
      fieldErrors: {
        synthesis_mode: "invalid",
        default_model_tier: "invalid",
      },
    });
    render(<PipelineSection config={config} />);
    expect(screen.getAllByText("invalid")).toHaveLength(2);
  });
});
