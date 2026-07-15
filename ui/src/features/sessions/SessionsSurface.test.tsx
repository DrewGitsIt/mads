import { describe, expect, it, vi, beforeEach, afterEach } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import { SessionsSurface } from "./SessionsSurface";
import { FakeEventSource } from "../../router/testFakeEventSource";
import type { Session } from "../../router/types";

function makeSession(overrides: Partial<Session>): Session {
  return {
    id: "s1",
    name: "Worktree merge-back UX",
    activity: "active",
    created_at: "2026-07-01T00:00:00Z",
    updated_at: "2026-07-01T00:00:00Z",
    ...overrides,
  };
}

describe("SessionsSurface", () => {
  beforeEach(() => {
    FakeEventSource.instances = [];
    vi.stubGlobal("EventSource", FakeEventSource);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("shows the teaching empty state when there are no sessions", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockImplementation(() => Promise.resolve(new Response("[]", { status: 200 }))),
    );

    render(<SessionsSurface />);

    await waitFor(() => expect(screen.getByText("No sessions yet")).toBeTruthy());
    expect(screen.getByText("What can I help you with?")).toBeTruthy();
  });

  it("shows a new session in the rail once the SSE subscription reports it", async () => {
    let sessions: Session[] = [];
    vi.stubGlobal(
      "fetch",
      vi.fn().mockImplementation((input: URL) => {
        const url = input.toString();
        if (url.includes("/sessions")) {
          return Promise.resolve(new Response(JSON.stringify(sessions), { status: 200 }));
        }
        return Promise.resolve(new Response("[]", { status: 200 }));
      }),
    );

    render(<SessionsSurface />);
    await waitFor(() => expect(screen.getByText("No sessions yet")).toBeTruthy());

    sessions = [makeSession({})];
    const source = FakeEventSource.instances.find((s) => s.url.includes("/events"));
    source?.emit(
      "session.created",
      JSON.stringify({ seq: 1, type: "session.created", actor: "user", session_id: "s1" }),
    );

    await waitFor(() => expect(screen.getByText("Worktree merge-back UX")).toBeTruthy());
  });

  it("selects the session named by jumpToSessionId once sessions load", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockImplementation(() =>
        Promise.resolve(
          new Response(
            JSON.stringify([makeSession({ id: "s1", name: "Alpha" }), makeSession({ id: "s2", name: "Bravo" })]),
            { status: 200 },
          ),
        ),
      ),
    );

    render(<SessionsSurface jumpToSessionId="s2" />);

    await waitFor(() => {
      const row = screen.getByText("Bravo").closest("button");
      expect(row?.className.includes("selected")).toBe(true);
    });
  });
});
