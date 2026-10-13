import { render, screen, waitFor, within } from "@testing-library/react";
import { fireEvent } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { AppearanceSection } from "./AppearanceSection";
import { paletteInfo, paletteNames } from "../../../tokens/palettes";
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

  it("saves the export format live; Ask clears it", () => {
    const config = makeConfig({ draft: { ...fixtureConfig, export_format: "md" } });
    render(<AppearanceSection config={config} />);
    const group = screen.getByRole("radiogroup", { name: "Export format" });
    expect(within(group).getByRole("radio", { name: "Markdown" }).getAttribute("aria-checked")).toBe("true");

    fireEvent.click(within(group).getByRole("radio", { name: "JSON" }));
    expect(config.saveFields).toHaveBeenCalledWith({ export_format: "json" });
    fireEvent.click(within(group).getByRole("radio", { name: "Ask" }));
    expect(config.saveFields).toHaveBeenCalledWith({ export_format: "" });
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

  it("draws a preview card per palette, ringed on the saved one", () => {
    document.documentElement.dataset.theme = "light";
    render(<AppearanceSection config={makeConfig()} />);
    const group = screen.getByRole("radiogroup", { name: "Palette" });
    const cards = within(group).getAllByRole("radio");
    expect(cards.map((c) => c.getAttribute("aria-label"))).toEqual(paletteInfo.map((p) => p.label));
    expect(cards.map((c) => c.getAttribute("data-palette"))).toEqual([...paletteNames]);
    // Scoped to the applied theme, so each card reads as this app in that palette.
    expect(cards.every((c) => c.getAttribute("data-theme") === "light")).toBe(true);
    expect(within(group).getByRole("radio", { name: "Clay" }).getAttribute("aria-checked")).toBe("true");
  });

  it("tries a palette on while hovered, keeps the ring on the saved one, and reverts on leave", () => {
    const config = makeConfig();
    render(<AppearanceSection config={config} />);
    const space = screen.getByRole("radio", { name: "Space" });

    fireEvent.mouseEnter(space);
    expect(document.documentElement.dataset.palette).toBe("space");
    expect(space.getAttribute("aria-checked")).toBe("false");
    expect(screen.getByRole("radio", { name: "Clay" }).getAttribute("aria-checked")).toBe("true");
    expect(config.saveFields).not.toHaveBeenCalled();

    fireEvent.mouseLeave(space);
    expect(document.documentElement.dataset.palette).toBe("clay");
  });

  it("reverts a try-on if Settings unmounts mid-hover", () => {
    const { unmount } = render(<AppearanceSection config={makeConfig()} />);
    fireEvent.mouseEnter(screen.getByRole("radio", { name: "Glacier" }));
    expect(document.documentElement.dataset.palette).toBe("glacier");
    unmount();
    expect(document.documentElement.dataset.palette).toBe("clay");
  });

  it("doesn't try a palette on from keyboard focus; arrow keys choose and save", () => {
    const config = makeConfig();
    render(<AppearanceSection config={config} />);
    const clay = screen.getByRole("radio", { name: "Clay" });
    document.documentElement.dataset.palette = "clay";

    fireEvent.focus(screen.getByRole("radio", { name: "Space" }));
    expect(document.documentElement.dataset.palette).toBe("clay");

    fireEvent.keyDown(clay, { key: "ArrowRight" });
    expect(document.activeElement).toBe(screen.getByRole("radio", { name: paletteInfo[1].label }));
    expect(config.saveFields).toHaveBeenCalledWith({ palette: paletteInfo[1].name });
  });

  it("filters the cards by tag; arrow keys move among the cards shown", () => {
    const config = makeConfig();
    render(<AppearanceSection config={config} />);
    const filters = screen.getByRole("group", { name: "Filter palettes" });
    const group = screen.getByRole("radiogroup", { name: "Palette" });

    fireEvent.click(within(filters).getByRole("button", { name: "Cool" }));
    const cool = paletteInfo.filter((p) => p.tags.includes("cool"));
    expect(within(filters).getByRole("button", { name: "Cool" }).getAttribute("aria-pressed")).toBe("true");
    expect(within(group).getAllByRole("radio").map((c) => c.getAttribute("data-palette"))).toEqual(
      cool.map((p) => p.name),
    );

    // The saved palette (clay) is filtered out, so the first card shown holds the tab stop.
    const first = within(group).getByRole("radio", { name: cool[0].label });
    expect(first.tabIndex).toBe(0);
    fireEvent.keyDown(first, { key: "ArrowLeft" });
    expect(config.saveFields).toHaveBeenCalledWith({ palette: cool[cool.length - 1].name });

    fireEvent.click(within(filters).getByRole("button", { name: "All" }));
    expect(within(group).getAllByRole("radio")).toHaveLength(paletteInfo.length);
  });

  it("reverts a clicked palette whose save fails", async () => {
    const config = makeConfig({ saveFields: vi.fn().mockResolvedValue(false) });
    render(<AppearanceSection config={config} />);
    fireEvent.click(screen.getByRole("radio", { name: "Glacier" }));
    expect(document.documentElement.dataset.palette).toBe("glacier");
    await waitFor(() => expect(document.documentElement.dataset.palette).toBe("clay"));
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
