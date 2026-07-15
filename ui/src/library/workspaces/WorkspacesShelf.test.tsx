import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { WorkspacesShelf } from "./WorkspacesShelf";
import { FakeEventSource } from "../../router/testFakeEventSource";

describe("WorkspacesShelf", () => {
  beforeEach(() => {
    FakeEventSource.instances = [];
    vi.stubGlobal("EventSource", FakeEventSource);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("shows the empty state when there are no workspaces", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response("[]", { status: 200 })));
    render(<WorkspacesShelf />);
    await waitFor(() => expect(screen.getByText("No workspaces yet.")).toBeTruthy());
  });

  it("opens the editor for a new workspace", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response("[]", { status: 200 })));
    render(<WorkspacesShelf />);
    await waitFor(() => expect(screen.getByText("No workspaces yet.")).toBeTruthy());
    fireEvent.click(screen.getByText("New workspace"));
    expect(screen.getByRole("complementary")).toBeTruthy();
  });

  it("renders a card per fetched workspace", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(
        new Response(
          JSON.stringify([
            { id: "w1", name: "mads", defaults: {}, repos: [{ path: "./a" }], created_at: "t", updated_at: "t" },
          ]),
          { status: 200 },
        ),
      ),
    );
    render(<WorkspacesShelf />);
    await waitFor(() => expect(screen.getByText("mads")).toBeTruthy());
    expect(screen.getByText("1 repo")).toBeTruthy();
  });
});
