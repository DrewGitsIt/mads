import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { CapabilitiesShelf } from "./CapabilitiesShelf";
import { FakeEventSource } from "../../router/testFakeEventSource";

describe("CapabilitiesShelf", () => {
  beforeEach(() => {
    FakeEventSource.instances = [];
    vi.stubGlobal("EventSource", FakeEventSource);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("shows the empty state when there are no capabilities", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response("[]", { status: 200 })));
    render(<CapabilitiesShelf />);
    await waitFor(() => expect(screen.getByText("No capabilities yet.")).toBeTruthy());
  });

  it("opens the editor for a new capability", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response("[]", { status: 200 })));
    render(<CapabilitiesShelf />);
    await waitFor(() => expect(screen.getByText("No capabilities yet.")).toBeTruthy());
    fireEvent.click(screen.getByText("New capability"));
    expect(screen.getByRole("complementary")).toBeTruthy();
    expect(screen.getByText("Credential env vars")).toBeTruthy();
  });

  it("renders a card with a health dot per fetched capability", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(
        new Response(
          JSON.stringify([
            {
              id: "c1",
              name: "github",
              type: "mcp",
              config: {},
              health: { healthy: true },
              created_at: "t",
              updated_at: "t",
            },
          ]),
          { status: 200 },
        ),
      ),
    );
    render(<CapabilitiesShelf />);
    await waitFor(() => expect(screen.getByText("github")).toBeTruthy());
    expect(screen.getByRole("img", { name: "Healthy" })).toBeTruthy();
  });
});
