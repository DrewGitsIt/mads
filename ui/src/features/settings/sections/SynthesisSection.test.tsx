import { fireEvent, render, screen, within } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { SynthesisSection } from "./SynthesisSection";
import { fixtureConfig } from "../../../router/fixtures";
import type { UseConfig } from "../state/useConfig";

function makeConfig(overrides: Partial<UseConfig> = {}): UseConfig {
  return {
    saved: fixtureConfig,
    draft: fixtureConfig,
    loading: false,
    fieldErrors: {},
    dirtyFields: [],
    setDraft: vi.fn(),
    save: vi.fn().mockResolvedValue(true),
    saveFields: vi.fn().mockResolvedValue(true),
    refetch: vi.fn().mockResolvedValue(undefined),
    ...overrides,
  };
}

const offDraft = { ...fixtureConfig, synthesis_mode: "off" as const };

describe("SynthesisSection", () => {
  it("shows a static cost hint for the current synthesis mode", () => {
    render(<SynthesisSection config={makeConfig()} />);
    expect(screen.getByText("About $0.01 per session: summary only.")).toBeTruthy();
  });

  it("changing synthesis mode updates the draft", () => {
    const config = makeConfig();
    render(<SynthesisSection config={config} />);
    const group = screen.getByRole("radiogroup", { name: "Synthesis" });
    // Labels are humanized; the stored values stay off/light/full.
    expect(within(group).getAllByRole("radio").map((r) => r.textContent)).toEqual(["Off", "Summary", "Summary + DPA"]);
    fireEvent.click(within(group).getByRole("radio", { name: "Summary + DPA" }));
    expect(config.setDraft).toHaveBeenCalledWith(expect.objectContaining({ synthesis_mode: "full" }));
  });

  it("toggling apply-automatically updates the draft", () => {
    const config = makeConfig();
    render(<SynthesisSection config={config} />);
    fireEvent.click(screen.getByRole("switch", { name: "Apply proposals automatically" }));
    expect(config.setDraft).toHaveBeenCalledWith(expect.objectContaining({ synthesis_auto_approve: true }));
  });

  it("edits the wait, marked as needing a restart", () => {
    const config = makeConfig();
    render(<SynthesisSection config={config} />);
    expect((screen.getByLabelText("Wait before synthesizing") as HTMLInputElement).value).toBe(
      String(fixtureConfig.synthesis_debounce_ms),
    );
    expect(screen.getByText("Restart")).toBeTruthy();
    fireEvent.change(screen.getByLabelText("Wait before synthesizing"), { target: { value: "3000" } });
    expect(config.setDraft).toHaveBeenCalledWith(expect.objectContaining({ synthesis_debounce_ms: 3000 }));
  });

  it("hides the dependent settings while synthesis is off", () => {
    render(<SynthesisSection config={makeConfig({ draft: offDraft })} />);
    expect(screen.getByText("No synthesis cost.")).toBeTruthy();
    expect(screen.queryByRole("radiogroup", { name: "Model tier" })).toBeNull();
    expect(screen.queryByLabelText("Wait before synthesizing")).toBeNull();
  });

  it("still shows a dependent setting's error while synthesis is off", () => {
    render(
      <SynthesisSection
        config={makeConfig({ draft: offDraft, fieldErrors: { synthesis_debounce_ms: "must not be negative" } })}
      />,
    );
    expect(screen.getByText("must not be negative")).toBeTruthy();
  });

  it("renders every field error from one submit", () => {
    const config = makeConfig({ fieldErrors: { synthesis_mode: "invalid", default_model_tier: "invalid" } });
    render(<SynthesisSection config={config} />);
    expect(screen.getAllByText("invalid")).toHaveLength(2);
  });
});
