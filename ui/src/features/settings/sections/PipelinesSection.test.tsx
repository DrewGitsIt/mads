import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { PipelinesSection } from "./PipelinesSection";
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

const keepAwake = () => screen.getByRole("switch", { name: "Keep the computer awake during runs" });

describe("PipelinesSection", () => {
  it("edits the concurrency limit, marked as needing a restart", () => {
    const config = makeConfig();
    render(<PipelinesSection config={config} />);
    expect(screen.getByText("Restart")).toBeTruthy();
    fireEvent.change(screen.getByLabelText("Concurrency limit"), { target: { value: "5" } });
    expect(config.setDraft).toHaveBeenCalledWith(expect.objectContaining({ concurrency_limit: 5 }));
  });

  it("keeps the computer awake when allow_sleep_during_pipelines is false", () => {
    render(<PipelinesSection config={makeConfig({ draft: { ...fixtureConfig, platform: "windows" } })} />);
    expect(keepAwake().getAttribute("aria-checked")).toBe("true");
    expect(screen.getByText("Applies to runs started afterward.")).toBeTruthy();
  });

  it("is off when allow_sleep_during_pipelines is true", () => {
    const draft = { ...fixtureConfig, allow_sleep_during_pipelines: true };
    render(<PipelinesSection config={makeConfig({ draft })} />);
    expect(keepAwake().getAttribute("aria-checked")).toBe("false");
  });

  it("turning it off writes allow_sleep_during_pipelines: true", () => {
    const config = makeConfig();
    render(<PipelinesSection config={config} />);
    fireEvent.click(keepAwake());
    expect(config.setDraft).toHaveBeenCalledWith(expect.objectContaining({ allow_sleep_during_pipelines: true }));
  });

  it.each([
    ["darwin", "Closing the lid on battery still sleeps your Mac; the run pauses and recovers when it wakes."],
    ["linux", "Closing a laptop lid may still suspend, depending on your desktop's lid settings."],
  ])("shows the %s sleep caveat", (platform, caveat) => {
    render(<PipelinesSection config={makeConfig({ draft: { ...fixtureConfig, platform } })} />);
    expect(screen.getByText(`${caveat} Applies to runs started afterward.`)).toBeTruthy();
  });

  it("shows no sleep caveat on other platforms", () => {
    render(<PipelinesSection config={makeConfig({ draft: { ...fixtureConfig, platform: "windows" } })} />);
    expect(screen.queryByText(/closing (the|a laptop) lid/i)).toBeNull();
  });

  it("renders every field error from one submit", () => {
    const config = makeConfig({
      fieldErrors: { concurrency_limit: "invalid", pipeline_stage_timeout_minutes: "invalid" },
    });
    render(<PipelinesSection config={config} />);
    expect(screen.getAllByText("invalid")).toHaveLength(2);
  });

  it("changing the stage timeout updates the draft", () => {
    const config = makeConfig();
    render(<PipelinesSection config={config} />);
    fireEvent.change(screen.getByLabelText("Stage timeout"), { target: { value: "45" } });
    expect(config.setDraft).toHaveBeenCalledWith(expect.objectContaining({ pipeline_stage_timeout_minutes: 45 }));
  });

  it("shows the cost alert in dollars and stores cents", () => {
    const config = makeConfig({ draft: { ...fixtureConfig, cost_alert_cents: 250 } });
    render(<PipelinesSection config={config} />);
    const input = screen.getByLabelText("Cost alert") as HTMLInputElement;
    expect(input.value).toBe("2.5");
    fireEvent.change(input, { target: { value: "1.75" } });
    expect(config.setDraft).toHaveBeenCalledWith(expect.objectContaining({ cost_alert_cents: 175 }));
  });
});
