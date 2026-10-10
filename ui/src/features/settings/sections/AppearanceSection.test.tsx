import { render, screen, waitFor, within } from "@testing-library/react";
import { fireEvent } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { AppearanceSection } from "./AppearanceSection";
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

describe("AppearanceSection", () => {
  beforeEach(() => {
    delete document.documentElement.dataset.theme;
    delete document.documentElement.dataset.palette;
    delete document.documentElement.dataset.motion;
  });

  it("turns reduce motion on live, persists it, and leaves streaming speed alone", () => {
    const config = makeConfig({ draft: { ...fixtureConfig, stream_speed: "slow" } });
    render(<AppearanceSection config={config} />);
    const group = screen.getByRole("group", { name: "Reduce motion" });
    expect(within(group).getByText("system").getAttribute("aria-pressed")).toBe("true");

    fireEvent.click(within(group).getByText("on"));

    expect(document.documentElement.dataset.motion).toBe("reduce");
    expect(config.save).toHaveBeenCalledWith(
      expect.objectContaining({ reduce_motion: true, stream_speed: "slow" }),
    );
  });

  it("back to system clears the override, and a failed save reverts it", async () => {
    const config = makeConfig({
      saved: { ...fixtureConfig, reduce_motion: true },
      draft: { ...fixtureConfig, reduce_motion: true },
      save: vi.fn().mockResolvedValue(false),
    });
    document.documentElement.dataset.motion = "reduce";
    render(<AppearanceSection config={config} />);

    fireEvent.click(within(screen.getByRole("group", { name: "Reduce motion" })).getByText("system"));
    expect(document.documentElement.dataset.motion).toBeUndefined();
    await waitFor(() => expect(document.documentElement.dataset.motion).toBe("reduce"));
  });

  it("switches palette live and persists it via save()", () => {
    const config = makeConfig();
    render(<AppearanceSection config={config} />);

    fireEvent.click(screen.getByText("space"));

    expect(document.documentElement.dataset.palette).toBe("space");
    expect(config.setDraft).toHaveBeenCalledWith(expect.objectContaining({ palette: "space" }));
    expect(config.save).toHaveBeenCalledWith(expect.objectContaining({ palette: "space" }));
  });

  it("switches theme live and persists it via save()", () => {
    const config = makeConfig();
    render(<AppearanceSection config={config} />);

    fireEvent.click(screen.getByText("light"));

    expect(document.documentElement.dataset.theme).toBe("light");
    expect(config.save).toHaveBeenCalledWith(expect.objectContaining({ theme: "light" }));
  });

  it("reverts the live theme if save() fails", async () => {
    const config = makeConfig({ save: vi.fn().mockResolvedValue(false) });
    render(<AppearanceSection config={config} />);

    fireEvent.click(screen.getByText("light"));
    expect(document.documentElement.dataset.theme).toBe("light");

    await waitFor(() => expect(document.documentElement.dataset.theme).toBe(fixtureConfig.theme));
  });

  it("offers native, slow, fast and custom, and reads the retired medium as fast", () => {
    render(<AppearanceSection config={makeConfig({ draft: { ...fixtureConfig, stream_speed: "medium" } })} />);
    const group = screen.getByRole("group", { name: "Streaming speed" });
    expect([...group.querySelectorAll("button")].map((b) => b.textContent)).toEqual([
      "native",
      "slow",
      "fast",
      "custom",
    ]);
    expect(screen.getByText("fast").closest("button")?.getAttribute("aria-pressed")).toBe("true");
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
      expect(config.save).toHaveBeenCalledWith(expect.objectContaining({ stream_wpm: 2000 })),
    );
    expect(input.value).toBe("2000");
  });
});
