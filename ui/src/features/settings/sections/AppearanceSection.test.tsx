import { render, screen, waitFor, within } from "@testing-library/react";
import { fireEvent } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { AppearanceSection } from "./AppearanceSection";
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

describe("AppearanceSection", () => {
  beforeEach(() => {
    delete document.documentElement.dataset.theme;
    delete document.documentElement.dataset.palette;
    delete document.documentElement.dataset.motion;
  });

  it("turns reduce motion on live, persists it, and leaves streaming speed alone", () => {
    const config = makeConfig({ draft: { ...fixtureConfig, stream_speed: "slow" } });
    render(<AppearanceSection config={config} />);
    const group = screen.getByRole("radiogroup", { name: "Reduce motion" });
    expect(within(group).getByRole("radio", { name: "System" }).getAttribute("aria-checked")).toBe("true");

    fireEvent.click(within(group).getByRole("radio", { name: "On" }));

    expect(document.documentElement.dataset.motion).toBe("reduce");
    expect(config.saveFields).toHaveBeenCalledWith({ reduce_motion: true });
  });

  it("back to system clears the override, and a failed save reverts it", async () => {
    const config = makeConfig({
      saved: { ...fixtureConfig, reduce_motion: true },
      draft: { ...fixtureConfig, reduce_motion: true },
      saveFields: vi.fn().mockResolvedValue(false),
    });
    document.documentElement.dataset.motion = "reduce";
    render(<AppearanceSection config={config} />);

    fireEvent.click(within(screen.getByRole("radiogroup", { name: "Reduce motion" })).getByRole("radio", { name: "System" }));
    expect(document.documentElement.dataset.motion).toBeUndefined();
    await waitFor(() => expect(document.documentElement.dataset.motion).toBe("reduce"));
  });

  it("switches palette live and persists only that field", () => {
    const config = makeConfig();
    render(<AppearanceSection config={config} />);

    fireEvent.click(screen.getByRole("radio", { name: "Space" }));

    expect(document.documentElement.dataset.palette).toBe("space");
    expect(config.saveFields).toHaveBeenCalledWith({ palette: "space" });
    // Never the whole draft: that would commit unrelated unsaved edits.
    expect(config.save).not.toHaveBeenCalled();
    expect(config.setDraft).not.toHaveBeenCalled();
  });

  it("switches theme live and persists only that field", () => {
    const config = makeConfig();
    render(<AppearanceSection config={config} />);

    fireEvent.click(within(screen.getByRole("radiogroup", { name: "Theme" })).getByRole("radio", { name: "Light" }));

    expect(document.documentElement.dataset.theme).toBe("light");
    expect(config.saveFields).toHaveBeenCalledWith({ theme: "light" });
    expect(config.save).not.toHaveBeenCalled();
  });

  it("reverts the live theme if the save fails", async () => {
    const config = makeConfig({ saveFields: vi.fn().mockResolvedValue(false) });
    render(<AppearanceSection config={config} />);

    fireEvent.click(within(screen.getByRole("radiogroup", { name: "Theme" })).getByRole("radio", { name: "Light" }));
    expect(document.documentElement.dataset.theme).toBe("light");

    await waitFor(() => expect(document.documentElement.dataset.theme).toBe(fixtureConfig.theme));
  });

  it("offers as sent, slow, fast and custom, and reads the retired medium as fast", () => {
    render(<AppearanceSection config={makeConfig({ draft: { ...fixtureConfig, stream_speed: "medium" } })} />);
    const group = screen.getByRole("radiogroup", { name: "Reply streaming" });
    expect(within(group).getAllByRole("radio").map((b) => b.textContent)).toEqual([
      "As sent",
      "Slow",
      "Fast",
      "Custom",
    ]);
    expect(within(group).getByRole("radio", { name: "Fast" }).getAttribute("aria-checked")).toBe("true");
    expect(screen.queryByLabelText("Words per minute")).toBeNull();
  });

  it("saves a custom pace on blur, clamped to the contract's bounds", async () => {
    const config = makeConfig({ draft: { ...fixtureConfig, stream_speed: "custom" } });
    render(<AppearanceSection config={config} />);
    const input = screen.getByLabelText("Words per minute") as HTMLInputElement;
    expect(input.value).toBe("350"); // unset → the default pace

    fireEvent.change(input, { target: { value: "5000" } });
    fireEvent.blur(input);
    await waitFor(() =>
      expect(config.saveFields).toHaveBeenCalledWith({ stream_wpm: 2000 }),
    );
    expect(input.value).toBe("2000");
  });

  it("density is a live choice with readable labels that persists only display_mode", () => {
    const config = makeConfig({ draft: { ...fixtureConfig, display_mode: "comfortable" } });
    render(<AppearanceSection config={config} />);
    const group = screen.getByRole("radiogroup", { name: "Density" });
    expect(within(group).getAllByRole("radio").map((r) => r.textContent)).toEqual(["Comfortable", "Compact"]);
    fireEvent.click(within(group).getByRole("radio", { name: "Compact" }));
    expect(config.saveFields).toHaveBeenCalledWith({ display_mode: "compact" });
    expect(config.setDraft).not.toHaveBeenCalled();
    expect(config.save).not.toHaveBeenCalled();
  });

  it("keeps an unrecognized stored density visible", () => {
    render(<AppearanceSection config={makeConfig({ draft: { ...fixtureConfig, display_mode: "dense" } })} />);
    const group = screen.getByRole("radiogroup", { name: "Density" });
    expect(within(group).getByRole("radio", { name: "dense" }).getAttribute("aria-checked")).toBe("true");
  });
});
