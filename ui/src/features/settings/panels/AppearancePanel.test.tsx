import { render, screen, waitFor } from "@testing-library/react";
import { fireEvent } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { AppearancePanel } from "./AppearancePanel";
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

describe("AppearancePanel", () => {
  beforeEach(() => {
    delete document.documentElement.dataset.theme;
    delete document.documentElement.dataset.palette;
  });

  it("switches palette live and persists it via save()", () => {
    const config = makeConfig();
    render(<AppearancePanel config={config} />);

    fireEvent.click(screen.getByText("space"));

    expect(document.documentElement.dataset.palette).toBe("space");
    expect(config.setDraft).toHaveBeenCalledWith(expect.objectContaining({ palette: "space" }));
    expect(config.save).toHaveBeenCalledWith(expect.objectContaining({ palette: "space" }));
  });

  it("switches theme live and persists it via save()", () => {
    const config = makeConfig();
    render(<AppearancePanel config={config} />);

    fireEvent.click(screen.getByText("light"));

    expect(document.documentElement.dataset.theme).toBe("light");
    expect(config.save).toHaveBeenCalledWith(expect.objectContaining({ theme: "light" }));
  });

  it("reverts the live theme if save() fails", async () => {
    const config = makeConfig({ save: vi.fn().mockResolvedValue(false) });
    render(<AppearancePanel config={config} />);

    fireEvent.click(screen.getByText("light"));
    expect(document.documentElement.dataset.theme).toBe("light");

    await waitFor(() => expect(document.documentElement.dataset.theme).toBe(fixtureConfig.theme));
  });
});
