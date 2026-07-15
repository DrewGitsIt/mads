import { afterEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import { UsedBySessions } from "./UsedBySessions";

describe("UsedBySessions", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("filters the session list client-side by workspace id", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(
        new Response(
          JSON.stringify([
            {
              id: "s1",
              name: "session-one",
              activity: "active",
              workspace: "w1",
              created_at: "t",
              updated_at: "t",
            },
            {
              id: "s2",
              name: "session-two",
              activity: "active",
              workspace: "w2",
              created_at: "t",
              updated_at: "t",
            },
          ]),
          { status: 200 },
        ),
      ),
    );

    render(<UsedBySessions workspaceId="w1" />);

    await waitFor(() => expect(screen.getByText("session-one")).toBeTruthy());
    expect(screen.queryByText("session-two")).toBeNull();
  });

  it("shows an empty state when no sessions use the workspace", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response("[]", { status: 200 })));

    render(<UsedBySessions workspaceId="w1" />);

    await waitFor(() =>
      expect(screen.getByText("No sessions currently use this workspace.")).toBeTruthy(),
    );
  });
});
