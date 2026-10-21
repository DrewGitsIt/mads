import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { SessionSleepPrompt, useSessionSleep, type DoneRequestNotices, type SleepVerb } from "./useSessionSleep";
import { FakeRouterClient } from "../../../router/fixtures";
import type { LandResult, Session } from "../../../router/types";

// The one sleep/wake path, tested once. Both controls that offer them — the
// rail card's ⏸ and the workspace kebab's "Sleep now" — call straight into
// this hook, so the unlanded-commits question (docs/SESSION-WORKTREES-
// DESIGN.md D6) is asked from either, and these tests are the proof for both.
// SessionsSurface.test.tsx holds the integration side: that each control is
// really wired here.

function isolated(overrides: Partial<Session> = {}): Session {
  return {
    id: "s1",
    name: "Fix the deploy script",
    activity: "inactive",
    created_at: "2026-07-01T00:00:00Z",
    updated_at: "2026-07-01T00:00:00Z",
    worktree_dir: "/home/u/.mads/session-worktrees/s1",
    worktree_branch: "mads/session/s1",
    worktree_base: "main",
    uncommitted_count: 0,
    unlanded_count: 3,
    ...overrides,
  };
}

/** A stand-in for the one caller that owns the controller (SessionsSurface). */
function renderHost(session: Session, client = new FakeRouterClient()) {
  const onError = vi.fn<(verb: SleepVerb, session: Session, err: unknown) => void>();
  const notices = {
    sent: vi.fn<DoneRequestNotices["sent"]>(),
    confirmed: vi.fn<DoneRequestNotices["confirmed"]>(),
    failed: vi.fn<DoneRequestNotices["failed"]>(),
  };

  function Host() {
    const sleep = useSessionSleep(client, onError, notices);
    return (
      <>
        <button type="button" onClick={() => sleep.requestSleep(session)}>
          Sleep it
        </button>
        <button type="button" onClick={() => sleep.wake(session)}>
          Wake it
        </button>
        <button type="button" onClick={() => sleep.requestDone(session, false)}>
          Done it
        </button>
        <button type="button" onClick={() => sleep.requestDone(session, true)}>
          Done it mid-turn
        </button>
        <button type="button" onClick={() => sleep.bringBack(session)}>
          Bring it back
        </button>
        <SessionSleepPrompt sleep={sleep} routerClient={client} />
      </>
    );
  }

  render(<Host />);
  return { client, onError, notices };
}

function clickSleep() {
  fireEvent.click(screen.getByRole("button", { name: "Sleep it" }));
}

describe("useSessionSleep — sleeping", () => {
  it("sleeps straight away when nothing waits to land", async () => {
    const { client } = renderHost(isolated({ unlanded_count: 0 }));
    const terminate = vi.spyOn(client, "closeSession");

    clickSleep();

    expect(terminate).toHaveBeenCalledWith("s1");
    expect(screen.queryByRole("alertdialog")).toBeNull();
  });

  it("sleeps straight away for a session with no worktree at all", () => {
    const { client } = renderHost({
      id: "s1",
      name: "Plain session",
      activity: "active",
      created_at: "2026-07-01T00:00:00Z",
      updated_at: "2026-07-01T00:00:00Z",
      unlanded_count: 3,
    });
    const terminate = vi.spyOn(client, "closeSession");

    clickSleep();

    // unlanded_count is only meaningful for an isolated session; a shared
    // checkout has no base of its own to be ahead of.
    expect(terminate).toHaveBeenCalledWith("s1");
    expect(screen.queryByRole("alertdialog")).toBeNull();
  });

  it("asks Land-and-sleep or Keep for later, and Keep for later just sleeps", () => {
    const { client } = renderHost(isolated());
    const terminate = vi.spyOn(client, "closeSession");

    clickSleep();

    const dialog = screen.getByRole("alertdialog");
    expect(within(dialog).getByText("Sleep “Fix the deploy script”?")).toBeTruthy();
    expect(dialog.textContent).toContain("This session has 3 commits that aren't in main yet.");

    fireEvent.click(within(dialog).getByRole("button", { name: "Keep for later" }));
    expect(terminate).toHaveBeenCalledWith("s1");
    expect(client.landCalls).toEqual([]);
  });

  it("dismissing neither lands nor sleeps", () => {
    const { client } = renderHost(isolated());
    const terminate = vi.spyOn(client, "closeSession");

    clickSleep();
    fireEvent.keyDown(document, { key: "Escape" });

    expect(screen.queryByRole("alertdialog")).toBeNull();
    expect(terminate).not.toHaveBeenCalled();
    expect(client.landCalls).toEqual([]);
  });

  it("lands, then sleeps", async () => {
    const { client } = renderHost(isolated());
    const terminate = vi.spyOn(client, "closeSession");

    clickSleep();
    fireEvent.click(screen.getByRole("button", { name: "Land into main and sleep" }));

    await waitFor(() => expect(terminate).toHaveBeenCalledWith("s1"));
    expect(client.landCalls).toEqual(["s1"]);
  });

  it("stays open and says why when Land doesn't land", async () => {
    const client = new FakeRouterClient();
    client.landOutcome = {
      outcome: "refused",
      reason: "primary_dirty",
      base: "main",
      files: ["a.go"],
    } as LandResult;
    renderHost(isolated(), client);
    const terminate = vi.spyOn(client, "closeSession");

    clickSleep();
    fireEvent.click(screen.getByRole("button", { name: "Land into main and sleep" }));

    expect(await screen.findByText(/Didn't land — still open/)).toBeTruthy();
    expect(screen.getByRole("alert").textContent).toContain(
      "Git won't land over a.go in your checkout.",
    );
    expect(terminate).not.toHaveBeenCalled();
  });

  it("reports a failed terminate to the caller, naming the session", async () => {
    const client = new FakeRouterClient();
    vi.spyOn(client, "closeSession").mockRejectedValue(
      new Error("terminate session: child unresponsive"),
    );
    const { onError } = renderHost(isolated({ unlanded_count: 0 }), client);

    clickSleep();

    await waitFor(() => expect(onError).toHaveBeenCalled());
    const [verb, session, err] = onError.mock.calls[0];
    expect(verb).toBe("sleep");
    expect(session.id).toBe("s1");
    expect((err as Error).message).toBe("terminate session: child unresponsive");
  });
});

describe("useSessionSleep — waking", () => {
  it("wakes without asking anything, even with commits waiting to land", () => {
    // Waking takes nothing away, so there is nothing to warn about: the land
    // prompt belongs to sleep alone.
    const { client } = renderHost(isolated());
    const resume = vi.spyOn(client, "resumeSession");

    fireEvent.click(screen.getByRole("button", { name: "Wake it" }));

    expect(resume).toHaveBeenCalledWith("s1");
    expect(screen.queryByRole("alertdialog")).toBeNull();
  });

  it("reports a failed wake to the caller under its own verb", async () => {
    const client = new FakeRouterClient();
    vi.spyOn(client, "resumeSession").mockRejectedValue(new Error("resume session: no provider"));
    const { onError } = renderHost(isolated(), client);

    fireEvent.click(screen.getByRole("button", { name: "Wake it" }));

    await waitFor(() => expect(onError).toHaveBeenCalled());
    expect(onError.mock.calls[0][0]).toBe("wake");
  });
});

// The rail's ✓: Done-and-sleep. Same land question as ⏸, plus one before it
// when a turn is running — disclosure rather than a silently-disabled check.
describe("useSessionSleep — marking done", () => {
  function spies(client: FakeRouterClient, session: Session) {
    return {
      aside: vi.spyOn(client, "setSessionAside").mockResolvedValue(session),
      terminate: vi.spyOn(client, "closeSession").mockResolvedValue(),
    };
  }

  it("checks off then sleeps, asking nothing when nothing waits", async () => {
    const session = isolated({ unlanded_count: 0 });
    const client = new FakeRouterClient();
    const { aside, terminate } = spies(client, session);
    renderHost(session, client);

    fireEvent.click(screen.getByRole("button", { name: "Done it" }));

    expect(screen.queryByRole("alertdialog")).toBeNull();
    expect(aside).toHaveBeenCalledWith("s1", true);
    await waitFor(() => expect(terminate).toHaveBeenCalledWith("s1"));
  });

  it("asks before stopping a running turn, and keeping it running does neither", () => {
    const session = isolated({ unlanded_count: 0 });
    const client = new FakeRouterClient();
    const { aside, terminate } = spies(client, session);
    renderHost(session, client);

    fireEvent.click(screen.getByRole("button", { name: "Done it mid-turn" }));
    const dialog = screen.getByRole("alertdialog");
    expect(dialog.textContent).toContain("is still working");

    fireEvent.click(within(dialog).getByRole("button", { name: "Keep it running" }));
    expect(screen.queryByRole("alertdialog")).toBeNull();
    expect(aside).not.toHaveBeenCalled();
    expect(terminate).not.toHaveBeenCalled();
  });

  it("goes on to the land question after a stop, and Keep for later finishes the check-off", async () => {
    const session = isolated();
    const client = new FakeRouterClient();
    const { aside, terminate } = spies(client, session);
    renderHost(session, client);

    fireEvent.click(screen.getByRole("button", { name: "Done it mid-turn" }));
    fireEvent.click(screen.getByRole("button", { name: "Mark done & stop" }));

    const land = screen.getByRole("alertdialog");
    expect(land.textContent).toContain("3 commits");
    expect(aside).not.toHaveBeenCalled();

    fireEvent.click(within(land).getByRole("button", { name: "Keep for later" }));
    expect(aside).toHaveBeenCalledWith("s1", true);
    await waitFor(() => expect(terminate).toHaveBeenCalledWith("s1"));
  });

  it("dismissing the land question leaves the session unchecked and awake", () => {
    const session = isolated();
    const client = new FakeRouterClient();
    const { aside, terminate } = spies(client, session);
    renderHost(session, client);

    fireEvent.click(screen.getByRole("button", { name: "Done it" }));
    fireEvent.keyDown(screen.getByRole("alertdialog"), { key: "Escape" });

    expect(aside).not.toHaveBeenCalled();
    expect(terminate).not.toHaveBeenCalled();
  });

  it("doesn't sleep a session whose check-off failed, and says so", async () => {
    const session = isolated({ unlanded_count: 0 });
    const client = new FakeRouterClient();
    vi.spyOn(client, "setSessionAside").mockRejectedValue(new Error("data service down"));
    const terminate = vi.spyOn(client, "closeSession");
    const { onError } = renderHost(session, client);

    fireEvent.click(screen.getByRole("button", { name: "Done it" }));

    await waitFor(() => expect(onError).toHaveBeenCalled());
    expect(onError.mock.calls[0][0]).toBe("mark done");
    expect(terminate).not.toHaveBeenCalled();
  });

  // The rail's motion (rail/useDoneMotion) moves a card only on the server's
  // answer, so the controller says when a request went out and how it ended.
  it("tells the rail a check-off went out, then what the server answered", async () => {
    const session = isolated({ unlanded_count: 0 });
    const client = new FakeRouterClient();
    const answered = { ...session, tags: ["aside"], updated_at: "2026-07-02T00:00:00Z" };
    vi.spyOn(client, "setSessionAside").mockResolvedValue(answered);
    vi.spyOn(client, "closeSession").mockResolvedValue();
    const { notices } = renderHost(session, client);

    fireEvent.click(screen.getByRole("button", { name: "Done it" }));
    expect(notices.sent).toHaveBeenCalledWith(session, true);
    await waitFor(() => expect(notices.confirmed).toHaveBeenCalledWith(answered, true));
    expect(notices.failed).not.toHaveBeenCalled();
  });

  it("tells the rail nothing until a dialog is answered, and nothing at all if it's dismissed", () => {
    const session = isolated();
    const client = new FakeRouterClient();
    spies(client, session);
    const { notices } = renderHost(session, client);

    fireEvent.click(screen.getByRole("button", { name: "Done it" }));
    expect(notices.sent).not.toHaveBeenCalled();
    fireEvent.keyDown(screen.getByRole("alertdialog"), { key: "Escape" });
    expect(notices.sent).not.toHaveBeenCalled();
  });

  it("tells the rail a refused check-off failed", async () => {
    const session = isolated({ unlanded_count: 0 });
    const client = new FakeRouterClient();
    vi.spyOn(client, "setSessionAside").mockRejectedValue(new Error("data service down"));
    const { notices } = renderHost(session, client);

    fireEvent.click(screen.getByRole("button", { name: "Done it" }));
    await waitFor(() => expect(notices.failed).toHaveBeenCalledWith("s1"));
    expect(notices.confirmed).not.toHaveBeenCalled();
  });

  it("a failed sleep after an accepted check-off still counts as accepted", async () => {
    const session = isolated({ unlanded_count: 0 });
    const client = new FakeRouterClient();
    vi.spyOn(client, "setSessionAside").mockResolvedValue(session);
    vi.spyOn(client, "closeSession").mockRejectedValue(new Error("child stuck"));
    const { notices, onError } = renderHost(session, client);

    fireEvent.click(screen.getByRole("button", { name: "Done it" }));
    await waitFor(() => expect(onError).toHaveBeenCalled());
    expect(notices.confirmed).toHaveBeenCalled();
    expect(notices.failed).not.toHaveBeenCalled();
  });

  it("brings a session back without waking it", () => {
    const session = isolated({ tags: ["aside"] });
    const client = new FakeRouterClient();
    const { aside, terminate } = spies(client, session);
    const resume = vi.spyOn(client, "resumeSession");
    renderHost(session, client);

    fireEvent.click(screen.getByRole("button", { name: "Bring it back" }));

    expect(aside).toHaveBeenCalledWith("s1", false);
    expect(terminate).not.toHaveBeenCalled();
    expect(resume).not.toHaveBeenCalled();
  });
});
