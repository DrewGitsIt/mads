import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import { UsedBySessions } from "./UsedBySessions";
import { resetSessionsStoreForTests } from "../../router/sessionsStore";
import { FakeEventSource } from "../../router/testFakeEventSource";

describe("UsedBySessions", () => {
  beforeEach(() => {
    vi.stubGlobal("EventSource", FakeEventSource);
  });

  afterEach(() => {
    resetSessionsStoreForTests();
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

  it("on a failed fetch, doesn't claim no sessions use the workspace", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response("boom", { status: 500 })));

    render(<UsedBySessions workspaceId="w1" />);

    await waitFor(() => expect(screen.getByText("Couldn't load sessions.")).toBeTruthy());
    expect(screen.queryByText("No sessions currently use this workspace.")).toBeNull();
  });

  it("renders nothing while the first fetch is in flight", () => {
    vi.stubGlobal("fetch", vi.fn().mockReturnValue(new Promise(() => {})));

    const { container } = render(<UsedBySessions workspaceId="w1" />);

    expect(container.firstChild).toBeNull();
  });
});
