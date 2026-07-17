import { describe, expect, it, vi, beforeEach, afterEach } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import { SessionsSurface } from "./SessionsSurface";
import { FakePipelineClient } from "../../pipeline/client/fixtures";
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

function json(body: unknown) {
  return Promise.resolve(new Response(JSON.stringify(body), { status: 200 }));
}

/** Scopes queries to the rail, since a selected session's name also appears in the ConversationHeader. */
function rail() {
  return within(screen.getByLabelText("Session list"));
}

/** Stubs fetch to serve the session list plus the per-session endpoints ConversationTab calls. */
function stubSessionsAndConversationFetch(sessions: Session[]) {
  vi.stubGlobal(
    "fetch",
    vi.fn().mockImplementation((input: URL) => {
      const url = input.toString();
      if (url.match(/\/sessions\/[^/]+\/transcript/)) return json([]);
      if (url.match(/\/sessions\/[^/]+$/)) {
        const id = url.split("/sessions/")[1];
        const session = sessions.find((s) => s.id === id);
        return session ? json(session) : Promise.resolve(new Response("not found", { status: 404 }));
      }
      if (url.includes("/sessions")) return json(sessions);
      return json([]);
    }),
  );
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

    render(<SessionsSurface pipelineClient={new FakePipelineClient()} />);

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

    render(<SessionsSurface pipelineClient={new FakePipelineClient()} />);
    await waitFor(() => expect(screen.getByText("No sessions yet")).toBeTruthy());

    sessions = [makeSession({})];
    const source = FakeEventSource.instances.find((s) => s.url.includes("/events"));
    source?.emit(
      "session.created",
      JSON.stringify({ seq: 1, type: "session.created", actor: "user", session_id: "s1" }),
    );

    await waitFor(() => expect(screen.getByText("Worktree merge-back UX")).toBeTruthy());
  });

  it("selects the session named by jumpToSessionId once sessions load and shows its conversation", async () => {
    const sessions = [makeSession({ id: "s1", name: "Alpha" }), makeSession({ id: "s2", name: "Bravo" })];
    stubSessionsAndConversationFetch(sessions);

    render(<SessionsSurface jumpToSessionId="s2" pipelineClient={new FakePipelineClient()} />);

    await waitFor(() => {
      const row = rail().getByText("Bravo").closest("button");
      expect(row?.className.includes("selected")).toBe(true);
    });

    await waitFor(() => expect(screen.getByLabelText("Back to Launchpad")).toBeTruthy());
    expect(screen.getAllByText("Bravo").length).toBeGreaterThan(1);
  });

  it("mounts the conversation for the selected session", async () => {
    const sessions = [makeSession({ id: "s1", name: "Alpha" }), makeSession({ id: "s2", name: "Bravo" })];
    stubSessionsAndConversationFetch(sessions);

    render(<SessionsSurface pipelineClient={new FakePipelineClient()} />);
    await waitFor(() => expect(rail().getByText("Alpha")).toBeTruthy());

    rail().getByText("Alpha").closest("button")?.click();

    await waitFor(() => expect(screen.getByLabelText("Back to Launchpad")).toBeTruthy());
    expect(screen.getByText("Chat")).toBeTruthy();
    expect(screen.getByText("Terminal")).toBeTruthy();
  });

  it("returns to the Launchpad via the back control", async () => {
    const sessions = [makeSession({ id: "s1", name: "Alpha" })];
    stubSessionsAndConversationFetch(sessions);

    render(<SessionsSurface pipelineClient={new FakePipelineClient()} />);
    await waitFor(() => expect(rail().getByText("Alpha")).toBeTruthy());

    rail().getByText("Alpha").closest("button")?.click();
    await waitFor(() => expect(screen.getByLabelText("Back to Launchpad")).toBeTruthy());

    screen.getByLabelText("Back to Launchpad").click();

    await waitFor(() => expect(screen.getByText("What can I help you with?")).toBeTruthy());
    expect(screen.queryByLabelText("Back to Launchpad")).toBeNull();
  });

  it("returns to the Launchpad by re-clicking the selected row", async () => {
    const sessions = [makeSession({ id: "s1", name: "Alpha" })];
    stubSessionsAndConversationFetch(sessions);

    render(<SessionsSurface pipelineClient={new FakePipelineClient()} />);
    await waitFor(() => expect(rail().getByText("Alpha")).toBeTruthy());

    rail().getByText("Alpha").closest("button")?.click();
    await waitFor(() => expect(screen.getByLabelText("Back to Launchpad")).toBeTruthy());

    rail().getByText("Alpha").closest("button")?.click();

    await waitFor(() => expect(screen.getByText("What can I help you with?")).toBeTruthy());
  });

  it("selecting an inactive session renders without crashing", async () => {
    const sessions = [makeSession({ id: "s1", name: "Alpha", activity: "inactive" })];
    stubSessionsAndConversationFetch(sessions);

    render(<SessionsSurface pipelineClient={new FakePipelineClient()} />);
    await waitFor(() => expect(rail().getByText("Alpha")).toBeTruthy());

    rail().getByText("Alpha").closest("button")?.click();

    await waitFor(() => expect(screen.getByLabelText("Back to Launchpad")).toBeTruthy());
    expect(screen.getByText("Chat")).toBeTruthy();
  });
});
