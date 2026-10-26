import { describe, expect, it, vi, beforeEach, afterEach } from "vitest";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { SessionsSurface } from "./SessionsSurface";
import { FakePipelineClient } from "../../pipeline/client/fixtures";
import { FakeEventSource } from "../../router/testFakeEventSource";
import type { Session } from "../../router/types";
import styles from "./SessionsSurface.module.css";
import { readFileSync } from "node:fs";
import { FLAGS } from "../../featureFlags";

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

/** Scopes queries to the rail, since a selected session's name also appears in the workspace's own header. */
function rail() {
  return within(screen.getByLabelText("Session list"));
}

/** Stubs fetch to serve the session list plus the per-session endpoints ConversationTab (and, when needed, the Tasks tab) call. */
function stubSessionsAndConversationFetch(sessions: Session[]) {
  vi.stubGlobal(
    "fetch",
    vi.fn().mockImplementation((input: URL) => {
      const url = input.toString();
      if (url.match(/\/sessions\/[^/]+\/transcript/)) return json([]);
      if (url.includes("/tasks")) return json([]);
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

  // A rail card's hover action cluster paints position:fixed outside the card,
  // so SessionRow has to know which ancestor is the scroll viewport to tell a
  // visible row from one scrolled out of sight. It finds that box by the
  // data-rail-scroll marker, and the marker has to sit on the element that
  // actually owns the overflow — the aside here, not the SessionRail div nested
  // inside it, which has no height of its own and is always exactly as tall as
  // the list. On the wrong element the dismissal guard compares every row
  // against the full content height and silently never fires.
  it("marks the rail's scrolling box, not the unbounded list inside it, as the scroll viewport", async () => {
    stubSessionsAndConversationFetch([makeSession({ id: "a", name: "Alpha" })]);

    render(<SessionsSurface pipelineClient={new FakePipelineClient()} />);
    await waitFor(() => expect(rail().getByText("Alpha")).toBeTruthy());

    const marked = document.querySelectorAll("[data-rail-scroll]");
    expect(marked).toHaveLength(1);
    const viewport = marked[0] as HTMLElement;

    // It is the aside, and the rail list lives inside it rather than being it.
    expect(viewport.tagName).toBe("ASIDE");
    const list = screen.getByLabelText("Session list");
    expect(viewport.contains(list)).toBe(true);
    expect(viewport).not.toBe(list);

    // And that aside is the box the stylesheet gives the scrollbar to.
    const surfaceCss = readFileSync("src/features/sessions/SessionsSurface.module.css", "utf8");
    const railRule = surfaceCss.match(/\.rail \{([^}]*)\}/)![1];
    expect(railRule).toMatch(/overflow-y:\s*auto/);
    expect(viewport.className).toBe(styles.rail);
  });

  // The link editor floats over the main pane only: the pane behind it is
  // blurred and out of reach, the rail stays live as its picker.
  it("opens the link editor over a blurred, inert pane and leaves the rail live", async () => {
    FLAGS.linkedSessions = true;
    try {
      const sessions = [makeSession({ id: "a", name: "Alpha" }), makeSession({ id: "b", name: "Beta" })];
      vi.stubGlobal(
        "fetch",
        vi.fn().mockImplementation((input: URL) => {
          const url = input.toString();
          if (url.includes("/links")) return json([]);
          if (url.match(/\/sessions(\?|$)/)) return json(sessions);
          return json([]);
        }),
      );
      render(<SessionsSurface pipelineClient={new FakePipelineClient()} />);
      await waitFor(() => expect(rail().getByText("Alpha")).toBeTruthy());

      const pane = document.querySelector("[data-session-pane]")!;
      expect(pane.hasAttribute("data-blurred")).toBe(false);
      fireEvent.click(rail().getByRole("button", { name: "Links for Alpha" }));
      const dialog = screen.getByRole("dialog", { name: "Links · Alpha" });
      expect(pane.hasAttribute("data-blurred")).toBe(true);
      expect(pane.hasAttribute("inert")).toBe(true);
      expect(pane.contains(dialog)).toBe(false);
      expect(document.querySelector("[data-rail-scroll]")!.contains(dialog)).toBe(false);

      // A rail click picks rather than opening the session.
      fireEvent.click(rail().getByText("Beta"));
      expect(within(dialog).getByRole("group", { name: "Edit link Alpha → Beta" })).toBeTruthy();
      expect(screen.queryByRole("tabpanel")).toBeNull();

      fireEvent.click(document.querySelector("[data-link-scrim]")!);
      fireEvent.click(screen.getByRole("button", { name: "Discard" }));
      expect(screen.queryByRole("dialog")).toBeNull();
      expect(pane.hasAttribute("inert")).toBe(false);
    } finally {
      FLAGS.linkedSessions = false;
    }
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
      const row = rail().getByText("Bravo").closest('[role="button"]');
      expect(row?.className.includes("selected")).toBe(true);
    });

    await waitFor(() => expect(screen.getByLabelText("Workspace actions")).toBeTruthy());
    expect(screen.getAllByText("Bravo").length).toBeGreaterThan(1);
  });

  it("mounts the conversation for the selected session", async () => {
    const sessions = [makeSession({ id: "s1", name: "Alpha" }), makeSession({ id: "s2", name: "Bravo" })];
    stubSessionsAndConversationFetch(sessions);

    render(<SessionsSurface pipelineClient={new FakePipelineClient()} />);
    await waitFor(() => expect(rail().getByText("Alpha")).toBeTruthy());

    fireEvent.click(rail().getByText("Alpha"));

    await waitFor(() => expect(screen.getByLabelText("Workspace actions")).toBeTruthy());
    // The conversation's session dock, in chat with the composer inside it.
    const dock = await screen.findByTestId("session-dock");
    expect(dock.getAttribute("data-mode")).toBe("chat");
    expect(within(dock).getByPlaceholderText(/message the agent/i)).toBeTruthy();
  });

  it("returns to the Launchpad by re-clicking the selected row", async () => {
    const sessions = [makeSession({ id: "s1", name: "Alpha" })];
    stubSessionsAndConversationFetch(sessions);

    render(<SessionsSurface pipelineClient={new FakePipelineClient()} />);
    await waitFor(() => expect(rail().getByText("Alpha")).toBeTruthy());

    fireEvent.click(rail().getByText("Alpha"));
    await waitFor(() => expect(screen.getByLabelText("Workspace actions")).toBeTruthy());

    fireEvent.click(rail().getByText("Alpha"));

    await waitFor(() => expect(screen.getByText("What can I help you with?")).toBeTruthy());
  });

  it("shows the teaching empty state after deleting the last session", async () => {
    let sessions: Session[] = [makeSession({})];
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
    await waitFor(() => expect(rail().getByText("Worktree merge-back UX")).toBeTruthy());

    sessions = [];
    const source = FakeEventSource.instances.find((s) => s.url.includes("/events"));
    source?.emit(
      "session.deleted",
      JSON.stringify({ seq: 2, type: "session.deleted", actor: "user", session_id: "s1" }),
    );

    await waitFor(() => expect(screen.getByText("No sessions yet")).toBeTruthy());
  });

  it("creating a session via the Launchpad lands directly in the conversation, goal visible, no rail click", async () => {
    const created = makeSession({ id: "s99", name: "Fix the deploy script" });
    let sessions: Session[] = [];
    vi.stubGlobal(
      "fetch",
      vi.fn().mockImplementation((input: URL, init?: RequestInit) => {
        const url = input.toString();
        const method = init?.method ?? "GET";
        if (method === "POST" && url.match(/\/sessions$/)) {
          sessions = [...sessions, created];
          return json({ session: created, addr: "fake://s99" });
        }
        if (method === "POST" && url.includes("/messages")) return json({});
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

    render(<SessionsSurface pipelineClient={new FakePipelineClient()} />);
    await waitFor(() => expect(screen.getByText("What can I help you with?")).toBeTruthy());

    fireEvent.change(screen.getByLabelText("Describe your goal"), {
      target: { value: "Fix the deploy script" },
    });
    fireEvent.click(screen.getByText("Start session"));

    await waitFor(() => expect(screen.getByLabelText("Workspace actions")).toBeTruthy());
    expect(screen.getByPlaceholderText(/message the agent/i)).toBeTruthy();

    // The rail itself only updates via the session.created SSE event (as in the
    // "shows a new session in the rail" test above) — simulate it to confirm the
    // conversation stays open with no click needed on the rail entry.
    const source = FakeEventSource.instances.find((s) => s.url.includes("session.created"));
    source?.emit(
      "session.created",
      JSON.stringify({ seq: 1, type: "session.created", actor: "user", session_id: "s99" }),
    );
    await waitFor(() => expect(rail().getByText("Fix the deploy script")).toBeTruthy());
    expect(screen.getByLabelText("Workspace actions")).toBeTruthy();
  });

  it("a fully-loaded session workspace holds at most 2 EventSource connections", async () => {
    const sessions = [makeSession({ id: "s1", name: "Alpha" })];
    stubSessionsAndConversationFetch(sessions);

    render(<SessionsSurface pipelineClient={new FakePipelineClient()} />);
    await waitFor(() => expect(rail().getByText("Alpha")).toBeTruthy());

    fireEvent.click(rail().getByText("Alpha"));
    await waitFor(() => expect(screen.getByLabelText("Workspace actions")).toBeTruthy());

    fireEvent.click(screen.getByRole("tab", { name: "Tasks" }));

    await waitFor(() => {
      const open = FakeEventSource.instances.filter((s) => !s.closed);
      expect(open.length).toBeLessThanOrEqual(2);
    });
  });

  it("clears a rail action-error toast on navigation so it can't linger over another session", async () => {
    const sessions = [
      makeSession({ id: "s1", name: "Alpha", activity: "inactive" }),
      makeSession({ id: "s2", name: "Bravo", activity: "active" }),
    ];
    vi.stubGlobal(
      "fetch",
      vi.fn().mockImplementation((input: URL, init?: RequestInit) => {
        const url = input.toString();
        const method = init?.method ?? "GET";
        if (method === "POST" && url.match(/\/sessions\/[^/]+\/terminate/)) {
          return Promise.resolve(
            new Response(JSON.stringify({ error: "supervisor unreachable" }), { status: 500 }),
          );
        }
        if (url.match(/\/sessions\/[^/]+\/transcript/)) return json([]);
        if (url.includes("/tasks")) return json([]);
        if (url.match(/\/sessions\/[^/]+$/)) {
          const id = url.split("/sessions/")[1];
          const session = sessions.find((s) => s.id === id);
          return session ? json(session) : Promise.resolve(new Response("not found", { status: 404 }));
        }
        if (url.includes("/sessions")) return json(sessions);
        return json([]);
      }),
    );

    render(<SessionsSurface pipelineClient={new FakePipelineClient()} />);
    await waitFor(() => expect(rail().getByText("Alpha")).toBeTruthy());

    fireEvent.click(screen.getByRole("button", { name: "Sleep Alpha" }));
    await waitFor(() => expect(screen.getByText(/Couldn't sleep/)).toBeTruthy());

    // Navigating to another session drops the surface-scoped error.
    fireEvent.click(rail().getByText("Bravo"));
    await waitFor(() => expect(screen.queryByText(/Couldn't sleep/)).toBeNull());
  });

  // Both controls that offer sleep go through the surface's one
  // useSessionSleep controller, so both ask the unlanded-commits question
  // (D6). The workspace kebab used to call terminate itself and skip it —
  // from the one place you are most likely to have commits waiting.
  describe("sleeping an isolated session with commits to land", () => {
    const isolated = makeSession({
      id: "s1",
      name: "Alpha",
      activity: "inactive",
      worktree_dir: "/home/u/.mads/session-worktrees/s1",
      worktree_branch: "mads/session/s1",
      worktree_base: "main",
      unlanded_count: 3,
    });

    async function renderSurface() {
      stubSessionsAndConversationFetch([isolated]);
      render(<SessionsSurface pipelineClient={new FakePipelineClient()} />);
      await waitFor(() => expect(rail().getByText("Alpha")).toBeTruthy());
    }

    it("asks from the rail card's sleep button", async () => {
      await renderSurface();

      fireEvent.click(screen.getByRole("button", { name: "Sleep Alpha" }));

      const dialog = screen.getByRole("alertdialog");
      expect(within(dialog).getByText("Sleep “Alpha”?")).toBeTruthy();
      expect(within(dialog).getByRole("button", { name: "Land into main and sleep" })).toBeTruthy();
    });

    it("asks from the workspace kebab's Sleep now, the same way", async () => {
      await renderSurface();
      fireEvent.click(rail().getByText("Alpha"));
      await waitFor(() => expect(screen.getByLabelText("Workspace actions")).toBeTruthy());

      fireEvent.click(screen.getByLabelText("Workspace actions"));
      fireEvent.click(screen.getByRole("button", { name: "Sleep now" }));

      const dialog = screen.getByRole("alertdialog");
      expect(within(dialog).getByText("Sleep “Alpha”?")).toBeTruthy();
      expect(within(dialog).getByRole("button", { name: "Land into main and sleep" })).toBeTruthy();
    });
  });

  it("selecting an inactive session renders without crashing", async () => {
    const sessions = [makeSession({ id: "s1", name: "Alpha", activity: "inactive" })];
    stubSessionsAndConversationFetch(sessions);

    render(<SessionsSurface pipelineClient={new FakePipelineClient()} />);
    await waitFor(() => expect(rail().getByText("Alpha")).toBeTruthy());

    fireEvent.click(rail().getByText("Alpha"));

    await waitFor(() => expect(screen.getByLabelText("Workspace actions")).toBeTruthy());
    expect(await screen.findByTestId("session-dock")).toBeTruthy();
  });

  it("a failed refresh over a loaded list says so and can be dismissed", async () => {
    let failSessions = false;
    const sessions = [makeSession({ id: "a", name: "Alpha" })];
    vi.stubGlobal(
      "fetch",
      vi.fn().mockImplementation((input: URL) => {
        const url = input.toString();
        if (url.endsWith("/sessions") && failSessions) {
          return Promise.resolve(new Response("bad gateway", { status: 502 }));
        }
        if (url.endsWith("/sessions")) return json(sessions);
        return json([]);
      }),
    );

    render(<SessionsSurface pipelineClient={new FakePipelineClient()} />);
    await waitFor(() => expect(rail().getByText("Alpha")).toBeTruthy());

    failSessions = true;
    FakeEventSource.instances[0].emit(
      "session.updated",
      JSON.stringify({ seq: 1, type: "session.updated", actor: "user", session_id: "a" }),
    );
    await waitFor(() => expect(screen.getByText("Couldn't refresh sessions")).toBeTruthy());
    expect(screen.getByText(/may be out of date/)).toBeTruthy();

    fireEvent.click(screen.getByLabelText("Dismiss"));
    expect(screen.queryByText("Couldn't refresh sessions")).toBeNull();
    expect(rail().getByText("Alpha")).toBeTruthy();
  });
});
