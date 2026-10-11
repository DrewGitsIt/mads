import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { SystemSection } from "./SystemSection";
import { fixtureConfig, fixturePreflightChecks } from "../../../router/fixtures";
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

let fetchMock: ReturnType<typeof vi.fn>;

describe("SystemSection", () => {
  beforeEach(() => {
    fetchMock = vi.fn((input: RequestInfo | URL) => {
      const url = String(input);
      if (url.includes("/preflight")) {
        return Promise.resolve(new Response(JSON.stringify(fixturePreflightChecks), { status: 200 }));
      }
      return Promise.resolve(new Response("{}", { status: 200 }));
    });
    vi.stubGlobal("fetch", fetchMock);
  });
  afterEach(() => vi.unstubAllGlobals());

  const preflightCalls = () => fetchMock.mock.calls.filter(([u]) => String(u).includes("/preflight")).length;

  it("renders the health check, and Run again re-runs it", async () => {
    render(<SystemSection config={makeConfig()} shellsOpen={false} onShellsOpenChange={vi.fn()} />);
    await waitFor(() => expect(screen.getByText("Git")).toBeTruthy());
    expect(screen.getByText("mads has only been proven against < 1.49.0, detected 1.50.1")).toBeTruthy();
    const before = preflightCalls();
    fireEvent.click(screen.getByRole("button", { name: "Run again" }));
    await waitFor(() => expect(preflightCalls()).toBe(before + 1));
  });

  it("edits log retention, marked as needing a restart", () => {
    const config = makeConfig();
    render(<SystemSection config={config} shellsOpen={false} onShellsOpenChange={vi.fn()} />);
    expect(screen.getByText("Restart")).toBeTruthy();
    fireEvent.change(screen.getByLabelText("Keep logs for"), { target: { value: "14" } });
    expect(config.setDraft).toHaveBeenCalledWith(expect.objectContaining({ log_retention_days: 14 }));
  });

  it("shows the log retention field error", () => {
    render(
      <SystemSection
        config={makeConfig({ fieldErrors: { log_retention_days: "must be at least 1" } })}
        shellsOpen={false}
        onShellsOpenChange={vi.fn()}
      />,
    );
    expect(screen.getByText("must be at least 1")).toBeTruthy();
  });

  it("holds the shells row and conversation import", () => {
    const onShellsOpenChange = vi.fn();
    render(<SystemSection config={makeConfig()} shellsOpen={false} onShellsOpenChange={onShellsOpenChange} />);
    expect(screen.getByText("Shells")).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Manage…" }));
    expect(onShellsOpenChange).toHaveBeenCalledWith(true);
    expect(screen.getByRole("button", { name: "Scan for conversations" })).toBeTruthy();
  });
});
