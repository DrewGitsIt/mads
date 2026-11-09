import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import { SessionRail } from "./SessionRail";
import { FakeRouterClient } from "../../../router/fixtures";
import type { Session } from "../../../router/types";

// The work note on the rail (docs/mockups/session-worktrees.html §1).
// Sleep's "land or keep for later" question (§5, D6) is no longer the rail's
// to ask — it moved into useSessionSleep so the workspace kebab asks it too,
// and its tests moved with it to ../actions/useSessionSleep.test.tsx.

function isolated(overrides: Partial<Session> = {}): Session {
  return {
    id: "s1",
    name: "Fix the deploy script",
    activity: "inactive",
    created_at: "2026-07-01T00:00:00Z",
    updated_at: "2026-07-01T00:00:00Z",
    last_turn_at: "2026-07-01T00:00:00Z",
    worktree_dir: "/home/u/.mads/session-worktrees/s1",
    worktree_branch: "mads/session/s1",
    worktree_base: "main",
    uncommitted_count: 0,
    unlanded_count: 3,
    ...overrides,
  };
}

function renderRail(session: Session, client = new FakeRouterClient()) {
  render(<SessionRail sessions={[session]} openTaskCounts={{}} routerClient={client} />);
  return { client };
}

describe("SessionRail — work note", () => {
  const note = () => document.querySelector("[data-work-note]");

  it("reads 'N commits to land' when commits wait to land", () => {
    renderRail(isolated());
    expect(note()?.getAttribute("data-work-note")).toBe("land");
    expect(screen.getByRole("status").textContent).toContain("· 3 commits to land");
  });

  it("reads 'uncommitted changes' while files are uncommitted", () => {
    renderRail(isolated({ uncommitted_count: 2 }));
    expect(note()?.getAttribute("data-work-note")).toBe("commit");
    expect(screen.getByRole("status").textContent).toContain("· uncommitted changes");
  });

  it("is the only work indicator: no dot by the name says it again", () => {
    renderRail(isolated({ uncommitted_count: 2 }));
    expect(screen.getAllByText(/uncommitted changes/)).toHaveLength(1);
    expect(screen.queryByLabelText("uncommitted changes")).toBeNull();
  });

  it("doesn't note commits waiting to be pushed: they're the checkout's, shared by its sessions", () => {
    renderRail(isolated({ unlanded_count: 0, unpushed_count: 2 }));
    expect(note()).toBeNull();
    expect(screen.queryByText(/to push/)).toBeNull();
  });

  it("is absent when there's no work waiting", () => {
    renderRail(isolated({ unlanded_count: 0 }));
    expect(note()).toBeNull();
  });

  it("gives a live state's status line to the live clause, with no note", () => {
    render(
      <SessionRail
        sessions={[isolated({ activity: "active" })]}
        openTaskCounts={{}}
        agentStates={{ s1: { subagents: 0, workingSince: new Date().toISOString(), label: "Running bash" } }}
      />,
    );
    expect(note()).toBeNull();
    expect(screen.getByRole("status").textContent).not.toContain("to land");
  });
});
