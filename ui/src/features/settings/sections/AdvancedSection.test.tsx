import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { AdvancedSection } from "./AdvancedSection";
import { FLAGS, setFlags } from "../../../featureFlags";
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

describe("AdvancedSection", () => {
  afterEach(() => {
    setFlags({ multiRepo: false, linkedSessions: false, remoteRegistry: false });
  });

  it("renders exactly the three flagged toggles", () => {
    render(<AdvancedSection config={makeConfig()} />);
    expect(screen.getByText("Multi-repo workspaces")).toBeTruthy();
    expect(screen.getByText("Linked sessions")).toBeTruthy();
    expect(screen.getByText("Remote registry")).toBeTruthy();
  });

  it("toggling saves the draft and applies FLAGS live on success", async () => {
    const config = makeConfig();
    render(<AdvancedSection config={config} />);

    fireEvent.click(screen.getByRole("switch", { name: "Multi-repo workspaces" }));

    expect(config.setDraft).toHaveBeenCalledWith(
      expect.objectContaining({ feature_flags: expect.objectContaining({ multi_repo: true }) }),
    );
    await waitFor(() => expect(FLAGS.multiRepo).toBe(true));
  });

  it("does not show a restart badge — feature_flags applies live", () => {
    render(<AdvancedSection config={makeConfig({ dirtyRestartFields: ["feature_flags"] })} />);
    expect(screen.queryByText(/restart required/i)).toBeNull();
  });
});
