import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { AgentsShelf } from "./AgentsShelf";
import { FakeEventSource } from "../../router/testFakeEventSource";

describe("AgentsShelf", () => {
  beforeEach(() => {
    FakeEventSource.instances = [];
    vi.stubGlobal("EventSource", FakeEventSource);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("shows the empty state when there are no agents", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response("[]", { status: 200 })));
    render(<AgentsShelf />);
    await waitFor(() => expect(screen.getByText("No agents yet.")).toBeTruthy());
  });

  it("opens the editor for a new agent", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response("[]", { status: 200 })));
    render(<AgentsShelf />);
    await waitFor(() => expect(screen.getByText("No agents yet.")).toBeTruthy());
    fireEvent.click(screen.getByText("New agent"));
    expect(screen.getByText("Describe the agent you want")).toBeTruthy();
  });

  it("renders a card per fetched agent", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(
        new Response(
          JSON.stringify([{ id: "a1", name: "code-reviewer", prompt: "p", created_at: "t", updated_at: "t" }]),
          { status: 200 },
        ),
      ),
    );
    render(<AgentsShelf />);
    await waitFor(() => expect(screen.getByText("code-reviewer")).toBeTruthy());
  });
});
