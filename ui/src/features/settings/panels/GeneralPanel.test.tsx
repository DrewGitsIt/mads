import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { GeneralPanel } from "./GeneralPanel";
import { fixtureConfig, fixturePreflightChecks } from "../../../router/fixtures";
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

describe("GeneralPanel", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("shows the restart-required badge when a restart field is dirty", () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(new Response(JSON.stringify(fixturePreflightChecks), { status: 200 })),
    );
    const config = makeConfig({ dirtyRestartFields: ["concurrency_limit"] });
    render(<GeneralPanel config={config} />);
    expect(screen.getByText(/restart required/i)).toBeTruthy();
  });

  it("does not show the restart badge when only a live field is dirty", () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(new Response(JSON.stringify(fixturePreflightChecks), { status: 200 })),
    );
    const config = makeConfig({ dirtyRestartFields: [] });
    render(<GeneralPanel config={config} />);
    expect(screen.queryByText(/restart required/i)).toBeNull();
  });

  it("renders every field error from one submit, not just the first", () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(new Response(JSON.stringify(fixturePreflightChecks), { status: 200 })),
    );
    const config = makeConfig({
      fieldErrors: {
        concurrency_limit: "must be at least 1",
        log_retention_days: "must be at least 1",
      },
    });
    render(<GeneralPanel config={config} />);
    expect(screen.getAllByText("must be at least 1")).toHaveLength(2);
  });

  it("edits call setDraft with the new value", () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(new Response(JSON.stringify(fixturePreflightChecks), { status: 200 })),
    );
    const config = makeConfig();
    render(<GeneralPanel config={config} />);
    fireEvent.change(screen.getByLabelText(/concurrency limit/i), { target: { value: "5" } });
    expect(config.setDraft).toHaveBeenCalledWith(expect.objectContaining({ concurrency_limit: 5 }));
  });

  it("renders preflight checks from usePreflight", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(new Response(JSON.stringify(fixturePreflightChecks), { status: 200 })),
    );
    render(<GeneralPanel config={makeConfig()} />);
    await waitFor(() => expect(screen.getByText("git")).toBeTruthy());
  });
});
