import { render, screen, waitFor } from "@testing-library/react";
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
