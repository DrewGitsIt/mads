import { afterEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { DiagnosePanel } from "./DiagnosePanel";
import type { Capability } from "../../router/types";

function makeCapability(overrides: Partial<Capability> = {}): Capability {
  return {
    id: "c1",
    name: "github",
    type: "mcp",
    config: {},
    created_at: "t",
    updated_at: "t",
    ...overrides,
  };
}

describe("DiagnosePanel", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("renders suggestions from the diagnose response", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(
        new Response(
          JSON.stringify({ healthy: false, reason: "connection refused", suggestions: ["Check the command path"] }),
          { status: 200 },
        ),
      ),
    );

    render(<DiagnosePanel capability={makeCapability()} />);
    fireEvent.click(screen.getByText("Run diagnose"));

    await waitFor(() => expect(screen.getByText("connection refused")).toBeTruthy());
    expect(screen.getByText("Check the command path")).toBeTruthy();
  });

  it("shows the not-a-live-check caveat for local MCP capabilities without a URL", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(new Response(JSON.stringify({ healthy: true, suggestions: [] }), { status: 200 })),
    );

    render(<DiagnosePanel capability={makeCapability({ config: { command: "local-server" } })} />);
    fireEvent.click(screen.getByText("Run diagnose"));

    await waitFor(() =>
      expect(screen.getByText(/Based on configuration, not a live check/)).toBeTruthy(),
    );
  });
});
