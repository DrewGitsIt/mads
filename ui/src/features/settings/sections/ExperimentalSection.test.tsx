import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ExperimentalSection } from "./ExperimentalSection";
import { FLAGS, setFlags } from "../../../featureFlags";
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

describe("ExperimentalSection", () => {
  afterEach(() => {
    setFlags({ multiRepo: false, linkedSessions: false, remoteRegistry: false });
  });

  it("renders exactly the three flagged toggles", () => {
    render(<ExperimentalSection config={makeConfig()} />);
    expect(screen.getByText("Multi-repo workspaces")).toBeTruthy();
    expect(screen.getByText("Linked sessions")).toBeTruthy();
    expect(screen.getByText("Remote registry")).toBeTruthy();
  });

  it("toggling saves only feature_flags and applies FLAGS live on success", async () => {
    const config = makeConfig();
    render(<ExperimentalSection config={config} />);

    fireEvent.click(screen.getByRole("switch", { name: "Multi-repo workspaces" }));

    expect(config.saveFields).toHaveBeenCalledWith({
      feature_flags: expect.objectContaining({ multi_repo: true }),
    });
    expect(config.save).not.toHaveBeenCalled();
    await waitFor(() => expect(FLAGS.multiRepo).toBe(true));
  });

  it("leaves FLAGS alone when the save fails", async () => {
    const saveFields = vi.fn().mockResolvedValue(false);
    render(<ExperimentalSection config={makeConfig({ saveFields })} />);

    fireEvent.click(screen.getByRole("switch", { name: "Multi-repo workspaces" }));

    await waitFor(() => expect(saveFields).toHaveBeenCalled());
    expect(FLAGS.multiRepo).toBe(false);
  });

  it("does not show a restart badge — feature_flags applies live", () => {
    render(<ExperimentalSection config={makeConfig()} />);
    expect(screen.queryByText(/restart/i)).toBeNull();
  });
});
